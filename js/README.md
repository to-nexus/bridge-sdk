# @to-nexus/bridge-sdk (JS / TypeScript)

CROSS / ONE 브리지 SDK — TypeScript + **ethers v6**, ESM, Node ≥ 18. 네트워크 설정(주소·RPC·chainId)이 내장되어 있다.

## 설치 · 빌드 · 테스트

```bash
cd js
npm ci                     # (또는 npm install)
npm run typecheck          # tsc --noEmit  (src + test + examples)
npm run build              # dist/ (ESM + d.ts, 설정 모듈 포함)
npm test                   # 골든 벡터 + 모의 RPC 테스트 — node --test
npm run examples:build     # dist-examples/ (예제 실행용)
```

내 프로젝트에서 쓰려면: `npm pack` 으로 만든 tgz 를 `npm install ./to-nexus-bridge-sdk-0.1.0.tgz ethers`.

## 사용법

```ts
import { BridgeClient, Route, parseUnits } from '@to-nexus/bridge-sdk';
import { Wallet } from 'ethers';

const client = new BridgeClient({ network: 'testnet' });            // rpcUrls 로 체인별 RPC 교체 가능
const wallet = new Wallet(process.env.PRIVATE_KEY!);

const q  = await client.quote({ route: Route.BSC_TO_ONE_TO_HYPEREVM, amount: parseUnits('10'), sender: wallet.address, recipient: wallet.address });
const pf = await client.preflight(q);                              // { ok, issues:[{level,code,message,hint}] }
const sent   = await client.send(wallet, q, { onBroadcast: (h) => save(h) });   // approve(필요 시) → 재견적 → 시뮬레이션 → 전송
const handle = await sent.wait();                                  // TransferHandle (JSON) — 저장하세요
const result = await client.track(handle, { timeoutMs: 600_000 });
```

`amount`: 단일 홉 = 도착 value, 멀티홉(R5/R6) = **최소** 도착 v2, X1 = USDT value, X3 = `amountIn` (bigint wei; `parseUnits`/`formatUnits` 제공).

옵션: `recipients{onHub,dest,core}` · `hypercore`(R5) · `swap{slippageBps,deadlineSec}`(생략=50 bps/1800 s, 명시한 `0` 은 정확히 0; `slippageBps` ∈ [0,10000), `deadlineSec` > 0 아니면 `InvalidOptionError`) · `hop2FeeBuffer` · `limits{maxTotalSpend,minDelivered}` · `quoteTtlMs` · send: `approveAmount('exact'|'max')`, `zeroFirst('auto'|'always'|'never')`, `onSubmitting`, `onSigned`, `onBroadcast`, `signerMode`, `walletTimeoutMs`(선택 — **기본은 지갑 타임아웃 없음**). 클라이언트: `callTimeoutMs`(RPC 호출 하나당, 기본 15 000 ms). **X3 는 `extraData` 옵션이 없다**(`0x` 만 지원; 넣으면 `InvalidOptionError`).

### API

| 메서드 | 설명 |
|---|---|
| `quote(req)` | 라이브 수수료 조회 + 경로별 인코딩. 설정이 null 인 경로는 **`AddressNotConfiguredError`**(approve 전에) |
| `preflight(q)` | pause(소스·허브·**소스 뒤의 모든 브리지 = 최종 목적지 `DEST_BRIDGE_PAUSED`**, 목적지 페어는 점검하지 않음)·페어·executor whitelist·extraData 길이·잔고·allowance·(X1) 풀·(X2) Core 링크·(X3) 라우터 점검. **`error` 이슈는 전부 `send()` 를 막는다**(RPC 오류·타임아웃은 `PREFLIGHT_INCOMPLETE`) |
| `buildTx(q)` · `simulate(q)` | 보낼 tx 그대로 / `eth_call` 시뮬레이션(읽기 전용) |
| `ensureAllowance(signer, q)` | approve 단독 실행 |
| `send(signer, q, opts)` | §안전 계약. 해시가 확정되면 바로 `SentTx` 반환. 블로킹 사전 점검을 approve 전·후 두 번 실행(`PreflightError{issues, stage}`). **`sent.callbackError`**: `onBroadcast` 가 던지면 tx 는 이미 나간 것이므로 `send()` 는 예외 대신 `SentTx` 를 돌려주고 `{name,message}` 를 여기 담는다 — 확인하고 **재시도하지 말 것** |
| `track(handle)` / `trackOnce(handle)` | 상태 추적(재개 가능) |
| `handleFromTx(chain, txHash, opts?)` | 해시로 TransferHandle 복원. 경로는 소스 영수증의 `BridgeInitiated` 에서 복원(`opts.route` 는 선택·검증, `opts.index`/`opts.logIndex` 로 후보 지정; `AmbiguousSourceError`·`UnsupportedRouteError`·`RouteMismatchError`) |
| `findSourceTransfer(intent, checkpoint)` | 해시 없이 소스 체인에서 전송 찾기 |
| `analyzeReplacement({…})` | Mode L: 같은 nonce 의 교체 tx 분석(전송 모양 로그가 **하나라도** 있으면 새 tx 불가; 귀속 못 하는 증거는 `REPLACED_UNVERIFIED`, GUIDE §8) — RPC 체인(`eth_chainId`)을 먼저 확인하고, 교체 tx 는 chainId(EIP-155)·sender·nonce 가 모두 일치해야 `NEW_TX_ALLOWED` (판정 함수는 하나) |
| `rebroadcast(chain, raw)` | Mode L: 같은 서명 tx 재브로드캐스트(중복 불가) |
| `checkCoreBalance(coreRecipient)` | X2 용, 최선 노력(공개 info API) |
| `decodeBridgeError(data)` | revert 데이터 → `{name,args,hint}` |

## 견적은 봉인된다 (sealed quotes)

`quote()` 가 돌려주는 객체는 공개·열거 가능·frozen 속성 `seal = {v, sdk, payload, digest}` 를 갖는다. `payload` 는 **전체 견적**(네트워크·체인·소스 컨트랙트·approval·시각·한도·요청·만들어진 tx·match)의 정규 JSON(JS 전용 wire DTO, 금액·체인 id·시각은 십진 문자열), `digest` 는 `keccak256(utf8(payload))`. `send()` 는 **동기적으로 맨 먼저** `shape → 버전/sdk → digest → payload 파싱·스키마 → 공개 필드 대조 → 복원` 순으로 검증하고, 이후에는 봉인에서 복원한 **새 비공개 deep-frozen 객체만** 쓴다(approve·재견적 기준선·intent·콜백 모두). 호출자의 객체는 다시 읽지 않는다.

- **Quote 를 받는 모든 공개 API**(`send`, `buildTx`, `ensureAllowance`, `preflight`, `simulate`)는 같은 내부 함수 `openQuote(q)` 로 봉인을 먼저 검증하고 복원본만 쓴다 — 수정된 견적이 서명 가능한 calldata(`buildTx`)·approve(`ensureAllowance`)·통과 보고(`preflight`/`simulate`)로 이어지지 않는다.
- 요청을 바꾸려면 객체를 고치지 말고 **`quote()` 를 다시 호출**한다.
- 값이 안 바뀐 복사(`{...q}`, `structuredClone(q)`, `jsonParse(jsonStringify(q))`)는 통과한다. 공개 필드가 봉인과 다르면 `LimitViolationError{field:'seal', reason:'modified', path}`, 봉인이 없거나 깨졌으면 `missing` / `unsupported-version` / `unsupported-sdk` / `corrupt` / `invalid-payload`. 어느 경우든 RPC·approve·서명 전에 거부된다. `send()` 가 시작된 뒤의 수정은 영향이 없다.
- 검증은 두 층이다. **구조 검증**(설정을 읽지 않음)는 `openQuote` 에서 돌고, **클라이언트 바인딩 검사**(설정 일치)는 **발급 클라이언트 자신의 networks 설정**(`ClientOptions.networksData`)으로 `assertQuoteBelongs` 에서 돈다(소스 브리지/라우터·허브·고정 토큰·X1 라우터/ONEUSD/풀 fee·X2 토큰·X3 라우터·`details.destChainId`). 다른 설정의 클라이언트는 `NetworkMismatchError`. 봉인/route 규칙 코드는 `getNetworksData()` 를 호출하지 않는다.
- payload 검증의 **2차 패스(구조 검증)는 route 별 필수 필드 표**다: 예를 들어 X1/X3 은 `limits.amountOutMinimum`(0 허용)·`deadlineSec`(X3 는 `limits.minBridgeValue` 도), 멀티홉은 `v2`·`fees.hop2`·`fees.hop2ExFeeDeclared`·`verifiers.hub` 가 있어야 하고, 봉인된 calldata 를 디코드해(핸들 복원과 같은 디코더, 수령자는 `quote()` 와 같은 `resolveRecipients`) X1 수령자·X2 `coreRecipient`·금액이 봉인된 값과 일치해야 한다. 어긋나면 `invalid-payload` + 경로. 시간 값(`ttlMs`, `createdAtMs`, `request.quoteTtlMs`)은 양의 safe integer 여야 한다.
- 봉인은 **SDK 별**이다(`sdk:"js"`). Go 견적을 JS 로 보내면 `unsupported-sdk`. 봉인 없는 손조립 견적은 거부 — 손조립한 견적이 필요한 테스트는 `test/helpers.ts` 의 `sealForTest()` 로 다시 봉인한다.
- ⚠️ digest 는 **실수·동시 변경을 막는 무결성 검사**이지 인증이 아니다(견적을 가진 사람은 새 봉인을 만들 수 있다).
- `PreflightError.stage` 는 `send()` 에서 항상 채워진다: `'pre-approve'` | `'requote'`(approve 뒤 채택 견적을 만들다 난 오류, 예: X3 견적 status) | `'post-approve'`.
- 서명자의 `provider.getNetwork()` / `getAddress()` 읽기는 `callTimeoutMs` 로 제한된다(`RpcTimeoutError`). 지갑 서명 프롬프트(`signTransaction`/`sendTransaction`)는 그대로 제한하지 않는다.

## 안전 계약 (요약 — 상세 `../GUIDE.md §6, §8`)

- **네트워크 바인딩**: 모든 provider 의 `eth_chainId` 를 설정과 대조(불일치 → `NetworkMismatchError`), 서명자 주소·네트워크가 견적과 다르면 서명 전에 거부. **견적↔클라이언트 바인딩**: `send()` 는 맨 먼저 견적의 네트워크·체인·설정 소유 주소(브리지·라우터·허브·고정 토큰·approve spender)를 이 클라이언트 설정과 대조한다(다르면 `NetworkMismatchError`, 사용자 입력 값이 calldata 와 다르면 `LimitViolationError`).
- **견적 한도는 불변**: `maxTotalSpend`, `minDelivered`, 스왑 `amountOutMinimum`(절대 아래로 재계산하지 않음), X3 `minBridgeValue`. approve 후 **재견적**이 한도를 느슨하게 하면 `RequoteRequiredError`(전송 안 함).
- **approve**: 기본 정확한 금액, `0 < allowance < 필요액` 이면 0 먼저, 각 approve 는 receipt `status==1` 확인.
- **서명 모드는 능력으로만 결정**(서명 전): Mode L(로컬 키: `Wallet`/`HDNodeWallet`/`NonceManager(Wallet)` 또는 `signerMode:'local'`) — `onSigned({raw,hash,nonce})` 를 **브로드캐스트 전에** await. Mode W(지갑 `sendTransaction`) — 지갑이 돌려준 해시. **에러로 모드를 바꾸지 않는다.**
- **서명된 tx 검증(Mode L)**: 브로드캐스트 전에 raw tx 를 파싱해 replay 보호(pre-EIP-155 금지)·chainId·복구한 sender·`to/value/data/nonce/gasLimit/gasPrice` 를 요청과 대조 — 어긋나면 `SignerMisbehaviorError{field}`, 아무것도 보내지 않는다.
- **RPC 타임아웃**: 모든 호출은 `callTimeoutMs` 로 제한(`RpcTimeoutError`). 전송 요청이 나간 뒤의 타임아웃은 항상 `BroadcastOutcomeUnknownError`(해시 포함)로 나온다 — **재전송 금지**, `findSourceTransfer`/`handleFromTx`. `track(handle, {timeoutMs})` 는 호출 단위로도 `timeoutMs` 까지로 묶여, 응답 없는 provider 에서도 기한 안에 돌아온다.
- **불변 스냅샷**: 견적의 request·limits·intent·checkpoint 는 `structuredClone` + deep-freeze 다(호출자가 원본을 바꿔도 서명 내용은 그대로). 콜백은 새 frozen 복사본을 받는다.
- **자동 재전송 없음.** 전송 결과 불명 → `BroadcastOutcomeUnknownError{intent, checkpoint, hash?, nonce?, raw?}`. `findSourceTransfer` 는 SubmittedIntent **전체 필드**로 후보를 대조(수수료만 `이벤트 ≤ intent`, 나머지는 정확히 일치; `FOUND`/`AMBIGUOUS`/`NOT_FOUND_YET`).
- 직렬화: Quote/Intent 는 bigint 를 포함하므로 `jsonStringify`/`jsonParse` 사용. `TransferHandle` 은 순수 JSON.

## 예제 (dry-run 기본)

```bash
npm run build && npm run examples:build
node dist-examples/examples/r1-bsc-to-one.js --network testnet                 # 견적·사전 점검·tx 조립·시뮬레이션만
SENDER=0x내주소 node dist-examples/examples/r5-bsc-to-hyperevm.js --network testnet
PRIVATE_KEY=... node dist-examples/examples/r1-bsc-to-one.js --network testnet --send    # 실제 전송 (키는 환경변수로만)
```

예제: `r1-bsc-to-one` `r2-one-to-bsc` `r3-one-to-hyperevm` `r4-hyperevm-to-one` `r5-bsc-to-hyperevm` `r6-hyperevm-to-bsc` `x1-usdt-to-oneusd` `x2-one-to-hypercore` `x3-swap-bridge`.
공통 플래그: `--network mainnet|testnet` `--amount 10` `--sender` `--recipient` `--send` · R5/R6: `--recipient-hub` `--recipient-core` `--buffer` · X1/X3: `--slippage-bps` · X3: `--tokenIn` `--tokenOut` `--fee` `--to`.
`RPC_CROSS` / `RPC_BSC` / `RPC_HYPEREVM` 환경변수로 RPC 를 바꿀 수 있다(anvil fork 스모크에 사용).

마지막 줄 `RESULT <경로> <네트워크> encoding-verified <simulation-passed | simulation-blocked(allowance|balance) | simulation-reverted(...) | preflight-refused:<CODES>>` — `simulation-blocked` 와 `preflight-refused` 는 종단 간 검증으로 세지 않는다. `--send` 에서 `send()` 가 `PreflightError` 를 던지면 예제는 `RESULT <경로> <네트워크> preflight-refused:<CODES> stage=<pre-approve|post-approve>` 를 찍고 **멈춘다**(종료코드 2, 아무것도 보내지 않음).

## 파일

`src/networks.generated.ts`, `src/abi/*.ts`, `src/errors.generated.ts` 는 **생성물**이다(`../tools/gen.mjs`, `../tools/gen-abi.mjs`). 값은 `../networks.json` 에서 바꾼다.
