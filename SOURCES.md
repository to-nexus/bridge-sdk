# SOURCES — 값의 출처와 확인 방법

`networks.json` 의 모든 값은 아래 표의 행(`S-…`)에 연결된다. 재현은 `bash tools/probe.sh` (읽기 전용 `cast call` / `cast code` /
`cast storage` / `cast chain-id`, 공개 RPC 만 사용). 확인일: **2026-10-27** (HyperEVM 메인넷 브리지·ONE 체인 메인넷 explorer/네이티브 심볼·ONE 체인 테스트넷 네이티브 심볼·메인넷 SwapBridgeRouter 는 **2026-10-28**).

출처 종류: `onchain` = 온체인 읽기로 확인(명령·결과 기록) · `repo+onchain` = `bridge-contracts` 레포의 값이 온체인과 일치 ·
`public-official` = 운영 주체의 공식 공개 문서(RPC·explorer·네이티브 심볼 한정) ·
`operator` = 체인 운영 주체가 제공한 네트워크 정보(explorer·네이티브 심볼 한정, 공개 페이지 응답으로 교차 확인). **확인 불가 값은 `null` + `TODO(PENDING)` / `TODO(UNCONFIRMED)`.**

## 1. 체인 ID / RPC

| id | 값 | 종류 | 근거 (명령 → 결과) |
|---|---|---|---|
| S-C01 | ONE 체인 메인넷 612055 / `https://mainnet.onechain.nexus:22001` | onchain | `cast chain-id --rpc-url …` → `612055` |
| S-C02 | BSC 메인넷 56 / `https://bsc-mainnet-public.cross-api.in` | onchain | `cast chain-id` → `56` |
| S-C03 | HyperEVM 메인넷 999 / `https://rpc.hyperliquid.xyz/evm` | onchain + public-official | `cast chain-id` → `999`; Hyperliquid 공식 문서(`hyperliquid.gitbook.io` → HyperEVM)의 RPC·chainId 와 일치 |
| S-C04 | ONE 체인 테스트넷 612044 / `https://testnet.onechain.nexus:22001` (비표준 포트) | onchain | `cast chain-id` → `612044` |
| S-C05 | BSC 테스트넷 97 / `https://bsc-testnet-public.cross-api.in` | onchain | `cast chain-id` → `97` |
| S-C06 | HyperEVM 테스트넷 998 / `https://rpc.hyperliquid-testnet.xyz/evm` | onchain + public-official | `cast chain-id` → `998`; Hyperliquid 공식 문서와 일치 |

## 2. 메인넷 주소

| id | 값 | 종류 | 근거 |
|---|---|---|---|
| S-M01 | ONE 브리지(프록시) `0xb81d…C0de` | onchain | `allChainIDs()` → `[56,1000,8217,999]`; `paused()` false; ERC-1967 impl 슬롯 → `0x02aE228e…e917`, 그 코드에 `bridgeTokenForwarded` selector `0x55fd0776` 존재; `maxExtraDataLength()` 1024 |
| S-M02 | ONE BridgeVerifier `0xcf64…F9c4` (참고용, SDK 는 라이브 조회) | onchain | `hub.bridgeVerifier()` 일치; `calculateFee(56, NATIVE, 10e18)` → `(5e18, 1.8e17, 0)` |
| S-M03 | ONE BridgeExecutor `0xF356…0f8F` (참고용) | onchain | `hub.bridgeExecutor()` 일치; `isWhitelistedTarget(hub)`=true, `isMethodCheckEnabled(hub)`=true, `isWhitelistedMethod(hub,0x55fd0776)`=true |
| S-M04 | ONE 체인 네이티브 토큰 센티넬 `0x…0001` | onchain | `getTokenPair(56, NATIVE)` → remote `0x6bf6…A510`(isOrigin false); `getTokenPair(999, NATIVE)` → remote `0xFaA9…Ebe5`(isOrigin true) |
| S-M05 | USDTx `0x1c37…a85C` (18 dec) | onchain | `symbol()` `"USDTx"`; `getTokenPair(56, USDTx)` → remote USDT `0x55d3…7955` |
| S-M06 | ONEUSD `0xff42…d452` (18 dec) | onchain | `symbol()` `"ONEUSD"`; USDTx/ONEUSD 풀의 token0/1; QuoterV2 `quoteExactInputSingle(USDTx→ONEUSD, 1e18, fee 0)` → `1.000035e18` |
| S-M07 | WCROSS `0x731A…E682` | onchain | `symbol()` `"WCROSS"`; V3 router `WETH9()` 일치 |
| S-M08 | Uniswap V3 SwapRouter `0x0e36…39Ba` (deadline-in-struct 변형) | onchain | `factory()`=S-M10, `WETH9()`=S-M07, 코드에 selector `0x414bf389`(exactInputSingle) 존재, executor `isWhitelistedTarget`=true / `isMethodCheckEnabled`=false |
| S-M09 | QuoterV2 `0x9FBa…07eb` | onchain | `factory()`=S-M10, `WETH9()`=S-M07, `quoteExactInputSingle` 호출 성공(>0) |
| S-M10 | V3 Factory `0x4658…b61D` | onchain | router/quoter 의 `factory()` |
| S-M11 | USDTx/ONEUSD 풀(fee 0) `0x298c…DbfE` | onchain | `factory.getPool(USDTx, ONEUSD, 0)` 일치; `liquidity()` `2.0e27`, `fee()` 0 |
| S-M12 | BSC 브리지(프록시) `0xa6a1…06c5` | onchain | `allChainIDs()` → `[612055]`; impl `0x61C1…340e`; `paused()` false; `maxExtraDataLength()` 1024; `bridgeExecutor()` 0 (리프) |
| S-M13 | BSC BridgeVerifier `0x101c…A2A9` (참고용) | onchain | `bridge.bridgeVerifier()` 일치; `calculateFee(612055, ONE 토큰, 10e18)` → `(5e18, 0, 0)` |
| S-M14 | BSC 의 ONE 토큰 `0x6bf6…A510` (컨트랙트 심볼 `CROSS`, 18 dec) | onchain | `symbol()` `"CROSS"`; `getTokenPair(612055, ONE 토큰)` → remote NATIVE, isOrigin true |
| S-M15 | BSC USDT `0x55d3…7955` (18 dec) | onchain | `symbol()` `"USDT"`, `decimals()` 18; `getTokenPair(612055, USDT)` → remote USDTx, isOrigin true |
| S-M16 | HyperEVM 메인넷 ONE 토큰 `0xFaA9…Ebe5` (18 dec) | onchain | `name()` `"ONEchain"`, `symbol()` `"ONE"`, 18 dec, `isCoreTokenIndexSet()` false; ONE 허브 `getTokenPair(999, NATIVE)` → remote `0xFaA9…Ebe5`, isOrigin true; HyperEVM 브리지 `getTokenPair(612055, 0xFaA9…Ebe5)` → remote NATIVE, isOrigin false, 미정지 |
| S-M17 | HyperEVM 메인넷 브리지(프록시) `0xd8A8…97E5` | onchain | `cast chain-id` 999; `allChainIDs()` → `[612055]`; `paused()` false; ERC-1967 impl 슬롯 → `0x71D9…8D8E`; `maxExtraDataLength()` 1024; ONE 허브 `allChainIDs()` 에 999 포함 (2026-10-28) |
| S-M18 | HyperEVM 메인넷 BridgeVerifier `0x7566…DcFC` (참고용) | onchain | `bridge.bridgeVerifier()` 일치; 코드 11,538 B; `calculateFee(612055, ONE, 10e18)` → `(5e18, 0, 0)` |
| S-M19 | HyperEVM 메인넷 BridgeExecutor `0x4c3b…707b` (참고용) | onchain | `bridge.bridgeExecutor()` 일치; 코드 4,977 B. 도착 후 후속 호출(X2) 화이트리스트는 아직 비어 있다 |
| S-M20 | 메인넷 SwapBridgeRouter `0xB298…e357` (X3) | onchain | 체인 612055 읽기 전용 확인 (2026-10-28): 런타임 코드 12,628 B; `bridge()` = 허브 `0xb81d…C0de`; `swapRouter()` = S-M08 `0x0e36…39Ba`, `quoter()` = S-M09 `0x9FBa…07eb` (설정된 v3SwapRouter / quoterV2 와 일치); `swapBridgeExactInputSingle` · `swapBridgeExactInputSingleETH` · `getAmountSwapBridgeOut` selector 존재; 최근 500k 블록에서 이 주소가 낸 `SwapBridge` 이벤트 83건; `getAmountSwapBridgeOut(56, ONEUSD, USDTx, 0, 10e18)` → status 1 |

## 3. 테스트넷 주소

| id | 값 | 종류 | 근거 |
|---|---|---|---|
| S-T01 | ONE 브리지 `0xb81d…C0de` (메인넷과 같은 주소) | onchain | `allChainIDs()` → `[97,1001,11155111,2021,998]`; impl `0x6d85…ec69` 에 `0x55fd0776` 존재 |
| S-T02 | ONE BridgeVerifier `0x3110…1c37` | onchain | `bridgeVerifier()` 일치; `calculateFee(97/998, NATIVE, 10e18)` → `(5e18, 1.8e17, 0)` |
| S-T03 | ONE BridgeExecutor `0x6147…2279` | onchain | `bridgeExecutor()` 일치; hub + `0x55fd0776` whitelist true |
| S-T04 | 네이티브 센티넬 `0x…0001` | onchain | `getTokenPair(97, NATIVE)` → `0x26ca…Cff1`; `getTokenPair(998, NATIVE)` → `0xDD93…66bB`(isOrigin true) |
| S-T05 | USDTx `0x9F85…33d9` | onchain | `symbol()` `"USDTx"`; `getTokenPair(97, USDTx)` → BSC tUSDt `0xBB18…B492` |
| S-T06 | WCROSS `0xda37…566b` | onchain | `symbol()` `"WCROSS"`; router `WETH9()` 일치 |
| S-T07 | V3 SwapRouter `0x6174…cd05` | onchain | `factory()` `0xCB5c…5449B`, `WETH9()`=S-T06, 코드에 `0x414bf389` |
| S-T08 | QuoterV2 `0x19D6…D10A` | onchain | `factory()` 일치 |
| S-T09 | V3 Factory `0xCB5c…5449B` | onchain | router/quoter `factory()` |
| S-T10 | SwapBridgeRouter `0xc9b2…cf5b` | repo+onchain | `bridge-contracts` 레포의 배포 기록(612044)에 있는 주소; `bridge()`=허브, `swapRouter()`=S-T07, `quoter()`=S-T08, `WETH9()`=S-T06 모두 일치 |
| S-T11 | BSC 테스트넷 브리지 `0x192F…bf2d` | onchain | `allChainIDs()` → `[612044]`; `paused()` false |
| S-T12 | BSC BridgeVerifier `0x1f82…d34f` | onchain | `bridgeVerifier()` 일치; `calculateFee(612044, ONE 토큰, 10e18)` → `(1e18,0,0)` |
| S-T13 | BSC 테스트넷 ONE 토큰 `0x26ca…Cff1` (컨트랙트 심볼 `CROSS`) | onchain | `symbol()` `"CROSS"`; pair → NATIVE (isOrigin true) |
| S-T14 | BSC 테스트넷 USDT `0xBB18…B492` (심볼 `tUSDt`) | onchain | `symbol()` `"tUSDt"`; pair → USDTx |
| S-T15 | HyperEVM 테스트넷 브리지 `0xeF7F…8c22` | onchain | `allChainIDs()` → `[612044]`; `paused()` false |
| S-T16 | HyperEVM BridgeVerifier `0x934f…f559` | onchain | `bridgeVerifier()` 일치; `calculateFee(612044, ONE, 10e18)` → `(5e18,0,0)` |
| S-T17 | HyperEVM BridgeExecutor `0x1ec3…764f` | onchain | `bridgeExecutor()` 일치; token + `transferToCoreFor`(`0x9438568d`) whitelist true |
| S-T18 | HyperEVM 테스트넷 ONE `0xDD93…66bB` | onchain | `symbol()` `"ONE"`; `isCoreTokenIndexSet()` true; `coreUnit()` 1e10; pair(612044) 등록·미정지 |
| S-T19 | HyperCore 시스템 주소 `0x2000…0Bda` | onchain | 토큰 `coreSystemAddress()` 일치 |

## 4. explorer · 네이티브 심볼 · info API (공식 문서)

| id | 값 | 종류 | 근거 |
|---|---|---|---|
| S-E01 | BSC 메인넷 `https://bscscan.com`, 네이티브 `BNB` | public-official | BNB Chain 공식 문서 "wallet configuration" (`docs.bnbchain.org`) 에 chainId 56 / BNB / bscscan.com 표기 (2026-10-27 조회) |
| S-E02 | HyperEVM 메인넷 explorer `https://hyperevmscan.io`, 네이티브 `HYPE` | public-official | Hyperliquid 공식 문서 "HyperEVM tools → Explorers" 목록에 hyperevmscan.io 표기(문서가 메인넷/테스트넷을 구분하지 않음 — 메인넷 값으로 사용, `https://hyperevmscan.io/address/<token>` 200 응답 확인); 가스 토큰 HYPE |
| S-E03 | BSC 테스트넷 `https://testnet.bscscan.com`, 네이티브 `tBNB` | public-official | 위 BNB Chain 문서의 테스트넷 항목 |
| S-E04 | HyperCore info API `https://api.hyperliquid.xyz/info` | public-official | Hyperliquid 공식 문서 "Info endpoint → Spot" (`spotClearinghouseState`) |
| S-E05 | ONE 체인 메인넷 explorer `https://explorer.onechain.nexus/612055`, 네이티브 `ONE` (메인넷·테스트넷 공통) | operator | 체인 운영 주체의 네트워크 정보: 메인넷 (chainId 612055 · 네이티브 코인 ONE · explorer URL), 테스트넷 네이티브 코인 `ONE` (운영 주체 제공; 테스트넷 explorer 는 미확인). explorer 루트가 `/612055` 로 리다이렉트하고 페이지 제목이 "ONEchain Mainnet Blockchain Explorer", `/612055/tx/<허브 tx>` 200 응답 확인 (2026-10-28) |

## 5. 미확인 값 목록 (greppable: `TODO(PENDING)` / `TODO(UNCONFIRMED)`)

| 값 | 마커 | 이유 / 필요한 것 |
|---|---|---|
| `mainnet.hyperevm.coreSystemAddress` | `TODO(PENDING)` | 메인넷 ONE 토큰이 아직 HyperCore 에 링크되지 않았다(`isCoreTokenIndexSet()` false, executor 화이트리스트 비어 있음) → Core 시스템 주소 없음. X2 메인넷 비활성(`preflight` 가 `CORE_NOT_LINKED` 로 막는다) |
| `testnet.one.explorer` | `TODO(UNCONFIRMED)` | ONE 체인 테스트넷 explorer URL 미확인 |
| `testnet.hyperevm.explorer` | `TODO(UNCONFIRMED)` | HyperEVM 테스트넷 explorer 공식 URL 미확인 |
| `testnet.hyperevm.coreInfoApi` | `TODO(UNCONFIRMED)` | 공식 문서에서 테스트넷 info API URL 을 확인하지 못함 |
| `testnet.one.tokens.ONEUSD`, `testnet.one.swap.x1Pool` | `TODO(UNCONFIRMED)` | 테스트넷 ONEUSD / USDTx-ONEUSD 풀 미발견 → X1 테스트넷 비활성 |

## 6. 테스트 방법

값을 바꾸거나 코드를 고친 뒤에는 아래를 순서대로 실행한다. 모두 읽기 전용이거나 로컬 anvil fork 안에서만 트랜잭션을 보낸다.

```bash
# 설정 / 정합성
node tools/gen.mjs && node tools/gen.mjs --check      # networks.json → 생성 파일, 복사본이 최신인지
node tools/check-consistency.mjs                      # 출처 행·미확인 값 목록·stray 주소/URL·경로 커버리지

# JS SDK
cd js && npm ci && npm run typecheck && npm run build && npm test

# Go SDK
cd go && go build ./... && go vet ./... && go test ./... && go test -race ./...

# cast 예제 (읽기 전용 견적; 값이 미확인이면 SKIPPED, 예상 밖 실패는 FAILED → 종료코드 1)
cd cast && NETWORK=testnet ./00-quote.sh && NETWORK=mainnet ./00-quote.sh
bash -n *.sh

# 선택: 로컬 anvil fork 종단 테스트 (anvil 필요, 공개 RPC 를 읽으므로 rate limit 으로 실패할 수 있다)
bash tools/fork-test/fork-smoke.sh --all
bash tools/fork-test/x1-executor.sh
```

- 온체인 값을 다시 확인하려면 `bash tools/probe.sh` (읽기 전용 `cast call` / `cast code` / `cast storage` / `cast chain-id`).
- X3: 메인넷 라우터(S-M20)는 설정되어 있다. 테스트넷 라우터에는 유동성 있는 V3 풀이 없어 테스트넷 X3 견적은 대개 실패한다(status 0). 종단 전송은 이 키트에서 실행하지 않고, 견적·인코딩은 단위 테스트와 골든 벡터로 검증한다.
- HyperEVM 메인넷 경로 R3·R4·R5·R6 은 설정이 채워져 있다. X2(HyperCore 입금)는 메인넷 ONE 토큰이 HyperCore 에 링크되기 전이라 `preflight` 가 `CORE_NOT_LINKED` 로 막는다.
