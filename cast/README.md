# cast 예제

foundry `cast` 로 브리지 전송을 실행하는 셸 스크립트입니다. SDK 없이 터미널에서 견적을 보거나 전송할 때 씁니다.

기본 동작은 **진단(읽기 전용)** 입니다. `SEND=1` 을 줄 때만 트랜잭션을 보냅니다.

## 준비

- `cast`(foundry), `python3`
- 출발 체인의 가스 토큰 (BSC: BNB, HyperEVM: HYPE, CROSS: ONE)

## 빠른 시작

```bash
cd cast

# 모든 경로의 현재 수수료 견적 (읽기 전용)
NETWORK=testnet ./00-quote.sh

# R1 진단: 체인 확인 → 견적 → 잔고·allowance 확인 → 시뮬레이션
FROM=0xYourAddress AMOUNT=10 NETWORK=testnet ./r1.sh

# R1 실제 전송 (approve 가 필요하면 함께 보낸다)
FROM=0xYourAddress AMOUNT=10 NETWORK=testnet SEND=1 PRIVATE_KEY=... ./r1.sh

# 전송 상태 확인
./track.sh <출발 chainId> <txhash>
```

## 스크립트

| 스크립트 | 경로 |
|---|---|
| `00-quote.sh [경로\|all]` | 경로별 수수료 견적 요약 (읽기 전용) |
| `r1.sh` | R1 BSC → ONE |
| `r2.sh` | R2 ONE → BSC |
| `r3.sh` | R3 ONE → HyperEVM (`CORE_RECIPIENT` 를 주면 X2 HyperCore 입금) |
| `r4.sh` | R4 HyperEVM → ONE |
| `r5.sh` | R5 BSC → ONE → HyperEVM (`CORE_RECIPIENT` 를 주면 X2) |
| `r6.sh` | R6 HyperEVM → ONE → BSC |
| `x1.sh` | X1 BSC USDT → ONEUSD |
| `x3.sh` | X3 CROSS 체인에서 스왑 후 브리지 |
| `track.sh <chainId> <txhash>` | 전송 상태 조회 (읽기 전용) |

X2(HyperCore 입금)는 별도 스크립트 없이 `r3.sh` · `r5.sh` 의 옵션으로 씁니다. 자세한 내용은 [x2.md](x2.md) 에 있습니다.

`env.mainnet.sh` · `env.testnet.sh` 는 네트워크별 주소와 RPC 이고, `lib.sh` · `multihop.inc.sh` 는 스크립트들이 공유하는 함수입니다.

## 환경변수

| 변수 | 설명 |
|---|---|
| `NETWORK` | `mainnet` 또는 `testnet` (기본 `testnet`) |
| `FROM` | 내 주소 (`00-quote.sh` 외에는 필수) |
| `AMOUNT` | 금액, 소수 표기 (기본 `10`). 단일 홉은 도착 금액, R5/R6 은 최종 도착 최소 금액, X1 은 USDT 금액, X3 는 스왑 입력 금액 |
| `RECIPIENT` | 받는 주소 (기본 `FROM`) |
| `RECIPIENT_HUB` · `RECIPIENT_DEST` | 멀티홉에서 홉별 받는 주소 |
| `CORE_RECIPIENT` | HyperCore 입금(X2)을 받을 주소 |
| `HOP2_BUFFER` | 멀티홉 두 번째 홉 수수료에 더할 여유분 (ONE) |
| `SLIPPAGE_BPS` · `DEADLINE_SEC` | X1 · X3 슬리피지와 기한 (기본 50 / 1800) |
| `TOKEN_IN` · `TOKEN_OUT` · `POOL_FEE` · `TO_CHAIN` | X3 옵션 (`TOKEN_IN=native` 가능) |
| `SEND=1` | 실제 전송 |
| `PRIVATE_KEY` 또는 `CAST_SIGNER_ARGS` | 전송 서명자. `CAST_SIGNER_ARGS="--account <이름>"` 이나 `--ledger` 도 쓸 수 있다 |
| `RPC_CROSS` · `RPC_BSC` · `RPC_HYPEREVM` | RPC 교체 |
| `GAS_PRICE` | BSC 테스트넷 가스 가격 (기본 `3gwei`) |

## `SEND=1` 일 때 하는 일

1. RPC 의 chainId 와 서명자 주소가 설정·`FROM` 과 같은지 확인합니다. 다르면 서명하지 않습니다.
2. 수수료를 견적하고 지출 상한 · 도착 하한(X1 · X3 는 최소 수령량)을 정합니다.
3. 브리지 정지, 페어 등록, 잔고, 데이터 길이 등을 점검합니다. 문제가 있으면 종료코드 4 로 멈춥니다.
4. allowance 가 모자라면 필요한 금액만큼 approve 하고 영수증을 확인합니다.
5. 다시 견적해 2 의 한도를 넘으면 멈춥니다.
6. 같은 점검을 다시 하고(이번에는 allowance 포함) 시뮬레이션한 뒤 전송합니다.
7. 출발 트랜잭션 정보와 상태 확인 명령(`track.sh`)을 출력합니다.

실패해도 자동으로 다시 보내지 않습니다. 결과가 불확실하면 [GUIDE.md §8](../GUIDE.md#8-에러-처리) 의 복구 절차를 따르세요.

## 종료코드

| 코드 | 의미 |
|---|---|
| `0` | 성공 (진단 모드는 점검에 걸려도 보고만 하고 0) |
| `1` | 오류 |
| `2` | 사용법 오류 |
| `3` | 해당 네트워크에서 아직 열리지 않은 경로 |
| `4` | 사전 점검에서 거부 (아무것도 보내지 않음) |

## 참고

- BSC 테스트넷에서 가스 가격을 직접 지정할 때는 1 gwei 이상(권장 3 gwei)을 쓰세요. 0.05 gwei 로 보내면 채굴되지 않습니다.
- 사전 점검 항목은 [GUIDE.md §9](../GUIDE.md#9-체크리스트--faq), revert 코드는 [GUIDE.md §8](../GUIDE.md#8-에러-처리) 에 있습니다.
