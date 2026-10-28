# CROSS / ONE 브리지 SDK

CROSS / ONE 브리지(BSC ↔ ONE 허브 ↔ HyperEVM)를 브리지 내부 구조를 몰라도 쓸 수 있게 하는 SDK 와 예제 모음: TypeScript SDK, Go SDK, 셸(cast) 예제.
값(주소·RPC·chainId)은 모두 `networks.json` 한 곳에서 온다. 확인일 2026-10-27.

| 파일 | 내용 |
|---|---|
| `GUIDE.md` | 한국어 가이드 — **Quick Start 가 맨 앞**, 이어서 경로별 상세·원리·수수료·상태 추적·에러 처리 |
| `networks.json` | **단일 진실 원천**: chainId / RPC / explorer / 주소 (+ provenance, 미확인 마커) |
| `SOURCES.md` | 모든 값의 출처(명령·결과·날짜), 미확인 목록, 테스트 방법 |
| `js/` | TypeScript SDK (ethers v6) — `@to-nexus/bridge-sdk` |
| `go/` | Go SDK — `github.com/to-nexus/bridge-sdk/go` (공개 `bridge-contracts` 바인딩 모듈 사용) |
| `cast/` | 복사해 치는 셸 예제 |
| `tools/` | 코드 생성(`gen.mjs`, `gen-abi.mjs`), 정합성 검사(`check-consistency.mjs`), 골든 벡터, 온체인 프로브, 선택적 anvil fork 테스트(`tools/fork-test/`) |

## 경로 × 산출물 (완전성 체크리스트)

| ID | 경로 | GUIDE | JS | Go | cast | 비고 |
|---|---|---|---|---|---|---|
| R1 | BSC → ONE | ✅ | ✅ `Route.BSC_TO_ONE` | ✅ `RouteBSCToOne` | ✅ `r1.sh` | |
| R2 | ONE → BSC | ✅ | ✅ | ✅ | ✅ `r2.sh` | |
| R3 | ONE → HyperEVM | ✅ | ✅ | ✅ | ✅ `r3.sh` | ⚠️ 메인넷 `TODO(PENDING)` |
| R4 | HyperEVM → ONE | ✅ | ✅ | ✅ | ✅ `r4.sh` | ⚠️ 메인넷 `TODO(PENDING)` |
| R5 | BSC → ONE → HyperEVM | ✅ | ✅ | ✅ | ✅ `r5.sh` | ⚠️ 메인넷 `TODO(PENDING)` |
| R6 | HyperEVM → ONE → BSC | ✅ | ✅ | ✅ | ✅ `r6.sh` | ⚠️ 메인넷 `TODO(PENDING)` |
| X1 | BSC USDT → ONEUSD (executor 스왑) | ✅ | ✅ | ✅ | ✅ `x1.sh` | ⚠️ 테스트넷 `TODO(UNCONFIRMED)` |
| X2 | HyperCore 입금 (R3/R5 옵션) | ✅ | ✅ `Route.ONE_TO_HYPERCORE`, `hypercore: true` | ✅ `RouteOneToHyperCore`, `Hypercore` | ✅ `CORE_RECIPIENT` + `x2.md` | ⚠️ 메인넷 `TODO(PENDING)` |
| X3 | SwapBridgeRouter | ✅ | ✅ `Route.SWAP_BRIDGE` | ✅ `RouteSwapBridge` | ✅ `x3.sh` | ⚠️ 메인넷 `TODO(UNCONFIRMED)` |

⚠️ 는 값이 아직 확인되지 않아 **SDK 가 `quote()` 에서 거부**(`AddressNotConfiguredError`)하는 조합이다. 목록: `GUIDE.md §10.1`, `SOURCES.md §5`.

## 가장 빠른 시작

```bash
cd cast && NETWORK=testnet ./00-quote.sh                         # 읽기 전용 견적
cd ../js && npm ci && npm run build && node dist-examples/...   # 예제는 js/README.md
cd ../go && go run ./examples/r1-bsc-to-one -network testnet     # dry-run
```

## 안전 원칙 (요약)

- 예제와 스크립트는 **기본 dry-run/진단**. 전송은 `--send` / `-send` / `SEND=1` + `PRIVATE_KEY` 환경변수일 때만.
- 견적에서 **사용자 한도**(총 지출 상한, 도착 하한, 스왑 `amountOutMinimum`)를 고정하고, approve 후 재견적이 한도를 느슨하게 하면 전송하지 않는다.
- **자동 재전송 없음.** 전송 결과가 불명하면 `BroadcastOutcomeUnknownError` + `findSourceTransfer` 로 확인한다(`GUIDE.md §8`).
- CROSS 에는 reorg 가 없다 — 컨펌/finality 대기 로직 없음.

## 값을 바꾸려면

`networks.json` 수정 → `node tools/gen.mjs` → `node tools/check-consistency.mjs` → `js` 테스트 · `go test` · `cast/00-quote.sh` 재실행 → `SOURCES.md` 갱신. 전체 테스트 방법은 `SOURCES.md` 의 "테스트 방법".

## License

MIT — see [LICENSE](LICENSE).
