package bridgesdk

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	_ "embed"
)

// networks.json is a byte-identical copy of ../../networks.json written by tools/gen.mjs (single source of truth).
//
//go:embed networks.json
var networksJSON []byte

type NetworkName string

const (
	Mainnet NetworkName = "mainnet"
	Testnet NetworkName = "testnet"
)

type ChainKey string

const (
	ChainOne    ChainKey = "one"
	ChainBSC      ChainKey = "bsc"
	ChainHyperEVM ChainKey = "hyperevm"
)

const NativeToken = "0x0000000000000000000000000000000000000001"

type Token struct {
	Address  string  `json:"address"`
	Symbol   *string `json:"symbol"`
	Decimals int     `json:"decimals"`
	Kind     string  `json:"kind"`
	Status   string  `json:"status,omitempty"`
}

type X1Pool struct {
	TokenIn  string `json:"tokenIn"`
	TokenOut string `json:"tokenOut"`
	Fee      uint32 `json:"fee"`
	Pool     string `json:"pool"`
}

type SwapConfig struct {
	V3SwapRouter     *string `json:"v3SwapRouter"`
	QuoterV2         *string `json:"quoterV2"`
	V3Factory        *string `json:"v3Factory"`
	SwapBridgeRouter *string `json:"swapBridgeRouter"`
	X1Pool           *X1Pool `json:"x1Pool"`
}

type ChainConfig struct {
	ChainID           int64             `json:"chainId"`
	Name              string            `json:"name"`
	NativeSymbol      *string           `json:"nativeSymbol"`
	RPC               string            `json:"rpc"`
	Explorer          *string           `json:"explorer"`
	Bridge            *string           `json:"bridge"`
	BridgeVerifier    *string           `json:"bridgeVerifier"`
	BridgeExecutor    *string           `json:"bridgeExecutor"`
	Tokens            map[string]*Token `json:"tokens"`
	Swap              *SwapConfig       `json:"swap"`
	CoreSystemAddress *string           `json:"coreSystemAddress"`
	CoreInfoAPI       *string           `json:"coreInfoApi"`
	Todo              map[string]string `json:"todo"`
}

type NetworkData struct {
	Label    string                    `json:"label"`
	HubChain ChainKey                  `json:"hubChain"`
	Chains   map[ChainKey]*ChainConfig `json:"chains"`
}

type NetworksData struct {
	SchemaVersion int                          `json:"schemaVersion"`
	VerifiedAt    string                       `json:"verifiedAt"`
	Networks      map[NetworkName]*NetworkData `json:"networks"`
	Provenance    map[string]string            `json:"provenance"`
}

var (
	netOnce sync.Once
	netData *NetworksData
	netErr  error
)

// LoadNetworks parses the embedded networks.json.
func LoadNetworks() (*NetworksData, error) {
	netOnce.Do(func() {
		netData = &NetworksData{}
		netErr = json.Unmarshal(networksJSON, netData)
	})
	return netData, netErr
}

// GetNetwork returns the built-in configuration of "mainnet" or "testnet".
func GetNetwork(name NetworkName) (*NetworkData, error) {
	d, err := LoadNetworks()
	if err != nil {
		return nil, err
	}
	n, ok := d.Networks[name]
	if !ok {
		return nil, fmt.Errorf("unknown network %q (expected mainnet or testnet)", name)
	}
	return n, nil
}

// Chain returns the chain configuration.
func (n *NetworkData) Chain(k ChainKey) *ChainConfig { return n.Chains[k] }

// ConfigValue resolves a dotted path such as "hyperevm.bridge", "one.swap.swapBridgeRouter" or "bsc.tokens.USDT".
// It returns nil when the value is null/unconfigured.
func (n *NetworkData) ConfigValue(path string) *string {
	parts := strings.Split(path, ".")
	c := n.Chains[ChainKey(parts[0])]
	if c == nil {
		return nil
	}
	rest := parts[1:]
	switch {
	case len(rest) == 1 && rest[0] == "bridge":
		return c.Bridge
	case len(rest) == 1 && rest[0] == "bridgeVerifier":
		return c.BridgeVerifier
	case len(rest) == 1 && rest[0] == "bridgeExecutor":
		return c.BridgeExecutor
	case len(rest) == 2 && rest[0] == "tokens":
		if t := c.Tokens[rest[1]]; t != nil {
			return &t.Address
		}
		return nil
	case len(rest) == 2 && rest[0] == "swap":
		if c.Swap == nil {
			return nil
		}
		switch rest[1] {
		case "v3SwapRouter":
			return c.Swap.V3SwapRouter
		case "quoterV2":
			return c.Swap.QuoterV2
		case "v3Factory":
			return c.Swap.V3Factory
		case "swapBridgeRouter":
			return c.Swap.SwapBridgeRouter
		case "x1Pool":
			if c.Swap.X1Pool != nil {
				return &c.Swap.X1Pool.Pool
			}
		}
	}
	return nil
}

// requireConfigured returns *AddressNotConfiguredError when a required value is null (before any approve or send).
func (n *NetworkData) requireConfigured(route Route, paths []string) error {
	for _, p := range paths {
		if n.ConfigValue(p) == nil {
			parts := strings.SplitN(p, ".", 2)
			todo := "TODO(UNCONFIRMED)"
			if c := n.Chains[ChainKey(parts[0])]; c != nil && len(parts) == 2 {
				if t, ok := c.Todo[parts[1]]; ok {
					todo = t
				}
			}
			return &AddressNotConfiguredError{Route: route, Field: p, Todo: todo}
		}
	}
	return nil
}
