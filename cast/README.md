# cast 요리책 (셸 예제)

복사해서 그대로 치는 `cast` 명령 모음. **기본은 진단(읽기 전용)** 이고 `SEND=1` 일 때만 전송한다.
필요: `cast`(foundry), `python3`(큰 정수 덧셈), 소스 체인 가스.

## 환경변수 계약

| 변수 | 의미 |
|---|---|
| `NETWORK` | `mainnet` \| `testnet` (기본 `testnet`) — `env.<net>.sh` 를 읽는다 |
| `FROM` | 내 주소 (필수; `00-quote.sh` 는 불필요) |
| `AMOUNT` | 소수 금액 (기본 `10`). 단일 홉=도착액, R5/R6=최종 도착 **최소**액, X1=USDT 금액, X3=`amountIn` |
| `RECIPIENT`, `RECIPIENT_HUB`, `RECIPIENT_DEST`, `CORE_RECIPIENT` | 홉별 수령 주소 (기본 `FROM`). `CORE_RECIPIENT` 를 주면 X2(HyperCore 입금) |
| `HOP2_BUFFER` | 멀티홉 버퍼(ONE, **선언 exFee 에** 더해짐) |
| `SLIPPAGE_BPS`, `DEADLINE_SEC` | X1/X3 (기본 50 / 1800) |
| `TOKEN_IN`, `TOKEN_OUT`, `POOL_FEE`, `TO_CHAIN` | X3 (`TOKEN_IN=native` 가능) |
| `SEND=1` | 실제 전송 |
| `RPC_CROSS`, `RPC_BSC`, `RPC_HYPEREVM` | RPC 교체(SDK 와 같은 이름; 예: 로컬 anvil fork). 생성된 `env.*.sh` **뒤에** 적용된다 |
| `PRIVATE_KEY` 또는 `CAST_SIGNER_ARGS="--account <이름>"`(`--ledger` 등) | `SEND=1` 의 서명자. 출력하지 않는다 |
| `GAS_PRICE` | BSC 테스트넷(97) `--legacy` 전송 가스 가격 (기본 `3gwei`) |

## 스크립트

| 스크립트 | 경로 |
|---|---|
| `00-quote.sh [R1..X3\|all]` | **읽기 전용 견적** — 경로마다 `OK` / `SKIPPED_UNCONFIGURED` / `SKIPPED_ACCEPTED` / `FAILED` 로 분류해 요약표를 찍고, **하나라도 `FAILED` 면 종료코드 1**. |
| `r1.sh` `r2.sh` `r3.sh` `r4.sh` `r5.sh` `r6.sh` | R1 BSC→ONE · R2 ONE→BSC · R3 ONE→HyperEVM(+X2) · R4 HyperEVM→ONE · R5 BSC→ONE→HyperEVM(+X2) · R6 HyperEVM→ONE→BSC |
| `x1.sh` | BSC USDT → ONEUSD |
| `x2.md` | HyperCore 입금 옵션 설명 (R3/R5 에 `CORE_RECIPIENT`) |
| `x3.sh` | SwapBridgeRouter |
| `track.sh <srcChainId> <txhash>` | 읽기 전용 상태 확인 |
| `lib.sh`, `multihop.inc.sh`, `env.*.sh` | 공용 함수 / 멀티홉 계산 / **생성된** 설정(`tools/gen.mjs`) |

```bash
NETWORK=testnet ./00-quote.sh                           # 견적만
FROM=0x내주소 AMOUNT=10 NETWORK=testnet ./r1.sh          # 진단: 체인 확인 → 견적 → 잔고/allowance → (부족하면 approve 명령 출력) → 시뮬레이션
FROM=0x내주소 AMOUNT=10 NETWORK=testnet SEND=1 PRIVATE_KEY=... ./r1.sh
```

## SEND=1 의 순서 (SDK 와 동일)

1. `cast chain-id` 가 설정과 같은지 확인, `cast wallet address <서명자>` 가 `FROM` 과 같은지 확인 — 다르면 서명 전에 중단
2. 견적(소스 verifier `calculateFee`) → **한도 기록**(총 지출, 도착 하한, X1/X3 `amountOutMinimum`)
3. **`preflight_route`(approve 전)** — 아래 목록 중 하나라도 어긋나면 **종료코드 4**, approve 도 전송도 없음
4. approve 가 모자라면 정확한 금액으로(`0 < allowance < 필요액` 이면 0 먼저) 보내고 **영수증 `status 1`** 확인
5. **재견적** → 총 지출이 올랐거나 도착 하한이 내렸거나(X1/X3 는 원래 `amountOutMinimum` 을 재사용, X3 는 원래 바닥으로 다시 계산한 도착 바닥 < 승인값) 이면 중단
6. **`preflight_route`(재견적 뒤, 전송 직전)** — 같은 점검을 다시, 이번에는 allowance 부족도 거절
7. 최종 `cast call --from` 시뮬레이션 → `cast send`
8. 체크포인트(`from`, pending nonce, 블록)와 `track.sh` 명령 출력. **자동 재전송 없음** — 실패하면 GUIDE §8 의 복구 절차를 따를 것

### `preflight_route` 가 읽는 것 (읽기 전용 `cast call`, SDK 의 블로킹 점검과 같은 코드)

| 대상 | 점검 (거절 코드) |
|---|---|
| 모든 경로 | 각 RPC 의 `chainId`(`NETWORK_MISMATCH`) · 소스 브리지 `paused()==false`(`BRIDGE_PAUSED`) · 소스 페어 등록·미정지(`PAIR_NOT_REGISTERED`/`PAIR_PAUSED`), wrapped 면 `minted ≥ value`(`INSUFFICIENT_MINTED`) · 소스 extraData 길이 ≤ `maxExtraDataLength`(`EXTRADATA_TOO_LONG_SOURCE`) · **소스 뒤의 모든 브리지(최종 목적지)** `paused()==false`(`DEST_BRIDGE_PAUSED`) · 잔고(`INSUFFICIENT_BALANCE`), post 단계에서는 allowance(`ALLOWANCE_INSUFFICIENT`) |
| 멀티홉 (R5/R6) | 허브 `paused()`(`HUB_PAUSED`) · 허브 `bridgeExecutor()!=0`(`HUB_EXECUTOR_UNSET`) · 허브→최종 목적지 NATIVE 페어(`HUB_PAIR_NOT_REGISTERED`/`HUB_PAIR_PAUSED`) · 허브 한도 대비 **안쪽** extraData 길이(`EXTRADATA_TOO_LONG_HUB`) · 허브 executor 가 허브 + `0x55fd0776` 허용(`EXECUTOR_NOT_WHITELISTED`) |
| X1 | 허브 `paused()` · `bridgeExecutor()!=0` · executor 가 V3 router + `0x414bf389`(메서드 체크가 켜진 경우) 허용 · 풀 존재(`factory.getPool`, `POOL_NOT_FOUND`)·유동성(`POOL_NO_LIQUIDITY`) |
| X2 (R3/R5 + `CORE_RECIPIENT`) | `isCoreTokenIndexSet()`(`CORE_NOT_LINKED`) · HyperEVM executor 가 토큰 + `0x9438568d` 허용 · `coreTransferableAmount(금액) > 0`(`CORE_AMOUNT_ZERO`) — 상세는 `x2.md` |
| X3 | 라우터 `bridge() == 허브`(`ROUTER_BRIDGE_MISMATCH`) · 견적 status == 1(`X3_QUOTE_STATUS_<n>`) · tokenOut 허브 페어 |

**목적지 페어는 점검하지 않는다**(홉마다 어떤 토큰 쌍을 기대해야 하는지 알아야 하므로); 목적지는 브리지 pause 만 점검한다. 진단 모드(`SEND` 없음)는 같은 점검을 **보고만** 하고(`preflight: ok` 또는
`preflight: refused(<코드>)`) 종료코드는 0 이다. 종료코드: `0` 성공 · `1` 오류 · `2` 사용법 · `3` 값 미설정 · **`4` preflight 거절** · `5` X3 테스트넷 알려진 한계(`00-quote.sh` 의 `SKIPPED_ACCEPTED`,
`QUOTE_ONLY=1` 일 때만).

## 결과 분류 (진단 모드 마지막 줄)

`RESULT <경로> <네트워크> encoding-verified simulation-passed | simulation-blocked(allowance|balance) | simulation-reverted`
— `simulation-blocked` 는 실패가 아니라 "approve/잔고가 없어 시뮬레이션을 못 돌렸다" 는 뜻이며 종단 간 검증으로 세지 않는다. 그 앞에 `preflight: ok | refused(<코드>)` 줄이 있다.

> BSC 테스트넷에서 가스 가격을 직접 지정할 때는 1 gwei 이상(권장 3 gwei) — 표시가(0.05 gwei)로 보내면 채굴되지 않는다.
