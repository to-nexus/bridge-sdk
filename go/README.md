# Go SDK (github.com/to-nexus/bridge-sdk/go)

CROSS / ONE 브리지 Go SDK — 모듈 `github.com/to-nexus/bridge-sdk/go`, Go ≥ 1.24. 컨트랙트 ABI/타입은 **공개 모듈**
`github.com/to-nexus/bridge-contracts/binding/go` (`v0.0.0-20261009203603-2776b4267adb` = release v1.1.0)에서 쓴다.

## 설치 · 릴리스

서브디렉터리 모듈이므로 릴리스는 `go/vX.Y.Z` 형식의 git 태그로 낸다(예: `go/v0.1.0`). 사용하는 쪽은 태그에서 `go/` 접두사를 뺀 버전을 쓴다:

```bash
go get github.com/to-nexus/bridge-sdk/go@v0.1.0
```

## 빌드 · 테스트

```bash
cd go
go mod verify && go build ./... && go vet ./... && go test ./...     # 공개 프록시에서 binding 을 받는다
```


## 사용법

```go
ctx := context.Background()
c, _ := bridgesdk.NewClient(ctx, bridgesdk.Testnet, nil)           // &bridgesdk.ClientOptions{RPCURLs: ...} 로 RPC 교체
q, err := c.Quote(ctx, bridgesdk.QuoteRequest{Route: bridgesdk.RouteBSCToOneToHyperEVM, Amount: amt, Sender: from, Recipient: to})
rep, _ := c.Preflight(ctx, q)                                       // rep.OK / rep.Issues
signer := bridgesdk.NewKeyedSigner(key, big.NewInt(q.SourceChainID))
sent, err := c.Send(ctx, signer, q, bridgesdk.SendOptions{
    OnSigned: func(s bridgesdk.SignedInfo, in *bridgesdk.SubmittedIntent) error { return persist(s, in) }, // 브로드캐스트 전에 호출
})
h, err := c.WaitHandle(ctx, sent)                                   // *TransferHandle (JSON, 저장)
res, err := c.Track(ctx, h, bridgesdk.TrackOpts{})                  // 터미널 상태 또는 타임아웃(미완료 상태)
```

에러는 포인터 타입이라 `errors.As` 로 분기한다: `*NetworkMismatchError`(체인 id 불일치 + 견적↔클라이언트 바인딩), `*AddressNotConfiguredError`, `*QuoteExpiredError`, `*RequoteRequiredError`, `*PreflightError{Issues, Stage}`, `*SimulationRevertError`, `*ApproveFailedError`, `*SigningError`, `*SignerMisbehaviorError{Field}`, `*SenderMismatchError`, `*BroadcastOutcomeUnknownError`, `*RpcTimeoutError{Method, Timeout}`, `*InvalidOptionError`, …

- **`Send` 는 블로킹 사전 점검을 두 번 강제**한다(approve 전 `Stage == "pre-approve"`, approve·재견적 뒤 시뮬레이션 직전 `"post-approve"`). 모든 `error` 이슈가 막고, RPC 오류·타임아웃도 `PREFLIGHT_INCOMPLETE{Check, Error}` 로 막는다. 막히면 그 지점부터 아무것도 서명·전송하지 않는다.
- **`SentTx.CallbackError`**: `OnBroadcast` 가 panic 하면(반환값이 없는 콜백이라 recover) tx 는 이미 나간 것이므로 `Send` 는 에러 없이 `*SentTx` 를 돌려주고 여기에 담는다 — 확인하고 **재시도하지 말 것**.
- **`ClientOptions.CallTimeout`**(기본 15 s): RPC 호출 하나당 상한. 초과하면 `*RpcTimeoutError`; 전송 요청 뒤의 타임아웃은 항상 `*BroadcastOutcomeUnknownError`(해시 포함, `errors.As` 로 둘 다 잡힌다)로 나온다 — **재전송 금지**. `TrackOpts.Timeout` 은 context 기한으로도 걸려 응답 없는 provider 에서도 기한 안에 돌아온다(`TimedOut`, 또는 `UNKNOWN{Reason: "timeout: …"}`).
- **X3 는 `extraData` 가 없다**(`X3Request` 에 필드 없음 — `0x` 만 지원).

스왑 옵션(X1/X3): `bridgesdk.SwapOptions{SlippageBps: bridgesdk.Bps(0), DeadlineSec: bridgesdk.Seconds(600)}` — **포인터**라서 `nil` = 기본값(50 bps / 1800 s), 명시한 `Bps(0)` 은 정확히 0 bps. `SlippageBps` ∈ [0,10000), `DeadlineSec` > 0 이 아니면 `*InvalidOptionError`.

| 함수 | 설명 |
|---|---|
| `NewClient` `Quote` `Preflight` `BuildTx` `Simulate` | 견적 / 사전 점검 / tx 조립 / 읽기 전용 시뮬레이션 |
| `EnsureAllowance` `Send` `WaitHandle` | approve → 재견적 → 시뮬레이션 → 서명 → 브로드캐스트 → 핸들 |
| `Track` `TrackOnce` | 상태 추적 (`TransferHandle` 은 JS SDK 와 같은 JSON) |
| `HandleFromTx(ctx, srcChainID, txHash, *HandleOptions)` | 해시로 핸들 복원 — 경로는 소스 영수증의 `BridgeInitiated` 에서 복원(`Route` 는 선택·검증, `Index`/`LogIndex` 로 후보 지정; `*AmbiguousSourceError` `*UnsupportedRouteError` `*RouteMismatchError` `*SourceTransferNotFoundError`) |
| `FindSourceTransfer` `AnalyzeReplacement` `Rebroadcast` | 결과 불명 복구(교체 영수증에 전송 모양 로그가 **하나라도** 있으면 새 tx 불가; 귀속 못 하는 증거는 `VerdictReplacedUnverified`, GUIDE §8) — 증거를 읽기 전에 RPC 체인(`eth_chainId`)을 확인하고(다르면 `*NetworkMismatchError`), 교체 tx 는 chainId(EIP-155)·sender·nonce 가 모두 일치해야 `NewTxAllowed`(판정 함수는 하나). `Preflight` 는 소스 뒤의 모든 브리지(최종 목적지)의 `paused()` 도 점검한다(`DEST_BRIDGE_PAUSED`; 목적지 페어는 점검하지 않음) |
| `TxSigner` `NewKeyedSigner` `NewTxSignerFromOpts` | 로컬 서명 (`*bind.TransactOpts` 감싸기 가능) |
| `DecodeBridgeError` | revert 데이터 → 이름·인자·힌트 |

## 견적은 봉인된다 (sealed quotes)

`Quote()` 가 돌려주는 `*Quote` 는 `Seal{V, SDK, Payload, Digest}` 를 갖는다. `Payload` 는 **전체 견적**의 정규 JSON(Go 전용 wire DTO `quoteWireV1` — 명시적 `json` 태그, 금액·체인 id·시각은 십진 문자열, `createdAtMs`/`ttlMs`/`deadlineSec` 단위 고정), `Digest` 는 `keccak256(utf8(Payload))`. `Send` 는 **RPC 를 하기 전에** shape → 버전/SDK → digest → 엄격한 파싱(`DisallowUnknownFields`)·스키마 → 공개 필드 대조 → 복원 순으로 검증하고, 이후에는 봉인에서 복원한 **비공개 복사본만** 쓴다. 노출된 필드(`Limits`, `Request`, `Tx`, …)는 **참고용**이다.

- **`*Quote` 를 받는 모든 공개 API**(`Send`, `BuildTx`(마지막 반환값이 `error`), `EnsureAllowance`, `Preflight`, `Simulate`)는 같은 내부 함수 `openQuote(q)` 로 봉인을 먼저 검증하고 복원본만 쓴다.
- 요청을 바꾸려면 `*Quote` 를 고치지 말고 **`Quote()` 를 다시 호출**한다.
- 값이 안 바뀐 값 복사와 JSON 왕복(`json.Marshal` → `json.Unmarshal`)은 통과한다. 노출 필드가 봉인과 다르면 `*LimitViolationError{Field: "seal", Reason: "modified", Path}`; 봉인이 없거나 깨졌으면 `missing` / `unsupported-version` / `unsupported-sdk` / `corrupt` / `invalid-payload`(어떤 경우에도 panic 하지 않는다).
- 봉인된 값은 `q.ApprovedRequest()` / `q.ApprovedLimits()` 로 읽는다(봉인에서 파싱한 deep copy 와 error — 노출 필드가 수정되었어도 봉인된 값을 돌려준다).
- 검증은 두 층이다. **구조 검증**(설정을 읽지 않음)는 `openQuote` 와 클라이언트 없는 getter(`ApprovedRequest()`/`ApprovedLimits()`)에서 돌고, **클라이언트 바인딩 검사**(설정 일치)는 **발급 클라이언트 자신의 설정**(`ClientOptions.Networks`)으로 `assertQuoteBelongs` 에서 돈다(`Send`/`BuildTx`/`EnsureAllowance`/`Preflight`/`Simulate`). 다른 설정의 클라이언트는 `*NetworkMismatchError`. 봉인/route 규칙 코드는 `GetNetwork()` 를 호출하지 않는다.
- payload 검증의 **2차 패스(구조 검증)는 route 별 필수 필드 표**다: X1/X3 은 `limits.amountOutMinimum`(0 허용)·`deadlineSec`(X3 는 `limits.minBridgeValue` 도), 멀티홉은 `v2`·`hop2Fees`·`hop2ExFeeDeclared`·`hubVerifier` 가 있어야 하고, 봉인된 calldata 를 디코드해(핸들 복원과 같은 디코더, 수령자는 `quote()` 와 같은 `resolveRecipients`) X1 수령자·X2 `coreRecipient`·금액이 일치해야 한다. 어긋나면 `invalid-payload` + 경로. ms 값(`ttlMs`, `request.quoteTtlMs`)은 `> 0` 이고 `<= math.MaxInt64 / 1e6`(`time.Duration` 범위, 곱셈 전에 검사), `createdAtMs` 는 `time.UnixMilli` 범위여야 한다 — `ApprovedRequest()`/`ApprovedLimits()` 도 같은 검증을 거친다.
- 봉인은 **SDK 별**이다(`SDK: "go"`). JS 견적은 `unsupported-sdk`. 구조체 리터럴로 만든 견적은 `missing`.
- ⚠️ `Digest` 는 **실수·동시 변경을 막는 무결성 검사**이지 인증이 아니다(견적을 가진 사람은 새 봉인을 만들 수 있다).
- `*PreflightError.Stage` 는 `Send` 에서 항상 채워진다: `"pre-approve"` | `"requote"`(approve 뒤 채택 견적을 만들다 난 오류) | `"post-approve"`.

## 안전 계약 (JS SDK 와 동일 — `../GUIDE.md §6, §8`)

- Go 는 항상 **로컬 서명(Mode L)**: SDK 가 `ChainID = q.SourceChainID` 로 직접 legacy tx 를 만들고 서명한 뒤 **브로드캐스트 전에 검증**한다 — 서명 결과가 nil 이 아니고, replay 보호(`Protected()`), `ChainId() == q.SourceChainID`, `types.Sender` 로 복구한 송신자 == `q.Sender`, 그리고 `To/Value/Data/Nonce/Gas/GasPrice` 가 요청과 같아야 한다. 어긋나면 `*SignerMisbehaviorError{Field}`.
- **견적↔클라이언트 바인딩**: `Send` 는 맨 먼저 견적의 네트워크·체인·설정 소유 주소(브리지·라우터·허브·고정 토큰·approve spender)를 이 클라이언트 설정과 대조한다(다르면 `*NetworkMismatchError`, 사용자 입력 값이 calldata 와 다르면 `*LimitViolationError`).
- **불변 스냅샷**: `Quote()` 는 요청을 복사하고(`*big.Int`·바이트 슬라이스·맵), `Send` 는 견적의 **비공개 복사본**으로 일하며, 콜백(`OnSubmitting`/`OnSigned`)과 반환 구조체는 복사본을 받는다 — 호출자가 `big.Int` 나 슬라이스를 바꿔도 서명 내용은 그대로다. `OnSigned` 는 raw tx 를 브로드캐스트 **전에** 받는다(오류를 돌려주면 보내지 않는다).
- 견적 한도 불변 + 재견적 규칙, 정확한 approve(+ zero-first), 자동 재전송 없음, `BroadcastOutcomeUnknownError{Intent, Checkpoint, Hash, Nonce, Raw}`.
- 설정이 null 인 경로(메인넷 X3 등)는 `Quote` 에서 `*AddressNotConfiguredError`.

## 예제 (dry-run 기본)

```bash
go run ./examples/r1-bsc-to-one -network testnet                      # 견적·사전 점검·tx 조립·시뮬레이션만
go run ./examples/r5-bsc-to-hyperevm -network testnet -recipient-core 0x...   # R5 + X2
PRIVATE_KEY=... go run ./examples/r1-bsc-to-one -network testnet -send         # 실제 전송 (키는 환경변수로만)
```

`examples/{r1-bsc-to-one,r2-one-to-bsc,r3-one-to-hyperevm,r4-hyperevm-to-one,r5-bsc-to-hyperevm,r6-hyperevm-to-bsc,x1-usdt-to-oneusd,x2-one-to-hypercore,x3-swap-bridge}`.
플래그: `-network -amount -sender -recipient -send` · `-recipient-hub -recipient-core -buffer` · `-slippage-bps`(엄격 파싱: 10진수만, `uint32` 범위, `< 10000` — `4294977295`·`50x`·`-1` 은 거부) · X3: `-tokenIn -tokenOut -fee -to`.
마지막 `RESULT` 줄에 `preflight-refused:<CODES>` 분류가 있다. `-send` 에서 `Send` 가 `*PreflightError` 를 돌려주면 예제는 `RESULT … preflight-refused:<CODES> stage=<…>` 를 찍고 **멈춘다**(종료코드 2, `go run` 은 이를 1 로 바꾸므로 빌드한 바이너리로 확인). `RPC_CROSS` / `RPC_BSC` / `RPC_HYPEREVM` 환경변수로 RPC 교체.

## 파일

`bridgesdk/networks.json`, `abi_extra.json`, `errors_table.json` 은 **생성물**(`../tools/gen.mjs`, `../tools/gen-abi.mjs`)이다. 테스트 벡터는 `bridgesdk/testdata/vectors.json`(JS 와 같은 파일).
