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

// NetworkName selects a built-in network configuration.
type NetworkName string

const (
	Mainnet NetworkName = "mainnet" // the production networks
	Testnet NetworkName = "testnet" // the test networks
)

// ChainKey names a chain inside a NetworkData. The ONE chain is the hub of every multihop route.
type ChainKey string

const (
	ChainOne      ChainKey = "one"      // the ONE chain (hub)
	ChainBSC      ChainKey = "bsc"      // BSC
	ChainHyperEVM ChainKey = "hyperevm" // HyperEVM
)

// NativeToken is the placeholder address that stands for the native coin (ONE on the ONE chain) wherever a token address is expected, for example
// as the source token of a native bridge transfer.
const NativeToken = "0x0000000000000000000000000000000000000001"

// Token is a token of a chain. A token that is not configured on a network appears as a nil entry in ChainConfig.Tokens.
type Token struct {
	Address  string  `json:"address"`
	Symbol   *string `json:"symbol"`
	Decimals int     `json:"decimals"`
	Kind     string  `json:"kind"`             // "native" or "erc20"
	Status   string  `json:"status,omitempty"` // optional marker carried by the configuration schema; empty when unset
}

// X1Pool is the swap pool used by the X1 route (USDTx to ONEUSD on the ONE chain).
type X1Pool struct {
	TokenIn  string `json:"tokenIn"`
	TokenOut string `json:"tokenOut"`
	Fee      uint32 `json:"fee"`
	Pool     string `json:"pool"`
}

// SwapConfig holds the swap contracts of a chain. A nil field means the contract is not configured on this network; routes that need it are
// refused with *AddressNotConfiguredError.
type SwapConfig struct {
	V3SwapRouter     *string `json:"v3SwapRouter"`
	QuoterV2         *string `json:"quoterV2"`
	V3Factory        *string `json:"v3Factory"`
	SwapBridgeRouter *string `json:"swapBridgeRouter"`
	X1Pool           *X1Pool `json:"x1Pool"`
}

// ChainConfig is the configuration of one chain. Pointer fields are nil when the value is not configured on this network.
type ChainConfig struct {
	ChainID      int64   `json:"chainId"`
	Name         string  `json:"name"`
	NativeSymbol *string `json:"nativeSymbol"`
	RPC          string  `json:"rpc"` // default public RPC; ClientOptions.RPCURLs overrides it
	Explorer     *string `json:"explorer"`
	Bridge       *string `json:"bridge"`
	// BridgeVerifier and BridgeExecutor are informational: the SDK always resolves them live from the bridge.
	BridgeVerifier *string `json:"bridgeVerifier"`
	BridgeExecutor *string `json:"bridgeExecutor"`
	// Tokens is keyed by token name (for example "ONE", "USDT", "USDTx", "ONEUSD", "WCROSS"); an unconfigured token is a nil entry.
	Tokens map[string]*Token `json:"tokens"`
	Swap   *SwapConfig       `json:"swap"`
	// CoreSystemAddress is the HyperCore system address (HyperEVM only).
	CoreSystemAddress *string `json:"coreSystemAddress"`
	// CoreInfoAPI is the HyperCore info API that CheckCoreBalance queries (HyperEVM only).
	CoreInfoAPI *string `json:"coreInfoApi"`
	// Todo explains each value that is not configured, keyed by its dotted path inside the chain (for example "tokens.ONEUSD").
	Todo map[string]string `json:"todo"`
}

// NetworkData is the configuration of one network: its chains and which one is the hub.
type NetworkData struct {
	Label    string                    `json:"label"`
	HubChain ChainKey                  `json:"hubChain"`
	Chains   map[ChainKey]*ChainConfig `json:"chains"`
}

// NetworksData is the whole embedded configuration file (all networks plus its metadata).
type NetworksData struct {
	SchemaVersion int                          `json:"schemaVersion"`
	VerifiedAt    string                       `json:"verifiedAt"` // when the values were last checked against their sources
	Networks      map[NetworkName]*NetworkData `json:"networks"`
	Provenance    map[string]string            `json:"provenance"` // source reference per configured value
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
