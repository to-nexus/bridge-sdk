package bridgesdk

// Deterministic in-package fake chain implementing RPC No network access.

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
)

type revertErr struct {
	data []byte
	msg  string
}

func (e *revertErr) Error() string          { return e.msg }
func (e *revertErr) ErrorData() interface{} { return hexutil.Encode(e.data) }

func revert(hexData string) error {
	return &revertErr{data: common.FromHex(hexData), msg: "execution reverted"}
}

type callCtx struct {
	From  common.Address
	Value *big.Int
}

type txCtx struct {
	From  common.Address
	To    common.Address
	Value *big.Int
	Chain *fakeChain
	Hash  common.Hash
}

type fakeContract struct {
	abi  *abi.ABI
	call map[string]func(args []interface{}, c callCtx) ([]interface{}, error)
	tx   map[string]func(args []interface{}, c txCtx) ([]types.Log, error)
}

type fakeChain struct {
	mu        sync.Mutex
	chainID   int64
	block     uint64
	nonces    map[common.Address]uint64
	contracts map[common.Address]*fakeContract
	logs      []types.Log
	receipts  map[common.Hash]*types.Receipt
	txs       map[common.Hash]*types.Transaction
	blocks    map[uint64][]*types.Transaction
	gasPrice  *big.Int
	// sendMode: "" (mine) | "pending" (accept, never mine) | "throw-no-mine" | "throw-after-mine"
	sendMode        string
	failLogs        int
	unavailableLogs bool
	rawSent         int
	getLogsCalls    []ethereum.FilterQuery
	keys            map[common.Address]*ecdsa.PrivateKey
	// hang: RPC methods ("eth_call", "eth_getLogs", ...) that never answer (they block until the caller's context ends)
	hang map[string]bool
	// calls counts every RPC call (any method) so tests can assert "0 RPC"
	calls int
	// chainIDCalls counts eth_chainId calls only (the chain check); callCount()-chainIDCalls = evidence reads / sends
	chainIDCalls int
	// chainIDErr makes eth_chainId fail with this error (receipts / logs / blocks keep answering normally)
	chainIDErr error
}

func newFakeChain(id int64) *fakeChain {
	return &fakeChain{chainID: id, block: 1000, nonces: map[common.Address]uint64{}, contracts: map[common.Address]*fakeContract{}, receipts: map[common.Hash]*types.Receipt{},
		txs: map[common.Hash]*types.Transaction{}, blocks: map[uint64][]*types.Transaction{}, gasPrice: big.NewInt(1_000_000_000), keys: map[common.Address]*ecdsa.PrivateKey{}, hang: map[string]bool{}}
}

func (f *fakeChain) touch() {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
}

// evidenceCount is every RPC call except eth_chainId.
func (f *fakeChain) evidenceCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls - f.chainIDCalls
}

func (f *fakeChain) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeChain) rawCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rawSent
}

func (f *fakeChain) setHang(method string, on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hang[method] = on
}

// hangIf blocks until the context ends when the method is configured to hang (a hung provider).
func (f *fakeChain) hangIf(ctx context.Context, method string) error {
	f.mu.Lock()
	h := f.hang[method]
	f.mu.Unlock()
	if h {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func (f *fakeChain) deploy(a common.Address, c *fakeContract) { f.contracts[a] = c }

func (f *fakeChain) ChainID(ctx context.Context) (*big.Int, error) {
	f.touch()
	f.mu.Lock()
	f.chainIDCalls++
	f.mu.Unlock()
	if err := f.hangIf(ctx, "eth_chainId"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	e := f.chainIDErr
	f.mu.Unlock()
	if e != nil {
		return nil, e
	}
	return big.NewInt(f.chainID), nil
}
func (f *fakeChain) BlockNumber(ctx context.Context) (uint64, error) {
	f.touch()
	if err := f.hangIf(ctx, "eth_blockNumber"); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.block, nil
}
func (f *fakeChain) SuggestGasPrice(ctx context.Context) (*big.Int, error) {
	f.touch()
	return new(big.Int).Set(f.gasPrice), nil
}
func (f *fakeChain) BalanceAt(ctx context.Context, a common.Address, b *big.Int) (*big.Int, error) {
	f.touch()
	return new(big.Int).Lsh(big.NewInt(1), 90), nil
}
func (f *fakeChain) PendingNonceAt(ctx context.Context, a common.Address) (uint64, error) {
	f.touch()
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nonces[a], nil
}
func (f *fakeChain) NonceAt(ctx context.Context, a common.Address, b *big.Int) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nonces[a], nil
}

func (f *fakeChain) EstimateGas(ctx context.Context, m ethereum.CallMsg) (uint64, error) {
	if _, err := f.CallContract(ctx, m, nil); err != nil {
		return 0, err
	}
	return 200_000, nil
}

func (f *fakeChain) CallContract(ctx context.Context, m ethereum.CallMsg, b *big.Int) ([]byte, error) {
	f.touch()
	if err := f.hangIf(ctx, "eth_call"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if m.To == nil {
		return nil, nil
	}
	c := f.contracts[*m.To]
	if c == nil {
		return nil, nil
	}
	if len(m.Data) < 4 {
		return nil, revert("0x")
	}
	method, err := c.abi.MethodById(m.Data[:4])
	if err != nil {
		return nil, revert("0x")
	}
	args, err := method.Inputs.Unpack(m.Data[4:])
	if err != nil {
		return nil, err
	}
	val := m.Value
	if val == nil {
		val = new(big.Int)
	}
	h := c.call[method.Name]
	if h == nil {
		return nil, nil
	}
	out, err := h(args, callCtx{From: m.From, Value: val})
	if err != nil {
		return nil, err
	}
	if len(method.Outputs) == 0 {
		return nil, nil
	}
	return method.Outputs.Pack(out...)
}

func (f *fakeChain) FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	f.touch()
	if err := f.hangIf(ctx, "eth_getLogs"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getLogsCalls = append(f.getLogsCalls, q)
	if f.unavailableLogs {
		return nil, errors.New("missing trie node / logs pruned")
	}
	if f.failLogs > 0 {
		f.failLogs--
		return nil, errors.New("rate limited")
	}
	from, to := q.FromBlock.Uint64(), q.ToBlock.Uint64()
	var out []types.Log
	for _, l := range f.logs {
		if l.BlockNumber < from || l.BlockNumber > to {
			continue
		}
		if len(q.Addresses) > 0 {
			ok := false
			for _, a := range q.Addresses {
				ok = ok || a == l.Address
			}
			if !ok {
				continue
			}
		}
		match := true
		for i, set := range q.Topics {
			if len(set) == 0 {
				continue
			}
			if i >= len(l.Topics) {
				match = false
				break
			}
			ok := false
			for _, t := range set {
				ok = ok || t == l.Topics[i]
			}
			match = match && ok
		}
		if match {
			out = append(out, l)
		}
	}
	return out, nil
}

func (f *fakeChain) TransactionReceipt(ctx context.Context, h common.Hash) (*types.Receipt, error) {
	f.touch()
	if err := f.hangIf(ctx, "eth_getTransactionReceipt"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.receipts[h]; ok {
		return r, nil
	}
	return nil, ethereum.NotFound
}

func (f *fakeChain) TransactionByHash(ctx context.Context, h common.Hash) (*types.Transaction, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.txs[h]; ok {
		return t, false, nil
	}
	return nil, false, ethereum.NotFound
}

func (f *fakeChain) BlockByNumber(ctx context.Context, n *big.Int) (*types.Block, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return types.NewBlockWithHeader(&types.Header{Number: n}).WithBody(f.blocks[n.Uint64()], nil), nil
}

func (f *fakeChain) signer() types.Signer { return types.LatestSignerForChainID(big.NewInt(f.chainID)) }

func (f *fakeChain) SendTransaction(ctx context.Context, tx *types.Transaction) error {
	f.touch()
	f.mu.Lock()
	f.rawSent++
	f.mu.Unlock()
	if err := f.hangIf(ctx, "eth_sendRawTransaction"); err != nil {
		return err // the request left, no answer ever came: the tx is NOT mined here
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch f.sendMode {
	case "throw-no-mine":
		return errors.New("connection reset by peer")
	case "pending":
		return nil
	}
	from, err := types.Sender(f.signer(), tx)
	if err != nil {
		return err
	}
	if tx.Nonce() < f.nonces[from] {
		return errors.New("nonce too low")
	}
	f.applyTxLocked(from, tx)
	if f.sendMode == "throw-after-mine" {
		return errors.New("timeout while waiting for the node response")
	}
	return nil
}

// applyTx mines a tx built by the test itself (wallet-style or replacement).
func (f *fakeChain) applyTx(from common.Address, to common.Address, data []byte, value *big.Int, nonce *uint64) common.Hash {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.nonces[from]
	if nonce != nil {
		n = *nonce
	}
	tx := types.NewTx(&types.LegacyTx{Nonce: n, GasPrice: big.NewInt(1), Gas: 300000, To: &to, Value: value, Data: data})
	if k := f.keys[from]; k != nil { // a real chain returns signed txs; the SDK recovers the sender from the signature
		tx, _ = types.SignTx(tx, f.signer(), k)
	}
	return f.applyTxFake(from, tx)
}

func (f *fakeChain) applyTxFake(from common.Address, tx *types.Transaction) common.Hash {
	return f.applyTxLocked(from, tx)
}

func (f *fakeChain) applyTxLocked(from common.Address, tx *types.Transaction) common.Hash {
	f.block++
	hash := tx.Hash()
	status := types.ReceiptStatusSuccessful
	var logs []*types.Log
	to := common.Address{}
	if tx.To() != nil {
		to = *tx.To()
	}
	if c := f.contracts[to]; c != nil && len(tx.Data()) >= 4 {
		if m, err := c.abi.MethodById(tx.Data()[:4]); err == nil {
			if h := c.tx[m.Name]; h != nil {
				args, _ := m.Inputs.Unpack(tx.Data()[4:])
				out, err := h(args, txCtx{From: from, To: to, Value: tx.Value(), Chain: f, Hash: hash})
				if err != nil {
					status = types.ReceiptStatusFailed
				} else {
					for i, l := range out {
						l.BlockNumber, l.TxHash, l.Index = f.block, hash, uint(i)
						logs = append(logs, &types.Log{Address: l.Address, Topics: l.Topics, Data: l.Data, BlockNumber: l.BlockNumber, TxHash: hash, Index: uint(i)})
					}
				}
			}
		}
	}
	f.nonces[from] = tx.Nonce() + 1
	for _, l := range logs {
		f.logs = append(f.logs, *l)
	}
	f.receipts[hash] = &types.Receipt{Status: status, Logs: logs, TxHash: hash, BlockNumber: new(big.Int).SetUint64(f.block)}
	f.txs[hash] = tx
	f.blocks[f.block] = append(f.blocks[f.block], tx)
	return hash
}

// mineLogs appends raw logs into a new mined tx (tracking fixtures).
func (f *fakeChain) mineLogs(to common.Address, data []byte, logs []types.Log) common.Hash {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.block++
	tx := types.NewTx(&types.LegacyTx{Nonce: uint64(len(f.txs)), GasPrice: big.NewInt(1), Gas: 1, To: &to, Value: new(big.Int), Data: data})
	hash := tx.Hash()
	var rl []*types.Log
	for i, l := range logs {
		l.BlockNumber, l.TxHash, l.Index = f.block, hash, uint(i)
		f.logs = append(f.logs, l)
		cp := l
		rl = append(rl, &cp)
	}
	f.receipts[hash] = &types.Receipt{Status: types.ReceiptStatusSuccessful, Logs: rl, TxHash: hash, BlockNumber: new(big.Int).SetUint64(f.block)}
	f.txs[hash] = tx
	return hash
}

var _ = fmt.Sprint
var _ = strings.ToLower
