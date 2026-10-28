package bridgesdk

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	binding "github.com/to-nexus/bridge-contracts/binding/go/src"
)

//go:embed abi_extra.json
var abiExtraJSON []byte

func mustMeta(m interface{ GetAbi() (*abi.ABI, error) }) *abi.ABI {
	a, err := m.GetAbi()
	if err != nil {
		panic(fmt.Sprintf("bridgesdk: binding ABI: %v", err))
	}
	return a
}

var abiExtra = func() map[string]json.RawMessage {
	var extra map[string]json.RawMessage
	if err := json.Unmarshal(abiExtraJSON, &extra); err != nil {
		panic(err)
	}
	return extra
}()

func loadExtra(k string) *abi.ABI {
	a, err := abi.JSON(strings.NewReader(string(abiExtra[k])))
	if err != nil {
		panic(fmt.Sprintf("bridgesdk: abi_extra.json %s: %v", k, err))
	}
	return &a
}

// ABIs come from the public binding module (generated at release v1.1.0); the Uniswap V3 fragments from abi_extra.json.
var (
	bridgeABI      = mustMeta(binding.CrossBridgeMetaData) // CrossBridge (superset of BaseBridge/BSCBridgeV2)
	forwardLibABI  = mustMeta(binding.ForwardLibMetaData)  // ForwardInitiated / ForwardFailed (emitted by the hub via delegatecall)
	verifierABI    = mustMeta(binding.BridgeVerifierMetaData)
	executorABI    = mustMeta(binding.BridgeExecutorMetaData)
	hyperABI       = mustMeta(binding.HyperMintableERC20MetaData)
	sbrABI         = mustMeta(binding.SwapBridgeRouterMetaData)
	erc20ABI       = mustMeta(binding.CrossMintableERC20MetaData)
	v3RouterABI    = loadExtra("v3Router")
	quoterABI      = loadExtra("quoterV2")
	v3PoolABI      = loadExtra("v3Pool")
	v3FactoryABI   = loadExtra("v3Factory")
	v3PeripheryABI = loadExtra("v3Periphery")
)

// Selectors (verified by tests against `cast sig`).
var (
	ForwardSelector          = selectorOf("bridgeTokenForwarded(uint256,address,uint256,uint256,uint256,bytes)")                // 0x55fd0776
	CoreSelector             = selectorOf("transferToCoreFor(address,uint256)")                                                 // 0x9438568d
	ExactInputSingleSelector = selectorOf("exactInputSingle((address,address,uint24,address,uint256,uint256,uint256,uint160))") // 0x414bf389
)

func topicOf(a *abi.ABI, event string) common.Hash { return a.Events[event].ID }

// unpackLog decodes a log into out (same approach as bind.BoundContract.UnpackLog).
func unpackLog(a *abi.ABI, event string, log types.Log, out interface{}) error {
	ev, ok := a.Events[event]
	if !ok {
		return fmt.Errorf("unknown event %s", event)
	}
	if len(log.Topics) == 0 || log.Topics[0] != ev.ID {
		return fmt.Errorf("log is not %s", event)
	}
	if len(log.Data) > 0 {
		if err := a.UnpackIntoInterface(out, event, log.Data); err != nil {
			return err
		}
	}
	var indexed abi.Arguments
	for _, arg := range ev.Inputs {
		if arg.Indexed {
			indexed = append(indexed, arg)
		}
	}
	return abi.ParseTopics(out, indexed, log.Topics[1:])
}
