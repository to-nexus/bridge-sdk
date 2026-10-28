# ONE 브리지 연동 가이드

BSC · ONE (허브) · HyperEVM 사이에서 ONE 과 USDT 를 옮기는 방법. 브리지 내부를 몰라도 되도록 **쉽게 쓰기(Quick Start)** 를 맨 앞에 두고,
동작 원리·수수료·상태 추적·에러 처리는 그 뒤에 둔다.

> 모든 주소·RPC·chainId 는 `networks.json` 한 곳에서 온다(출처: `SOURCES.md`).
> ⚠️ 표시는 아직 확인되지 않았거나 체인에 아직 설정되지 않은 값이다 — 해당 경로의 호출은 SDK 가 거부한다(`quote()` 또는 `preflight()`).
> ONE 체인에는 reorg 가 없다. 컨펌 대기·finality 처리가 필요 없다(소스 체인이 BSC/HyperEVM 이면 소스 영수증만 기다린다).

---

## 1. 쉽게 쓰기 (Quick Start)

### 1.1 필요한 것

| 도구 | 용도 |
|---|---|
| Node.js ≥ 18 + `npm` | JS SDK (`js/`, TypeScript + ethers v6) |
| Go ≥ 1.24 | Go SDK (`go/`) — 컨트랙트 바인딩은 공개 모듈에서 자동으로 내려받는다 |
| `cast`(foundry) + `python3` | 셸 예제 (`cast/`) — 큰 정수 덧셈에 python3 를 쓴다 |
| 소스 체인 가스 | BSC: BNB, HyperEVM: HYPE, ONE 체인: 네이티브 ONE |
| 키 | **환경변수 `PRIVATE_KEY` 로만** 읽는다. 코드·저장소에 넣지 말 것 |

### 1.2 설치

```bash
# JS  (저장소를 받아 빌드한 뒤 내 프로젝트에 로컬 설치)
cd js && npm ci && npm run build            # dist/ 생성
npm install /경로/js                         # 내 프로젝트에서 설치 (패키지 이름: @to-nexus/bridge-sdk)

# Go  (릴리스는 `go/vX.Y.Z` 태그 — 서브디렉터리 모듈)
#   go get github.com/to-nexus/bridge-sdk/go@vX.Y.Z
#   로컬 소스를 쓰려면 go.mod 에:  replace github.com/to-nexus/bridge-sdk/go => /경로/go

# cast
source cast/env.testnet.sh   # 또는 NETWORK=mainnet ./cast/r1.sh 처럼 스크립트가 알아서 읽는다
```

### 1.3 다섯 줄 패턴

모든 경로가 같은 패턴이다: **`quote → send → wait → track`** (`preflight` 로 사전 점검 가능).

```ts
import { BridgeClient, Route, parseUnits } from '@to-nexus/bridge-sdk';
import { Wallet } from 'ethers';

const client = new BridgeClient({ network: 'testnet' });                 // 공개 RPC 기본 내장
const wallet = new Wallet(process.env.PRIVATE_KEY!);                    // 키는 환경변수에서만
const q      = await client.quote({ route: Route.BSC_TO_ONE, amount: parseUnits('10'), sender: wallet.address, recipient: wallet.address });
const sent   = await client.send(wallet, q);        // approve(필요 시) → 재견적 → 시뮬레이션 → 전송. 해시를 알면 바로 반환
const handle = await sent.wait();                   // TransferHandle (JSON, 꼭 저장하세요 — 재시작 후에도 추적 가능)
const result = await client.track(handle);          // DELIVERED / DELIVERED_FALLBACK / PENDING_ON_DEST ...
```

```go
c, _ := bridgesdk.NewClient(ctx, bridgesdk.Testnet, nil)
q, _ := c.Quote(ctx, bridgesdk.QuoteRequest{Route: bridgesdk.RouteBSCToOne, Amount: amt, Sender: from, Recipient: from})
sent, _ := c.Send(ctx, bridgesdk.NewKeyedSigner(key, big.NewInt(q.SourceChainID)), q, bridgesdk.SendOptions{})
h, _ := c.WaitHandle(ctx, sent)
res, _ := c.Track(ctx, h, bridgesdk.TrackOpts{})
```

```bash
FROM=0x내주소 AMOUNT=10 NETWORK=testnet ./cast/r1.sh                    # 기본은 진단(읽기 전용): 견적·잔고·allowance·시뮬레이션
FROM=0x내주소 AMOUNT=10 NETWORK=testnet SEND=1 PRIVATE_KEY=... ./cast/r1.sh   # 실제 전송 (approve 포함)
```

> **`send()` 는 블로킹 사전 점검을 두 번 강제한다** — approve 전, 그리고 approve·재견적 뒤 시뮬레이션 직전(두 번째에는 allowance 도 막는다). 하나라도 걸리면 `PreflightError{issues, stage}`(Go `*PreflightError`)이고 그 지점부터 아무것도 서명·전송하지 않는다. `preflight()` 를 건너뛰어도 안전하지만, 사유를 미리 보려면 호출한다(§8.2). 한편 `onBroadcast` 콜백이 던져도 tx 는 이미 나갔으므로 `send()` 는 `SentTx` 를 **그대로 돌려주고** 예외는 `sent.callbackError` 에 담는다 — 반드시 확인하고 **재시도하지 말 것**.

> **견적은 봉인(seal)된다.** `quote()` 가 돌려주는 객체는 공개 속성 `seal`(`{v, sdk, payload, digest}` — 전체 견적의 정규 JSON 과 keccak256)을 갖고, `send()` 와 견적을 받는 모든 API(`buildTx`/`BuildTx`, `ensureAllowance`, `preflight`, `simulate`)는 **맨 먼저** 같은 함수(`openQuote`: 봉인 + 설정을 읽지 않는 구조 검증)로 이를 검증하고, 이어서 **그 클라이언트 자신의 networks 설정**과의 일치(클라이언트 바인딩 검사, `assertQuoteBelongs`)를 확인한 뒤 봉인에서 복원한 **비공개 복사본만** 쓴다. 바꾸고 싶으면 객체를 고치지 말고 **`quote()` 를 다시 호출**한다. 값이 안 바뀐 복사(JS 스프레드·`structuredClone`·`jsonParse(jsonStringify(q))`, Go 값 복사·JSON 왕복)는 통과하고, 공개 필드가 봉인과 다르면 `LimitViolationError{field:'seal', reason, path}`(`missing` / `unsupported-version` / `unsupported-sdk` / `corrupt` / `invalid-payload` / `modified`)로 아무 RPC 도 하기 전에 거부된다. 봉인은 **JS 와 Go 가 각자의 형식**이라 한 SDK 의 견적을 다른 SDK 로 보내면 `unsupported-sdk` 다. ⚠️ digest 는 실수·동시 변경을 막는 **무결성 검사**이지 인증이 아니다 — 견적을 가진 사람은 새 봉인을 만들 수 있다.

> 예제 프로그램(`js/examples`, `go/examples`)은 **기본이 dry-run** 이다 — 견적·사전 점검·tx 조립·시뮬레이션만 하고 아무것도 보내지 않는다. 보내려면 `--send`(JS) / `-send`(Go) 와 `PRIVATE_KEY`.

### 1.4 경로별 최소 코드 (JS)

`amount` 는 wei(`bigint`). 단일 홉은 **도착 금액**, 멀티홉(R5/R6)은 **최종 도착 최소액**, X1 은 USDT 금액, X3 는 `amountIn` 이다.

```ts
// R1  BSC → ONE          (BSC 의 ONE 토큰 → ONE 체인 네이티브 ONE)
client.quote({ route: Route.BSC_TO_ONE, amount, sender, recipient });
// R2  ONE → BSC          (네이티브 ONE → BSC 의 ONE 토큰)
client.quote({ route: Route.ONE_TO_BSC, amount, sender, recipient });
// R3  ONE → HyperEVM
client.quote({ route: Route.ONE_TO_HYPEREVM, amount, sender, recipient });
// R4  HyperEVM → ONE
client.quote({ route: Route.HYPEREVM_TO_ONE, amount, sender, recipient });
// R5  BSC → ONE → HyperEVM  (브릿지 요청 한 번으로 두 홉 처리, ERC20 이면 최초 approve 는 별도)
client.quote({ route: Route.BSC_TO_ONE_TO_HYPEREVM, amount: minDelivered, sender, recipient, recipients: { onHub, dest } });
// R6  HyperEVM → ONE → BSC  (브릿지 요청 한 번으로 두 홉 처리, ERC20 이면 최초 approve 는 별도)
client.quote({ route: Route.HYPEREVM_TO_ONE_TO_BSC, amount: minDelivered, sender, recipient });
// X1  BSC USDT → ONE 체인 ONEUSD  (executor 스왑)   ⚠️ 테스트넷: TODO(UNCONFIRMED)
client.quote({ route: Route.BSC_USDT_TO_ONEUSD, amount, sender, recipient, swap: { slippageBps: 50, deadlineSec: 1800 } });
// X2  HyperCore 입금 옵션  (R3: Route.ONE_TO_HYPERCORE / R5: hypercore: true)   ⚠️ 메인넷: HyperCore 링크 전 (TODO(PENDING))
client.quote({ route: Route.ONE_TO_HYPERCORE, amount, sender, recipient, recipients: { core } });
// X3  ONE 체인에서 스왑 후 브리지 (SwapBridgeRouter)
client.quote({ route: Route.SWAP_BRIDGE, amount: amountIn, sender, recipient, x3: { tokenIn, tokenOut, poolFee: 0, toChain: 'bsc' } });
```

Go: `bridgesdk.RouteBSCToOne` … `RouteSwapBridge`(같은 9개) · cast: `cast/r1.sh r2.sh r3.sh r4.sh r5.sh r6.sh x1.sh x3.sh`, X2 는 `CORE_RECIPIENT=… ./cast/r3.sh`(R3) 또는 `./cast/r5.sh`(R5), 상세는 `cast/x2.md`.

### 1.5 최소 금액 (예시 — **항상 라이브로 견적**하세요)

| 경로 | 최소 전송액 | 네트워크 수수료 |
|---|---|---|
| 메인넷 BSC → ONE (R1) | 5 ONE | 0 |
| 메인넷 ONE → BSC / HyperEVM (R2/R3) | 5 ONE | 0.18 ONE |
| 메인넷 HyperEVM → ONE (R4) | 5 ONE | 0 |
| 테스트넷 BSC → ONE (R1) | 1 ONE | 0 |
| 테스트넷 HyperEVM → ONE (R4) | 5 ONE | 0 |
| 테스트넷 ONE → BSC / HyperEVM (R2/R3) | 5 ONE | 0.18 ONE |
| 멀티홉 (R5/R6) | 마지막 홉 최소 5 ONE → 첫 홉 5.18 ONE | 합산 |

수수료·최소액은 운영자가 가격 피드로 수시로 바꾼다. 화면 진입 시 값을 그대로 쓰지 말고 **서명 직전에 다시 견적**(SDK 는 `send()` 안에서 자동으로 한다).

### 1.6 X1 한 줄 요약

BSC 의 USDT 를 보내면 ONE 체인에서 USDTx 로 도착해 Uniswap V3 로 **ONEUSD 로 바꿔 받는다.** 스왑이 못 돌면(슬리피지·deadline·풀) **자금은 USDTx 그대로 받는다 — 손실 없음.** SDK 상태는 이때 `DELIVERED_FALLBACK`(token USDTx)로 알려준다. "ONEUSD 도착"이라고 단정하지 않는다.

### 1.7 경로 고르기

| 하고 싶은 것 | 경로 |
|---|---|
| BSC 의 ONE 토큰(컨트랙트 심볼 `CROSS`)을 ONE 체인으로 | R1 |
| ONE 체인의 ONE 을 BSC 로 | R2 |
| ONE 체인의 ONE 을 HyperEVM 으로 / 되돌리기 | R3 / R4 |
| BSC ↔ HyperEVM 을 **브릿지 요청 한 번으로 두 홉 처리 (ERC20 이면 최초 approve 는 별도)** (허브 경유) | R5 (BSC→HyperEVM) / R6 (HyperEVM→BSC) |
| BSC 의 USDT 를 ONE 체인 ONEUSD 로 | X1 |
| HyperEVM 에 도착한 ONE 을 곧바로 HyperCore 계정에 | X2 (R3/R5 옵션) |
| ONE 체인에서 토큰을 스왑하면서 다른 체인으로 | X3 |

---

## 2. 지원 경로 한눈에

| ID | 경로 | 소스 호출 | 소스 토큰 → 도착 | 홉 |
|---|---|---|---|---|
| R1 | BSC → ONE | BSC 브리지 `bridgeToken(612055, ONE토큰, …)` | ONE ERC20(BSC) → 네이티브 ONE | 1 |
| R2 | ONE → BSC | 허브 `bridgeToken(56, NATIVE, …)` payable | 네이티브 ONE → ONE ERC20(BSC) | 1 |
| R3 | ONE → HyperEVM | 허브 `bridgeToken(999, NATIVE, …)` payable | 네이티브 ONE → HyperEVM ONE ERC20 | 1 |
| R4 | HyperEVM → ONE | HyperEVM 브리지 `bridgeToken(612055, ONE토큰, …)` | ONE ERC20 → 네이티브 ONE | 1 |
| R5 | BSC → ONE → HyperEVM | BSC 브리지, extraData 에 허브 `bridgeTokenForwarded` | ONE ERC20(BSC) → (허브 네이티브) → ONE ERC20 | 2 |
| R6 | HyperEVM → ONE → BSC | HyperEVM 브리지, extraData 에 허브 `bridgeTokenForwarded` | ONE ERC20 → (허브 네이티브) → ONE ERC20(BSC) | 2 |
| X1 | BSC USDT → ONEUSD | BSC 브리지, extraData = V3 router `exactInputSingle` | USDT → USDTx → ONEUSD | 1 + extraCall |
| X2 | HyperCore 입금 | R3/R5 의 가장 안쪽 extraData = `transferToCoreFor` | HyperCore 스팟 잔고 | + extraCall |
| X3 | 스왑 후 브리지 | `SwapBridgeRouter.swapBridgeExactInputSingle(ETH)` | tokenIn → 스왑 → tokenOut 브리지 | 스왑 + 1 |

```
R5   BSC ──bridgeToken(extraData1)──▶ ONE 허브 ──bridgeTokenForwarded──▶ HyperEVM ──(X2: transferToCoreFor)──▶ HyperCore
     사용자 요청 1번 (+필요시 approve)    자동(검증자+컨트랙트)                자동
```

멀티홉 중첩: 다음 홉의 지시를 앞 홉의 `extraData` 안에 접어 넣는다.

```
extraData1 = 허브브리지(20B) ‖ bridgeTokenForwarded(목적지, toDest, v2, nf2, ef2', inner)
inner      = 0x                                  (평범한 도착)
           | HyperEVM-ONE(20B) ‖ transferToCoreFor(coreRecipient, v2)    (X2)
```

---

## 3. 네트워크 정보

아래 표는 `tools/gen.mjs` 가 `networks.json` 에서 생성한다(직접 고치지 말 것).

<!-- BEGIN:networks (generated by tools/gen.mjs from networks.json - do not edit) -->

### Mainnet

| 체인 | chainId | RPC (공개) | Explorer | 네이티브 |
|---|---|---|---|---|
| ONE Chain Mainnet | `612055` | `https://mainnet.onechain.nexus:22001` | `https://explorer.onechain.nexus/612055` | `ONE` |
| BNB Smart Chain Mainnet | `56` | `https://bsc-mainnet-public.cross-api.in` | `https://bscscan.com` | `BNB` |
| HyperEVM Mainnet | `999` | `https://rpc.hyperliquid.xyz/evm` | `https://hyperevmscan.io` | `HYPE` |

| 체인 | 역할 | 주소 |
|---|---|---|
| ONE | 브리지 (프록시) — 직접 호출 | `0xb81d6e000000000000000000000000000000C0de` |
| ONE | 네이티브 토큰 센티넬 (ONE) | `0x0000000000000000000000000000000000000001` |
| ONE | 토큰 USDTx (USDTx, 18 dec) | `0x1c37d4F44ed3eC86d96868001CfA89e97112a85C` |
| ONE | 토큰 ONEUSD (ONEUSD, 18 dec) | `0xff42C13D927a6b9265236619161Accb227b6d452` |
| ONE | 토큰 WCROSS (WCROSS, 18 dec) | `0x731A104a4461aE45BFEf520468B43c262d35E682` |
| ONE | Uniswap V3 SwapRouter (X1 extraCall 타깃) | `0x0e36011c70e28DF14B7Eb39Aab23F8e9F3fa39Ba` |
| ONE | QuoterV2 | `0x9FBa55a11f845Fe029546f6Fe1CA7f6B2a3e07eb` |
| ONE | SwapBridgeRouter (X3) | `0xB298C340816fee45cfB39B1e6EE4DB4D5739e357` |
| ONE | USDTx/ONEUSD 풀 (fee 0) | `0x298c360039de417A1b12aFAc64b9175Dbdd0DbfE` |
| ONE | (참고) BridgeVerifier — SDK 는 라이브 조회 | `0xcf6498dDc3a5d5Ca797444a87EB8B4AA6eB1F9c4` |
| ONE | (참고) BridgeExecutor — SDK 는 라이브 조회 | `0xF356ab353c7f9d2010B66c24bba69c4532AC0f8F` |
| BSC | 브리지 (프록시) — 직접 호출 | `0xa6a1a0c1e6ABa5FC6C84E856dCF81506e2bE06c5` |
| BSC | 토큰 ONE (CROSS, 18 dec) | `0x6bf62ca91e397B5A7d1D6bCe97D9092065d7A510` |
| BSC | 토큰 USDT (USDT, 18 dec) | `0x55d398326f99059fF775485246999027B3197955` |
| BSC | (참고) BridgeVerifier — SDK 는 라이브 조회 | `0x101c1db54Cf056c77ED6c61d446000011A24A2A9` |
| BSC | (참고) BridgeExecutor — SDK 는 라이브 조회 | — (리프 체인, 없음) |
| HYPEREVM | 브리지 (프록시) — 직접 호출 | `0xd8A89e931E504Bdf7775640F800fe46528D097E5` |
| HYPEREVM | 토큰 ONE (ONE, 18 dec) | `0xFaA92e0e9BF90102ff1F731e7DeBD89CD7D1Ebe5` |
| HYPEREVM | HyperCore 시스템 주소 (ONE) | ⚠️ `null` — TODO(PENDING) 메인넷 ONE 토큰이 아직 HyperCore 에 링크되지 않음 (isCoreTokenIndexSet=false), X2 메인넷 비활성 |
| HYPEREVM | (참고) BridgeVerifier — SDK 는 라이브 조회 | `0x75665acd4B5C94c0E865f6ba92FbA266338ADcFC` |
| HYPEREVM | (참고) BridgeExecutor — SDK 는 라이브 조회 | `0x4c3bd7914F9d2bDA7C67233D320953a0976f707b` |

### Testnet

| 체인 | chainId | RPC (공개) | Explorer | 네이티브 |
|---|---|---|---|---|
| ONE Chain Testnet | `612044` | `https://testnet.onechain.nexus:22001` | ⚠️ `null` — TODO(UNCONFIRMED) ONE 체인 테스트넷 익스플로러 공식 URL 미확인 | `ONE` |
| BNB Smart Chain Testnet | `97` | `https://bsc-testnet-public.cross-api.in` | `https://testnet.bscscan.com` | `tBNB` |
| HyperEVM Testnet | `998` | `https://rpc.hyperliquid-testnet.xyz/evm` | ⚠️ `null` — TODO(UNCONFIRMED) HyperEVM 테스트넷 익스플로러 공식 URL 미확인 | `HYPE` |

| 체인 | 역할 | 주소 |
|---|---|---|
| ONE | 브리지 (프록시) — 직접 호출 | `0xb81d6e000000000000000000000000000000C0de` |
| ONE | 네이티브 토큰 센티넬 (ONE) | `0x0000000000000000000000000000000000000001` |
| ONE | 토큰 USDTx (USDTx, 18 dec) | `0x9F85c7B5D7637E18f946cc8AF9C131318c6833d9` |
| ONE | 토큰 ONEUSD | ⚠️ `null` — TODO(UNCONFIRMED) 테스트넷 ONEUSD 주소를 찾지 못함 |
| ONE | 토큰 WCROSS (WCROSS, 18 dec) | `0xda37a3B60E6b97E8CB44b460130d40331efc566b` |
| ONE | Uniswap V3 SwapRouter (X1 extraCall 타깃) | `0x61745D6Dc4342cF01bE2C145218CE1952260cd05` |
| ONE | QuoterV2 | `0x19D62F16FbFc200F8a1eFc521f10b54A3801D10A` |
| ONE | SwapBridgeRouter (X3) | `0xc9B23a5E14747Cc2Acc3Dca7237D3174CE1BCf5B` |
| ONE | USDTx/ONEUSD 풀 (fee 0) | ⚠️ `null` — TODO(UNCONFIRMED) 테스트넷 USDTx/ONEUSD 풀을 찾지 못함 (X1 테스트넷 비활성) |
| ONE | (참고) BridgeVerifier — SDK 는 라이브 조회 | `0x31104F827Ac2AE6C5a1A5315C7D7D8b8dFa61c37` |
| ONE | (참고) BridgeExecutor — SDK 는 라이브 조회 | `0x6147FA2602699dE1eb0141959d93bFE06dEF2279` |
| BSC | 브리지 (프록시) — 직접 호출 | `0x192FFaCAf26A53b439C33F3d2C166ceB0A26bf2d` |
| BSC | 토큰 ONE (CROSS, 18 dec) | `0x26ca2A92ea9659e073621bb85b9117Dc5944Cff1` |
| BSC | 토큰 USDT (tUSDt, 18 dec) | `0xBB188503c32FA7F61E019c3b3Ae61ae6B0b3B492` |
| BSC | (참고) BridgeVerifier — SDK 는 라이브 조회 | `0x1f821CB6D89eC4A343b6812B26c50768D388d34f` |
| BSC | (참고) BridgeExecutor — SDK 는 라이브 조회 | — (리프 체인, 없음) |
| HYPEREVM | 브리지 (프록시) — 직접 호출 | `0xeF7F79dA2281704647306267E7E1C72848768c22` |
| HYPEREVM | 토큰 ONE (ONE, 18 dec) | `0xDD937e58A0fc5094CB9677Cf37754c58602166bB` |
| HYPEREVM | HyperCore 시스템 주소 (ONE) | `0x2000000000000000000000000000000000000Bda` |
| HYPEREVM | (참고) BridgeVerifier — SDK 는 라이브 조회 | `0x934f3d170Ccc91B6001A4Cc333e14317f68df559` |
| HYPEREVM | (참고) BridgeExecutor — SDK 는 라이브 조회 | `0x1ec307E2Ab99198d115A12e65cA586B6b6b1764f` |

<!-- END:networks -->

- BSC 의 ONE 토큰의 컨트랙트 심볼은 `CROSS`, 네이티브 래퍼(wrapped ONE) 토큰의 키·심볼은 `WCROSS` 다. 위 표의 `CROSS` / `WCROSS` 는 온체인 심볼이다.
- 브리지 주소는 **프록시**다(업그레이드되어도 같은 주소). 검증자·executor 주소는 재배포될 수 있어 SDK 가 항상 `bridge.bridgeVerifier()` / `bridge.bridgeExecutor()` 로 라이브 조회한다.
- 라우터(SwapBridgeRouter)는 immutable 이라 재배포될 수 있다 — **주소는 설정에서만** 가져오고 하드코딩하지 말 것.
- 소스 체인 RPC 의 `chainId` 가 설정과 다르면 SDK 는 `NetworkMismatchError` 로 **서명 전에** 멈춘다.

---

## 4. 경로별 상세

공통 규칙(§6 상세):

- **수수료는 소스 체인의 `BridgeVerifier.calculateFee(toChainID, fromToken, value)` 가 돌려준 값을 가공 없이** 넘긴다 → `(minimumValue, networkFee, exFee)`; `value ≥ minimumValue`.
- **msg.value**: 소스가 네이티브면 `value + networkFee + exFee` (SDK 는 정확히 그 값), ERC20 이면 0.
- **approve**: ERC20 소스면 spender = 소스 브리지, 금액 = `value + networkFee + exFee` (X3 는 spender = SwapBridgeRouter, 금액 = `amountIn`). 기본은 **정확한 금액**(`approveAmount: 'max'` 로 바꿀 수 있음). `0 < allowance < 필요액` 이면 USDT 식 토큰을 위해 먼저 0 으로 approve(`zeroFirst: 'auto'`).
- **1:1 전달**: 브리지는 소수점 변환을 하지 않는다. 모든 토큰은 18 decimals(온체인 확인).
- `to` 는 도착 체인에서 **내가 컨트롤하는 주소**여야 한다.

### R1 · BSC → ONE

BSC 브리지 `bridgeToken(612055, BSC_ONE토큰, to, value, networkFee, exFee, 0x)`. ERC20 소스이므로 ONE 토큰을 BSC 브리지에 approve(`value+fees`) 하고 `msg.value=0`. 도착: ONE 체인에서 **네이티브 ONE**. 단계: approve → (재견적) → bridgeToken → 검증자 finalize → 도착(보통 10초~수 분, 테스트넷 관측값).
JS `examples/r1-bsc-to-one.ts` · Go `examples/r1-bsc-to-one` · cast `cast/r1.sh`.

### R2 · ONE → BSC

허브 `bridgeToken(56, NATIVE(0x…01), to, value, networkFee, exFee, 0x)` — **payable**, `msg.value = value + networkFee + exFee`. 도착: BSC 의 ONE 토큰. approve 없음.
JS `r2-one-to-bsc.ts` · Go `r2-one-to-bsc` · cast `cast/r2.sh`.

### R3 · ONE → HyperEVM  (+ X2)

허브 `bridgeToken(999 | 998, NATIVE, to, …)` payable. 도착: HyperEVM 의 ONE ERC20(`HyperMintableERC20`).
**X2 (HyperCore 입금)**: `Route.ONE_TO_HYPERCORE` / cast `CORE_RECIPIENT=0x… ./cast/r3.sh` — extraData = `HyperEVM-ONE토큰(20B) ‖ transferToCoreFor(coreRecipient, value)`. 도착 후 executor 가 이 함수를 불러 토큰을 Core 계정으로 밀어 넣는다(§5.4).
JS `r3-one-to-hyperevm.ts` / `x2-one-to-hypercore.ts` · Go 동일 이름 · cast `cast/r3.sh`.

### R4 · HyperEVM → ONE

HyperEVM 브리지 `bridgeToken(612055, ONE토큰, to, value, networkFee, exFee, 0x)`. 이 토큰은 wrapped(burn 경로)라 `approve(spender = HyperEVM 브리지)` 가 필요하고, 페어의 `minted ≥ value` 여야 한다(SDK 사전 점검).
JS `r4-hyperevm-to-one.ts` · Go `r4-hyperevm-to-one` · cast `cast/r4.sh`.

### R5 · BSC → ONE → HyperEVM (멀티홉) — 브릿지 요청 한 번으로 두 홉 처리 (ERC20 이면 최초 approve 는 별도)

**금액은 뒤에서 앞으로 계산한다**(허브는 부등호가 아닌 엄격 등식을 요구):

```
1) (mv2, nf2, ef2) = 허브 verifier.calculateFee(목적지체인, NATIVE, v2)     v2 ≥ mv2
2) ef2' = ef2 + buffer          ← buffer(hop2FeeBuffer)는 선언 exFee 에만 더한다. value 에 더하면 안 된다
3) v1  = v2 + nf2 + ef2'        ← 허브에 도착하는 금액 == 선언 합계 (엄격 등식)
4) (mv1, nf1, ef1) = 소스(BSC) verifier.calculateFee(612055, 소스토큰, v1)   v1 ≥ mv1
5) 소스 호출: bridgeToken(612055, 소스토큰, toOnHub, v1, nf1, ef1, extraData1)
   extraData1 = 허브브리지 ‖ bridgeTokenForwarded(목적지, toDest, v2, nf2, ef2', inner)
```

허브는 도착 시 **그 시점의 온체인 수수료로 다시 정산**한다(`avail = ctxValue − 현재 nf`, `value_ = max(floor(avail·d/(d+rate)), v2)`, `exFee_ = avail − value_`). 그래서 `v2` 는 "정확한 전달액"이 아니라 **"최소 전달액"** 이다:
수수료가 내려가면 차액이 `toDest` 에게 더 간다. 올라가면 buffer 가 흡수하고, buffer 가 모자라면 그 건은 `BaseBridgeForwardStagedInsufficient` 로 hop 2 가 실패해 **허브에서 멈춘다**(§8). buffer 를 안 쓴 만큼도 `toDest` 에게 간다.

수령 주소는 홉마다 다르다 — **세 개가 의미가 다르므로 각각 채운다**(보통 같은 주소):

| 인자 | 역할 |
|---|---|
| `toOnHub` (hop 1 의 `to`) | hop 2 가 실패하면 **허브에서 네이티브 ONE 을 받는 주소** |
| `toDest` (hop 2 의 `to`) | extraCall 이 실패하면 평범한 토큰을 받는 주소 + 잔돈(dust) |
| `coreRecipient` (X2) | HyperCore 크레딧을 받는 주소 |

사전 조건(SDK `preflight()`): 세 체인 브리지 pause 아님 · 페어 pause 아님 · 허브 `bridgeExecutor() != 0` · executor 가 허브와 `0x55fd0776` 를 허용 · `extraData` 길이 ≤ `maxExtraDataLength`(소스·허브 둘 다; 0 은 무제한). 참고 길이(테스트넷, X2 포함 시): `extraData2` 88 B, `extraData1` 344 B.
JS `r5-bsc-to-hyperevm.ts` · Go `r5-bsc-to-hyperevm` · cast `cast/r5.sh`.

### R6 · HyperEVM → ONE → BSC

R5 와 같은 규칙, 소스가 HyperEVM(burn 경로, `approve(HyperEVM 브리지)`), 목적지가 BSC. `inner` 는 `0x`.
JS `r6-hyperevm-to-bsc.ts` · Go `r6-hyperevm-to-bsc` · cast `cast/r6.sh`.

### X1 · BSC USDT → ONE 체인 ONEUSD

BSC 브리지 `bridgeToken(612055, USDT, to, value, nf, ef, extraData)`, `extraData = V3SwapRouter(20B) ‖ exactInputSingle((USDTx, ONEUSD, fee 0, to, deadline, value, amountOutMinimum, 0))`
(deadline 이 struct 안에 있는 변형, selector `0x414bf389`). USDT 가 허브에 USDTx 로 도착하면 executor 가 V3 router 를 호출해 스왑한다.
견적은 ONE 체인 QuoterV2 의 `quoteExactInputSingle` 로 하고 `amountOutMinimum = 견적 × (1 − slippageBps/10000)`(기본 50 bps, deadline 1800 초 — 둘 다 조절 가능).
**extraData 검증**: SDK 는 `exactInputSingle` 을 **전부 디코드**한다 — `tokenIn == 허브 USDTx`, `tokenOut == 설정된 ONEUSD`, `fee == 풀 fee`, `amountIn == value`, `sqrtPriceLimitX96 == 0`, 정규 인코딩(뒤에 바이트 없음)이 아니면 `handleFromTx` 는 `UnsupportedRouteError`, 추적은 `PROCESSED_UNVERIFIED{unsupported extraData}`(성공 보고 안 함). 디코드한 `{recipient, tokenOut, amountOutMinimum, deadline, amountIn}` 은 핸들(`expect.x1`)에 저장된다.
**도착 보고**: `DELIVERED` 의 `recipient`·`token` 은 **디코드한 `exactInputSingle` 의 `recipient`·`tokenOut`** 이다(`BridgeFinalized.to` 는 executor 경로의 지급 대상이라 다를 수 있다 — `detail.bridgeFinalizedTo`). 금액은 `ExtraCallExecuted.returnData`.
**결과 계약**: 스왑이 revert 하면(슬리피지/deadline/풀) executor 프레임이 되돌아가고 브리지가 **평범한 USDTx 를 `to`(= `BridgeFinalized.to`) 에게 지급**한다 — 손실 없음. 사전 조건: executor 가 V3 router 를 whitelist(+메서드 체크 시 selector) 해야 한다. ⚠️ 테스트넷은 USDTx/ONEUSD 풀을 찾지 못해 `TODO(UNCONFIRMED)`.
JS `x1-usdt-to-oneusd.ts` · Go `x1-usdt-to-oneusd` · cast `cast/x1.sh`.

### X2 · HyperCore 입금 옵션

가장 안쪽 extraData = `HyperEVM-ONE(20B) ‖ transferToCoreFor(coreRecipient, value)`. executor 의 approve spender 가 토큰 자신이라 감사 후 요구되는 allowance 조건을 만족한다.
**`transferToCore(uint256)`(수령자 인자 없음)를 extraData 에 절대 넣지 말 것** — 토큰이 executor 의 Core 계정에 크레딧되어 **영구 동결**된다.
`coreUnit`(ONE 은 10¹⁰ wei) 배수로 **내림**하고 나머지는 `toDest` 로 돌아온다. 사전 조건(SDK): `isCoreTokenIndexSet()`, executor 가 토큰+`transferToCoreFor`(`0x9438568d`) 허용, `coreTransferableAmount(value) > 0`. ⚠️ 메인넷은 `isCoreTokenIndexSet=false` + `TODO(PENDING)` 라 비활성.
**extraData 검증·약정 금액**: SDK 는 `transferToCoreFor(coreRecipient, amount)` 도 디코드해 핸들에 `expect.x2 = {token, coreRecipient, encodedAmount}` 로 저장한다. **추론**(소스 영수증만)은 `encodedAmount` 가 소스에서 **약정한 금액**과 다르면 `UnsupportedRouteError` — 직접 X2 는 `BridgeInitiated.value`, R5 의 중첩 X2 는 forward payload 의 `bridgeTokenForwarded` `value`(v2). **추적**(도착 쪽)은 컨트랙트 규칙을 따른다(`src/BridgeExecutor.sol`, `src/token/HyperMintableERC20.sol`): executor 가 finalize 된 `value` 를 당겨 approve 하고, 토큰이 `coreTransferableAmount(encodedAmount)`(coreUnit 내림) 만 보내며 그 값이 `ExtraCallExecuted.returnData` 이고, 남은 `finalized − consumed` 는 `BridgeFinalized.to` 에게 돌아간다. 보고: `CORE_DEPOSIT_SUBMITTED{coreRecipient, coreAmount(EVM wei 단위, 내림된 값), returnedToDest, finalizedValue}` — hop 2 수수료가 내려 finalize 된 값이 v2 보다 크면 그 차이가 `returnedToDest` 에 보인다. `encodedAmount > finalized` 등으로 `ExtraCallExecuted(false)` 면 `DELIVERED_FALLBACK`. 실행된 call 의 target/selector 가 기대와 다르거나 핸들에 디코드 결과가 없으면 `PROCESSED_UNVERIFIED`.
도착 확인은 HyperEVM 쪽 `ExtraCallExecuted(success=true)` = **`CORE_DEPOSIT_SUBMITTED`** 까지만 증명한다(EVM 에서 시스템 주소로 보낸 것). Core 크레딧은 비동기이므로 공개 info API `spotClearinghouseState`(사용자=coreRecipient)로 확인한다(SDK `checkCoreBalance`, 최선 노력).

### X3 · SwapBridgeRouter (ONE 체인에서 스왑 후 브리지)

한 트랜잭션: Uniswap V3 로 `tokenIn → tokenOut` 스왑 후 `tokenOut` 을 `toChainID` 로 브리지. 어느 단계든 실패하면 **전부 revert**(아무것도 안 움직임).
- 네이티브 입력: `swapBridgeExactInputSingleETH`, `tokenIn == WCROSS`, `msg.value == amountIn`. ERC20 입력: `swapBridgeExactInputSingle`, `msg.value = 0`, **라우터에 `amountIn` approve**(브리지 수수료는 스왑 출력에서 차감된다).
- `tokenOut` 은 허브에 `toChainID` 페어가 있는 ERC20 이어야 한다(없으면 견적 status `NoPair`).
- 견적: `getAmountSwapBridgeOut(toChainID, tokenIn, tokenOut, fee, amountIn)` → `(status, swapAmountOut, bridgeValue, networkFee, exFee)` (view 가 아니라 `eth_call` 로 호출).
- **바닥이 두 개**다: ① 스왑 출력 바닥 `amountOutMinimum = floor(swapAmountOut×(10000−bps)/10000)` — **온체인에서 강제**; ② 도착 바닥 `minBridgeValue = getExpectedBridgeAmount(…, amountOutMinimum).bridgeValue` — 사용자에게 보여주는 값(견적~포함 사이에 브리지 수수료가 오르지 않는 한 유지).
- **extraData 미지원**: X3 는 `extraData = 0x` 만 지원한다(`x3` 요청에 extraData 옵션이 없고, JS 가 런타임에 넣으면 `InvalidOptionError`). 영수증의 X3 `BridgeInitiated.extraData` 가 비어 있지 않으면 `handleFromTx` 는 `UnsupportedRouteError`.
- 대표 예: ONE 체인 **ONEUSD → USDTx → BSC USDT**. SDK 는 임의 조합을 받는다.
JS `x3-swap-bridge.ts` · Go `x3-swap-bridge` · cast `cast/x3.sh`.

---

## 5. 동작 원리

### 5.1 lock / mint / burn, 허브 앤 스포크
ONE 체인이 허브다. 원본 토큰이 있는 체인에서는 **lock**, 반대편(wrapped)에서는 **mint/burn**. 네이티브 ONE 은 ONE 체인에서 그대로 움직인다(센티넬 주소 `0x…0001`).
소스 체인의 `bridgeToken` 이 `BridgeInitiated(toChainID, index, …)` 를 내면 검증자들이 서명해 목적지에서 `finalizeBridgeBatch` 로 처리한다. **`finalizeBridgeBatch` 는 한 트랜잭션에 여러 항목**을 담을 수 있어서 이벤트는 항상 `(fromChainID, index)` 키로 구분해야 한다(§7).

### 5.2 extraData 와 executor
도착 브리지는 `extraData` 를 `target(20B) ‖ calldata` 로 파싱해 `BridgeExecutor` 에게 target 호출을 맡긴다(target 이 whitelist 에 있어야 하고, 토큰 target 은 메서드 체크가 필요). executor 가 실패하면 브리지가 **평범한 지급으로 폴백**한다(자금 안전).

### 5.3 멀티홉 재정산
허브는 hop 1 도착 시 `bridgeTokenForwarded` 를 executor 경유로 호출해 hop 2 를 개시한다(`msg.sender == executor`, 일시 컨텍스트 소비). 선언값 합계가 도착액과 **정확히 같아야** 하고, 실제 정산은 현재 온체인 수수료로 다시 계산된다(§4 R5).

### 5.4 HyperCore 크레딧은 누구에게 가나
Core 는 시스템 주소로 들어온 ERC20 `Transfer` 의 `from` 에게 크레딧한다. extraCall 시점 토큰 보유자는 **executor** 라서 평범한 `transferToCore` 는 executor 의 Core 계정에 크레딧(= 동결)된다. `transferToCoreFor(coreRecipient, amount)` 는 `executor → 사용자 → 시스템주소` 두 단계로 `from` 이 사용자가 되게 한다. `coreUnit` 배수로 내림하고 나머지는 호출자(→ `toDest`)에게 남는다.

---

## 6. 수수료

| 항목 | 의미 |
|---|---|
| `minimumValue` | 전송액 하한 (USD 환산, 가격 피드에 반비례) |
| `networkFee` | 목적지 finalize 가스를 소스 토큰으로 환산 |
| `exFee` | `value × exFeeRate / 10000` |

- **황금률**: 수수료는 직접 계산하지 말고 소스 verifier 가 돌려준 값을 **가공 없이** 넘긴다. native 전송에서 버퍼를 얹으면 `BaseBridgeInvalidValue` 로 실패한다(인자는 하한 검사에만 쓰이고 컨트랙트가 계산값으로 덮어쓴다).
- **`exFee = 0` 하드코딩 금지**: 요율 `0` 은 "기본값 사용", `type(uint).max` 가 "면제"다.
- **견적은 시간이 지나면 무효**: SDK 기본 TTL 30초. `send()` 는 approve 후 **재견적**하며, 재견적은 사용자가 승인한 한도를 절대 느슨하게 하지 못한다 — 총 지출 상한 ↑ 이거나 도착 하한 ↓ 이면 `RequoteRequiredError`(전송 안 함). 스왑(X1/X3)은 **원래 `amountOutMinimum` 을 그대로 유지**하고(아래로 재계산하지 않음), X3 는 원래 바닥으로 다시 계산한 도착 바닥이 승인값 이상이어야 한다.
- 멀티홉은 선언 수수료가 hop 1 인코딩 시점에 고정되므로 변동에 특히 민감하다 → `hop2FeeBuffer` (선언 exFee 에 얹는다, §4 R5).
- 시뮬레이션 후 포함 사이에도 상태가 바뀔 수 있다(허브 hop 2 는 실행 시점에 재정산).

---

## 7. 이벤트 & 상태 추적

### 7.1 TransferHandle
`sent.wait()` / `client.handleFromTx(체인, txHash)` 가 돌려주는 **직렬화 가능한 JSON** — `{network, route, srcChain…, srcTxHash, hop1:{destChain, index}, hop2?, expect:{value, v2?, x1?, x2?}, scanFrom, scans}` (`expect.x1` / `expect.x2` 는 소스 영수증에서 **디코드한** X1·X2 호출 — 이것이 없으면 추적은 X1/X2 를 성공으로 보고하지 않는다). 저장해 두면 재시작 후에도 `track(handle)` 이 이어서 로그를 스캔한다(`lastScanned` 보존, `(txHash, logIndex)` 로 중복 제거). `handleFromTx` 는 경로를 **소스 영수증의 `BridgeInitiated` 내용**(목적지 체인, `extraData` 의 target·selector, 중첩 hop-2 목적지)에서 결정적으로 복원한다 — 설정된 소스 브리지(X3 는 설정된 SwapBridgeRouter)가 낸 로그만 후보이고 이벤트의 **부재**로는 추론하지 않는다. `route` 옵션은 선택이며 복원 결과와 다르면 `RouteMismatchError`, 지원하지 않는 target/selector 는 `UnsupportedRouteError`, 한 영수증에 후보가 둘 이상이면 고르지 않고 `AmbiguousSourceError`(후보 `(toChainID, index, logIndex)` 목록 포함) — `index` / `logIndex` 옵션으로 하나를 지정한다.

### 7.2 키 상관 규칙
이벤트는 인덱스된 **`(fromChainID, index)`** 로만 연결한다: `BridgeFinalized`, `BridgePending`, `ExtraCallExecuted`, `ForwardInitiated/ForwardFailed`, `ManualReleased`, `PendingRemoved`. 같은 트랜잭션 안에 있어도 키가 다르면 무시한다(배치 영수증). 멀티홉 hop 2 의 index 는 같은 영수증에서 허브가 낸 `BridgeInitiated`(ForwardInitiated 앞, `toChainID/to/value/networkFee/exFee/extraDataHash` 가 일치하고 사이에 다른 ForwardInitiated 가 없는 **유일한** 후보)로 정한다. 0개 또는 2개 이상이면 추측하지 않고 `PROCESSED_UNVERIFIED`.

### 7.3 결정 순서 (홉마다, 먼저 맞는 규칙)
1. 소스 영수증 없음/실패 → `SOURCE_SUBMITTED` / `SOURCE_FAILED`
2. 현재 `allPendingIndex(src)` 에 있음 → `PENDING_ON_DEST` (옛 `BridgePending` 로그는 근거가 못 된다)
3. `getNextFinalizeIndex(src) ≤ index` → `IN_FLIGHT`
4. 명시적 해소 이벤트 먼저: `PendingRemoved` → `PENDING_REMOVED`; `BridgeFinalized` 가 `releasePending`/`manualReleasePending(WithRecipient)` 트랜잭션에서 나왔거나 `ManualReleased` 가 있으면 → `RELEASED_FROM_PENDING` (pending 저장소가 extraData 를 지웠으므로 **평범한 토큰**이 지급됨)
5. 키가 맞는 `BridgeFinalized` 가 없음 → `PROCESSED_UNVERIFIED`
6. `finalizeBridgeBatch` 에서 나온 `BridgeFinalized` → 요청한 홉 종류로 분류

### 7.4 상태표

| 상태 | 의미 / 근거 |
|---|---|
| `SOURCE_SUBMITTED` / `SOURCE_FAILED` | 영수증 없음 / `status 0` |
| `IN_FLIGHT` | 도착 체인 finalize index 가 아직 못 미침 |
| `PENDING_ON_DEST{reason}` | 도착 pending 큐에 보류(임계치 등). 자동/운영자 해제 |
| `DELIVERED` | 평범한 홉 완료, 또는 X1 에서 `ExtraCallExecuted(true)` — X1 의 수령자·토큰은 **디코드한 `exactInputSingle` 의 `recipient`·`tokenOut`**, 금액은 키가 맞는 `returnData` 에서 |
| `DELIVERED_FALLBACK{token,reason}` | 요청한 extraCall 이 실패/생략되어 **평범한 토큰** 지급(X1 → USDTx, X2 → HyperEVM ONE) |
| `RELEASED_FROM_PENDING` | pending 에서 풀려 평범한 토큰 지급(허브 hop 1 이면 허브 네이티브 ONE, hop 2 없음) |
| `CORE_DEPOSIT_SUBMITTED` (X2) | EVM 쪽 입금 성공만 증명(`detail`: `coreRecipient`, `coreAmount`, `returnedToDest`, `finalizedValue`). Core 크레딧은 비동기 — info API 로 확인 |
| `FORWARDED{hop2}` | 허브가 hop 2 를 개시(키가 맞는 `ForwardInitiated` + 유일한 `BridgeInitiated`). hop 2 가 아직 진행 중인 동안의 전체 상태이기도 하다. SDK 는 별도의 `HUB_ARRIVED` 상태를 내지 않는다 — 허브 도착은 `ForwardInitiated`/`ForwardFailed` 와 함께일 때만 관측 가능하기 때문 |
| `HUB_FALLBACK{code}` | 키가 맞는 `ForwardFailed` — 허브에서 네이티브 ONE 을 `toOnHub` 에게 지급 |
| `PENDING_REMOVED` | 운영자가 pending 제거 — 문의 |
| `PROCESSED_UNVERIFIED` | 처리는 됐으나 근거 부족/모호 — **성공으로 보고하지 않음** |
| `UNKNOWN{reason}` | RPC·로그 불가(백오프 소진) 또는 호출 타임아웃(`reason` 이 `timeout:` 로 시작, `timedOut: true`) — 수동 복구 안내 포함 |

정상 구간 소요는 홉당 **10초 ~ 5분**(테스트넷 관측치, 보장 아님). `PENDING`/운영 정지는 어떤 deadline 보다 길 수 있다(X1 deadline → USDTx 폴백).
`track()` 의 타임아웃은 **현재(미완료) 상태**를 돌려준다 — 온체인 실패와 다르다. 모든 RPC 호출은 `min(callTimeoutMs(기본 15초), 남은 시간)` 으로 제한되므로(Go: `ClientOptions.CallTimeout` + `TrackOpts.Timeout` 의 context 기한), 응답이 멈춘 provider 에서도 `timeoutMs` + 여유 안에 돌아온다 — 확립된 상태가 없으면 `UNKNOWN{reason:'timeout: …'}` + `timedOut`. 로그 조회는 청크(기본 2,000 블록) + 지수 백오프(1초 ×2, 상한 30초, 6회) 후 `UNKNOWN`. ONE 체인 노드가 오래된 로그를 주지 않을 수 있어 그 경우 `PROCESSED_UNVERIFIED`/`UNKNOWN` 이지 거짓 성공이 아니다.

`FinalizeStatus`(pending 사유): `TokenPaused, VerificationAmountThresholdExceeded, PeriodTotalValueThresholdExceeded, TransferFailed, MintFailed, TokenScoreOverflow, TokenCurrentVolumeOverflow, CrossSupplyLimitExceeded, TokenPriceUnavailable, InsufficientLiquidity, InvalidRecipient`.

---

## 8. 에러 처리

### 8.1 revert 선택자 (`decodeBridgeError(data)` / Go `DecodeBridgeError`)

| selector | error | 사용자 조치 |
|---|---|---|
| `0x58e88785` | `BaseBridgeInvalidAmount()` | 최소액 미달 또는 견적 만료 → 재견적 |
| `0x943892eb` | `BaseBridgeInvalidValue(uint256,uint256)` | `msg.value` 불일치, 첫 인자가 기대값 |
| `0x9df556d1` | `BaseBridgeForwardAmountMismatch(uint256,uint256)` | 멀티홉 등식 불일치 → 뒤에서 앞으로 재계산 |
| `0x8399ac8d` | `BaseBridgeForwardStagedInsufficient(uint256,uint256)` | 허브 수수료 상승 > buffer → hop 2 실패, 허브에서 멈춤 |
| `0xbe75e8c7` | `BaseBridgeForwardNotOrigin()` | 이 토큰은 멀티홉 경유 불가 |
| `0x72a7667e` | `BaseBridgeExtraDataTooLong()` | extraData 길이 초과 |
| `0x8219abe3` | `BaseBridgeCanNotZeroAddress()` | 수령 주소 미입력 |
| `0x70709ede` / `0xdf8496f2` / `0x2a612de6` | `RegistryTokenPaused` / `RegistryChainPaused` / `RegistryNotExistToken` | 토큰·체인 정지 / 미등록 페어 |
| `0xd93c0665` | `EnforcedPause()` | 브리지 점검 중 |
| `0xfb8f41b2` | `ERC20InsufficientAllowance(...)` | approve 필요(금액은 `value+fee`) |
| `0xa6bec22b` / `0x428239e8` | `BETargetNotWhitelisted` / `BEMethodNotWhitelisted` | executor 미등록 — 브리지 운영자에게 문의 (폴백으로 평범한 지급) |

선택자 표는 ABI 에서 **생성**한다(`tools/gen-abi.mjs`). 알 수 없는 선택자는 원문 그대로 돌려준다.

### 8.2 재시도 결정표 (절대 자동 재전송하지 않는다)

| 상황 | SDK 가 하는 일 | 사용자 조치 |
|---|---|---|
| 시뮬레이션 revert | `SimulationRevertError`(디코드된 에러), 전송 안 함 | 사유 해결 후 새 견적 |
| 견적 만료 | `QuoteExpiredError` | `quote()` 다시 |
| **사전 점검 실패** | `PreflightError{issues, stage}` — `stage` 는 항상 채워진다: `pre-approve`(approve 전) / `requote`(approve 뒤 채택 견적을 만들다 난 오류, 예: X3 견적 status) / `post-approve`(시뮬레이션 직전). 그 지점부터 서명·전송 안 함. 모든 `error` 이슈가 막는다. RPC 오류·타임아웃도 `PREFLIGHT_INCOMPLETE{check, error}` 로 막는다 — **검증하지 못한 점검은 통과가 아니다** | 이슈 해결(또는 RPC 교체) 후 새 견적 |
| **봉인이 깨진 견적** | `LimitViolationError{field:'seal', reason, path}` — 봉인 없음(`missing`)·버전/SDK 불일치·digest 불일치(`corrupt`)·payload 스키마 위반(`invalid-payload`, 잘못된 JSON·필수 필드 누락·숫자 아닌 금액·잘못된 주소)·공개 필드가 봉인과 다름(`modified`, 처음 다른 JSON 경로를 `path` 로). **아무 RPC·approve·서명도 하기 전에** 거부 | `quote()` 를 다시 호출 (객체를 고쳐 쓰지 않는다) |
| **다른 네트워크·설정의 견적** | `NetworkMismatchError`(견적 바인딩) — 견적의 네트워크·체인·설정 소유 주소(브리지/라우터/허브/고정 토큰/approve spender)가 이 클라이언트 설정과 다르면 approve·서명 **전에** 거부. 사용자 입력 값(X3 토큰·수령자·금액)은 설정과 비교하지 않고 요청 스냅샷·calldata 와 대조 — 어긋나면 `LimitViolationError`(내부 변조) | 이 클라이언트로 새로 견적 |
| **서명된 tx 가 요청과 다름** | `SignerMisbehaviorError{field}` — replay 보호 없음(pre-EIP-155) / 다른 chainId / 복구한 sender 가 다름 / `to·value·data·nonce·gas·gasPrice` 가 바뀜. 브로드캐스트 전에 거부 | 서명 도구·지갑 점검 |
| **RPC 호출 타임아웃**(전송 전) | `RpcTimeoutError{method, timeoutMs}` — 읽기 단계라 전송된 것 없음(서명자의 `provider.getNetwork()`·`getAddress()` 읽기도 `callTimeoutMs` 로 제한된다. 지갑 서명 프롬프트는 제외)(approve 는 이미 나갔을 수 있으니 allowance 확인) | 재시도 안전 |
| 재견적이 한도 완화 | `RequoteRequiredError{oldQuote,newQuote,violated}`, 전송 안 함 | `newQuote` 검토 후 새로 동의 |
| approve 실패 | `ApproveFailedError`, 중단 | 원인 해결 |
| 지갑이 서명 전에 거절(4001/ACTION_REJECTED) | `SignerRejectedError{safeToRetry}` | 다시 시도해도 안전(아무것도 서명 안 됨) |
| 서명 오류 / `onSigned`·`onSubmitting` 예외 | 전송 안 함 | 원인 해결. **다른 서명 방식으로 자동 전환하지 않는다** |
| **전송 결과 불명** (`BroadcastOutcomeUnknownError`) — 전송 요청이 나간 뒤의 에러·**타임아웃**(원인이 `RpcTimeoutError` 일 수 있으나 단독으로는 노출되지 않는다) | `intent` + `checkpoint`(+ Mode L 이면 raw/hash/nonce) 제공 | **재전송 금지.** `findSourceTransfer(intent, checkpoint)`, 해시를 알면 `handleFromTx(체인, hash)`; Mode L 이면 `rebroadcast(raw)`(같은 서명 tx 라 중복 불가) |
| **`onBroadcast` 콜백 예외** | `send()` 는 예외 없이 **`SentTx` 를 반환**하고 `sent.callbackError{name,message}`(Go: `SentTx.CallbackError`; 콜백은 반환값이 없어 panic 을 recover) 를 채운다 — 이미 전송된 tx 라서 사전 실패처럼 보이면 안 된다 | 저장 실패는 따로 처리하고 **재시도 금지**(중복 전송) |
| Mode W 지갑이 응답하지 않음 | 지갑 프롬프트는 **기본적으로 타임아웃이 없다**(사용자가 시간이 필요하고, 타임아웃이 "지갑이 안 보냈다"를 증명하지 못한다). `walletTimeoutMs` 를 주면 만료 시 `BroadcastOutcomeUnknownError` | `findSourceTransfer` |

**언제 새 트랜잭션이 안전한가** — 원본이 **절대 올라올 수 없음이 증명**될 때만: (a) 지갑이 **서명 전에** 거절한 경우, 또는 (b) Mode L 에서 원본 서명 tx(hash+nonce)를 알고, **다른 해시가 같은 nonce 로 채굴**되었으며 그 영수증이 브리지 전송을 하지 않았음을 증명할 때(`status 0`, 또는 `status 1` 인데 소스 브리지/라우터의 `BridgeInitiated`(X3 는 `SwapBridge`)가 sender 로 없는 0원 자기전송 취소 등). 교체 tx 가 **같은 전송**을 실행했으면(지갑 "속도 올리기") 그것이 그 전송이다 → `FOUND`, 계속 추적, 재전송 금지. **다른** 전송을 실행했으면 `REPLACED_BY_DIFFERENT_TRANSFER` 로 알리고 사용자가 판단. 해시가 다르다는 것만으로는 근거가 못 되고, nonce 가 올라갔다/로그가 없다/지갑 UI 에 "취소됨" 이 떴다는 것도 Mode W 에서는 충분하지 않다(지갑이 나중 nonce 로 다시 제출할 수 있다). 그 밖의 경우: `findSourceTransfer` 로 계속 확인하고 지갑 내역을 확인하고 **재전송하지 않는다.**

**교체 tx 의 영수증 분류(원본이 어떤 종류였든 같다)** — 성공한 교체 영수증의 **모든** 전송 모양 로그(`BridgeInitiated`·`SwapBridge`, **어느 emitter 든**)를 모은다. 하나도 없을 때만 `NEW_TX_ALLOWED`(+ 체인·sender·nonce 조건). 있으면 M(intent 와 완전히 일치하는 사용자 전송)·D(일치하지 않는 사용자 전송)·U(귀속할 수 없는 증거: 모르는 emitter, sender 가 아닌 from/user 인데 X3 링크로 설명되지 않음, 링크가 깨진 `SwapBridge`)로 나눠 **위에서부터 처음 맞는 규칙**으로 판정한다:

| 조건 | 판정 |
|---|---|
| U 가 비어 있지 않음 | **`REPLACED_UNVERIFIED`**{matched, different, unattributed} — 새 tx 금지, 수동 검토(M·D 가 같이 있어도 마찬가지) |
| \|M\| > 1 | `AmbiguousSourceError` (고르지 않음) |
| \|M\| = 1, D 없음 | `FOUND` |
| \|M\| = 1, D 있음 | `REPLACED_UNVERIFIED` (같은 sender 가 한 tx 에서 여러 전송을 했다) |
| M 없음, D 있음 | `REPLACED_BY_DIFFERENT_TRANSFER` |
| M·D·U 모두 없음 | `NEW_TX_ALLOWED` |

X3 매칭은 둘로 나뉜다: **구조적 링크**(`linkSwapTransfer`, intent 와 무관 — 설정된 라우터의 `SwapBridge` 를 같은 영수증의 설정된 브리지 `BridgeInitiated`(from=라우터, `index==initiateIndex`, `toChainID`·`fromToken==tokenOut` 일치, `value+수수료 ≤ amountOut`)에 연결, 견적의 수수료·산출 상한은 **비교하지 않는다**)와 **intent 비교**(`matchesSwapIntent`: user, tokenIn/tokenOut/amountIn/toChainID, **수령자**, extraData 해시 = keccak(0x), **목적지 토큰 = 견적 시점에 저장한 `match.toToken`**, `amountOut ≥ amountOutMinimum`). 목적지 토큰은 허브 `getTokenPair(toChainID, tokenOut).remoteToken` 이다(허브 tokenOut 이 아니다). `handleFromTx`(intent 없음)는 링크만 쓴다.

**복구·추적은 증거를 읽기 전에 RPC 의 체인부터 확인한다** — `findSourceTransfer`·`analyzeReplacement`·`handleFromTx`·`track`/`trackOnce`·`wait`·`rebroadcast` 모두 `eth_chainId` 가 설정과 같은지 먼저 보고(다르면 `NetworkMismatchError`, 증거를 읽지 않음), intent/체크포인트의 chainId 가 클라이언트 설정과 다르면 어떤 읽기보다 먼저 `NetworkMismatchError` 를 던진다. `NEW_TX_ALLOWED` 는 하나의 판정 함수에서만 나오며, 교체 tx 는 **chainId(EIP-155; 서명에 chainId 가 없는 구형 tx 는 증거로 받지 않음)·sender·nonce 가 모두 원본과 일치**해야 하고 그 영수증이 전송을 하지 않았음을 보여야 한다. 다른 체인의 RPC 가 같은 nonce 를 채굴했다고 말해도 그것은 증거가 아니다. `eth_chainId` 자체가 **오류이거나 응답이 없으면**(불일치가 아니라 검증 불가) 추적은 `UNKNOWN`(사유 `chain-unverified: …`, 시간 초과면 `timeout: …` + `timedOut`)을 돌려주고, 복구 API 는 오류를 그대로 전파한다(판정 없음 = 새 tx 허용 없음). 어느 쪽이든 그 RPC 에서 영수증·로그·블록·tx 를 읽지 않는다.

`findSourceTransfer` 는 해시 없이도 소스 체인에서 후보를 찾고, 후보를 **SubmittedIntent 전체 필드**(토큰·보낸이·수령자·value·extraData 해시는 정확히 일치, 수수료는 `이벤트 수수료 ≤ intent 수수료`)와 대조한다. 브리지는 호출 수수료가 계산값 이상일 때만 받아들이고 **계산값을 이벤트에 기록**하므로, 견적~포함 사이에 수수료가 내려가면 이벤트 수수료가 intent 보다 작다(`BaseBridge._checkInitiateAmount`). `FOUND`(정확히 1개) / `AMBIGUOUS`(동일 요청 2개 이상 — 고르지 않음) / `NOT_FOUND_YET`. X3 는 브리지 이벤트의 `from` 이 **라우터**이므로 라우터 `SwapBridge(user,…)` 로 찾는다.

### 8.3 중간에 멈춘 자금은 어떻게 되나 — 자금은 안전하다

| 실패 지점 | 결과 |
|---|---|
| hop 1 자체 | 트랜잭션 revert, 아무 일도 없음 |
| 허브 hop 2 개시(R5/R6) | 허브에서 **네이티브 ONE 지급**(`toOnHub`) → `HUB_FALLBACK`. 사용자가 R2/R3 으로 직접 이어서 보낸다 |
| extraCall(X1 스왑 / X2 Core 입금) | executor 프레임 revert → **평범한 토큰** 지급(`DELIVERED_FALLBACK`: X1 → USDTx, X2 → HyperEVM ONE) |
| pending 보류 | 임계치 해제 후 자동 또는 운영자 release → 평범한 토큰 |

extraCall 실패의 흔한 원인: executor whitelist 누락, `coreTokenIndex` 미설정, 내림 결과 0, 스왑 슬리피지/deadline.

---

## 9. 체크리스트 & FAQ

**전송 전 체크리스트(`preflight` — `send()` 가 같은 점검을 강제한다)**: 세 체인 브리지 `paused()==false`(소스 + 허브 + **소스 뒤의 모든 브리지(최종 목적지), `DEST_BRIDGE_PAUSED`** — 목적지 RPC 의 `chainId` 도 확인하고, 읽기 실패·타임아웃·체인 불일치는 `PREFLIGHT_INCOMPLETE{check:'DEST_BRIDGE_PAUSED'}` 로 막는다. 목적지 **페어**는 점검하지 않는다) · 페어 등록·미정지 · (멀티홉) 허브 `bridgeExecutor()!=0` 및 whitelist · (X1) executor 의 V3 router whitelist(+메서드 체크), 풀 존재(`factory.getPool`)·유동성 · (X2) `isCoreTokenIndexSet()` 와 executor whitelist(메서드 체크 켜진 경우 `transferToCoreFor`) · (X3) 라우터 `bridge() == 허브`, tokenOut 허브 페어 · extraData 길이(소스 한도는 **바깥** extraData `EXTRADATA_TOO_LONG_SOURCE`, 허브 한도는 허브가 hop 2 로 넘기는 **안쪽** extraData `EXTRADATA_TOO_LONG_HUB`) · (X3) 라우터 견적 status 가 Success(`X3_QUOTE_<이름>`, approve 전에도 다시 확인) · 잔고 · allowance(approve 후에 막음) · wrapped 페어는 `minted ≥ value`.

- **BSC 테스트넷 가스 가격**: `eth_gasPrice` 표시가(약 0.05 gwei)로 보내면 채굴되지 않고 mempool 에 걸린다. 같은 nonce 에 더 높은 가격으로 교체해야 풀린다(3 gwei 로 해소 확인). SDK 는 chain 97 에서 최소 1 gwei 를, cast 스크립트는 `--legacy --gas-price ${GAS_PRICE:-3gwei}` 를 쓴다.
- **동시성/nonce**: SDK 는 한 호출 안에서 approve → bridge 순서를 직렬화한다. **같은 계정에서 동시에 여러 전송**을 보내면 nonce 는 호출자 책임이다(Mode W 는 지갑이 nonce 를 관리).
- **키 관리**: SDK 는 키를 저장·전송하지 않는다. 예제는 `PRIVATE_KEY` 환경변수에서만 읽는다. 브라우저 지갑은 Mode W(`sendTransaction`), 로컬 키는 Mode L(서명 → `onSigned` 로 raw 저장 → 브로드캐스트).
- **finality**: ONE 체인에는 reorg 가 없다 — 컨펌 수 임계값을 만들지 말 것. 검증자 쪽 finality 는 연동 범위 밖이다.
- **주소 입력**: 홉마다 `to` 는 그 체인에서 **내가 통제하는** 주소.
- **`SELF_SIGNED_CERT_IN_CHAIN`(Node)**: TLS 를 가로채는 프록시 환경에서 나는 오류다. 그 프록시의 CA 를 `NODE_EXTRA_CA_CERTS=/경로/ca.pem` 으로 추가한다(Go·cast 는 시스템 신뢰 저장소를 쓴다).
- Q. 수수료를 넉넉히 넣으면? — A. 안 된다(§6). 소스 verifier 값을 그대로.
- Q. 멀티홉에서 BSC → HyperEVM 으로 간 금액이 `amount` 보다 크다? — A. `amount` 는 **최소** 전달액이다(§4 R5, 수수료 하락/미사용 buffer 는 `toDest` 에게).

---

## 10. 부록

### 10.1 미확인 값 목록 — `SOURCES.md §5` 와 동일

| 항목 | 마커 |
|---|---|
| HyperEVM 메인넷 Core 시스템 주소 (X2 메인넷 — ONE 토큰 HyperCore 링크 전) | `TODO(PENDING)` — 체인에 설정되면 갱신 |
| ONE 체인 테스트넷 explorer, HyperEVM 테스트넷 explorer / info API | `TODO(UNCONFIRMED)` |
| 테스트넷 ONEUSD / USDTx-ONEUSD 풀 (X1 테스트넷) | `TODO(UNCONFIRMED)` |

값이 채워지면: `networks.json` 수정 → `node tools/gen.mjs` → `node tools/check-consistency.mjs` → 테스트(SOURCES.md "테스트 방법") + `cast/00-quote.sh` 재실행 → 이 ⚠️ 박스 갱신.
