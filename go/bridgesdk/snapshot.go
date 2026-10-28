package bridgesdk

import (
	"math/big"
	"slices"

	"github.com/ethereum/go-ethereum/common"
)

// Immutable snapshots. The SDK never keeps (or hands out) a pointer into caller-owned memory: requests are copied when a quote is built,
// Send works on a private copy of the quote, and callbacks / returned structs get copies. Mutating a *big.Int, a byte slice or a map of
// something the caller passed in (or received) can therefore never change what the SDK signs or reports.

func cloneBig(b *big.Int) *big.Int {
	if b == nil {
		return nil
	}
	return new(big.Int).Set(b)
}

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	return slices.Clone(b)
}

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func cloneFees(f FeeTriple) FeeTriple {
	return FeeTriple{MinimumValue: cloneBig(f.MinimumValue), NetworkFee: cloneBig(f.NetworkFee), ExFee: cloneBig(f.ExFee)}
}

func cloneRequest(r QuoteRequest) QuoteRequest {
	r.Amount = cloneBig(r.Amount)
	r.Hop2FeeBuffer = cloneBig(r.Hop2FeeBuffer)
	r.MaxTotalSpend = cloneBig(r.MaxTotalSpend)
	r.MinDelivered = cloneBig(r.MinDelivered)
	r.Swap = SwapOptions{SlippageBps: clonePtr(r.Swap.SlippageBps), DeadlineSec: clonePtr(r.Swap.DeadlineSec)}
	r.X3 = clonePtr(r.X3)
	return r
}

func cloneMatch(m MatchSpec) MatchSpec {
	m.Value, m.NetworkFee, m.ExFee, m.AmountIn = cloneBig(m.Value), cloneBig(m.NetworkFee), cloneBig(m.ExFee), cloneBig(m.AmountIn)
	return m
}

func cloneQuote(q *Quote) *Quote {
	if q == nil {
		return nil
	}
	c := *q
	c.Request = cloneRequest(q.Request)
	c.Hop1Fees = cloneFees(q.Hop1Fees)
	if q.Hop2Fees != nil {
		f := cloneFees(*q.Hop2Fees)
		c.Hop2Fees = &f
	}
	c.Hop2ExFeeDeclared, c.Value, c.V2, c.TotalSpend = cloneBig(q.Hop2ExFeeDeclared), cloneBig(q.Value), cloneBig(q.V2), cloneBig(q.TotalSpend)
	c.Tx = TxData{To: q.Tx.To, Data: cloneBytes(q.Tx.Data), Value: cloneBig(q.Tx.Value)}
	if q.Approval != nil {
		c.Approval = &Approval{Token: q.Approval.Token, Spender: q.Approval.Spender, Amount: cloneBig(q.Approval.Amount)}
	}
	c.Limits = Limits{MaxTotalSpend: cloneBig(q.Limits.MaxTotalSpend), MinDelivered: cloneBig(q.Limits.MinDelivered), AmountOutMinimum: cloneBig(q.Limits.AmountOutMinimum), MinBridgeValue: cloneBig(q.Limits.MinBridgeValue)}
	c.Match = cloneMatch(q.Match)
	d := q.Details
	d.ExtraData = cloneBytes(q.Details.ExtraData)
	d.ExpectedSwapOut, d.SwapAmountOut, d.CoreAmount, d.CoreDust = cloneBig(d.ExpectedSwapOut), cloneBig(d.SwapAmountOut), cloneBig(d.CoreAmount), cloneBig(d.CoreDust)
	c.Details = d
	return &c
}

func cloneCheckpoint(cp Checkpoint) Checkpoint {
	m := make(map[ChainKey]uint64, len(cp.DestBlocks))
	for k, v := range cp.DestBlocks {
		m[k] = v
	}
	cp.DestBlocks = m
	return cp
}

func cloneIntent(i *SubmittedIntent) *SubmittedIntent {
	if i == nil {
		return nil
	}
	c := *i
	c.Data, c.Value = cloneBytes(i.Data), cloneBig(i.Value)
	c.AdoptedQuote = cloneQuote(i.AdoptedQuote)
	c.Match = cloneMatch(i.Match)
	return &c
}

func cloneSigned(s SignedInfo) SignedInfo {
	s.Raw = cloneBytes(s.Raw)
	return s
}

var _ = common.Address{}
