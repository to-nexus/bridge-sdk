// Package runner is the shared helper of the examples. DRY-RUN by default: quote + preflight + buildTx + simulation (read-only).
// Pass -send (and PRIVATE_KEY as a hex string in the environment) to really broadcast. NEVER put a key in source code.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/to-nexus/bridge-sdk/go/bridgesdk"
)

type Args struct {
	Network   bridgesdk.NetworkName
	Amount    *big.Int
	Sender    common.Address
	Recipient common.Address
	Send      bool
	Extra     map[string]string
}

func fmtUnits(v *big.Int) string { return bridgesdk.FormatUnits(v, 18) }

// Run executes one example.
func Run(label string, build func(a Args, net *bridgesdk.NetworkData) (bridgesdk.QuoteRequest, error)) {
	network := flag.String("network", envOr("NETWORK", "testnet"), "mainnet | testnet")
	amount := flag.String("amount", envOr("AMOUNT", "10"), "amount in ONE/token units (decimal)")
	sender := flag.String("sender", envOr("SENDER", "0x000000000000000000000000000000000000dEaD"), "read-only sender for dry-runs (ignored when PRIVATE_KEY is set)")
	rcpt := flag.String("recipient", envOr("RECIPIENT", ""), "recipient (default: sender)")
	send := flag.Bool("send", false, "really broadcast (needs PRIVATE_KEY)")
	noTrack := flag.Bool("no-track", false, "with -send: stop after the TransferHandle (smoke tests)")
	extra := map[string]*string{}
	for _, k := range []string{"recipient-hub", "recipient-core", "tokenIn", "tokenOut", "fee", "to", "slippage-bps", "buffer"} {
		extra[k] = flag.String(k, "", "")
	}
	flag.Parse()

	a := Args{Network: bridgesdk.NetworkName(*network), Send: *send, Extra: map[string]string{}}
	for k, v := range extra {
		if *v != "" {
			a.Extra[k] = *v
		}
	}
	var err error
	if a.Amount, err = bridgesdk.ParseUnits(*amount, 18); err != nil {
		fatal(err)
	}
	var key = os.Getenv("PRIVATE_KEY")
	a.Sender = common.HexToAddress(*sender)
	if key != "" {
		k, err := crypto.HexToECDSA(strings.TrimPrefix(key, "0x"))
		if err != nil {
			fatal(fmt.Errorf("PRIVATE_KEY: %w", err))
		}
		a.Sender = crypto.PubkeyToAddress(k.PublicKey)
	} else if a.Send {
		fatal(errors.New("-send needs PRIVATE_KEY in the environment"))
	}
	a.Recipient = a.Sender
	if *rcpt != "" {
		a.Recipient = common.HexToAddress(*rcpt)
	}

	ctx := context.Background()
	// optional RPC overrides, e.g. RPC_BSC=http://127.0.0.1:8545 (anvil fork) - see tools/fork-test
	urls := map[bridgesdk.ChainKey]string{}
	for _, k := range []bridgesdk.ChainKey{bridgesdk.ChainCross, bridgesdk.ChainBSC, bridgesdk.ChainHyperEVM} {
		if v := os.Getenv("RPC_" + strings.ToUpper(string(k))); v != "" {
			urls[k] = v
		}
	}
	client, err := bridgesdk.NewClient(ctx, a.Network, &bridgesdk.ClientOptions{RPCURLs: urls})
	if err != nil {
		fatal(err)
	}
	req, err := build(a, client.Network())
	if err != nil {
		fatal(err)
	}
	fmt.Printf("== %s on %s (%s)  sender=%s recipient=%s amount=%s\n", label, a.Network, map[bool]string{true: "SEND", false: "dry-run"}[a.Send], a.Sender, a.Recipient, fmtUnits(a.Amount))

	q, err := client.Quote(ctx, req)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("quote %s: route %s source %s(%d) to %s\n", q.ID, q.Route, q.SourceChain, q.SourceChainID, q.Tx.To)
	fmt.Printf("  total spend %s | msg.value %s | fees hop1 nf=%s ef=%s\n", fmtUnits(q.TotalSpend), fmtUnits(q.Tx.Value), fmtUnits(q.Hop1Fees.NetworkFee), fmtUnits(q.Hop1Fees.ExFee))
	fmt.Printf("  limits: maxTotalSpend %s minDelivered %s\n", fmtUnits(q.Limits.MaxTotalSpend), fmtUnits(q.Limits.MinDelivered))
	if q.Approval != nil {
		fmt.Printf("  approve %s spender %s amount %s\n", q.Approval.Token, q.Approval.Spender, fmtUnits(q.Approval.Amount))
	}
	pf, err := client.Preflight(ctx, q)
	if err != nil {
		fatal(err)
	}
	for _, i := range pf.Issues {
		fmt.Printf("  preflight %s %s: %s\n", strings.ToUpper(i.Level), i.Code, i.Message)
	}
	to, data, value, _, _, err := client.BuildTx(q)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("  tx: to=%s value=%s data=0x%x... (%d bytes)\n", to, value, data[:4], len(data))

	// Send runs the blocking subset of preflight and returns *PreflightError. A dry-run reports the same refusal: every error-level issue
	// except INSUFFICIENT_BALANCE (the dry-run sender is a dummy address with no funds, which says nothing about the route).
	blocked := map[string]bool{}
	refusing := map[string]bool{}
	for _, i := range pf.Issues {
		switch {
		case i.Code == "APPROVE_NEEDED":
			blocked["allowance"] = true
		case i.Code == "INSUFFICIENT_BALANCE":
			blocked["balance"] = true
		case i.Level == "error":
			refusing[i.Code] = true
		}
	}
	verdict := ""
	if len(refusing) > 0 {
		verdict = "preflight-refused:" + joinKeys(refusing)
	} else if len(blocked) > 0 {
		parts := []string{}
		for _, k := range []string{"allowance", "balance"} {
			if blocked[k] {
				parts = append(parts, k)
			}
		}
		verdict = "simulation-blocked(" + strings.Join(parts, ",") + ")"
	} else if err := client.Simulate(ctx, q); err != nil {
		name := err.Error()
		var sr *bridgesdk.SimulationRevertError
		if errors.As(err, &sr) && sr.Decoded != nil {
			name = sr.Decoded.Name
		}
		verdict = "simulation-reverted(" + name + ")"
	} else {
		verdict = "simulation-passed"
	}
	suffix := ""
	if !pf.OK {
		suffix = " preflight-issues"
	}
	fmt.Printf("RESULT %s %s encoding-verified %s%s\n", label, a.Network, verdict, suffix)
	if !a.Send {
		return
	}

	k, _ := crypto.HexToECDSA(strings.TrimPrefix(key, "0x"))
	signer := bridgesdk.NewKeyedSigner(k, big.NewInt(q.SourceChainID))
	sent, err := client.Send(ctx, signer, q, bridgesdk.SendOptions{
		OnSubmitting: func(in *bridgesdk.SubmittedIntent, cp bridgesdk.Checkpoint) error {
			fmt.Printf("  SubmittedIntent frozen (persist it!) quote=%s checkpoint=%+v\n", in.QuoteID, cp)
			return nil
		},
		OnSigned: func(s bridgesdk.SignedInfo, _ *bridgesdk.SubmittedIntent) error {
			fmt.Printf("  signed, persist before broadcast: hash=%s nonce=%d\n", s.Hash, s.Nonce)
			return nil
		},
		OnBroadcast: func(h common.Hash) { fmt.Println("  broadcast", h) },
	})
	if err != nil {
		var pe *bridgesdk.PreflightError
		if errors.As(err, &pe) { // blocking preflight (before OR after the approve step): STOP. Nothing was broadcast for the bridge tx.
			codes := map[string]bool{}
			for _, i := range pe.Issues {
				fmt.Printf("  preflight %s %s: %s\n", strings.ToUpper(i.Level), i.Code, i.Message)
				codes[i.Code] = true
			}
			fmt.Printf("RESULT %s %s preflight-refused:%s stage=%s\n", label, a.Network, joinKeys(codes), pe.Stage)
			os.Exit(2)
		}
		fatal(err)
	}
	if sent.CallbackError != nil {
		fmt.Println("  WARNING OnBroadcast callback failed (the tx IS broadcast, do not resend):", sent.CallbackError)
	}
	h, err := client.WaitHandle(ctx, sent)
	if err != nil {
		fatal(err)
	}
	hj, _ := json.Marshal(h)
	fmt.Printf("  source mined. TransferHandle (persist): %s\n", hj)
	if *noTrack {
		return
	}
	res, err := client.Track(ctx, h, bridgesdk.TrackOpts{Timeout: 15 * time.Minute, OnUpdate: func(r *bridgesdk.TrackResult) {
		fmt.Printf("  state %s\n", r.State)
	}})
	if err != nil {
		fatal(err)
	}
	if res.Terminal {
		fmt.Printf("FINAL %s: %s\n", res.State, res.Note)
	} else {
		fmt.Printf("NOT FINAL (timeout) %s: %s\n", res.State, res.Note)
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

// Addr parses an optional address flag (zero address when empty).
func Addr(s string) common.Address {
	if s == "" {
		return common.Address{}
	}
	return common.HexToAddress(s)
}

// Units parses an optional decimal amount flag (nil when empty).
func Units(s string) *big.Int {
	if s == "" {
		return nil
	}
	v, err := bridgesdk.ParseUnits(s, 18)
	if err != nil {
		fatal(err)
	}
	return v
}

// Int parses an optional integer flag (0 when empty).
func Int(s string) int {
	if s == "" {
		return 0
	}
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		fatal(err)
	}
	return n
}

func joinKeys(m map[string]bool) string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return strings.Join(ks, ",")
}

// ParseBps parses a slippage value strictly: base-10 digits only (no sign, space or suffix), fits uint32 (no wrap-around) and < 10000.
func ParseBps(s string) (uint32, error) {
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("slippage-bps %q: %w", s, err)
	}
	if n >= 10000 {
		return 0, fmt.Errorf("slippage-bps %d must be < 10000", n)
	}
	return uint32(n), nil
}

// Bps parses an optional slippage flag: nil (= SDK default 50 bps) when empty, otherwise the explicit value (0 is a valid explicit value).
func Bps(s string) *uint32 {
	if s == "" {
		return nil
	}
	n, err := ParseBps(s)
	if err != nil {
		fatal(err)
	}
	return bridgesdk.Bps(n)
}
