package bridgesdk

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

var zeroAddr = common.Address{}

// Preflight stages of the blocking preflight Send runs twice.
const (
	StagePreApprove  = "pre-approve"  // on the caller's quote, before any approve
	StagePostApprove = "post-approve" // on the adopted quote after approve + re-quote, immediately before the simulation
	StageRequote     = "requote"      // an error while building the adopted quote after the approve
)

// Preflight runs the checks and returns structured issues (it only errors on programming mistakes). Every "error" issue is
// BLOCKING: Send enforces them twice (see blockingPreflight) and returns *PreflightError. An RPC error or timeout during a check is itself
// a blocking PREFLIGHT_INCOMPLETE issue: an unverified check never counts as passed. The allowance is a warning here (Send approves
// first); it is blocking after the approve step.
func (c *Client) Preflight(ctx context.Context, qIn *Quote) (*PreflightReport, error) {
	q, err := c.openQuote(qIn) // a modified quote is never reported as passing
	if err != nil {
		return nil, err
	}
	return c.preflightAt(ctx, q, "")
}

// blockingPreflight is the BLOCKING subset Send uses: every error issue at the given stage => *PreflightError{Stage}.
func (c *Client) blockingPreflight(ctx context.Context, q *Quote, stage string) error {
	rep, err := c.preflightAt(ctx, q, stage)
	if err != nil {
		return err
	}
	var blocking []PreflightIssue
	for _, i := range rep.Issues {
		if i.Level == "error" {
			blocking = append(blocking, i)
		}
	}
	if len(blocking) > 0 {
		return &PreflightError{Issues: blocking, Stage: stage}
	}
	return nil
}

func (c *Client) preflightAt(ctx context.Context, q *Quote, stage string) (*PreflightReport, error) {
	rep := &PreflightReport{}
	add := func(name string, ok bool, detail string, issue *PreflightIssue) {
		rep.Checks = append(rep.Checks, PreflightCheck{Name: name, OK: ok, Detail: detail})
		if !ok && issue != nil {
			rep.Issues = append(rep.Issues, *issue)
		}
	}
	guard := func(name string, fn func() error) {
		if err := fn(); err != nil {
			add(name, false, err.Error(), &PreflightIssue{Level: "error", Code: "PREFLIGHT_INCOMPLETE", Message: name + ": could not be verified: " + err.Error(),
				Hint: "RPC read failed or timed out; an unverified check is never treated as passed. Retry or use another RPC.", Check: name, Error: err.Error()})
		}
	}
	chains := c.net.Chains
	srcKey := q.SourceChain
	toChainID := q.Details.Hop1ToChainID
	srcToken := common.HexToAddress(NativeToken)
	if q.Match.Kind == MatchBridge {
		srcToken = q.Match.FromToken
	}

	keys := []ChainKey{srcKey}
	if q.Details.HubChain != "" && q.Details.HubChain != srcKey {
		keys = append(keys, q.Details.HubChain)
	}
	core := q.Details.Hop1Kind == HopX2 || q.Details.Hop2Kind == HopX2
	if core {
		keys = append(keys, ChainHyperEVM)
	}
	for _, k := range keys {
		if err := c.assertChain(ctx, k); err != nil {
			var nm *NetworkMismatchError
			if errors.As(err, &nm) {
				add(fmt.Sprintf("chainId(%s)", k), false, err.Error(), &PreflightIssue{Level: "error", Code: "NETWORK_MISMATCH", Message: err.Error(), Hint: "Point the RPC at the right network."})
			} else {
				name := fmt.Sprintf("chainId(%s)", k)
				add(name, false, err.Error(), &PreflightIssue{Level: "error", Code: "PREFLIGHT_INCOMPLETE", Message: name + ": could not be verified: " + err.Error(),
					Hint: "RPC read failed or timed out; an unverified check is never treated as passed. Retry or use another RPC.", Check: name, Error: err.Error()})
			}
		} else {
			add(fmt.Sprintf("chainId(%s)", k), true, fmt.Sprint(chains[k].ChainID), nil)
		}
	}
	if len(rep.Issues) > 0 { // a chain id that is wrong or could not be verified: report it and read NOTHING further from these RPCs (every issue is blocking anyway)
		rep.OK = false
		return rep, nil
	}

	srcBridge := q.SourceBridge
	guard("source bridge paused()", func() error {
		out, err := c.callABI(ctx, srcKey, bridgeABI, srcBridge, "paused", nil)
		if err != nil {
			return err
		}
		p := out[0].(bool)
		add("source bridge paused()", !p, fmt.Sprint(p), &PreflightIssue{Level: "error", Code: "BRIDGE_PAUSED", Message: "the source bridge is paused", Hint: "EnforcedPause: bridge under maintenance."})
		return nil
	})

	if q.Match.Kind == MatchBridge {
		guard("pair", func() error {
			p, err := c.readPair(ctx, srcKey, srcBridge, toChainID, srcToken)
			if err != nil {
				return err
			}
			reg := p.LocalToken == srcToken
			add("pair registered", reg, "", &PreflightIssue{Level: "error", Code: "PAIR_NOT_REGISTERED", Message: fmt.Sprintf("no pair for %s -> %d", srcToken, toChainID), Hint: "RegistryNotExistToken"})
			if reg {
				add("pair not paused", !p.Paused, fmt.Sprint(p.Paused), &PreflightIssue{Level: "error", Code: "PAIR_PAUSED", Message: "the token pair is paused", Hint: "RegistryTokenPaused"})
				if !p.IsOrigin {
					add("wrapped stock (minted >= value)", p.Minted.Cmp(q.Match.Value) >= 0, "minted="+p.Minted.String(), &PreflightIssue{Level: "error", Code: "INSUFFICIENT_MINTED", Message: fmt.Sprintf("minted %s < value %s", p.Minted, q.Match.Value), Hint: "BaseBridgeInvalidValue: the wrapped-token stock on this pair is too low."})
				}
			}
			return nil
		})
		guard("source extraData length", func() error {
			max, err := c.maxExtraData(ctx, srcKey, srcBridge)
			if err != nil {
				return err
			}
			l := big.NewInt(int64(len(q.Details.ExtraData)))
			add("extraData length (source)", max.Sign() == 0 || l.Cmp(max) <= 0, fmt.Sprintf("%s/%s", l, max), &PreflightIssue{Level: "error", Code: "EXTRADATA_TOO_LONG_SOURCE", Message: fmt.Sprintf("extraData %s B > the source bridge max %s B", l, max), Hint: "BaseBridgeExtraDataTooLong"})
			return nil
		})
	}

	// X3: router.bridge() == hub, and the tokenOut pair on the hub
	if q.Kind == KindX3 && q.Match.Kind == MatchSwap {
		// fresh quote status through the timed RPC: a status other than Success (1) blocks at every stage, before any approve
		guard("x3 quote status", func() error {
			sender := q.Sender
			var poolFee uint32
			if q.Request.X3 != nil {
				poolFee = q.Request.X3.PoolFee
			}
			out, err := c.callABI(ctx, ChainCross, sbrABI, q.SourceContract, "getAmountSwapBridgeOut", &sender, u(q.Match.ToChainID), q.Match.TokenIn, q.Match.TokenOut, u(uint64(poolFee)), q.Match.AmountIn)
			if err != nil {
				return err
			}
			status := out[0].(uint8)
			name := quoteStatusName(status)
			add("router quote status == Success", status == 1, name, &PreflightIssue{Level: "error", Code: "X3_QUOTE_" + name, Message: "SwapBridgeRouter quote status " + name,
				Hint: "NoPair: tokenOut has no hub pair to the destination; InsufficientForFee/InsufficientValue: amount too small; InvalidSwap: no pool / liquidity."})
			return nil
		})
		guard("x3 router.bridge()", func() error {
			out, err := c.callABI(ctx, ChainCross, sbrABI, q.SourceContract, "bridge", nil)
			if err != nil {
				return err
			}
			b := out[0].(common.Address)
			add("router.bridge() == hub", b == q.SourceBridge, b.Hex(), &PreflightIssue{Level: "error", Code: "ROUTER_BRIDGE_MISMATCH", Message: fmt.Sprintf("SwapBridgeRouter.bridge() is %s, expected the hub %s", b, q.SourceBridge)})
			return nil
		})
		guard("x3 tokenOut pair", func() error {
			p, err := c.readPair(ctx, ChainCross, q.SourceBridge, q.Match.ToChainID, q.Match.TokenOut)
			if err != nil {
				return err
			}
			reg := p.LocalToken == q.Match.TokenOut
			add("pair registered (tokenOut)", reg, "", &PreflightIssue{Level: "error", Code: "PAIR_NOT_REGISTERED", Message: fmt.Sprintf("tokenOut %s has no hub pair to chain %d", q.Match.TokenOut, q.Match.ToChainID), Hint: "RegistryNotExistToken"})
			if reg {
				add("pair not paused (tokenOut)", !p.Paused, fmt.Sprint(p.Paused), &PreflightIssue{Level: "error", Code: "PAIR_PAUSED", Message: "the tokenOut pair is paused", Hint: "RegistryTokenPaused"})
			}
			return nil
		})
	}

	if q.Kind == KindMultihop || q.Kind == KindX1 {
		hub := addr(chains[ChainCross].Bridge)
		var executor common.Address
		guard("hub paused()", func() error {
			out, err := c.callABI(ctx, ChainCross, bridgeABI, hub, "paused", nil)
			if err != nil {
				return err
			}
			add("hub paused()", !out[0].(bool), "", &PreflightIssue{Level: "error", Code: "HUB_PAUSED", Message: "the hub bridge is paused"})
			return nil
		})
		guard("hub executor", func() error {
			out, err := c.callABI(ctx, ChainCross, bridgeABI, hub, "bridgeExecutor", nil)
			if err != nil {
				return err
			}
			executor = out[0].(common.Address)
			add("hub bridgeExecutor() != 0", executor != zeroAddr, executor.Hex(), &PreflightIssue{Level: "error", Code: "HUB_EXECUTOR_UNSET", Message: "hub executor is not set", Hint: "extraData cannot be executed on the hub."})
			return nil
		})
		if q.Kind == KindMultihop {
			guard("hub pair(dest, NATIVE)", func() error {
				p, err := c.readPair(ctx, ChainCross, hub, q.Details.DestChainID, common.HexToAddress(NativeToken))
				if err != nil {
					return err
				}
				reg := p.LocalToken == common.HexToAddress(NativeToken)
				add("hub pair (dest, NATIVE) registered", reg, "", &PreflightIssue{Level: "error", Code: "HUB_PAIR_NOT_REGISTERED", Message: fmt.Sprintf("hub has no NATIVE pair to chain %d", q.Details.DestChainID)})
				if reg {
					add("hub pair not paused", !p.Paused, "", &PreflightIssue{Level: "error", Code: "HUB_PAIR_PAUSED", Message: "hub pair is paused"})
				}
				return nil
			})
			guard("hub inner extraData length", func() error {
				max, err := c.maxExtraData(ctx, ChainCross, hub)
				if err != nil {
					return err
				}
				// the HUB limit applies to the decoded INNER extraData (what the hub forwards in hop 2: 88 B X2 payload, 0 B without core), not to the outer payload
				fwd, err := decodeForward(q.Details.ExtraData)
				if err != nil {
					return err
				}
				l := big.NewInt(int64(len(fwd.Inner)))
				add("inner extraData length (hub)", max.Sign() == 0 || l.Cmp(max) <= 0, fmt.Sprintf("%s/%s", l, max), &PreflightIssue{Level: "error", Code: "EXTRADATA_TOO_LONG_HUB", Message: fmt.Sprintf("inner extraData %s B > hub max %s B", l, max)})
				return nil
			})
		}
		if executor != zeroAddr {
			whitelist := func(label string, target common.Address, sel [4]byte) error {
				wl, err := c.callABI(ctx, ChainCross, executorABI, executor, "isWhitelistedTarget", nil, target)
				if err != nil {
					return err
				}
				mc, err := c.callABI(ctx, ChainCross, executorABI, executor, "isMethodCheckEnabled", nil, target)
				if err != nil {
					return err
				}
				wm := true
				if mc[0].(bool) {
					r, err := c.callABI(ctx, ChainCross, executorABI, executor, "isWhitelistedMethod", nil, target, sel)
					if err != nil {
						return err
					}
					wm = r[0].(bool)
				}
				add("executor whitelists "+label, wl[0].(bool) && wm, fmt.Sprintf("target=%v methodCheck=%v method=%v", wl[0], mc[0], wm), &PreflightIssue{Level: "error", Code: "EXECUTOR_NOT_WHITELISTED", Message: "hub executor does not whitelist " + label, Hint: "BETargetNotWhitelisted / BEMethodNotWhitelisted - contact the operator; the transfer would fall back to a plain payout."})
				return nil
			}
			if q.Kind == KindMultihop {
				guard("executor whitelist (hub forward)", func() error { return whitelist("hub + bridgeTokenForwarded", hub, ForwardSelector) })
			}
			if q.Kind == KindX1 {
				router := addr(chains[ChainCross].Swap.V3SwapRouter)
				guard("executor whitelist (V3 router)", func() error { return whitelist("V3 SwapRouter + exactInputSingle", router, ExactInputSingleSelector) })
				guard("x1 pool exists", func() error {
					sw := chains[ChainCross].Swap
					if sw.V3Factory == nil {
						return nil
					}
					out, err := c.callABI(ctx, ChainCross, v3FactoryABI, addr(sw.V3Factory), "getPool", nil, common.HexToAddress(chains[ChainCross].Tokens["USDTx"].Address), common.HexToAddress(chains[ChainCross].Tokens["ONEUSD"].Address), u(uint64(sw.X1Pool.Fee)))
					if err != nil {
						return err
					}
					got := out[0].(common.Address)
					add("factory.getPool(USDTx, ONEUSD, fee) == configured pool", got == common.HexToAddress(sw.X1Pool.Pool), got.Hex(), &PreflightIssue{Level: "error", Code: "POOL_NOT_FOUND", Message: fmt.Sprintf("factory has no pool %s for (USDTx, ONEUSD, fee %d)", sw.X1Pool.Pool, sw.X1Pool.Fee)})
					return nil
				})
				guard("x1 pool liquidity", func() error {
					out, err := c.callABI(ctx, ChainCross, v3PoolABI, common.HexToAddress(chains[ChainCross].Swap.X1Pool.Pool), "liquidity", nil)
					if err != nil {
						return err
					}
					add("USDTx/ONEUSD pool liquidity > 0", out[0].(*big.Int).Sign() > 0, out[0].(*big.Int).String(), &PreflightIssue{Level: "error", Code: "POOL_NO_LIQUIDITY", Message: "the swap pool has no liquidity"})
					return nil
				})
			}
		}
	}

	// the DESTINATION bridge (every bridge the route touches after the source): paused()==false on a chain-verified RPC (R1-R4, X2, X3 bridge leg, the final
	// destination of R5/R6; for X1 the destination IS the hub: HUB_PAUSED above). The destination PAIR check is deliberately not extended (it needs a per-hop
	// (query chain, counterpart chainId, local token, expected remote token) table). An RPC error / timeout / chain mismatch is blocking (PREFLIGHT_INCOMPLETE).
	if destKey := q.Details.DestChain; q.Kind != KindX1 && destKey != srcKey {
		guard("DEST_BRIDGE_PAUSED", func() error {
			if err := c.assertChain(ctx, destKey); err != nil {
				return err
			}
			out, err := c.callABI(ctx, destKey, bridgeABI, addr(chains[destKey].Bridge), "paused", nil)
			if err != nil {
				return err
			}
			p := out[0].(bool)
			add("destination bridge paused()", !p, fmt.Sprintf("%s: %v", destKey, p), &PreflightIssue{Level: "error", Code: "DEST_BRIDGE_PAUSED", Message: fmt.Sprintf("the destination bridge on %s is paused", destKey), Hint: "EnforcedPause on the destination: the source transfer would be locked/burned and then wait in the pending queue. Wait for the maintenance to end."})
			return nil
		})
	}

	if core {
		guard("hypercore", func() error {
			he := chains[ChainHyperEVM]
			token := common.HexToAddress(he.Tokens["ONE"].Address)
			if err := c.assertChain(ctx, ChainHyperEVM); err != nil {
				return err
			}
			set, err := c.callABI(ctx, ChainHyperEVM, hyperABI, token, "isCoreTokenIndexSet", nil)
			if err != nil {
				return err
			}
			add("token.isCoreTokenIndexSet()", set[0].(bool), "", &PreflightIssue{Level: "error", Code: "CORE_NOT_LINKED", Message: "the HyperEVM token is not linked to HyperCore (isCoreTokenIndexSet=false)", Hint: "HyperMintableERC20CoreTokenIndexNotSet - X2 is unavailable until the link is set."})
			ex, err := c.callABI(ctx, ChainHyperEVM, bridgeABI, addr(he.Bridge), "bridgeExecutor", nil)
			if err != nil {
				return err
			}
			exec := ex[0].(common.Address)
			wl, err := c.callABI(ctx, ChainHyperEVM, executorABI, exec, "isWhitelistedTarget", nil, token)
			if err != nil {
				return err
			}
			mc, err := c.callABI(ctx, ChainHyperEVM, executorABI, exec, "isMethodCheckEnabled", nil, token)
			if err != nil {
				return err
			}
			wm := true
			if mc[0].(bool) {
				r, err := c.callABI(ctx, ChainHyperEVM, executorABI, exec, "isWhitelistedMethod", nil, token, CoreSelector)
				if err != nil {
					return err
				}
				wm = r[0].(bool)
			}
			add("HyperEVM executor whitelists token (+ transferToCoreFor when the method check is on)", wl[0].(bool) && wm, fmt.Sprintf("target=%v methodCheck=%v method=%v", wl[0], mc[0], wm), &PreflightIssue{Level: "error", Code: "EXECUTOR_NOT_WHITELISTED", Message: "HyperEVM executor does not whitelist the ONE token / transferToCoreFor", Hint: "The deposit would fall back to plain ONE on HyperEVM."})
			amt := q.Value
			if q.V2 != nil {
				amt = q.V2
			}
			t, err := c.callABI(ctx, ChainHyperEVM, hyperABI, token, "coreTransferableAmount", nil, amt)
			if err != nil {
				return err
			}
			add("coreTransferableAmount(value) > 0", t[0].(*big.Int).Sign() > 0, t[0].(*big.Int).String(), &PreflightIssue{Level: "error", Code: "CORE_AMOUNT_ZERO", Message: "amount is below 1 Core wei after flooring", Hint: "CoreAmountBelowOneCoreWei"})
			return nil
		})
	}

	guard("balance", func() error {
		if q.Approval != nil {
			bal, err := c.callABI(ctx, srcKey, erc20ABI, q.Approval.Token, "balanceOf", nil, q.Sender)
			if err != nil {
				return err
			}
			b := bal[0].(*big.Int)
			add("token balance >= total", b.Cmp(q.Approval.Amount) >= 0, fmt.Sprintf("%s / %s", b, q.Approval.Amount), &PreflightIssue{Level: "error", Code: "INSUFFICIENT_BALANCE", Message: fmt.Sprintf("balance %s < required %s", b, q.Approval.Amount), Hint: "ERC20InsufficientBalance"})
			al, err := c.callABI(ctx, srcKey, erc20ABI, q.Approval.Token, "allowance", nil, q.Sender, q.Approval.Spender)
			if err != nil {
				return err
			}
			a := al[0].(*big.Int)
			allow := &PreflightIssue{Level: "warn", Code: "APPROVE_NEEDED", Message: fmt.Sprintf("allowance %s < %s: Send will approve first", a, q.Approval.Amount), Hint: fmt.Sprintf("approve(spender=%s, amount=%s)", q.Approval.Spender, q.Approval.Amount)}
			if stage == StagePostApprove {
				allow = &PreflightIssue{Level: "error", Code: "ALLOWANCE_INSUFFICIENT", Message: fmt.Sprintf("allowance %s < %s after the approve step", a, q.Approval.Amount), Hint: fmt.Sprintf("approve(spender=%s, amount=%s)", q.Approval.Spender, q.Approval.Amount)}
			}
			add("allowance >= total", a.Cmp(q.Approval.Amount) >= 0, fmt.Sprintf("%s / %s", a, q.Approval.Amount), allow)
			return nil
		}
		r, err := c.rpcFor(ctx, srcKey)
		if err != nil {
			return err
		}
		b, err := r.BalanceAt(ctx, q.Sender, nil)
		if err != nil {
			return err
		}
		add("native balance >= value", b.Cmp(q.Tx.Value) >= 0, fmt.Sprintf("%s / %s", b, q.Tx.Value), &PreflightIssue{Level: "error", Code: "INSUFFICIENT_BALANCE", Message: fmt.Sprintf("native balance %s < required %s (gas not included)", b, q.Tx.Value)})
		return nil
	})

	rep.OK = true
	for _, i := range rep.Issues {
		if i.Level == "error" {
			rep.OK = false
		}
	}
	return rep, nil
}
