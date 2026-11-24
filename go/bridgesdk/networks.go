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

// DexKind names the ONE chain DEX that X1 and X3 use.
type DexKind string

const (
	DexV3 DexKind = "v3" // Uniswap V3 (single-pool calls carry uint24 fee); the default when SwapConfig.Dex is empty
	DexCL DexKind = "cl" // Slipstream concentrated liquidity (single-pool calls carry int24 tickSpacing)
)

// Int24 range of a Slipstream tick spacing in the ABI.
const (
	MinInt24 = -(1 << 23)
	MaxInt24 = 1<<23 - 1
)

// CLX1Pool is the Slipstream CL pool used by X1. TickSpacing is the pool key (read from pool.tickSpacing()); the CL pool fee is dynamic and is
// never used to derive it.
type CLX1Pool struct {
	TokenIn     string `json:"tokenIn"`
	TokenOut    string `json:"tokenOut"`
	TickSpacing int32  `json:"tickSpacing"`
	Pool        string `json:"pool"`
}

// CLSwapConfig holds the Slipstream CL contracts of a chain (used when SwapConfig.Dex is "cl"). A nil field means not configured.
type CLSwapConfig struct {
	SwapRouter       *string   `json:"swapRouter"`
	QuoterV2         *string   `json:"quoterV2"`
	Factory          *string   `json:"factory"`
	SwapBridgeRouter *string   `json:"swapBridgeRouter"` // SwapBridgeRouterCL (X3)
	X1Pool           *CLX1Pool `json:"x1Pool"`
}

// SwapConfig holds the swap contracts of a chain. A nil field means the contract is not configured on this network; routes that need it are
// refused with *AddressNotConfiguredError.
type SwapConfig struct {
	V3SwapRouter     *string `json:"v3SwapRouter"`
	QuoterV2         *string `json:"quoterV2"`
	V3Factory        *string `json:"v3Factory"`
	SwapBridgeRouter *string `json:"swapBridgeRouter"`
	X1Pool           *X1Pool `json:"x1Pool"`
	// Dex selects the DEX for X1 and X3: "" or "v3" = the V3 fields above, "cl" = the contracts in CL.
	Dex DexKind       `json:"dex,omitempty"`
	CL  *CLSwapConfig `json:"cl,omitempty"`
}

// DexKind returns the configured DEX ("v3" when unset). It does not validate the value; see NetworkData.Dex.
func (s *SwapConfig) DexKind() DexKind {
	if s == nil || s.Dex == "" {
		return DexV3
	}
	return s.Dex
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

// Dex returns the DEX of the ONE chain ("v3" when unset or when the chain has no swap block). An unknown value is an error.
func (n *NetworkData) Dex() (DexKind, error) {
	one := n.Chains[ChainOne]
	if one == nil || one.Swap == nil {
		return DexV3, nil
	}
	switch d := one.Swap.DexKind(); d {
	case DexV3, DexCL:
		return d, nil
	default:
		return "", fmt.Errorf("one.swap.dex must be %q or %q, got %q", DexV3, DexCL, d)
	}
}

// dexPath maps a V3 swap config path to the path of the same role under the configured DEX (identity for V3 and for every other path).
func dexPath(dex DexKind, p string) string {
	if dex != DexCL {
		return p
	}
	switch p {
	case "one.swap.v3SwapRouter":
		return "one.swap.cl.swapRouter"
	case "one.swap.quoterV2":
		return "one.swap.cl.quoterV2"
	case "one.swap.v3Factory":
		return "one.swap.cl.factory"
	case "one.swap.swapBridgeRouter":
		return "one.swap.cl.swapBridgeRouter"
	case "one.swap.x1Pool":
		return "one.swap.cl.x1Pool"
	}
	return p
}

// swapView is the ONE chain swap configuration of the ACTIVE DEX: the V3 fields, or the CL block. Nil pointers = not configured.
type swapView struct {
	dex         DexKind
	router      *string // V3 SwapRouter | CL SwapRouter (the X1 extraCall target)
	quoter      *string
	factory     *string
	sbr         *string // SwapBridgeRouter | SwapBridgeRouterCL (X3)
	pool        *string // X1 pool address
	fee         uint32  // V3 X1 pool fee
	tickSpacing int32   // CL X1 pool tick spacing
}

// swapViewOf resolves the active DEX of a network configuration. An invalid dex value yields a view with no contracts (every route that needs
// one is then refused), so callers that must report the reason call NetworkData.Dex first.
func swapViewOf(n *NetworkData) swapView {
	dex, err := n.Dex()
	v := swapView{dex: dex}
	if err != nil {
		return v
	}
	one := n.Chains[ChainOne]
	if one == nil || one.Swap == nil {
		return v
	}
	sw := one.Swap
	if dex == DexCL {
		if sw.CL != nil {
			v.router, v.quoter, v.factory, v.sbr = sw.CL.SwapRouter, sw.CL.QuoterV2, sw.CL.Factory, sw.CL.SwapBridgeRouter
			if sw.CL.X1Pool != nil {
				v.pool, v.tickSpacing = &sw.CL.X1Pool.Pool, sw.CL.X1Pool.TickSpacing
			}
		}
		return v
	}
	v.router, v.quoter, v.factory, v.sbr = sw.V3SwapRouter, sw.QuoterV2, sw.V3Factory, sw.SwapBridgeRouter
	if sw.X1Pool != nil {
		v.pool, v.fee = &sw.X1Pool.Pool, sw.X1Pool.Fee
	}
	return v
}

// exactInputSelector is the exactInputSingle selector of the DEX's swap router.
func exactInputSelector(dex DexKind) [4]byte {
	if dex == DexCL {
		return CLExactInputSingleSelector
	}
	return ExactInputSingleSelector
}

// Chain returns the chain configuration.
func (n *NetworkData) Chain(k ChainKey) *ChainConfig { return n.Chains[k] }

// ConfigValue resolves a dotted path such as "hyperevm.bridge", "one.swap.swapBridgeRouter", "one.swap.cl.swapRouter" or "bsc.tokens.USDT".
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
	case len(rest) == 3 && rest[0] == "swap" && rest[1] == "cl":
		if c.Swap == nil || c.Swap.CL == nil {
			return nil
		}
		cl := c.Swap.CL
		switch rest[2] {
		case "swapRouter":
			return cl.SwapRouter
		case "quoterV2":
			return cl.QuoterV2
		case "factory":
			return cl.Factory
		case "swapBridgeRouter":
			return cl.SwapBridgeRouter
		case "x1Pool":
			if cl.X1Pool != nil {
				return &cl.X1Pool.Pool
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
