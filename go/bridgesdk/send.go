package bridgesdk

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// TxSigner signs transactions locally (Go is always "Mode L": the SDK knows raw tx, hash and nonce BEFORE broadcast).
type TxSigner struct {
	Address common.Address
	// ChainID the signer is bound to (nil = unknown; the SDK still signs for the quote's chain and re-verifies the sender).
	ChainID *big.Int
	Sign    func(tx *types.Transaction) (*types.Transaction, error)
}

// NewKeyedSigner signs with an in-memory key for the given chain.
func NewKeyedSigner(key *ecdsa.PrivateKey, chainID *big.Int) TxSigner {
	s := types.LatestSignerForChainID(chainID)
	return TxSigner{
		Address: crypto.PubkeyToAddress(key.PublicKey), ChainID: chainID,
		Sign: func(tx *types.Transaction) (*types.Transaction, error) { return types.SignTx(tx, s, key) },
	}
}

// NewTxSignerFromOpts wraps a *bind.TransactOpts (opts.From + opts.Signer), e.g. from a keystore or hardware wallet.
func NewTxSignerFromOpts(opts *bind.TransactOpts, chainID *big.Int) TxSigner {
	return TxSigner{
		Address: opts.From, ChainID: chainID,
		Sign: func(tx *types.Transaction) (*types.Transaction, error) { return opts.Signer(opts.From, tx) },
	}
}

// BSC testnet needs >= 1 gwei or the tx sits in the mempool (see GUIDE FAQ).
var minGasPrice = map[int64]*big.Int{97: big.NewInt(1_000_000_000)}

func (c *Client) gasPrice(ctx context.Context, r RPC, chainID int64) (*big.Int, error) {
	gp, err := r.SuggestGasPrice(ctx)
	if err != nil {
		return nil, err
	}
	if m, ok := minGasPrice[chainID]; ok && gp.Cmp(m) < 0 {
		return m, nil
	}
	return gp, nil
}

func (c *Client) assertSigner(ctx context.Context, s TxSigner, q *Quote) error {
	if s.Address != q.Sender {
		return &SenderMismatchError{Expected: q.Sender, Actual: s.Address}
	}
	if s.ChainID != nil && s.ChainID.Int64() != q.SourceChainID {
		return &NetworkMismatchError{Scope: "signer", Expected: q.SourceChainID, Actual: s.ChainID.String()}
	}
	return c.assertChain(ctx, q.SourceChain)
}

// signAndCheck builds a legacy tx, signs it and verifies the signed transaction BEFORE anything is broadcast: it must be non-nil, legacy,
// replay-protected (EIP-155), signed for the quote's chain id, recovered to the quote's sender, and every field (to, value, data, nonce,
// gas, gas price) must equal the unsigned request. A violation => *SignerMisbehaviorError and nothing is broadcast.
func (c *Client) signAndCheck(ctx context.Context, s TxSigner, q *Quote, r RPC, nonce uint64, to common.Address, data []byte, value *big.Int, gas uint64, gp *big.Int) (*types.Transaction, []byte, error) {
	tx := types.NewTx(&types.LegacyTx{Nonce: nonce, GasPrice: gp, Gas: gas, To: &to, Value: value, Data: data})
	signed, err := s.Sign(tx)
	if err != nil {
		return nil, nil, &SigningError{Cause: err}
	}
	bad := func(field, format string, a ...any) (*types.Transaction, []byte, error) {
		return nil, nil, &SignerMisbehaviorError{Field: field, Detail: fmt.Sprintf(format, a...)}
	}
	if signed == nil {
		return bad("signed", "the signer returned a nil transaction")
	}
	if signed.Type() != types.LegacyTxType {
		return bad("type", "signed transaction type %d, requested legacy", signed.Type())
	}
	if !signed.Protected() {
		return bad("protected", "the signature is not replay-protected (pre-EIP-155 legacy)")
	}
	want := big.NewInt(q.SourceChainID)
	if signed.ChainId().Cmp(want) != 0 {
		return bad("chainId", "signed for chain %s, expected %s", signed.ChainId(), want)
	}
	got, err := types.Sender(types.LatestSignerForChainID(want), signed)
	if err != nil {
		return bad("signature", "does not verify for chain %s: %v", want, err)
	}
	if got != q.Sender {
		return bad("from", "recovered sender %s != quote sender %s", got, q.Sender)
	}
	switch {
	case signed.To() == nil || *signed.To() != to:
		return bad("to", "signed %v != requested %s", signed.To(), to)
	case signed.Value().Cmp(value) != 0:
		return bad("value", "signed %s != requested %s", signed.Value(), value)
	case !bytes.Equal(signed.Data(), data):
		return bad("data", "the signed calldata differs from the requested calldata")
	case signed.Nonce() != nonce:
		return bad("nonce", "signed %d != requested %d", signed.Nonce(), nonce)
	case signed.Gas() != gas:
		return bad("gas", "signed %d != requested %d", signed.Gas(), gas)
	case signed.GasPrice().Cmp(gp) != 0:
		return bad("gasPrice", "signed %s != requested %s", signed.GasPrice(), gp)
	}
	raw, err := signed.MarshalBinary()
	if err != nil {
		return nil, nil, &SigningError{Cause: err}
	}
	return signed, raw, nil
}

func (c *Client) waitReceipt(ctx context.Context, key ChainKey, hash common.Hash, timeout, poll time.Duration) (*types.Receipt, error) {
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	if poll == 0 {
		poll = 3 * time.Second
	}
	r, err := c.rpcFor(ctx, key)
	if err != nil {
		return nil, err
	}
	start := c.now()
	for {
		rec, err := r.TransactionReceipt(ctx, hash)
		if err == nil && rec != nil {
			return rec, nil
		}
		if err != nil && !errors.Is(err, ethereum.NotFound) {
			return nil, err
		}
		if c.now().Sub(start) >= timeout {
			return nil, &WaitTimeoutError{Hash: hash}
		}
		if err := c.sleep(ctx, poll); err != nil {
			return nil, err
		}
	}
}

// EnsureAllowance approves the quote's token if needed (exact amount by default, zero-first when 0 < allowance < needed).
func (c *Client) EnsureAllowance(ctx context.Context, signer TxSigner, qIn *Quote, opts SendOptions) ([]common.Hash, error) {
	q, err := openQuote(qIn) // sealed quotes only; everything below uses the restored private copy
	if err != nil {
		return nil, err
	}
	if err := c.assertQuoteBelongs(q); err != nil { // client binding check against this client's config, also for a quote that needs no approval
		return nil, err
	}
	if q.Approval == nil {
		return nil, nil
	}
	if err := c.assertSigner(ctx, signer, q); err != nil {
		return nil, err
	}
	return c.doApprove(ctx, signer, q, opts)
}

func (c *Client) doApprove(ctx context.Context, signer TxSigner, q *Quote, opts SendOptions) ([]common.Hash, error) {
	a := q.Approval
	if a == nil {
		return nil, nil
	}
	key := q.SourceChain
	out, err := c.callABI(ctx, key, erc20ABI, a.Token, "allowance", nil, q.Sender, a.Spender)
	if err != nil {
		return nil, err
	}
	cur := out[0].(*big.Int)
	if cur.Cmp(a.Amount) >= 0 {
		return nil, nil
	}
	r, err := c.rpcFor(ctx, key)
	if err != nil {
		return nil, err
	}
	var hashes []common.Hash
	step := func(amount *big.Int, label string) error {
		data, err := erc20ABI.Pack("approve", a.Spender, amount)
		if err != nil {
			return err
		}
		nonce, err := r.PendingNonceAt(ctx, q.Sender)
		if err != nil {
			return &ApproveFailedError{Msg: label + " could not be submitted: " + err.Error(), Cause: err}
		}
		gp, err := c.gasPrice(ctx, r, q.SourceChainID)
		if err != nil {
			return &ApproveFailedError{Msg: label + " could not be submitted: " + err.Error(), Cause: err}
		}
		to := a.Token
		gas, err := r.EstimateGas(ctx, ethereum.CallMsg{From: q.Sender, To: &to, Data: data})
		if err != nil {
			return &ApproveFailedError{Msg: label + " could not be submitted: " + err.Error(), Cause: err}
		}
		signed, _, err := c.signAndCheck(ctx, signer, q, r, nonce, a.Token, data, new(big.Int), gas*12/10, gp)
		if err != nil {
			return err
		}
		if err := r.SendTransaction(ctx, signed); err != nil && !isAlreadyKnown(err) {
			var te *RpcTimeoutError
			if errors.As(err, &te) { // the request left: the approve may or may not be on chain. Re-read the allowance before doing anything.
				return &ApproveFailedError{Msg: label + " broadcast timed out; it may or may not be on chain (hash " + signed.Hash().Hex() + "): re-check the allowance before retrying", Hash: signed.Hash(), Cause: err}
			}
			return &ApproveFailedError{Msg: label + " could not be submitted: " + err.Error(), Cause: err}
		}
		hashes = append(hashes, signed.Hash())
		rec, err := c.waitReceipt(ctx, key, signed.Hash(), 0, 0)
		if err != nil {
			return err
		}
		if rec.Status != types.ReceiptStatusSuccessful {
			return &ApproveFailedError{Msg: fmt.Sprintf("%s transaction %s reverted (status 0)", label, signed.Hash()), Hash: signed.Hash()}
		}
		return nil
	}
	if opts.ZeroFirst == ZeroFirstAlways || (opts.ZeroFirst == ZeroFirstAuto && cur.Sign() > 0) {
		if err := step(new(big.Int), "approve(0)"); err != nil {
			return hashes, err
		}
	}
	amount := a.Amount
	if opts.ApproveAmount == ApproveMax {
		amount = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	}
	if err := step(amount, "approve"); err != nil {
		return hashes, err
	}
	return hashes, nil
}

func isAlreadyKnown(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "already known") || strings.Contains(s, "known transaction") || strings.Contains(s, "already imported")
}

func (c *Client) recordDestBlocks(ctx context.Context, q *Quote) map[ChainKey]uint64 {
	keys := map[ChainKey]bool{q.Details.DestChain: true}
	if q.Details.HubChain != "" {
		keys[q.Details.HubChain] = true
	}
	if q.Kind == KindX1 {
		keys[ChainCross] = true
	}
	out := map[ChainKey]uint64{}
	for k := range keys {
		if k == q.SourceChain && q.Kind != KindX3 && q.Kind != KindX1 {
			continue
		}
		r, err := c.rpcFor(ctx, k)
		if err != nil {
			continue
		}
		if n, err := r.BlockNumber(ctx); err == nil {
			out[k] = n
		}
	}
	return out
}

// Send: binding check -> blocking preflight -> approve (if needed) -> re-quote under the immutable limits -> blocking preflight again ->
// simulate -> sign -> broadcast. It returns as soon as the tx is broadcast. NEVER auto-resends: on *BroadcastOutcomeUnknownError use
// FindSourceTransfer / Rebroadcast. It works on a private snapshot of the quote, so the caller mutating its copy can never change what is
// signed; callbacks receive copies as well.
func (c *Client) Send(ctx context.Context, signer TxSigner, q0in *Quote, opts SendOptions) (*SentTx, error) {
	// verify the seal FIRST (before any RPC): shape -> version/sdk -> digest -> strict parse+schema -> public fields vs payload -> restore.
	// From here on ONLY q0 (an unexported copy restored from the payload) is used; the caller's *Quote is never read again.
	q0, err := openQuote(q0in)
	if err != nil {
		return nil, err
	}
	if err := c.assertQuoteBelongs(q0); err != nil { // (0) the quote must belong to THIS client, before anything else
		return nil, err
	}
	if age := c.now().Sub(q0.CreatedAt); age > q0.TTL {
		return nil, &QuoteExpiredError{AgeMs: age.Milliseconds(), TTLMs: q0.TTL.Milliseconds()}
	}
	if err := c.assertSigner(ctx, signer, q0); err != nil { // (1) network + sender, before any approve
		return nil, err
	}
	if err := c.blockingPreflight(ctx, q0, StagePreApprove); err != nil { // (1b) required preflight; nothing approved / signed / sent on failure
		return nil, err
	}
	if _, err := c.doApprove(ctx, signer, q0, opts); err != nil { // (2) approve if needed, receipt status == 1
		return nil, err
	}
	adopted, err := c.buildQuote(ctx, q0.Request, frozenFrom(q0)) // (3) re-quote under immutable limits (*RequoteRequiredError)
	if err != nil {
		var pe *PreflightError
		if errors.As(err, &pe) && pe.Stage == "" { // an error while building the adopted quote (e.g. X3 quote status)
			return nil, &PreflightError{Issues: pe.Issues, Stage: StageRequote}
		}
		return nil, err
	}
	if err := c.assertQuoteBelongs(adopted); err != nil {
		return nil, err
	}
	if adopted.Approval != nil && (q0.Approval == nil || adopted.Approval.Amount.Cmp(q0.Approval.Amount) > 0) {
		if _, err := c.doApprove(ctx, signer, adopted, opts); err != nil {
			return nil, err
		}
	}
	if err := c.blockingPreflight(ctx, adopted, StagePostApprove); err != nil { // (3b) same checks on the adopted quote; the allowance is blocking now
		return nil, err
	}
	key := adopted.SourceChain
	r, err := c.rpcFor(ctx, key)
	if err != nil {
		return nil, err
	}
	to := adopted.Tx.To
	msg := ethereum.CallMsg{From: adopted.Sender, To: &to, Data: adopted.Tx.Data, Value: adopted.Tx.Value}
	revert := func(err error) error {
		var te *RpcTimeoutError
		if errors.As(err, &te) {
			return err // a timeout is not a revert
		}
		rd := extractRevertData(err)
		return &SimulationRevertError{Decoded: DecodeBridgeError(rd), Data: rd, Cause: err}
	}
	if !opts.SkipSimulation {
		if _, err := r.CallContract(ctx, msg, nil); err != nil {
			return nil, revert(err)
		}
	}
	nonce, err := r.PendingNonceAt(ctx, adopted.Sender)
	if err != nil {
		return nil, err
	}
	block, err := r.BlockNumber(ctx)
	if err != nil {
		return nil, err
	}
	cp := Checkpoint{From: adopted.Sender, ChainID: adopted.SourceChainID, PendingNonceBefore: nonce, BlockBefore: block, SrcBridge: adopted.SourceContract, DestBlocks: c.recordDestBlocks(ctx, adopted)}
	intent := &SubmittedIntent{ChainID: adopted.SourceChainID, From: adopted.Sender, To: adopted.Tx.To, Data: cloneBytes(adopted.Tx.Data), Value: cloneBig(adopted.Tx.Value), QuoteID: adopted.ID, AdoptedQuote: adopted, Match: cloneMatch(adopted.Match)}
	if opts.OnSubmitting != nil {
		if err := opts.OnSubmitting(cloneIntent(intent), cloneCheckpoint(cp)); err != nil {
			return nil, err // aborts: nothing is sent
		}
	}
	gp, err := c.gasPrice(ctx, r, adopted.SourceChainID)
	if err != nil {
		return nil, err
	}
	gas, err := r.EstimateGas(ctx, msg)
	if err != nil {
		return nil, revert(err)
	}
	signed, raw, err := c.signAndCheck(ctx, signer, adopted, r, nonce, adopted.Tx.To, adopted.Tx.Data, adopted.Tx.Value, gas*12/10, gp)
	if err != nil {
		return nil, err
	}
	hash := signed.Hash()
	if opts.OnSigned != nil {
		if err := opts.OnSigned(cloneSigned(SignedInfo{Raw: raw, Hash: hash, Nonce: nonce}), cloneIntent(intent)); err != nil {
			return nil, err // aborts: nothing is broadcast
		}
	}
	if err := r.SendTransaction(ctx, signed); err != nil && !isAlreadyKnown(err) {
		// includes *RpcTimeoutError: after the request left, the outcome is unknown and the hash is known. NEVER resend.
		return nil, &BroadcastOutcomeUnknownError{Intent: cloneIntent(intent), Checkpoint: cloneCheckpoint(cp), Hash: hash, Nonce: nonce, Raw: cloneBytes(raw), Cause: err}
	}
	// The SentTx exists BEFORE the callback runs: once broadcast, the call must never look like a pre-send failure (a retry = duplicate send).
	sent := &SentTx{Hash: hash, ChainID: adopted.SourceChainID, Nonce: nonce, Raw: raw, Checkpoint: cp, Intent: intent, Quote: adopted}
	if opts.OnBroadcast != nil {
		func() {
			defer func() {
				if p := recover(); p != nil {
					sent.CallbackError = fmt.Errorf("OnBroadcast panicked: %v (the transaction %s IS broadcast; do not resend)", p, hash)
				}
			}()
			opts.OnBroadcast(hash)
		}()
	}
	return sent, nil
}

// WaitOptions for WaitHandle.
type WaitOptions struct{ Timeout, Poll time.Duration }

// WaitHandle waits for the source receipt and returns the TransferHandle (persist it).
func (c *Client) WaitHandle(ctx context.Context, sent *SentTx, o ...WaitOptions) (*TransferHandle, error) {
	if err := c.assertChain(ctx, sent.Quote.SourceChain); err != nil { // the chain of the RPC is verified before ANY evidence read (also for a SentTx restored after a restart)
		return nil, err
	}
	var w WaitOptions
	if len(o) > 0 {
		w = o[0]
	}
	rec, err := c.waitReceipt(ctx, sent.Quote.SourceChain, sent.Hash, w.Timeout, w.Poll)
	if err != nil {
		return nil, err
	}
	if rec.Status != types.ReceiptStatusSuccessful {
		return nil, &SourceTxFailedError{Hash: sent.Hash}
	}
	return c.buildHandle(c.handleInputFromQuote(sent.Quote, rec, sent.Checkpoint.DestBlocks))
}

// Simulate is an eth_call of the exact final tx (read-only).
func (c *Client) Simulate(ctx context.Context, qIn *Quote) error {
	q, err := c.openQuote(qIn)
	if err != nil {
		return err
	}
	r, err := c.rpcFor(ctx, q.SourceChain)
	if err != nil {
		return err
	}
	to := q.Tx.To
	if _, err := r.CallContract(ctx, ethereum.CallMsg{From: q.Sender, To: &to, Data: q.Tx.Data, Value: q.Tx.Value}, nil); err != nil {
		var te *RpcTimeoutError
		if errors.As(err, &te) {
			return err
		}
		rd := extractRevertData(err)
		return &SimulationRevertError{Decoded: DecodeBridgeError(rd), Data: rd, Cause: err}
	}
	return nil
}

// BuildTx returns the exact transaction the SDK would send.
// A modified quote can never produce modified signable calldata: the seal is verified first (*LimitViolationError otherwise) and the values come from
// the restored private copy.
func (c *Client) BuildTx(qIn *Quote) (to common.Address, data []byte, value *big.Int, chainID int64, from common.Address, err error) {
	q, err := c.openQuote(qIn)
	if err != nil {
		return common.Address{}, nil, nil, 0, common.Address{}, err
	}
	return q.Tx.To, q.Tx.Data, q.Tx.Value, q.SourceChainID, q.Sender, nil
}

// Rebroadcast sends the SAME signed tx again (idempotent; it cannot create a second transfer).
func (c *Client) Rebroadcast(ctx context.Context, chain ChainKey, raw []byte) (common.Hash, error) {
	if err := c.assertChain(ctx, chain); err != nil { // never push a raw tx (or read its receipt) through an RPC whose chain is unverified
		return common.Hash{}, err
	}
	var tx types.Transaction
	if err := tx.UnmarshalBinary(raw); err != nil {
		return common.Hash{}, err
	}
	r, err := c.rpcFor(ctx, chain)
	if err != nil {
		return common.Hash{}, err
	}
	if err := r.SendTransaction(ctx, &tx); err != nil {
		if isAlreadyKnown(err) {
			return tx.Hash(), nil
		}
		// e.g. "nonce too low": only success if THIS tx is on chain
		if rec, e2 := r.TransactionReceipt(ctx, tx.Hash()); e2 == nil && rec != nil {
			return tx.Hash(), nil
		}
		return common.Hash{}, err
	}
	return tx.Hash(), nil
}
