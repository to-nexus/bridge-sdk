package bridgesdk

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
)

// RPC is the small surface of an Ethereum node the SDK needs. *ethclient.Client satisfies it; tests mock it.
type RPC interface {
	ChainID(ctx context.Context) (*big.Int, error)
	BlockNumber(ctx context.Context) (uint64, error)
	CallContract(ctx context.Context, call ethereum.CallMsg, blockNumber *big.Int) ([]byte, error)
	FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error)
	TransactionReceipt(ctx context.Context, txHash common.Hash) (*types.Receipt, error)
	TransactionByHash(ctx context.Context, hash common.Hash) (tx *types.Transaction, isPending bool, err error)
	PendingNonceAt(ctx context.Context, account common.Address) (uint64, error)
	NonceAt(ctx context.Context, account common.Address, blockNumber *big.Int) (uint64, error)
	SuggestGasPrice(ctx context.Context) (*big.Int, error)
	EstimateGas(ctx context.Context, call ethereum.CallMsg) (uint64, error)
	SendTransaction(ctx context.Context, tx *types.Transaction) error
	BalanceAt(ctx context.Context, account common.Address, blockNumber *big.Int) (*big.Int, error)
	BlockByNumber(ctx context.Context, number *big.Int) (*types.Block, error)
}

// DefaultCallTimeout bounds every single RPC call (ClientOptions.CallTimeout overrides it).
const DefaultCallTimeout = 15 * time.Second

// callTimed runs one provider call under min(timeout, remaining parent deadline). On expiry it returns *RpcTimeoutError (the possibly
// still running provider call is abandoned, never retried here). A cancelled parent context is returned as is.
func callTimed[T any](ctx context.Context, timeout time.Duration, method string, fn func(ctx context.Context) (T, error)) (T, error) {
	var zero T
	eff := timeout
	if dl, ok := ctx.Deadline(); ok {
		if rem := time.Until(dl); rem < eff {
			eff = max(rem, 0)
		}
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	type res struct {
		v   T
		err error
	}
	ch := make(chan res, 1)
	go func() {
		v, err := fn(cctx)
		ch <- res{v, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil && errors.Is(cctx.Err(), context.DeadlineExceeded) {
			return zero, &RpcTimeoutError{Method: method, Timeout: eff}
		}
		return r.v, r.err
	case <-cctx.Done():
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			return zero, &RpcTimeoutError{Method: method, Timeout: eff}
		}
		return zero, ctx.Err()
	}
}

// timedRPC wraps an RPC so that every call is bounded by the per-call timeout and the caller's overall deadline.
type timedRPC struct {
	inner   RPC
	timeout time.Duration
}

func (t *timedRPC) ChainID(ctx context.Context) (*big.Int, error) {
	return callTimed(ctx, t.timeout, "eth_chainId", t.inner.ChainID)
}
func (t *timedRPC) BlockNumber(ctx context.Context) (uint64, error) {
	return callTimed(ctx, t.timeout, "eth_blockNumber", t.inner.BlockNumber)
}
func (t *timedRPC) CallContract(ctx context.Context, call ethereum.CallMsg, blockNumber *big.Int) ([]byte, error) {
	return callTimed(ctx, t.timeout, "eth_call", func(c context.Context) ([]byte, error) { return t.inner.CallContract(c, call, blockNumber) })
}
func (t *timedRPC) FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	return callTimed(ctx, t.timeout, "eth_getLogs", func(c context.Context) ([]types.Log, error) { return t.inner.FilterLogs(c, q) })
}
func (t *timedRPC) TransactionReceipt(ctx context.Context, txHash common.Hash) (*types.Receipt, error) {
	return callTimed(ctx, t.timeout, "eth_getTransactionReceipt", func(c context.Context) (*types.Receipt, error) { return t.inner.TransactionReceipt(c, txHash) })
}
func (t *timedRPC) TransactionByHash(ctx context.Context, hash common.Hash) (*types.Transaction, bool, error) {
	type r struct {
		tx      *types.Transaction
		pending bool
	}
	v, err := callTimed(ctx, t.timeout, "eth_getTransactionByHash", func(c context.Context) (r, error) {
		tx, p, e := t.inner.TransactionByHash(c, hash)
		return r{tx, p}, e
	})
	return v.tx, v.pending, err
}
func (t *timedRPC) PendingNonceAt(ctx context.Context, account common.Address) (uint64, error) {
	return callTimed(ctx, t.timeout, "eth_getTransactionCount", func(c context.Context) (uint64, error) { return t.inner.PendingNonceAt(c, account) })
}
func (t *timedRPC) NonceAt(ctx context.Context, account common.Address, blockNumber *big.Int) (uint64, error) {
	return callTimed(ctx, t.timeout, "eth_getTransactionCount", func(c context.Context) (uint64, error) { return t.inner.NonceAt(c, account, blockNumber) })
}
func (t *timedRPC) SuggestGasPrice(ctx context.Context) (*big.Int, error) {
	return callTimed(ctx, t.timeout, "eth_gasPrice", t.inner.SuggestGasPrice)
}
func (t *timedRPC) EstimateGas(ctx context.Context, call ethereum.CallMsg) (uint64, error) {
	return callTimed(ctx, t.timeout, "eth_estimateGas", func(c context.Context) (uint64, error) { return t.inner.EstimateGas(c, call) })
}
func (t *timedRPC) SendTransaction(ctx context.Context, tx *types.Transaction) error {
	_, err := callTimed(ctx, t.timeout, "eth_sendRawTransaction", func(c context.Context) (struct{}, error) { return struct{}{}, t.inner.SendTransaction(c, tx) })
	return err
}
func (t *timedRPC) BalanceAt(ctx context.Context, account common.Address, blockNumber *big.Int) (*big.Int, error) {
	return callTimed(ctx, t.timeout, "eth_getBalance", func(c context.Context) (*big.Int, error) { return t.inner.BalanceAt(c, account, blockNumber) })
}
func (t *timedRPC) BlockByNumber(ctx context.Context, number *big.Int) (*types.Block, error) {
	return callTimed(ctx, t.timeout, "eth_getBlockByNumber", func(c context.Context) (*types.Block, error) { return t.inner.BlockByNumber(c, number) })
}

// extractRevertData digs revert bytes out of a node error (rpc.DataError), nil if none.
func extractRevertData(err error) []byte {
	var de rpc.DataError
	if errors.As(err, &de) {
		if s, ok := de.ErrorData().(string); ok {
			if b, e := hexutil.Decode(s); e == nil {
				return b
			}
		}
	}
	type dataer interface{ ErrorData() interface{} }
	var d dataer
	if errors.As(err, &d) {
		switch v := d.ErrorData().(type) {
		case string:
			if b, e := hexutil.Decode(v); e == nil {
				return b
			}
		case []byte:
			return v
		}
	}
	return nil
}

// callABI encodes, eth_calls and decodes.
func (c *Client) callABI(ctx context.Context, key ChainKey, a *abi.ABI, to common.Address, method string, from *common.Address, args ...interface{}) ([]interface{}, error) {
	data, err := a.Pack(method, args...)
	if err != nil {
		return nil, fmt.Errorf("pack %s: %w", method, err)
	}
	r, err := c.rpcFor(ctx, key)
	if err != nil {
		return nil, err
	}
	msg := ethereum.CallMsg{To: &to, Data: data}
	if from != nil {
		msg.From = *from
	}
	out, err := r.CallContract(ctx, msg, nil)
	if err != nil {
		var te *RpcTimeoutError
		if errors.As(err, &te) {
			return nil, err // a timeout is not a revert: keep its identity for callers (Quote / Preflight / Send)
		}
		return nil, &callError{method: method, data: extractRevertData(err), cause: err}
	}
	return a.Unpack(method, out)
}

type callError struct {
	method string
	data   []byte
	cause  error
}

func (e *callError) Error() string { return fmt.Sprintf("%s reverted: %v", e.method, e.cause) }
func (e *callError) Unwrap() error { return e.cause }

// withBackoff: bounded exponential backoff (base 1 s, x2, cap 30 s, 6 tries).
func (c *Client) withBackoff(ctx context.Context, fn func() error) error {
	delay := time.Second
	var last error
	for i := 0; i < 6; i++ {
		if err := fn(); err == nil {
			return nil
		} else {
			last = err
		}
		if i == 5 || ctx.Err() != nil {
			break
		}
		if err := c.sleep(ctx, delay); err != nil {
			if errors.Is(err, context.DeadlineExceeded) && last != nil {
				return last // the overall deadline hit while backing off: report the call that kept failing
			}
			return err
		}
		delay *= 2
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
	}
	return last
}

func hash32(v *big.Int) common.Hash { return common.BigToHash(v) }
