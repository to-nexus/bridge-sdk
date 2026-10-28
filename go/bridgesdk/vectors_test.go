package bridgesdk

// Pure unit tests against the SAME golden vectors the JS SDK uses (testdata/vectors.json,
// copy of tools/vectors/vectors.json). Encodings come from `cast`; multihop math from the Solidity harness.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

type vectorFile struct {
	ForwardMath []map[string]interface{} `json:"forwardMath"`
	Multihop    []map[string]interface{} `json:"multihop"`
	Encoding    []struct {
		Name     string                 `json:"name"`
		Kind     string                 `json:"kind"`
		Inputs   map[string]interface{} `json:"inputs"`
		Expected map[string]interface{} `json:"expected"`
	} `json:"encoding"`
	X1Floor []map[string]interface{} `json:"x1Floor"`
	X3      []map[string]interface{} `json:"x3"`
	X2Core  []map[string]interface{} `json:"x2Core"`
}

func loadVectors(t *testing.T) *vectorFile {
	b, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectorFile
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return &v
}

func str(m map[string]interface{}, k string) string {
	switch v := m[k].(type) {
	case string:
		return v
	case float64:
		return new(big.Float).SetFloat64(v).Text('f', 0)
	}
	return ""
}
func num(m map[string]interface{}, k string) *big.Int { return bg(str(m, k)) }
func sub(m map[string]interface{}, k string) map[string]interface{} {
	s, _ := m[k].(map[string]interface{})
	return s
}
func hexb(s string) []byte { return common.FromHex(s) }
func eqHex(t *testing.T, name string, got []byte, want string) {
	t.Helper()
	if "0x"+hex.EncodeToString(got) != strings.ToLower(want) {
		t.Fatalf("%s mismatch\n got  0x%x\n want %s", name, got, want)
	}
}

func bridgeIn(m map[string]interface{}) BridgeTokenCall {
	return BridgeTokenCall{ToChainID: num(m, "toChainId").Uint64(), FromToken: common.HexToAddress(str(m, "fromToken")), To: common.HexToAddress(str(m, "to")),
		Value: num(m, "value"), NetworkFee: num(m, "networkFee"), ExFee: num(m, "exFee"), ExtraData: hexb(str(m, "extraData"))}
}
func fwdIn(m map[string]interface{}, inner []byte) ForwardInput {
	return ForwardInput{Hub: common.HexToAddress(str(m, "hub")), DestChainID: num(m, "destChainId").Uint64(), ToDest: common.HexToAddress(str(m, "toDest")), Value2: num(m, "value2"),
		NetworkFee2: num(m, "networkFee2"), ExFee2Declared: num(m, "exFee2Declared"), Inner: inner}
}
func coreIn(m map[string]interface{}) CoreInput {
	return CoreInput{Token: common.HexToAddress(str(m, "token")), CoreRecipient: common.HexToAddress(str(m, "coreRecipient")), Value: num(m, "value")}
}

func TestEncodingVectorsMatchCast(t *testing.T) {
	v := loadVectors(t)
	for _, e := range v.Encoding {
		switch e.Kind {
		case "bridgeToken":
			d, err := EncodeBridgeToken(bridgeIn(e.Inputs))
			if err != nil {
				t.Fatal(err)
			}
			eqHex(t, e.Name, d, e.Expected["calldata"].(string))
		case "multihop":
			inner := []byte{}
			if c, ok := e.Inputs["core"].(map[string]interface{}); ok {
				ed2, err := EncodeCoreExtraData(coreIn(c))
				if err != nil {
					t.Fatal(err)
				}
				eqHex(t, e.Name+" extraData2", ed2, e.Expected["extraData2"].(string))
				if len(ed2) != int(e.Expected["extraData2Bytes"].(float64)) {
					t.Fatalf("extraData2 length %d", len(ed2))
				}
				inner = ed2
			}
			ed1, err := EncodeForwardExtraData(fwdIn(sub(e.Inputs, "forward"), inner))
			if err != nil {
				t.Fatal(err)
			}
			eqHex(t, e.Name+" extraData1", ed1, e.Expected["extraData1"].(string))
			if len(ed1) != int(e.Expected["extraData1Bytes"].(float64)) {
				t.Fatalf("extraData1 length %d", len(ed1))
			}
			call := bridgeIn(sub(e.Inputs, "call"))
			call.ExtraData = ed1
			d, _ := EncodeBridgeToken(call)
			eqHex(t, e.Name+" calldata", d, e.Expected["calldata"].(string))
		case "core":
			ed, err := EncodeCoreExtraData(coreIn(sub(e.Inputs, "core")))
			if err != nil {
				t.Fatal(err)
			}
			eqHex(t, e.Name+" extraData", ed, e.Expected["extraData"].(string))
			call := bridgeIn(sub(e.Inputs, "call"))
			call.ExtraData = ed
			d, _ := EncodeBridgeToken(call)
			eqHex(t, e.Name+" calldata", d, e.Expected["calldata"].(string))
		case "x1":
			s := sub(e.Inputs, "swap")
			ed, err := EncodeX1ExtraData(X1Input{Router: common.HexToAddress(str(s, "router")), TokenIn: common.HexToAddress(str(s, "tokenIn")), TokenOut: common.HexToAddress(str(s, "tokenOut")),
				Fee: uint32(num(s, "fee").Uint64()), Recipient: common.HexToAddress(str(s, "recipient")), Deadline: num(s, "deadline").Uint64(), AmountIn: num(s, "amountIn"), AmountOutMinimum: num(s, "amountOutMinimum")})
			if err != nil {
				t.Fatal(err)
			}
			eqHex(t, e.Name+" extraData", ed, e.Expected["extraData"].(string))
			call := bridgeIn(sub(e.Inputs, "call"))
			call.ExtraData = ed
			d, _ := EncodeBridgeToken(call)
			eqHex(t, e.Name+" calldata", d, e.Expected["calldata"].(string))
		case "x3", "x3eth":
			i := e.Inputs
			d, err := EncodeSwapBridge(SwapBridgeInput{TokenIn: common.HexToAddress(str(i, "tokenIn")), TokenOut: common.HexToAddress(str(i, "tokenOut")), Fee: uint32(num(i, "fee").Uint64()),
				AmountIn: num(i, "amountIn"), AmountOutMinimum: num(i, "amountOutMinimum"), ToChainID: num(i, "toChainId").Uint64(), Recipient: common.HexToAddress(str(i, "recipient")),
				ExtraData: hexb(str(i, "extraData")), Deadline: num(i, "deadline").Uint64(), Native: e.Kind == "x3eth"})
			if err != nil {
				t.Fatal(err)
			}
			eqHex(t, e.Name, d, e.Expected["calldata"].(string))
		default:
			t.Fatalf("unknown vector kind %s", e.Kind)
		}
	}
}

func TestSelectors(t *testing.T) {
	for name, c := range map[string]struct {
		got  [4]byte
		want string
	}{"bridgeTokenForwarded": {ForwardSelector, "55fd0776"}, "transferToCoreFor": {CoreSelector, "9438568d"}, "exactInputSingle": {ExactInputSingleSelector, "414bf389"}} {
		if hex.EncodeToString(c.got[:]) != c.want {
			t.Fatalf("%s selector %x != %s", name, c.got, c.want)
		}
	}
}

func TestForwardMathMatchesSolidityVectors(t *testing.T) {
	v := loadVectors(t)
	if len(v.ForwardMath) < 8 {
		t.Fatal("raw vectors missing")
	}
	for _, m := range v.ForwardMath {
		r := ResolveForwardAmounts(num(m, "ctxValue"), num(m, "quotedValue"), num(m, "networkFee"), num(m, "rate"), num(m, "denominator"))
		if r.Reverted != m["reverted"].(bool) {
			t.Fatalf("%v: reverted=%v want %v", m["name"], r.Reverted, m["reverted"])
		}
		if !r.Reverted && (r.Value.String() != str(m, "value") || r.ExFee.String() != str(m, "exFee")) {
			t.Fatalf("%v: got %s/%s want %s/%s", m["name"], r.Value, r.ExFee, str(m, "value"), str(m, "exFee"))
		}
	}
}

func TestMultihopVectors(t *testing.T) {
	v := loadVectors(t)
	if len(v.Multihop) < 16 {
		t.Fatal("multihop vectors missing")
	}
	for _, m := range v.Multihop {
		name := str(m, "name")
		hq := sub(m, "hubQuote")
		hub := FeeTriple{MinimumValue: num(hq, "minimumValue"), NetworkFee: num(hq, "networkFee"), ExFee: num(hq, "exFee")}
		ef2, v1, err := MultihopAmounts(num(m, "v2"), hub, num(m, "buffer"))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if ef2.String() != str(m, "ef2Declared") || v1.String() != str(m, "v1") {
			t.Fatalf("%s: ef2'=%s v1=%s want %s %s", name, ef2, v1, str(m, "ef2Declared"), str(m, "v1"))
		}
		sq := sub(m, "srcQuote")
		if add(v1, num(sq, "networkFee"), num(sq, "exFee")).String() != str(m, "totalSpend") {
			t.Fatalf("%s: total", name)
		}
		ex := sub(m, "exec")
		r := ResolveForwardAmounts(v1, num(m, "v2"), num(ex, "networkFee"), num(ex, "rate"), num(ex, "denominator"))
		st := sub(m, "settle")
		if r.Reverted != st["reverted"].(bool) {
			t.Fatalf("%s: reverted %v want %v", name, r.Reverted, st["reverted"])
		}
		if r.Reverted {
			continue
		}
		if r.Value.String() != str(st, "value") || r.ExFee.String() != str(st, "exFee") {
			t.Fatalf("%s: settled %s/%s want %s/%s", name, r.Value, r.ExFee, str(st, "value"), str(st, "exFee"))
		}
		vc := sub(m, "verifierCheck")
		exCalc := new(big.Int).Mul(r.Value, num(ex, "rate"))
		exCalc.Div(exCalc, num(ex, "denominator"))
		ok := r.Value.Cmp(num(vc, "minimumValue")) >= 0 && num(ex, "networkFee").Cmp(num(vc, "networkFee")) >= 0 && r.ExFee.Cmp(exCalc) >= 0
		if ok != vc["ok"].(bool) || exCalc.String() != str(vc, "exFee") {
			t.Fatalf("%s: verifier check ok=%v want %v", name, ok, vc["ok"])
		}
	}
}

func TestX1X2X3Arithmetic(t *testing.T) {
	v := loadVectors(t)
	for _, c := range v.X1Floor {
		got, err := FloorBps(num(c, "quote"), int(c["slippageBps"].(float64)))
		if err != nil || got.String() != str(c, "amountOutMinimum") {
			t.Fatalf("x1 floor %v: %v %v", c, got, err)
		}
	}
	if _, err := FloorBps(bi(1), 10001); err == nil {
		t.Fatal("slippage > 10000 must be refused")
	}
	for _, c := range v.X2Core {
		ca, ce, d, err := CoreAmounts(num(c, "amount"), num(c, "coreUnit"))
		if err != nil || ca.String() != str(c, "coreAmount") || ce.String() != str(c, "coreAmountEvm") || d.String() != str(c, "dust") {
			t.Fatalf("x2 %v: %v %v %v", c, ca, ce, d)
		}
	}
	for _, c := range v.X3 {
		q, ex, fr, fc := sub(c, "quote"), sub(c, "expected"), sub(c, "fresh"), sub(c, "freshCheck")
		rate := sub(q, "networkFeeRate")
		slip := int(q["slippageBps"].(float64))
		min := num(q, "min")
		out := num(q, "swapAmountOut")
		floor, _ := FloorBps(out, slip)
		if floor.String() != str(ex, "amountOutMinimum") {
			t.Fatalf("%v floor", c["name"])
		}
		qq := ExpectedBridgeAmount(out, num(rate, "nf"), num(rate, "rate"), min)
		if qq.BridgeValue.String() != str(ex, "bridgeValue") || qq.ExFee.String() != str(ex, "exFee") {
			t.Fatalf("%v bridge value", c["name"])
		}
		minBridge := ExpectedBridgeAmount(floor, num(rate, "nf"), num(rate, "rate"), min).BridgeValue
		if minBridge.String() != str(ex, "minBridgeValue") {
			t.Fatalf("%v minBridge", c["name"])
		}
		at := ExpectedBridgeAmount(floor, num(fr, "nf"), num(fr, "rate"), min)
		if at.BridgeValue.String() != str(fc, "deliveryAtOriginalFloor") || (at.Status == "Success" && at.BridgeValue.Cmp(minBridge) >= 0) != fc["deliveryOk"].(bool) {
			t.Fatalf("%v fresh check: %s", c["name"], at.BridgeValue)
		}
	}
	if str(sub(v.X3[0], "freshCheck"), "deliveryAtOriginalFloor") != "94" || str(sub(v.X3[0], "expected"), "minBridgeValue") != "99" {
		t.Fatal("delivery at the original floor must be 94 < 99")
	}
}

func TestDecodeBridgeErrorTable(t *testing.T) {
	d := DecodeBridgeError(common.FromHex("0x58e88785"))
	if d == nil || d.Name != "BaseBridgeInvalidAmount" {
		t.Fatalf("%v", d)
	}
	data := append(common.FromHex("0x943892eb"), append(common.LeftPadBytes([]byte{5}, 32), common.LeftPadBytes([]byte{4}, 32)...)...)
	d = DecodeBridgeError(data)
	if d == nil || d.Name != "BaseBridgeInvalidValue" || d.Args["expected"].(*big.Int).Int64() != 5 {
		t.Fatalf("%+v", d)
	}
	if DecodeBridgeError(common.FromHex("0x8399ac8d"+strings.Repeat("00", 64))).Name != "BaseBridgeForwardStagedInsufficient" {
		t.Fatal("staged insufficient")
	}
	if DecodeBridgeError(common.FromHex("0xdeadbeef")) != nil || DecodeBridgeError(nil) != nil {
		t.Fatal("unknown must be nil")
	}
	if DecodeBridgeError(common.FromHex("0xa6bec22b")).Hint == "" {
		t.Fatal("hint missing")
	}
}

func TestConfigGateRefusesNullRoutesBeforeAnyRPC(t *testing.T) {
	ctx := context.Background()
	// no RPC injected and no dial possible: the config gate must fire first
	c, err := NewClient(ctx, Mainnet, &ClientOptions{RPCURLs: map[ChainKey]string{ChainCross: "http://127.0.0.1:1", ChainBSC: "http://127.0.0.1:1", ChainHyperEVM: "http://127.0.0.1:1"}})
	if err != nil {
		t.Fatal(err)
	}
	base := QuoteRequest{Amount: n18(10), Sender: senderAdr, Recipient: recipient}
	for _, r := range []Route{RouteOneToHyperEVM, RouteHyperEVMToOne, RouteBSCToOneToHyperEVM, RouteHyperEVMToOneToBSC, RouteOneToHyperCore} {
		req := base
		req.Route = r
		_, err := c.Quote(ctx, req)
		var nc *AddressNotConfiguredError
		if !errors.As(err, &nc) || !strings.Contains(nc.Field, "hyperevm.bridge") || !strings.Contains(nc.Todo, "TODO(PENDING)") {
			t.Fatalf("%s: %v", r, err)
		}
	}
	req := base
	req.Route = RouteSwapBridge
	req.X3 = &X3Request{NativeIn: true, TokenOut: tokenA, ToChain: ChainBSC}
	_, err = c.Quote(ctx, req)
	var nc *AddressNotConfiguredError
	if !errors.As(err, &nc) || !strings.Contains(nc.Field, "swapBridgeRouter") || !strings.Contains(nc.Todo, "TODO(UNCONFIRMED)") {
		t.Fatalf("X3 mainnet: %v", err)
	}
	tc, _ := NewClient(ctx, Testnet, &ClientOptions{RPCURLs: map[ChainKey]string{ChainCross: "http://127.0.0.1:1", ChainBSC: "http://127.0.0.1:1"}})
	req = base
	req.Route = RouteBSCUSDTToONEUSD
	if _, err = tc.Quote(ctx, req); !errors.As(err, &nc) || !strings.Contains(nc.Field, "ONEUSD") {
		t.Fatalf("X1 testnet: %v", err)
	}
}
