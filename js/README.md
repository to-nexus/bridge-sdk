# @to-nexus/bridge-sdk

CROSS / ONE 브리지 TypeScript SDK 입니다. BSC · CROSS(ONE) · HyperEVM 사이의 전송을 견적부터 도착 확인까지 처리합니다.

- ethers v6 기반, ESM, Node.js 18 이상
- 메인넷 · 테스트넷 설정 내장
- 타입 정의 포함

지원 경로와 전체 개요는 [루트 README](../README.md) 를 참고하세요.

## 설치

아직 npm 레지스트리에 배포하지 않았으므로 소스에서 패키지를 만들어 설치합니다.

```bash
git clone https://github.com/to-nexus/bridge-sdk.git
cd bridge-sdk/js
npm ci && npm run build && npm pack

# 내 프로젝트에서
npm install /path/to/to-nexus-bridge-sdk-<버전>.tgz ethers
```

## 사용법

```ts
import { BridgeClient, Route, parseUnits, jsonStringify } from '@to-nexus/bridge-sdk';
import { Wallet } from 'ethers';

const client = new BridgeClient({ network: 'mainnet' });
const wallet = new Wallet(process.env.PRIVATE_KEY!);

const quote = await client.quote({
  route: Route.BSC_TO_ONE_TO_HYPEREVM,
  amount: parseUnits('10'),
  sender: wallet.address,
  recipient: wallet.address,
});

const report = await client.preflight(quote);     // 선택: 보내기 전에 막히는 사유 확인
if (!report.ok) console.log(report.issues);

const sent = await client.send(wallet, quote, {
  onBroadcast: (hash) => db.save(hash),           // 해시가 나오는 즉시 저장
});
const handle = await sent.wait();                 // TransferHandle (JSON)
await db.save(JSON.stringify(handle));

const result = await client.track(handle, { timeoutMs: 600_000 });
console.log(result.state, result.hops);
```

### 클라이언트 옵션

```ts
new BridgeClient({
  network: 'mainnet',                 // 'mainnet' | 'testnet'
  rpcUrls: { bsc: 'https://...' },    // 체인별 RPC 교체 (cross | bsc | hyperevm)
  callTimeoutMs: 15_000,              // RPC 호출 하나당 제한 시간
  quoteTtlMs: 30_000,                 // 견적 유효 시간
});
```

### 견적 요청 (`quote`)

| 필드 | 설명 |
|---|---|
| `route` | `Route.BSC_TO_ONE` 등 ([지원 경로](../README.md#지원-경로)) |
| `amount` | wei 단위 `bigint`. 단일 홉은 도착 금액, R5/R6 은 최종 도착 최소 금액, X1 은 USDT 금액, X3 는 스왑 입력 금액 |
| `sender` | 보내는 주소 |
| `recipient` | 받는 주소 (모든 홉 기본값) |
| `recipients` | 홉별 수령인 `{ onHub, dest, core }` (선택) |
| `hypercore` | R5 에서 HyperCore 입금(X2)까지 할지 (선택) |
| `swap` | X1 · X3 의 `{ slippageBps, deadlineSec }`. 기본 50 bps / 1800 초 |
| `x3` | X3 전용 `{ tokenIn, tokenOut, poolFee, toChain }` |
| `hop2FeeBuffer` | 멀티홉에서 두 번째 홉 수수료에 더할 여유분 (선택) |
| `limits` | `{ maxTotalSpend, minDelivered }` — 직접 정하는 지출 상한 / 도착 하한 (선택) |

### 전송 옵션 (`send`)

| 옵션 | 설명 |
|---|---|
| `approveAmount` | `'exact'`(기본) 또는 `'max'` |
| `onSubmitting` · `onSigned` · `onBroadcast` | 서명 직전 · 서명 직후(브로드캐스트 전) · 브로드캐스트 직후 콜백 |
| `walletTimeoutMs` | 브라우저 지갑 서명 대기 제한 시간. 기본은 제한 없음 |

## API

| 메서드 | 설명 |
|---|---|
| `quote(req)` | 수수료를 조회하고 보낼 트랜잭션을 만든다 |
| `preflight(quote)` | 브리지 정지·페어·잔고·allowance 등을 점검해 `{ ok, issues }` 를 돌려준다 |
| `simulate(quote)` | 읽기 전용 시뮬레이션 |
| `buildTx(quote)` | 보낼 트랜잭션 `{ to, data, value, chainId, from }` (직접 서명할 때) |
| `ensureAllowance(signer, quote)` | approve 만 실행 |
| `send(signer, quote, opts?)` | approve → 재견적 → 점검 → 시뮬레이션 → 전송. 해시가 확정되면 `SentTx` 를 돌려준다 |
| `wait(sent)` / `sent.wait()` | 출발 영수증을 기다려 `TransferHandle` 을 만든다 |
| `track(handle, opts?)` | 도착까지 상태를 추적한다. 중단 후 같은 핸들로 이어서 추적할 수 있다 |
| `trackOnce(handle)` | 현재 상태를 한 번만 조회 |
| `handleFromTx(chain, txHash)` | 트랜잭션 해시로 `TransferHandle` 을 다시 만든다 |
| `findSourceTransfer(intent, checkpoint)` | 해시를 모를 때 출발 체인에서 전송을 찾는다 |
| `analyzeReplacement(...)` · `rebroadcast(chain, raw)` | 같은 nonce 로 다른 tx 가 나갔는지 판정 / 서명된 tx 재전송 |
| `checkCoreBalance(address)` | HyperCore 스팟 잔고 조회 (X2) |
| `decodeBridgeError(data)` | revert 데이터를 에러 이름·인자·조치 힌트로 해석 |

유틸리티: `parseUnits` · `formatUnits` · `jsonStringify` / `jsonParse`(bigint 포함 객체 직렬화) · `getNetwork(name)`(내장 설정 조회).

## 에러 처리

모든 에러는 `BridgeSdkError` 를 상속합니다.

| 에러 | 언제 | 할 일 |
|---|---|---|
| `AddressNotConfiguredError` | 해당 네트워크에서 아직 열리지 않은 경로 | 다른 경로 / 네트워크 사용 |
| `PreflightError` | 브리지 정지, 잔고·allowance 부족 등. `issues[].code` 로 사유 확인 | 사유 해결 후 다시 `quote()` |
| `RequoteRequiredError` | approve 뒤 다시 견적했더니 수수료가 올라 처음 한도를 넘음 | 새로 `quote()` |
| `QuoteExpiredError` | 견적 유효 시간 초과 | 새로 `quote()` |
| `NetworkMismatchError` | RPC 나 지갑의 체인이 설정과 다름 | RPC · 지갑 네트워크 확인 |
| `SimulationRevertError` | 시뮬레이션 실패. `decodeBridgeError` 로 사유 확인 | 사유 해결 |
| `BroadcastOutcomeUnknownError` | 전송 요청 후 응답을 받지 못해 결과를 모름 | **다시 보내지 말고** `findSourceTransfer(e.intent, e.checkpoint)` 로 확인 |
| `RpcTimeoutError` | RPC 응답 지연 | 재시도 또는 RPC 교체 |

`send()` 가 성공했더라도 `sent.callbackError` 가 있으면 `onBroadcast` 콜백이 실패한 것입니다. 트랜잭션은 이미 나갔으므로 다시 보내지 마세요.

복구 절차와 revert 코드 표는 [GUIDE.md §8](../GUIDE.md#8-에러-처리) 에 있습니다.

## 주의할 점

- **견적은 수정할 수 없습니다.** 바꾸려면 `quote()` 를 다시 호출하세요. 값이 같은 복사(`{...quote}`, `structuredClone`)는 그대로 쓸 수 있습니다.
- **처음 견적의 한도가 지켜집니다.** 지출 상한·도착 하한·스왑 최소 수령량은 전송 직전 재견적에서도 느슨해지지 않습니다.
- **approve 는 기본적으로 필요한 금액만큼만** 합니다. 기존 allowance 가 모자라면 0 으로 먼저 초기화합니다.
- **견적과 의도(intent)는 bigint 를 포함**하므로 저장할 때 `jsonStringify` / `jsonParse` 를 쓰세요. `TransferHandle` 은 일반 JSON 입니다.

## 예제

경로별 예제가 `examples/` 에 있습니다. 기본은 dry-run(견적·점검·시뮬레이션만)입니다.

```bash
npm run build && npm run examples:build
node dist-examples/examples/r1-bsc-to-one.js --network testnet --sender 0xYourAddress
PRIVATE_KEY=... node dist-examples/examples/r1-bsc-to-one.js --network testnet --send
```

| 플래그 | 설명 |
|---|---|
| `--network mainnet\|testnet` | 네트워크 (기본 testnet) |
| `--amount 10` | 금액 |
| `--sender` · `--recipient` | 보내는 / 받는 주소 |
| `--recipient-hub` · `--recipient-core` · `--buffer` | 멀티홉 · HyperCore 옵션 |
| `--slippage-bps` | X1 · X3 슬리피지 |
| `--tokenIn` · `--tokenOut` · `--fee` · `--to` | X3 옵션 |
| `--send` | 실제 전송 (`PRIVATE_KEY` 환경변수 필요) |

RPC 는 `RPC_CROSS` · `RPC_BSC` · `RPC_HYPEREVM` 환경변수로 바꿀 수 있습니다.

## 개발

```bash
npm ci
npm run typecheck
npm test
```

`src/networks.generated.ts`, `src/abi/`, `src/errors.generated.ts` 는 생성 파일입니다. 네트워크 설정은 루트의 `networks.json` 을 고친 뒤 `node ../tools/gen.mjs` 로 반영합니다.

## License

[MIT](../LICENSE)
