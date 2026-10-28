package bridgesdk

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

func rawFromLog(l types.Log) RawLog {
	t := make([]string, len(l.Topics))
	for i, x := range l.Topics {
		t[i] = x.Hex()
	}
	return RawLog{Address: l.Address.Hex(), Topics: t, Data: "0x" + common.Bytes2Hex(l.Data), TransactionHash: l.TxHash.Hex(), LogIndex: l.Index, BlockNumber: l.BlockNumber}
}

func logFromRaw(r RawLog) types.Log {
	t := make([]common.Hash, len(r.Topics))
	for i, x := range r.Topics {
		t[i] = common.HexToHash(x)
	}
	return types.Log{Address: common.HexToAddress(r.Address), Topics: t, Data: common.FromHex(r.Data), TxHash: common.HexToHash(r.TransactionHash), Index: r.LogIndex, BlockNumber: r.BlockNumber}
}

func (c *Client) headBlock(ctx context.Context, key ChainKey) (uint64, error) {
	r, err := c.rpcFor(ctx, key)
	if err != nil {
		return 0, err
	}
	var n uint64
	err = c.withBackoff(ctx, func() (e error) { n, e = r.BlockNumber(ctx); return })
	return n, err
}

// scanLogs: chunked (default 2 000 blocks), topic-filtered eth_getLogs; each chunk is retried with bounded exponential
// backoff; results are deduped by (txHash, logIndex). A chunk that keeps failing returns an error (the tracker turns it into UNKNOWN).
func (c *Client) scanLogs(ctx context.Context, key ChainKey, address common.Address, topics [][]common.Hash, from, to uint64) ([]types.Log, error) {
	chunk := c.opts.ScanChunkBlocks
	if chunk == 0 {
		chunk = 2000
	}
	r, err := c.rpcFor(ctx, key)
	if err != nil {
		return nil, err
	}
	var out []types.Log
	seen := map[string]bool{}
	for f := from; f <= to; f += chunk {
		t := f + chunk - 1
		if t > to {
			t = to
		}
		var logs []types.Log
		q := ethereum.FilterQuery{FromBlock: new(big.Int).SetUint64(f), ToBlock: new(big.Int).SetUint64(t), Addresses: []common.Address{address}, Topics: topics}
		if err := c.withBackoff(ctx, func() (e error) { logs, e = r.FilterLogs(ctx, q); return }); err != nil {
			return nil, err
		}
		for _, l := range logs {
			k := fmt.Sprintf("%s:%d", l.TxHash, l.Index)
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, l)
		}
	}
	return out, nil
}
