package bridgesdk

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
)

// ClientOptions configure a Client. All fields are optional.
type ClientOptions struct {
	// RPCURLs overrides the built-in public RPC per chain.
	RPCURLs map[ChainKey]string
	// RPCs injects RPC implementations (tests, custom transports). Takes precedence over URLs.
	RPCs map[ChainKey]RPC
	// CallTimeout bounds every single RPC call (default 15 s): eth_call, getLogs, receipts, blocks, chain id, nonce, send. A call that
	// exceeds it fails with *RpcTimeoutError (after a broadcast it is wrapped in *BroadcastOutcomeUnknownError, never "not sent").
	CallTimeout time.Duration
	// QuoteTTL defaults to 30 s.
	QuoteTTL time.Duration
	// ScanChunkBlocks is the eth_getLogs chunk size (default 2000).
	ScanChunkBlocks uint64
	// ScanLookbackBlocks: when a handle has no recorded scan start (default 20000).
	ScanLookbackBlocks uint64
	// Now replaces the clock (tests); default time.Now.
	Now func() time.Time
	// Sleep replaces the waiting between polls (tests); default is a timer that honours ctx.
	Sleep func(ctx context.Context, d time.Duration) error
	// Networks overrides the built-in configuration (tests).
	Networks *NetworkData
}

// Client: Quote -> Preflight -> Send -> WaitHandle -> Track. Safe for concurrent use for reads;
// concurrent sends from ONE account are the caller's responsibility (nonces).
type Client struct {
	network  NetworkName
	net      *NetworkData
	opts     ClientOptions
	mu       sync.Mutex
	rpcs     map[ChainKey]RPC
	verified map[ChainKey]bool
}

// NewClient creates a client for the built-in "mainnet" or "testnet" configuration. opts may be nil. It performs no network access: each chain's
// RPC is dialled on first use and its eth_chainId is checked against the configuration once (a mismatch fails with *NetworkMismatchError before
// anything is read or sent). It fails only if the network name is unknown.
func NewClient(ctx context.Context, network NetworkName, opts *ClientOptions) (*Client, error) {
	o := ClientOptions{}
	if opts != nil {
		o = *opts
	}
	nd := o.Networks
	if nd == nil {
		var err error
		if nd, err = GetNetwork(network); err != nil {
			return nil, err
		}
	}
	c := &Client{network: network, net: nd, opts: o, rpcs: map[ChainKey]RPC{}, verified: map[ChainKey]bool{}}
	for k, r := range o.RPCs {
		c.rpcs[k] = r
	}
	return c, nil
}

// Network returns the network configuration this client uses: the built-in one, or ClientOptions.Networks when set. Treat it as read-only.
func (c *Client) Network() *NetworkData { return c.net }

func (c *Client) now() time.Time {
	if c.opts.Now != nil {
		return c.opts.Now()
	}
	return time.Now()
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if c.opts.Sleep != nil {
		return c.opts.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *Client) rpcFor(ctx context.Context, key ChainKey) (RPC, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r, ok := c.rpcs[key]; ok {
		return c.timed(r), nil
	}
	cfg := c.net.Chains[key]
	if cfg == nil {
		return nil, fmt.Errorf("unknown chain %q", key)
	}
	url := cfg.RPC
	if u, ok := c.opts.RPCURLs[key]; ok {
		url = u
	}
	cl, err := ethclient.DialContext(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("dial %s (%s): %w", key, url, err)
	}
	c.rpcs[key] = cl
	return c.timed(cl), nil
}

func (c *Client) timed(r RPC) RPC {
	to := c.opts.CallTimeout
	if to <= 0 {
		to = DefaultCallTimeout
	}
	return &timedRPC{inner: r, timeout: to}
}

// assertChain checks eth_chainId against networks.json once per chain (mismatch => *NetworkMismatchError).
func (c *Client) assertChain(ctx context.Context, key ChainKey) error {
	c.mu.Lock()
	ok := c.verified[key]
	c.mu.Unlock()
	if ok {
		return nil
	}
	r, err := c.rpcFor(ctx, key)
	if err != nil {
		return err
	}
	id, err := r.ChainID(ctx)
	if err != nil {
		return fmt.Errorf("eth_chainId on %s: %w", key, err)
	}
	want := c.net.Chains[key].ChainID
	if id.Int64() != want {
		return &NetworkMismatchError{Scope: string(key) + " RPC", Expected: want, Actual: id.String()}
	}
	c.mu.Lock()
	c.verified[key] = true
	c.mu.Unlock()
	return nil
}
