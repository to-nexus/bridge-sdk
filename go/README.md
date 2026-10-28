# bridge-sdk/go

CROSS / ONE 브리지 Go SDK 입니다. BSC · CROSS(ONE) · HyperEVM 사이의 전송을 견적부터 도착 확인까지 처리합니다.

- Go 1.24 이상
- 메인넷 · 테스트넷 설정 내장
- 컨트랙트 바인딩은 [`github.com/to-nexus/bridge-contracts/binding/go`](https://github.com/to-nexus/bridge-contracts) 를 사용합니다

지원 경로와 전체 개요는 [루트 README](../README.md) 를 참고하세요.

## 설치

```bash
go get github.com/to-nexus/bridge-sdk/go@latest
```

```go
import "github.com/to-nexus/bridge-sdk/go/bridgesdk"
```

## 사용법

```go
ctx := context.Background()
c, err := bridgesdk.NewClient(ctx, bridgesdk.Mainnet, nil)
if err != nil {
	return err
}

amount, _ := bridgesdk.ParseUnits("10", 18)
q, err := c.Quote(ctx, bridgesdk.QuoteRequest{
	Route:     bridgesdk.RouteBSCToOneToHyperEVM,
	Amount:    amount,
	Sender:    from,
	Recipient: from,
})
if err != nil {
	return err
}

// 선택: 보내기 전에 막히는 사유 확인
if rep, err := c.Preflight(ctx, q); err == nil && !rep.OK {
	log.Println(rep.Issues)
}

signer := bridgesdk.NewKeyedSigner(key, big.NewInt(q.SourceChainID))
sent, err := c.Send(ctx, signer, q, bridgesdk.SendOptions{
	// 브로드캐스트 전에 서명된 tx 를 저장해 둔다. 에러를 돌려주면 전송하지 않는다
	OnSigned: func(s bridgesdk.SignedInfo, in *bridgesdk.SubmittedIntent) error { return store.Save(s, in) },
})
if err != nil {
	return err
}

h, err := c.WaitHandle(ctx, sent)            // *TransferHandle (JSON 으로 저장)
if err != nil {
	return err
}
res, err := c.Track(ctx, h, bridgesdk.TrackOpts{Timeout: 10 * time.Minute})
```

### 클라이언트 옵션

```go
c, err := bridgesdk.NewClient(ctx, bridgesdk.Mainnet, &bridgesdk.ClientOptions{
	RPCURLs:     map[bridgesdk.ChainKey]string{bridgesdk.ChainBSC: "https://..."}, // 체인별 RPC 교체
	CallTimeout: 15 * time.Second,                                            // RPC 호출 하나당 제한 시간
	QuoteTTL:    30 * time.Second,                                            // 견적 유효 시간
})
```

### 견적 요청 (`QuoteRequest`)

| 필드 | 설명 |
|---|---|
| `Route` | `RouteBSCToOne` 등 ([지원 경로](../README.md#지원-경로)) |
| `Amount` | wei 단위 `*big.Int`. 단일 홉은 도착 금액, R5/R6 은 최종 도착 최소 금액, X1 은 USDT 금액, X3 는 스왑 입력 금액 |
| `Sender` · `Recipient` | 보내는 주소 / 받는 주소 (모든 홉 기본값) |
| `Recipients` | 홉별 수령인 `{OnHub, Dest, Core}` (선택) |
| `Hypercore` | R5 에서 HyperCore 입금(X2)까지 할지 (선택) |
| `Swap` | X1 · X3 의 `SwapOptions{SlippageBps, DeadlineSec}`. `nil` 이면 50 bps / 1800 초. `bridgesdk.Bps(0)` 처럼 명시하면 그 값 |
| `X3` | X3 전용 `*X3Request` |
| `Hop2FeeBuffer` | 멀티홉에서 두 번째 홉 수수료에 더할 여유분 (선택) |
| `MaxTotalSpend` · `MinDelivered` | 직접 정하는 지출 상한 / 도착 하한 (선택) |

## API

| 함수 | 설명 |
|---|---|
| `NewClient(ctx, network, opts)` | 클라이언트 생성. RPC 는 처음 쓸 때 연결하고 chainId 를 확인한다 |
| `Quote` | 수수료를 조회하고 보낼 트랜잭션을 만든다 |
| `Preflight` | 브리지 정지·페어·잔고·allowance 등을 점검해 `OK` 와 `Issues` 를 돌려준다 |
| `Simulate` · `BuildTx` | 읽기 전용 시뮬레이션 / 보낼 트랜잭션 조립 (직접 서명할 때) |
| `EnsureAllowance` | approve 만 실행 |
| `Send` | approve → 재견적 → 점검 → 시뮬레이션 → 서명 → 전송 |
| `WaitHandle` | 출발 영수증을 기다려 `*TransferHandle` 을 만든다 |
| `Track` · `TrackOnce` | 도착까지 상태 추적 / 현재 상태 한 번 조회 |
| `HandleFromTx` | 트랜잭션 해시로 `*TransferHandle` 을 다시 만든다 |
| `FindSourceTransfer` | 해시를 모를 때 출발 체인에서 전송을 찾는다 |
| `AnalyzeReplacement` · `Rebroadcast` | 같은 nonce 로 다른 tx 가 나갔는지 판정 / 서명된 tx 재전송 |
| `CheckCoreBalance` | HyperCore 스팟 잔고 조회 (X2) |
| `DecodeBridgeError` | revert 데이터를 에러 이름·인자·조치 힌트로 해석 |
| `NewKeyedSigner` · `NewTxSignerFromOpts` | 개인 키 / `*bind.TransactOpts` 로 서명자 만들기 |
| `ParseUnits` · `FormatUnits` | 소수 문자열 ↔ wei 변환 |

`TransferHandle` 은 JS SDK 와 같은 JSON 형식이라 어느 SDK 로 만든 핸들이든 서로 추적할 수 있습니다.

## 에러 처리

에러는 포인터 타입이므로 `errors.As` 로 구분합니다.

```go
sent, err := c.Send(ctx, signer, q, bridgesdk.SendOptions{})
var unknown *bridgesdk.BroadcastOutcomeUnknownError
var pf *bridgesdk.PreflightError
switch {
case errors.As(err, &unknown):
	// 결과를 모름 — 다시 보내지 말고 먼저 확인한다
	found, _ := c.FindSourceTransfer(ctx, unknown.Intent, unknown.Checkpoint)
	_ = found
case errors.As(err, &pf):
	log.Println(pf.Stage, pf.Issues) // 아무것도 서명하지 않았다
}
```

| 에러 | 언제 | 할 일 |
|---|---|---|
| `*AddressNotConfiguredError` | 해당 네트워크에서 아직 열리지 않은 경로 | 다른 경로 / 네트워크 사용 |
| `*PreflightError` | 브리지 정지, 잔고·allowance 부족 등. `Issues[].Code` 로 사유 확인 | 사유 해결 후 다시 `Quote` |
| `*RequoteRequiredError` | approve 뒤 다시 견적했더니 수수료가 올라 처음 한도를 넘음 | 새로 `Quote` |
| `*QuoteExpiredError` | 견적 유효 시간 초과 | 새로 `Quote` |
| `*NetworkMismatchError` | RPC 의 체인이 설정과 다름 | RPC 확인 |
| `*SimulationRevertError` | 시뮬레이션 실패 | `DecodeBridgeError` 로 사유 확인 |
| `*BroadcastOutcomeUnknownError` | 전송 요청 후 응답을 받지 못해 결과를 모름 | **다시 보내지 말고** `FindSourceTransfer` 로 확인 |
| `*RpcTimeoutError` | RPC 응답 지연 | 재시도 또는 RPC 교체 |

`Send` 가 성공했더라도 `sent.CallbackError` 가 있으면 `OnBroadcast` 콜백에서 panic 이 난 것입니다. 트랜잭션은 이미 나갔으므로 다시 보내지 마세요.

복구 절차와 revert 코드 표는 [GUIDE.md §8](../GUIDE.md#8-에러-처리) 에 있습니다.

## 주의할 점

- **견적은 수정할 수 없습니다.** `*Quote` 의 필드는 참고용이며, 바꾸려면 `Quote` 를 다시 호출하세요. 봉인된 값은 `q.ApprovedRequest()` / `q.ApprovedLimits()` 로 읽습니다.
- **처음 견적의 한도가 지켜집니다.** 지출 상한·도착 하한·스왑 최소 수령량은 전송 직전 재견적에서도 느슨해지지 않습니다.
- **approve 는 기본적으로 필요한 금액만큼만** 합니다.
- Go SDK 는 항상 로컬 키로 서명하고, 브로드캐스트 전에 서명된 트랜잭션의 chainId · 보낸 주소 · 내용을 다시 확인합니다.

## 예제

경로별 예제가 `examples/` 에 있습니다. 기본은 dry-run(견적·점검·시뮬레이션만)입니다.

```bash
go run ./examples/r1-bsc-to-one -network testnet -sender 0xYourAddress
go run ./examples/r5-bsc-to-hyperevm -network testnet -sender 0xYourAddress -recipient-core 0xYourAddress   # R5 + HyperCore 입금
PRIVATE_KEY=... go run ./examples/r1-bsc-to-one -network testnet -send
```

| 플래그 | 설명 |
|---|---|
| `-network mainnet\|testnet` | 네트워크 (기본 testnet) |
| `-amount 10` | 금액 |
| `-sender` · `-recipient` | 보내는 / 받는 주소 |
| `-recipient-hub` · `-recipient-core` · `-buffer` | 멀티홉 · HyperCore 옵션 |
| `-slippage-bps` | X1 · X3 슬리피지 |
| `-tokenIn` · `-tokenOut` · `-fee` · `-to` | X3 옵션 |
| `-send` | 실제 전송 (`PRIVATE_KEY` 환경변수 필요) |

RPC 는 `RPC_CROSS` · `RPC_BSC` · `RPC_HYPEREVM` 환경변수로 바꿀 수 있습니다.

## 개발

```bash
go build ./... && go vet ./... && go test ./...
```

`bridgesdk/networks.json`, `abi_extra.json`, `errors_table.json` 은 생성 파일입니다. 네트워크 설정은 루트의 `networks.json` 을 고친 뒤 `node ../tools/gen.mjs` 로 반영합니다.

## License

[MIT](../LICENSE)
