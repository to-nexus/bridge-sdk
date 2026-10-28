package bridgesdk

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

func chainKeyByID(n *NetworkData, id int64) ChainKey {
	for k, c := range n.Chains {
		if c.ChainID == id {
			return k
		}
	}
	return ""
}

// sourceKeyOf is the source chain key of an intent, verified against THIS client's configuration BEFORE any read: the intent's chain id must be a
// configured chain of this network and the checkpoint must agree (otherwise *NetworkMismatchError: no verdict, no evidence read).
func (c *Client) sourceKeyOf(intent *SubmittedIntent, cp Checkpoint) (ChainKey, error) {
	key := chainKeyByID(c.net, intent.ChainID)
	if key == "" {
		return "", &NetworkMismatchError{Scope: "intent.chainId", ExpectedValue: "a configured chain of this network", Actual: fmt.Sprint(intent.ChainID)}
	}
	if cp.ChainID != intent.ChainID {
		return "", &NetworkMismatchError{Scope: "checkpoint.chainId", Expected: intent.ChainID, Actual: fmt.Sprint(cp.ChainID)}
	}
	return key, nil
}

func (c *Client) getReceipt(ctx context.Context, key ChainKey, h common.Hash) (*types.Receipt, error) {
	r, err := c.rpcFor(ctx, key)
	if err != nil {
		return nil, err
	}
	var rec *types.Receipt
	err = c.withBackoff(ctx, func() (e error) {
		rec, e = r.TransactionReceipt(ctx, h)
		if errors.Is(e, ethereum.NotFound) {
			rec, e = nil, nil
		}
		return
	})
	return rec, err
}

// linkCfg is the CONFIGURED SwapBridgeRouter (cross only) and source bridge of a chain: the only emitters whose logs can be attributed.
func (c *Client) linkCfg(key ChainKey) (router, bridge common.Address) {
	if key == ChainCross {
		if sw := c.net.Chains[ChainCross].Swap; sw != nil && sw.SwapBridgeRouter != nil {
			router = addr(sw.SwapBridgeRouter)
		}
	}
	return router, addr(c.net.Chains[key].Bridge)
}

func matchesIntentBridge(m MatchSpec, e *parsedInitiated, from common.Address) bool {
	return matchesBridge(&m, e, from)
}

// FindSourceTransfer searches the source chain for the transfer described by the adopted SubmittedIntent - WITHOUT a tx hash.
// Candidates are matched against the FULL intent (token, recipient, value, both fees, extraData hash), never just from/toChainID.
func (c *Client) FindSourceTransfer(ctx context.Context, intent *SubmittedIntent, cp Checkpoint) (*FindResult, error) {
	key, err := c.sourceKeyOf(intent, cp)
	if err != nil {
		return nil, err
	}
	if err := c.assertChain(ctx, key); err != nil { // before ANY evidence read (logs, receipts, nonce)
		return nil, err
	}
	head, err := c.headBlock(ctx, key)
	if err != nil {
		return nil, err
	}
	m := intent.Match
	var found []SourceCandidate
	if m.Kind == MatchBridge {
		topics := [][]common.Hash{{topicBridgeInitiated}, {common.BigToHash(u(m.ToChainID))}, nil, {common.BytesToHash(intent.From.Bytes())}}
		logs, err := c.scanLogs(ctx, key, cp.SrcBridge, topics, cp.BlockBefore, head)
		if err != nil {
			return nil, err
		}
		for _, l := range logs {
			if e, ok := parseInitiated(l); ok && matchesIntentBridge(m, e, intent.From) {
				found = append(found, SourceCandidate{TxHash: l.TxHash, LogIndex: l.Index})
			}
		}
	} else {
		// X3: the bridge-level `from` is the ROUTER. Search SwapBridge(user, tokenIn, tokenOut), then STRUCTURALLY link it to the bridge event of the same tx
		// (linkSwapTransfer, intent-free) and compare the linked transfer with the intent (matchesSwapIntent: recipient, extraData hash, destination token, ...).
		router := cp.SrcBridge
		topics := [][]common.Hash{{topicSwapBridge}, {common.BytesToHash(intent.From.Bytes())}, {common.BytesToHash(m.TokenIn.Bytes())}, {common.BytesToHash(m.TokenOut.Bytes())}}
		logs, err := c.scanLogs(ctx, key, router, topics, cp.BlockBefore, head)
		if err != nil {
			return nil, err
		}
		cfgRouter, cfgBridge := c.linkCfg(key)
		for _, l := range logs {
			rec, err := c.getReceipt(ctx, key, l.TxHash)
			if err != nil {
				return nil, err
			}
			if rec == nil {
				continue
			}
			lk := linkSwapTransfer(rec, cfgRouter, cfgBridge)
			for i := range lk.linked {
				if lk.linked[i].SwapLogIndex == l.Index && matchesSwapIntent(&lk.linked[i], &m, intent.From, intent.AdoptedQuote.Limits.AmountOutMinimum) {
					found = append(found, SourceCandidate{TxHash: l.TxHash, LogIndex: l.Index})
					break
				}
			}
		}
	}
	r, err := c.rpcFor(ctx, key)
	if err != nil {
		return nil, err
	}
	mined, err := r.NonceAt(ctx, intent.From, nil)
	if err != nil {
		return nil, err
	}
	res := &FindResult{MinedNonce: mined, PendingNonceBefore: cp.PendingNonceBefore}
	switch {
	case len(found) == 0:
		res.Status = FindNotFoundYet
	case len(found) > 1:
		res.Status, res.Candidates = FindAmbiguous, found
	default:
		rec, err := c.getReceipt(ctx, key, found[0].TxHash)
		if err != nil {
			return nil, err
		}
		if rec == nil {
			res.Status = FindNotFoundYet
			break
		}
		h, err := c.buildHandle(c.handleInputFromQuote(intent.AdoptedQuote, rec, cp.DestBlocks))
		if err != nil {
			return nil, err
		}
		res.Status, res.Handle, res.TxHash = FindFound, h, found[0].TxHash
	}
	return res, nil
}

// ReplacementParams for AnalyzeReplacement.
type ReplacementParams struct {
	Intent          *SubmittedIntent
	Checkpoint      Checkpoint
	OriginalHash    common.Hash
	OriginalNonce   uint64
	ReplacementHash common.Hash // optional (wallet history / explorer); otherwise a bounded block scan locates it
	MaxScanBlocks   uint64
}

// AnalyzeReplacement: is a new tx provably safe after a tx was mined at the original nonce? Only when the replacement's receipt proves it
// did NOT perform a bridge transfer. A differing hash alone, or nonce advancement alone, is never evidence.
func (c *Client) AnalyzeReplacement(ctx context.Context, p ReplacementParams) (*ReplacementVerdict, error) {
	intent := p.Intent
	key, err := c.sourceKeyOf(intent, p.Checkpoint)
	if err != nil {
		return nil, err
	}
	if err := c.assertChain(ctx, key); err != nil { // before ANY evidence read: an RPC on another chain can hold a same-account, same-nonce "cancel" tx that proves nothing here
		return nil, err
	}
	r, err := c.rpcFor(ctx, key)
	if err != nil {
		return nil, err
	}
	if rec, err := r.TransactionReceipt(ctx, p.OriginalHash); err == nil && rec != nil {
		return &ReplacementVerdict{Verdict: VerdictOriginalMined, ReplacementHash: p.OriginalHash}, nil
	}
	mined, err := r.NonceAt(ctx, intent.From, nil)
	if err != nil {
		return nil, err
	}
	if mined <= p.OriginalNonce {
		return &ReplacementVerdict{Verdict: VerdictOriginalStillPossible, Reason: "the nonce is not consumed yet: the original may still be mined"}, nil
	}
	rep := p.ReplacementHash
	if rep == (common.Hash{}) {
		head, err := c.headBlock(ctx, key)
		if err != nil {
			return nil, err
		}
		max := p.MaxScanBlocks
		if max == 0 {
			max = 500
		}
		to := p.Checkpoint.BlockBefore + max
		if to > head {
			to = head
		}
		signer := types.LatestSignerForChainID(big.NewInt(intent.ChainID))
	scan:
		for n := p.Checkpoint.BlockBefore; n <= to; n++ {
			blk, err := r.BlockByNumber(ctx, new(big.Int).SetUint64(n))
			if err != nil || blk == nil {
				continue
			}
			for _, t := range blk.Transactions() {
				if from, err := types.Sender(signer, t); err == nil && from == intent.From && t.Nonce() == p.OriginalNonce {
					rep = t.Hash()
					break scan
				}
			}
		}
	}
	if rep == (common.Hash{}) {
		return &ReplacementVerdict{Verdict: VerdictOriginalStillPossible, Reason: "could not locate the tx mined at that nonce; pass ReplacementHash (wallet history / explorer)"}, nil
	}
	if rep == p.OriginalHash {
		return &ReplacementVerdict{Verdict: VerdictOriginalMined, ReplacementHash: rep}, nil
	}
	tx, _, err := r.TransactionByHash(ctx, rep)
	if err != nil || tx == nil {
		return &ReplacementVerdict{Verdict: VerdictOriginalStillPossible, Reason: "the given hash is unknown to the node"}, nil
	}
	rec, err := r.TransactionReceipt(ctx, rep)
	if err != nil {
		rec = nil
	}
	return c.settleReplacement(ctx, key, p, rep, tx, rec)
}

// settleReplacement is THE ONLY FUNCTION THAT CAN RETURN VerdictNewTxAllowed (grep: the constant appears only inside it). It asserts, in this one place, every
// fact that makes a replacement receipt valid evidence that the original can never be mined as a transfer:
//  1. the chain of the RPC is verified (assertChain: the cached eth_chainId check),
//  2. the replacement tx is protected (EIP-155) and carries a chain id equal to the intent's source chain id (a pre-EIP-155 tx has none and is not accepted),
//  3. its recovered sender is the intent's sender and its nonce is the original's nonce,
//  4. its receipt is known and EITHER failed (status 0) OR holds NO transfer-shaped log AT ALL: not one BridgeInitiated and not one SwapBridge log, from ANY
//     emitter, whatever the kind of the original intent. Any transfer-shaped evidence blocks a new transaction (the conservative direction).
//
// A successful receipt WITH such logs is classified (M = user transfers that fully match the intent, D = user transfers that do not, U = evidence that cannot be
// attributed: unknown emitter, from/user that is not the sender and not explained by an X3 link, an unlinked SwapBridge). First rule that applies:
// U non-empty -> VerdictReplacedUnverified | |M| > 1 -> *AmbiguousSourceError | |M| = 1 and D empty -> Found | |M| = 1 and D non-empty -> VerdictReplacedUnverified |
// M empty and D non-empty -> VerdictReplacedByDifferentTransfer | all empty -> (step 4) VerdictNewTxAllowed. Anything else is VerdictOriginalStillPossible (never safe).
func (c *Client) settleReplacement(ctx context.Context, key ChainKey, p ReplacementParams, rep common.Hash, tx *types.Transaction, rec *types.Receipt) (*ReplacementVerdict, error) {
	intent := p.Intent
	if err := c.assertChain(ctx, key); err != nil { // fact 1
		return nil, err
	}
	if !tx.Protected() || tx.ChainId() == nil || tx.ChainId().Cmp(big.NewInt(intent.ChainID)) != 0 { // fact 2
		return &ReplacementVerdict{Verdict: VerdictOriginalStillPossible, Reason: "the replacement tx has no EIP-155 chain id or carries another chain id: it is not evidence for this chain"}, nil
	}
	if from, err := types.Sender(types.LatestSignerForChainID(big.NewInt(intent.ChainID)), tx); err != nil || from != intent.From || tx.Nonce() != p.OriginalNonce { // fact 3
		return &ReplacementVerdict{Verdict: VerdictOriginalStillPossible, Reason: "the given hash is not a tx of this sender at the original nonce"}, nil
	}
	if rec == nil {
		return &ReplacementVerdict{Verdict: VerdictOriginalStillPossible, Reason: "the replacement has no receipt yet"}, nil
	}
	if rec.Status != types.ReceiptStatusSuccessful {
		return &ReplacementVerdict{Verdict: VerdictNewTxAllowed, Reason: "replacement-failed", ReplacementHash: rep}, nil // fact 4
	}
	// 1. collect EVERY transfer-shaped log of the receipt, from any emitter, whatever the intent kind
	var biLogs, sbLogs []*types.Log
	for _, l := range rec.Logs {
		if l == nil || len(l.Topics) == 0 {
			continue
		}
		switch l.Topics[0] {
		case topicBridgeInitiated:
			biLogs = append(biLogs, l)
		case topicSwapBridge:
			sbLogs = append(sbLogs, l)
		}
	}
	if len(biLogs) == 0 && len(sbLogs) == 0 {
		return &ReplacementVerdict{Verdict: VerdictNewTxAllowed, Reason: "replacement-not-a-transfer", ReplacementHash: rep}, nil // fact 4
	}
	// 3. classify each collected log
	cfgRouter, cfgBridge := c.linkCfg(key)
	m := intent.Match
	lk := linkSwapTransfer(rec, cfgRouter, cfgBridge)
	type matched struct {
		ev       TransferEvidence
		toChain  uint64
		index    *big.Int
		biLogIdx uint
	}
	var M []matched
	var D, U []TransferEvidence
	for i := range lk.linked {
		t := &lk.linked[i]
		ev := TransferEvidence{Kind: "swap", Emitter: t.SwapEmitter, LogIndex: t.SwapLogIndex}
		if t.User != intent.From {
			ev.Detail = "SwapBridge user is not the sender"
			U = append(U, ev)
			continue
		}
		if m.Kind == MatchSwap && matchesSwapIntent(t, &m, intent.From, intent.AdoptedQuote.Limits.AmountOutMinimum) {
			M = append(M, matched{ev: ev, toChain: t.ToChainID.Uint64(), index: t.Index, biLogIdx: t.bi.LogIndex})
		} else {
			ev.Detail = "a linked X3 transfer of the sender that does not match the intent"
			D = append(D, ev)
		}
	}
	for _, u := range lk.unlinked {
		U = append(U, TransferEvidence{Kind: "swap", Emitter: u.Emitter, LogIndex: u.LogIndex, Detail: "SwapBridge log from an unconfigured emitter or without a consistent BridgeInitiated link"})
	}
	for _, l := range biLogs {
		configured := l.Address == cfgBridge
		if configured && lk.consumed[l.Index] {
			continue // consumed by an X3 link: not a separate transfer
		}
		var e *parsedInitiated
		if configured {
			if pe, ok := parseInitiated(*l); ok {
				e = pe
			}
		}
		if e != nil && e.From == intent.From {
			if m.Kind == MatchBridge && matchesBridge(&m, e, intent.From) {
				M = append(M, matched{ev: TransferEvidence{Kind: "bridge", Emitter: l.Address, LogIndex: l.Index}, toChain: e.ToChainID.Uint64(), index: e.Index, biLogIdx: l.Index})
			} else {
				D = append(D, TransferEvidence{Kind: "bridge", Emitter: l.Address, LogIndex: l.Index, Detail: "a direct transfer of the sender that does not match the intent"})
			}
			continue
		}
		why := "BridgeInitiated from an unconfigured emitter"
		if configured {
			why = "BridgeInitiated whose from is not the sender and is not explained by an X3 link"
		}
		U = append(U, TransferEvidence{Kind: "bridge", Emitter: l.Address, LogIndex: l.Index, Detail: why})
	}
	evs := func() []TransferEvidence {
		out := make([]TransferEvidence, 0, len(M))
		for _, x := range M {
			out = append(out, x.ev)
		}
		return out
	}
	// 4. the verdict: the first rule that applies
	switch {
	case len(U) > 0:
		return &ReplacementVerdict{Verdict: VerdictReplacedUnverified, ReplacementHash: rep, Matched: evs(), Different: D, Unattributed: U}, nil
	case len(M) > 1:
		cs := make([]SourceCandidateInfo, 0, len(M))
		for _, x := range M {
			cs = append(cs, SourceCandidateInfo{ToChainID: x.toChain, Index: x.index, LogIndex: x.biLogIdx})
		}
		return nil, &AmbiguousSourceError{Msg: "several transfers of the sender in the replacement receipt match the intent; refusing to pick one", Candidates: cs}
	case len(M) == 1 && len(D) == 0:
		in := c.handleInputFromQuote(intent.AdoptedQuote, rec, p.Checkpoint.DestBlocks)
		li := M[0].biLogIdx
		in.logIndex = &li
		h, err := c.buildHandle(in)
		if err != nil {
			return nil, err
		}
		return &ReplacementVerdict{Verdict: VerdictFound, ReplacementHash: rep, Handle: h}, nil
	case len(M) == 1:
		return &ReplacementVerdict{Verdict: VerdictReplacedUnverified, ReplacementHash: rep, Matched: evs(), Different: D}, nil
	case len(D) > 0:
		return &ReplacementVerdict{Verdict: VerdictReplacedByDifferentTransfer, ReplacementHash: rep, Different: D}, nil
	}
	return &ReplacementVerdict{Verdict: VerdictOriginalStillPossible, Reason: "internal: transfer-shaped logs were collected but none could be classified; treated as not safe"}, nil // unreachable in practice; never a new transaction
}
