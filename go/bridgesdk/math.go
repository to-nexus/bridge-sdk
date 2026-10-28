package bridgesdk

import (
	"fmt"
	"math/big"
	"strings"
)

var denominator = big.NewInt(10000)

func bi(v int64) *big.Int { return big.NewInt(v) }
func add(a ...*big.Int) *big.Int {
	r := new(big.Int)
	for _, x := range a {
		r.Add(r, x)
	}
	return r
}

// FloorBps = floor(x * (10000 - slippageBps) / 10000)
func FloorBps(x *big.Int, slippageBps int) (*big.Int, error) {
	if slippageBps < 0 || slippageBps > 10000 {
		return nil, fmt.Errorf("slippageBps must be in [0, 10000], got %d", slippageBps)
	}
	r := new(big.Int).Mul(x, bi(int64(10000-slippageBps)))
	return r.Div(r, denominator), nil
}

// MultihopAmounts: v1 = v2 + nf2 + (ef2 + buffer). The buffer sits in the declared exFee, never in value.
func MultihopAmounts(v2 *big.Int, hub FeeTriple, buffer *big.Int) (ef2Declared, v1 *big.Int, err error) {
	if buffer == nil {
		buffer = new(big.Int)
	}
	if buffer.Sign() < 0 {
		return nil, nil, fmt.Errorf("hop2FeeBuffer must be >= 0")
	}
	if v2.Cmp(hub.MinimumValue) < 0 {
		return nil, nil, fmt.Errorf("v2 %s is below the hub minimumValue %s", v2, hub.MinimumValue)
	}
	ef := add(hub.ExFee, buffer)
	return ef, add(v2, hub.NetworkFee, ef), nil
}

type ForwardSettlement struct {
	Reverted bool
	Value    *big.Int
	ExFee    *big.Int
}

// ResolveForwardAmounts mirrors CrossBridge._resolveForwardAmounts (verified against the Solidity-generated vectors).
func ResolveForwardAmounts(ctxValue, quotedValue, networkFee, rate, denom *big.Int) ForwardSettlement {
	if add(quotedValue, networkFee).Cmp(ctxValue) > 0 {
		return ForwardSettlement{Reverted: true}
	}
	avail := new(big.Int).Sub(ctxValue, networkFee)
	v0 := new(big.Int).Mul(avail, denom)
	v0.Div(v0, add(denom, rate))
	value := v0
	if v0.Cmp(quotedValue) <= 0 {
		value = quotedValue
	}
	return ForwardSettlement{Value: new(big.Int).Set(value), ExFee: new(big.Int).Sub(avail, value)}
}

type ExpectedBridge struct {
	Status                         string // Success | InsufficientForFee | InsufficientValue
	BridgeValue, NetworkFee, ExFee *big.Int
}

// ExpectedBridgeAmount mirrors SwapBridgeRouter._getExpectedBridgeAmount.
func ExpectedBridgeAmount(total, networkFee, rate, minimumValue *big.Int) ExpectedBridge {
	if total.Cmp(networkFee) <= 0 {
		return ExpectedBridge{Status: "InsufficientForFee", BridgeValue: new(big.Int), NetworkFee: networkFee, ExFee: new(big.Int)}
	}
	bv := new(big.Int).Sub(total, networkFee)
	bv.Mul(bv, denominator)
	bv.Div(bv, add(denominator, rate))
	ex := new(big.Int).Mul(bv, rate)
	ex.Div(ex, denominator)
	st := "InsufficientValue"
	if bv.Cmp(minimumValue) >= 0 {
		st = "Success"
	}
	return ExpectedBridge{Status: st, BridgeValue: bv, NetworkFee: networkFee, ExFee: ex}
}

// CoreAmounts floors amount to coreUnit; the remainder (dust) goes back to toDest.
func CoreAmounts(amount, coreUnit *big.Int) (coreAmount, coreAmountEvm, dust *big.Int, err error) {
	if coreUnit.Sign() <= 0 {
		return nil, nil, nil, fmt.Errorf("coreUnit must be > 0")
	}
	coreAmount = new(big.Int).Div(amount, coreUnit)
	coreAmountEvm = new(big.Int).Mul(coreAmount, coreUnit)
	dust = new(big.Int).Sub(amount, coreAmountEvm)
	return
}

// ParseUnits parses "1.5" into wei (18 decimals).
func ParseUnits(s string, decimals int) (*big.Int, error) {
	s = strings.TrimSpace(s)
	parts := strings.SplitN(s, ".", 2)
	frac := ""
	if len(parts) == 2 {
		frac = parts[1]
	}
	if len(frac) > decimals {
		return nil, fmt.Errorf("too many decimals in %q", s)
	}
	frac += strings.Repeat("0", decimals-len(frac))
	v, ok := new(big.Int).SetString(parts[0]+frac, 10)
	if !ok {
		return nil, fmt.Errorf("invalid decimal amount %q", s)
	}
	return v, nil
}

// FormatUnits renders wei as a decimal string.
func FormatUnits(v *big.Int, decimals int) string {
	base := new(big.Int).Exp(bi(10), bi(int64(decimals)), nil)
	q, r := new(big.Int).QuoRem(v, base, new(big.Int))
	if r.Sign() == 0 {
		return q.String()
	}
	f := fmt.Sprintf("%0*s", decimals, r.String())
	return q.String() + "." + strings.TrimRight(f, "0")
}
