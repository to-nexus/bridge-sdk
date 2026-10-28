package bridgesdk

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	binding "github.com/to-nexus/bridge-contracts/binding/go/src"
)

var (
	e18           = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	nativeAdr     = common.HexToAddress(NativeToken)
	recipient     = common.HexToAddress("0x2222222222222222222222222222222222222222")
	senderAdr     = common.HexToAddress("0x1111111111111111111111111111111111111111")
	tokenA        = common.HexToAddress("0xaAaAaAaaAaAaAaaAaAAAAAAAAaaaAaAaAaaAaaAa")
	invalidAmount = "0x58e88785"
)

func n18(v int64) *big.Int { return new(big.Int).Mul(big.NewInt(v), e18) }
func bg(s string) *big.Int { v, _ := new(big.Int).SetString(s, 10); return v }

type feeCfg struct{ min, nf, rate *big.Int }

type world struct {
	t            *testing.T
	network      NetworkName
	net          *NetworkData
	chains       map[ChainKey]*fakeChain
	fees         map[string]feeCfg
	pairs        map[string]binding.IBridgeRegistryTokenPair
	balances     map[string]*big.Int
	allow        map[string]*big.Int
	initIdx      map[string]*big.Int
	finNext      map[string]*big.Int
	pending      map[string][]*big.Int
	paused       map[ChainKey]bool
	whitelist    map[string]bool
	methodChecks map[string]bool
	hyperCoreSet bool
	poolMissing  bool
	override     *NetworkData          // client-side networks override (nil = built-in)
	swapStatus   *uint8                // override the SwapBridgeRouter quote status (e.g. 5 = InvalidSwap)
	maxExtra     map[ChainKey]*big.Int // per-chain maxExtraDataLength (default 1024)
	routerBridge *common.Address
	coreUnit     *big.Int
	x1Out        *big.Int
	swapOut      *big.Int
	approveFails bool
	approveCalls []*big.Int
	onApprove    func()
	clock        time.Time
}

func newWorld(t *testing.T, network NetworkName) *world { return newWorldNet(t, network, nil) }

// newWorldNet: `override` is a client-side networks override; the world then deploys its contracts at the OVERRIDE addresses.
func newWorldNet(t *testing.T, network NetworkName, override *NetworkData) *world {
	nd := override
	if nd == nil {
		var err error
		if nd, err = GetNetwork(network); err != nil {
			t.Fatal(err)
		}
	}
	w := &world{override: override, t: t, network: network, net: nd, chains: map[ChainKey]*fakeChain{}, fees: map[string]feeCfg{}, pairs: map[string]binding.IBridgeRegistryTokenPair{},
		balances: map[string]*big.Int{}, allow: map[string]*big.Int{}, initIdx: map[string]*big.Int{}, finNext: map[string]*big.Int{}, pending: map[string][]*big.Int{},
		paused: map[ChainKey]bool{}, whitelist: map[string]bool{}, methodChecks: map[string]bool{}, hyperCoreSet: true, maxExtra: map[ChainKey]*big.Int{}, coreUnit: new(big.Int).Exp(big.NewInt(10), big.NewInt(10), nil),
		x1Out: n18(1), swapOut: n18(100), clock: time.Unix(1_700_000_000, 0)}
	for _, k := range []ChainKey{ChainOne, ChainBSC, ChainHyperEVM} {
		w.chains[k] = newFakeChain(nd.Chains[k].ChainID)
	}
	for _, k := range []ChainKey{ChainOne, ChainBSC, ChainHyperEVM} {
		w.deployChain(k)
	}
	return w
}

func (w *world) opts() *ClientOptions {
	rpcs := map[ChainKey]RPC{}
	for k, c := range w.chains {
		rpcs[k] = c
	}
	return &ClientOptions{Networks: w.override, RPCs: rpcs, Now: func() time.Time { return w.clock }, Sleep: func(ctx context.Context, d time.Duration) error { w.clock = w.clock.Add(d); return nil }, ScanChunkBlocks: 500}
}

func (w *world) client() *Client {
	c, err := NewClient(context.Background(), w.network, w.opts())
	if err != nil {
		w.t.Fatal(err)
	}
	return c
}

func key(parts ...interface{}) string {
	s := make([]string, len(parts))
	for i, p := range parts {
		s[i] = strings.ToLower(fmt.Sprint(p))
	}
	return strings.Join(s, ":")
}

func (w *world) setFee(k ChainKey, to int64, token common.Address, min, nf, rate *big.Int) {
	w.fees[key(k, to, token)] = feeCfg{min, nf, rate}
}
func (w *world) getFee(k ChainKey, to uint64, token common.Address) feeCfg {
	if f, ok := w.fees[key(k, to, token)]; ok {
		return f
	}
	return feeCfg{n18(5), new(big.Int), new(big.Int)}
}
func (w *world) setPair(k ChainKey, to int64, local, remote common.Address, isOrigin bool) {
	w.pairs[key(k, to, local)] = binding.IBridgeRegistryTokenPair{LocalToken: local, RemoteToken: remote, IsOrigin: isOrigin, PendingAmount: new(big.Int), Deposited: new(big.Int), Minted: new(big.Int).Lsh(big.NewInt(1), 100)}
}
func (w *world) bal(k ChainKey, token, who common.Address) *big.Int {
	if v, ok := w.balances[key(k, token, who)]; ok {
		return v
	}
	return new(big.Int)
}
func (w *world) setBal(k ChainKey, token, who common.Address, v *big.Int) {
	w.balances[key(k, token, who)] = v
}
func (w *world) allowance(k ChainKey, token, owner, spender common.Address) *big.Int {
	if v, ok := w.allow[key(k, token, owner, spender)]; ok {
		return v
	}
	return new(big.Int)
}
func (w *world) setAllow(k ChainKey, token, owner, spender common.Address, v *big.Int) {
	w.allow[key(k, token, owner, spender)] = v
}

func (w *world) deployErc20(k ChainKey, a common.Address) {
	w.chains[k].deploy(a, &fakeContract{
		abi: erc20ABI,
		call: map[string]func([]interface{}, callCtx) ([]interface{}, error){
			"balanceOf": func(args []interface{}, c callCtx) ([]interface{}, error) {
				return []interface{}{w.bal(k, a, args[0].(common.Address))}, nil
			},
			"allowance": func(args []interface{}, c callCtx) ([]interface{}, error) {
				return []interface{}{w.allowance(k, a, args[0].(common.Address), args[1].(common.Address))}, nil
			},
			"decimals": func([]interface{}, callCtx) ([]interface{}, error) { return []interface{}{uint8(18)}, nil },
		},
		tx: map[string]func([]interface{}, txCtx) ([]types.Log, error){
			"approve": func(args []interface{}, c txCtx) ([]types.Log, error) {
				if w.approveFails {
					return nil, fmt.Errorf("approve reverts")
				}
				amt := args[1].(*big.Int)
				w.approveCalls = append(w.approveCalls, amt)
				w.setAllow(k, a, c.From, args[0].(common.Address), amt)
				if w.onApprove != nil {
					w.onApprove()
				}
				return nil, nil
			},
		},
	})
}

func (w *world) deployChain(k ChainKey) {
	cfg := w.net.Chains[k]
	ch := w.chains[k]
	for _, t := range cfg.Tokens {
		if t != nil && t.Kind == "erc20" {
			w.deployErc20(k, common.HexToAddress(t.Address))
		}
	}
	if k == ChainHyperEVM && cfg.Tokens["ONE"] != nil {
		w.deployHyper(common.HexToAddress(cfg.Tokens["ONE"].Address))
	}
	if cfg.Bridge == nil {
		return
	}
	bridgeAddr := common.HexToAddress(*cfg.Bridge)
	verifier := common.HexToAddress("0x00000000000000000000000000000000000000f1")
	if cfg.BridgeVerifier != nil {
		verifier = common.HexToAddress(*cfg.BridgeVerifier)
	}
	executor := common.Address{}
	if (k == ChainOne || k == ChainHyperEVM) && cfg.BridgeExecutor != nil {
		executor = common.HexToAddress(*cfg.BridgeExecutor)
	}
	ch.deploy(bridgeAddr, &fakeContract{
		abi: bridgeABI,
		call: map[string]func([]interface{}, callCtx) ([]interface{}, error){
			"paused":         func([]interface{}, callCtx) ([]interface{}, error) { return []interface{}{w.paused[k]}, nil },
			"bridgeVerifier": func([]interface{}, callCtx) ([]interface{}, error) { return []interface{}{verifier}, nil },
			"bridgeExecutor": func([]interface{}, callCtx) ([]interface{}, error) { return []interface{}{executor}, nil },
			"maxExtraDataLength": func([]interface{}, callCtx) ([]interface{}, error) {
				if m, ok := w.maxExtra[k]; ok {
					return []interface{}{m}, nil
				}
				return []interface{}{big.NewInt(1024)}, nil
			},
			"getTokenPair": func(args []interface{}, c callCtx) ([]interface{}, error) {
				if p, ok := w.pairs[key(k, args[0].(*big.Int), args[1].(common.Address))]; ok {
					return []interface{}{p}, nil
				}
				return []interface{}{binding.IBridgeRegistryTokenPair{PendingAmount: new(big.Int), Deposited: new(big.Int), Minted: new(big.Int)}}, nil
			},
			"allPendingIndex": func(args []interface{}, c callCtx) ([]interface{}, error) {
				p := w.pending[key(k, args[0].(*big.Int))]
				if p == nil {
					p = []*big.Int{}
				}
				return []interface{}{p}, nil
			},
			"getNextFinalizeIndex": func(args []interface{}, c callCtx) ([]interface{}, error) {
				if v, ok := w.finNext[key(k, args[0].(*big.Int))]; ok {
					return []interface{}{v}, nil
				}
				return []interface{}{new(big.Int)}, nil
			},
			"getPendingArguments": func(args []interface{}, c callCtx) ([]interface{}, error) {
				return []interface{}{binding.IBridgeRegistryPendingData{Args: binding.IBridgeRegistryFinalizeArguments{FromChainID: new(big.Int), Index: new(big.Int), Value: new(big.Int), ExtraData: []byte{}}, Status: 3, DelayExpiration: new(big.Int)}}, nil
			},
			"bridgeToken": func(args []interface{}, c callCtx) ([]interface{}, error) {
				_, err := w.checkBridge(k, args, c.From, c.Value)
				return []interface{}{true}, err
			},
		},
		tx: map[string]func([]interface{}, txCtx) ([]types.Log, error){
			"bridgeToken": func(args []interface{}, c txCtx) ([]types.Log, error) { return w.execBridge(k, bridgeAddr, args, c) },
		},
	})
	if cfg.BridgeVerifier != nil {
		ch.deploy(verifier, &fakeContract{abi: verifierABI, call: map[string]func([]interface{}, callCtx) ([]interface{}, error){
			"calculateFee": func(args []interface{}, c callCtx) ([]interface{}, error) {
				f := w.getFee(k, args[0].(*big.Int).Uint64(), args[1].(common.Address))
				ex := new(big.Int).Mul(args[2].(*big.Int), f.rate)
				ex.Div(ex, big.NewInt(10000))
				return []interface{}{f.min, f.nf, ex}, nil
			},
		}})
	}
	if executor != (common.Address{}) {
		ch.deploy(executor, &fakeContract{abi: executorABI, call: map[string]func([]interface{}, callCtx) ([]interface{}, error){
			"isWhitelistedTarget": func(args []interface{}, c callCtx) ([]interface{}, error) {
				return []interface{}{w.whitelist[key(k, args[0].(common.Address))]}, nil
			},
			"isMethodCheckEnabled": func(args []interface{}, c callCtx) ([]interface{}, error) {
				return []interface{}{w.methodChecks[key(k, args[0].(common.Address))]}, nil
			},
			"isWhitelistedMethod": func(args []interface{}, c callCtx) ([]interface{}, error) {
				return []interface{}{w.whitelist[key(k, args[0].(common.Address))]}, nil
			},
		}})
	}
	if k == ChainOne {
		w.deployOneExtras()
	}
}

func (w *world) deployHyper(a common.Address) {
	w.chains[ChainHyperEVM].deploy(a, &fakeContract{
		abi: hyperABI,
		call: map[string]func([]interface{}, callCtx) ([]interface{}, error){
			"isCoreTokenIndexSet": func([]interface{}, callCtx) ([]interface{}, error) { return []interface{}{w.hyperCoreSet}, nil },
			"coreUnit":            func([]interface{}, callCtx) ([]interface{}, error) { return []interface{}{w.coreUnit}, nil },
			"coreTransferableAmount": func(args []interface{}, c callCtx) ([]interface{}, error) {
				v := new(big.Int).Div(args[0].(*big.Int), w.coreUnit)
				return []interface{}{v.Mul(v, w.coreUnit)}, nil
			},
			"balanceOf": func(args []interface{}, c callCtx) ([]interface{}, error) {
				return []interface{}{w.bal(ChainHyperEVM, a, args[0].(common.Address))}, nil
			},
			"allowance": func(args []interface{}, c callCtx) ([]interface{}, error) {
				return []interface{}{w.allowance(ChainHyperEVM, a, args[0].(common.Address), args[1].(common.Address))}, nil
			},
		},
		tx: map[string]func([]interface{}, txCtx) ([]types.Log, error){
			"approve": func(args []interface{}, c txCtx) ([]types.Log, error) {
				w.approveCalls = append(w.approveCalls, args[1].(*big.Int))
				w.setAllow(ChainHyperEVM, a, c.From, args[0].(common.Address), args[1].(*big.Int))
				if w.onApprove != nil {
					w.onApprove()
				}
				return nil, nil
			},
		},
	})
}

func (w *world) deployOneExtras() {
	oneChain := w.net.Chains[ChainOne]
	sw := oneChain.Swap
	if sw == nil {
		return
	}
	ch := w.chains[ChainOne]
	if sw.QuoterV2 != nil {
		ch.deploy(common.HexToAddress(*sw.QuoterV2), &fakeContract{abi: quoterABI, call: map[string]func([]interface{}, callCtx) ([]interface{}, error){
			"quoteExactInputSingle": func([]interface{}, callCtx) ([]interface{}, error) {
				return []interface{}{w.x1Out, new(big.Int), uint32(0), big.NewInt(100000)}, nil
			},
		}})
	}
	if sw.V3Factory != nil && sw.X1Pool != nil {
		pool := common.HexToAddress(sw.X1Pool.Pool)
		ch.deploy(common.HexToAddress(*sw.V3Factory), &fakeContract{abi: v3FactoryABI, call: map[string]func([]interface{}, callCtx) ([]interface{}, error){
			"getPool": func([]interface{}, callCtx) ([]interface{}, error) {
				if w.poolMissing {
					return []interface{}{common.Address{}}, nil
				}
				return []interface{}{pool}, nil
			},
		}})
	}
	if sw.X1Pool != nil {
		ch.deploy(common.HexToAddress(sw.X1Pool.Pool), &fakeContract{abi: v3PoolABI, call: map[string]func([]interface{}, callCtx) ([]interface{}, error){
			"liquidity": func([]interface{}, callCtx) ([]interface{}, error) {
				return []interface{}{new(big.Int).Lsh(big.NewInt(1), 90)}, nil
			},
		}})
	}
	if sw.SwapBridgeRouter != nil {
		router := common.HexToAddress(*sw.SwapBridgeRouter)
		status := func(s string) uint8 {
			switch s {
			case "Success":
				return 1
			case "InsufficientForFee":
				return 3
			}
			return 4
		}
		ch.deploy(router, &fakeContract{abi: sbrABI,
			call: map[string]func([]interface{}, callCtx) ([]interface{}, error){
				"bridge": func([]interface{}, callCtx) ([]interface{}, error) {
					if w.routerBridge != nil {
						return []interface{}{*w.routerBridge}, nil
					}
					return []interface{}{common.HexToAddress(*w.net.Chains[ChainOne].Bridge)}, nil
				},
				"getAmountSwapBridgeOut": func(args []interface{}, c callCtx) ([]interface{}, error) {
					f := w.getFee(ChainOne, args[0].(*big.Int).Uint64(), args[2].(common.Address))
					eb := ExpectedBridgeAmount(w.swapOut, f.nf, f.rate, f.min)
					st := status(eb.Status)
					if w.swapStatus != nil {
						st = *w.swapStatus
					}
					return []interface{}{st, w.swapOut, eb.BridgeValue, eb.NetworkFee, eb.ExFee}, nil
				},
				"getExpectedBridgeAmount": func(args []interface{}, c callCtx) ([]interface{}, error) {
					f := w.getFee(ChainOne, args[0].(*big.Int).Uint64(), args[1].(common.Address))
					eb := ExpectedBridgeAmount(args[2].(*big.Int), f.nf, f.rate, f.min)
					return []interface{}{status(eb.Status), eb.BridgeValue, eb.NetworkFee, eb.ExFee}, nil
				},
			},
			tx: map[string]func([]interface{}, txCtx) ([]types.Log, error){
				"swapBridgeExactInputSingle": func(args []interface{}, c txCtx) ([]types.Log, error) {
					return w.execSwapBridge(router, args, c, false)
				},
				"swapBridgeExactInputSingleETH": func(args []interface{}, c txCtx) ([]types.Log, error) {
					return w.execSwapBridge(router, args, c, true)
				},
			}})
	}
}

func (w *world) checkBridge(k ChainKey, args []interface{}, from common.Address, msgValue *big.Int) (feeCfg, error) {
	to, token, value, nf, ef := args[0].(*big.Int), args[1].(common.Address), args[3].(*big.Int), args[4].(*big.Int), args[5].(*big.Int)
	f := w.getFee(k, to.Uint64(), token)
	calcEx := new(big.Int).Mul(value, f.rate)
	calcEx.Div(calcEx, big.NewInt(10000))
	if !(value.Sign() != 0 && value.Cmp(f.min) >= 0 && nf.Cmp(f.nf) >= 0 && ef.Cmp(calcEx) >= 0) {
		return f, revert(invalidAmount)
	}
	total := add(value, f.nf, calcEx)
	if token == nativeAdr {
		if msgValue.Cmp(total) < 0 {
			return f, revert("0x943892eb")
		}
	} else if from != (common.Address{}) {
		bridge := common.HexToAddress(*w.net.Chains[k].Bridge)
		if w.allowance(k, token, from, bridge).Cmp(total) < 0 {
			return f, revert("0xfb8f41b2")
		}
		if w.bal(k, token, from).Cmp(total) < 0 {
			return f, revert("0xe450d38c")
		}
	}
	return f, nil
}

func (w *world) initiatedLog(k ChainKey, bridgeAddr common.Address, to uint64, fromToken, toToken, from, rcpt common.Address, value, nf, ef *big.Int, extra []byte) types.Log {
	kk := key(k, to)
	idx := w.initIdx[kk]
	if idx == nil {
		idx = new(big.Int)
	}
	w.initIdx[kk] = new(big.Int).Add(idx, big.NewInt(1))
	return w.encodeEvent(bridgeABI, "BridgeInitiated", bridgeAddr, u(to), idx, fromToken, toToken, from, rcpt, value, nf, ef, extra, big.NewInt(1_700_000_000))
}

// encodeEvent builds a log: indexed args -> topics, the rest -> data.
func (w *world) encodeEvent(a *abi.ABI, name string, address common.Address, vals ...interface{}) types.Log {
	return encodeEventT(w.t, a, name, address, vals...)
}

func encodeEventT(t *testing.T, a *abi.ABI, name string, address common.Address, vals ...interface{}) types.Log {
	ev := a.Events[name]
	topics := []common.Hash{ev.ID}
	var nonIdx abi.Arguments
	var nonVals []interface{}
	for i, arg := range ev.Inputs {
		if arg.Indexed {
			switch v := vals[i].(type) {
			case *big.Int:
				topics = append(topics, common.BigToHash(v))
			case common.Address:
				topics = append(topics, common.BytesToHash(v.Bytes()))
			case uint64:
				topics = append(topics, common.BigToHash(u(v)))
			case int:
				topics = append(topics, common.BigToHash(big.NewInt(int64(v))))
			default:
				t.Fatalf("encodeEvent: unsupported indexed %T", v)
			}
		} else {
			nonIdx = append(nonIdx, arg)
			nonVals = append(nonVals, vals[i])
		}
	}
	data, err := nonIdx.Pack(nonVals...)
	if err != nil {
		t.Fatalf("encodeEvent %s: %v", name, err)
	}
	return types.Log{Address: address, Topics: topics, Data: data}
}

func (w *world) execBridge(k ChainKey, bridgeAddr common.Address, args []interface{}, c txCtx) ([]types.Log, error) {
	f, err := w.checkBridge(k, args, c.From, c.Value)
	if err != nil {
		return nil, err
	}
	to, token, rcpt, value := args[0].(*big.Int), args[1].(common.Address), args[2].(common.Address), args[3].(*big.Int)
	calcEx := new(big.Int).Mul(value, f.rate)
	calcEx.Div(calcEx, big.NewInt(10000))
	total := add(value, f.nf, calcEx)
	if token != nativeAdr {
		w.setBal(k, token, c.From, new(big.Int).Sub(w.bal(k, token, c.From), total))
		w.setAllow(k, token, c.From, bridgeAddr, new(big.Int).Sub(w.allowance(k, token, c.From, bridgeAddr), total))
	}
	remote := nativeAdr
	if p, ok := w.pairs[key(k, to, token)]; ok {
		remote = p.RemoteToken
	}
	return []types.Log{w.initiatedLog(k, bridgeAddr, to.Uint64(), token, remote, c.From, rcpt, value, f.nf, calcEx, args[6].([]byte))}, nil
}

func (w *world) execSwapBridge(router common.Address, args []interface{}, c txCtx, native bool) ([]types.Log, error) {
	p := *abi.ConvertType(args[0], new(binding.ISwapBridgeRouterSwapBridgeExactInputSingleParams)).(*binding.ISwapBridgeRouterSwapBridgeExactInputSingleParams)
	if w.swapOut.Cmp(p.AmountOutMinimum) < 0 {
		return nil, revert("0x")
	}
	f := w.getFee(ChainOne, p.BridgeParams.ToChainID.Uint64(), p.TokenOut)
	eb := ExpectedBridgeAmount(w.swapOut, f.nf, f.rate, f.min)
	if eb.Status != "Success" {
		return nil, revert("0x")
	}
	if !native {
		if w.allowance(ChainOne, p.TokenIn, c.From, router).Cmp(p.AmountIn) < 0 {
			return nil, revert("0xfb8f41b2")
		}
		w.setBal(ChainOne, p.TokenIn, c.From, new(big.Int).Sub(w.bal(ChainOne, p.TokenIn, c.From), p.AmountIn))
	}
	hub := common.HexToAddress(*w.net.Chains[ChainOne].Bridge)
	remote := nativeAdr
	if pr, ok := w.pairs[key(ChainOne, p.BridgeParams.ToChainID, p.TokenOut)]; ok {
		remote = pr.RemoteToken
	}
	bi := w.initiatedLog(ChainOne, hub, p.BridgeParams.ToChainID.Uint64(), p.TokenOut, remote, router, p.BridgeParams.Recipient, eb.BridgeValue, eb.NetworkFee, eb.ExFee, p.BridgeParams.ExtraData)
	idx := new(big.Int).Sub(w.initIdx[key(ChainOne, p.BridgeParams.ToChainID)], big.NewInt(1))
	sb := w.encodeEvent(sbrABI, "SwapBridge", router, c.From, p.TokenIn, p.TokenOut, p.AmountIn, w.swapOut, p.BridgeParams.ToChainID, idx, eb.BridgeValue, eb.NetworkFee, eb.ExFee)
	return []types.Log{sb, bi}, nil
}

// testnetWorld: fully working testnet world for R1-R6 / X2 / X3.
func testnetWorld(t *testing.T) *world { return testnetWorldNet(t, nil) }

func testnetWorldNet(t *testing.T, override *NetworkData) *world {
	w := newWorldNet(t, Testnet, override)
	c := w.net.Chains
	hubID := c[ChainOne].ChainID
	tok := func(k ChainKey, n string) common.Address { return common.HexToAddress(c[k].Tokens[n].Address) }
	w.setPair(ChainBSC, hubID, tok(ChainBSC, "ONE"), nativeAdr, true)
	w.setPair(ChainHyperEVM, hubID, tok(ChainHyperEVM, "ONE"), nativeAdr, false)
	w.setPair(ChainOne, c[ChainBSC].ChainID, nativeAdr, tok(ChainBSC, "ONE"), false)
	w.setPair(ChainOne, c[ChainHyperEVM].ChainID, nativeAdr, tok(ChainHyperEVM, "ONE"), true)
	w.setPair(ChainOne, c[ChainBSC].ChainID, tok(ChainOne, "USDTx"), tok(ChainBSC, "USDT"), false)
	w.setPair(ChainOne, c[ChainHyperEVM].ChainID, tok(ChainOne, "USDTx"), tok(ChainHyperEVM, "ONE"), false)
	nf := new(big.Int).Mul(big.NewInt(18), new(big.Int).Exp(big.NewInt(10), big.NewInt(16), nil))
	z := new(big.Int)
	w.setFee(ChainOne, c[ChainBSC].ChainID, nativeAdr, n18(5), nf, z)
	w.setFee(ChainOne, c[ChainHyperEVM].ChainID, nativeAdr, n18(5), nf, z)
	w.setFee(ChainOne, c[ChainBSC].ChainID, tok(ChainOne, "USDTx"), n18(5), nf, z)
	w.setFee(ChainBSC, hubID, tok(ChainBSC, "ONE"), n18(1), z, z)
	w.setFee(ChainHyperEVM, hubID, tok(ChainHyperEVM, "ONE"), n18(5), z, z)
	hub := *c[ChainOne].Bridge
	w.whitelist[key(ChainOne, hub)] = true
	w.whitelist[key(ChainHyperEVM, tok(ChainHyperEVM, "ONE"))] = true
	w.methodChecks[key(ChainOne, hub)] = true
	w.deployErc20(ChainOne, tokenA)
	return w
}

// fundedWallet returns a fresh key + address and funds it everywhere.
func (w *world) fundedWallet() (*ecdsa.PrivateKey, common.Address) {
	k, err := crypto.GenerateKey()
	if err != nil {
		w.t.Fatal(err)
	}
	a := crypto.PubkeyToAddress(k.PublicKey)
	for _, ch := range w.chains {
		ch.keys[a] = k
	}
	c := w.net.Chains
	if t := c[ChainBSC].Tokens["ONE"]; t != nil {
		w.setBal(ChainBSC, common.HexToAddress(t.Address), a, n18(1000))
	}
	if t := c[ChainHyperEVM].Tokens["ONE"]; t != nil {
		w.setBal(ChainHyperEVM, common.HexToAddress(t.Address), a, n18(1000))
	}
	w.setBal(ChainOne, tokenA, a, n18(1000))
	return k, a
}

func (w *world) signer(k *ecdsa.PrivateKey, chain ChainKey) TxSigner {
	return NewKeyedSigner(k, big.NewInt(w.net.Chains[chain].ChainID))
}
