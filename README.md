# bridge-sdk

BSC · ONE · HyperEVM 사이에서 ONE 과 USDT 를 옮기는 **ONE 브리지 SDK** 입니다.
브리지 컨트랙트 구조를 몰라도 견적부터 전송, 도착 확인까지 몇 줄로 처리할 수 있습니다.

- **TypeScript SDK** (`js/`, ethers v6) — `@to-nexus/bridge-sdk`
- **Go SDK** (`go/`) — `github.com/to-nexus/bridge-sdk/go`
- **cast 예제** (`cast/`) — 셸에서 바로 실행하는 foundry `cast` 스크립트

네트워크 설정(chainId · RPC · 컨트랙트 주소)이 SDK 에 내장되어 있어 따로 넣을 값이 없습니다.

## 지원 경로

| ID | 경로 | 설명 | JS `Route` | Go | 메인넷 | 테스트넷 |
|---|---|---|---|---|:---:|:---:|
| R1 | BSC → ONE | BSC 의 ONE 토큰 → ONE 체인 네이티브 ONE | `BSC_TO_ONE` | `RouteBSCToOne` | ✅ | ✅ |
| R2 | ONE → BSC | 네이티브 ONE → BSC 의 ONE 토큰 | `ONE_TO_BSC` | `RouteOneToBSC` | ✅ | ✅ |
| R3 | ONE → HyperEVM | 네이티브 ONE → HyperEVM 의 ONE 토큰 | `ONE_TO_HYPEREVM` | `RouteOneToHyperEVM` | ✅ | ✅ |
| R4 | HyperEVM → ONE | HyperEVM 의 ONE 토큰 → 네이티브 ONE | `HYPEREVM_TO_ONE` | `RouteHyperEVMToOne` | ✅ | ✅ |
| R5 | BSC → ONE → HyperEVM | 한 번의 요청으로 두 홉 이동 (멀티홉) | `BSC_TO_ONE_TO_HYPEREVM` | `RouteBSCToOneToHyperEVM` | ✅ | ✅ |
| R6 | HyperEVM → ONE → BSC | 한 번의 요청으로 두 홉 이동 (멀티홉) | `HYPEREVM_TO_ONE_TO_BSC` | `RouteHyperEVMToOneToBSC` | ✅ | ✅ |
| X1 | BSC USDT → ONEUSD | 도착 후 ONE 체인에서 ONEUSD 로 자동 스왑 | `BSC_USDT_TO_ONEUSD` | `RouteBSCUSDTToONEUSD` | ✅ | — |
| X2 | ONE → HyperCore | HyperEVM 도착 후 HyperCore 스팟 계정으로 입금 (R3 · R5 옵션) | `ONE_TO_HYPERCORE` | `RouteOneToHyperCore` | 준비 중 | ✅ |
| X3 | 스왑 후 브리지 | ONE 체인에서 스왑한 토큰을 BSC / HyperEVM 으로 전송 | `SWAP_BRIDGE` | `RouteSwapBridge` | ✅ | ✅¹ |

¹ 테스트넷에는 유동성 있는 스왑 풀이 거의 없어 견적이 실패할 수 있습니다.

아직 열리지 않은 경로는 SDK 가 서명 전에 거부합니다(`AddressNotConfiguredError` 또는 `PreflightError`).

### 네트워크

| 체인 | 메인넷 chainId | 테스트넷 chainId | 가스 토큰 |
|---|---|---|---|
| ONE (허브) | 612055 | 612044 | ONE |
| BNB Smart Chain | 56 | 97 | BNB |
| HyperEVM | 999 | 998 | HYPE |

컨트랙트 주소와 RPC 는 [GUIDE.md §3](GUIDE.md#3-네트워크-정보) 와 [`networks.json`](networks.json) 에 있습니다.

## 설치

### TypeScript / JavaScript

Node.js 18 이상이 필요합니다. 아직 npm 레지스트리에 배포하지 않았으므로 소스에서 패키지를 만들어 설치합니다.

```bash
git clone https://github.com/to-nexus/bridge-sdk.git
cd bridge-sdk/js
npm ci && npm run build && npm pack     # to-nexus-bridge-sdk-<버전>.tgz 생성

# 내 프로젝트에서
npm install /path/to/to-nexus-bridge-sdk-<버전>.tgz ethers
```

### Go

Go 1.24 이상이 필요합니다.

```bash
go get github.com/to-nexus/bridge-sdk/go@latest
```

### cast

foundry 의 `cast` 와 `python3` 가 필요합니다. 따로 설치할 것 없이 저장소의 `cast/` 스크립트를 실행합니다.

## 빠른 시작

모든 경로는 같은 흐름입니다: **견적(quote) → 전송(send) → 핸들 받기(wait) → 추적(track)**.

### TypeScript

```ts
import { BridgeClient, Route, parseUnits } from '@to-nexus/bridge-sdk';
import { Wallet } from 'ethers';

const client = new BridgeClient({ network: 'testnet' });
const wallet = new Wallet(process.env.PRIVATE_KEY!);

// 1. 견적: 수수료를 조회하고 보낼 트랜잭션을 만든다
const quote = await client.quote({
  route: Route.BSC_TO_ONE,
  amount: parseUnits('10'),          // 도착 금액 (wei)
  sender: wallet.address,
  recipient: wallet.address,
});

// 2. 전송: 필요하면 approve 하고, 다시 견적·사전 점검·시뮬레이션을 거쳐 보낸다
const sent = await client.send(wallet, quote);

// 3. 핸들: 전송을 식별하는 JSON. 저장해 두면 재시작 후에도 추적할 수 있다
const handle = await sent.wait();

// 4. 추적: 목적지 도착까지 상태를 따라간다
const result = await client.track(handle);
console.log(result.state);           // 'DELIVERED' 등
```

### Go

```go
package main

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"os"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/to-nexus/bridge-sdk/go/bridgesdk"
)

func main() {
	ctx := context.Background()
	key, err := crypto.HexToECDSA(os.Getenv("PRIVATE_KEY"))
	if err != nil {
		log.Fatal(err)
	}
	from := crypto.PubkeyToAddress(key.PublicKey)

	c, err := bridgesdk.NewClient(ctx, bridgesdk.Testnet, nil)
	if err != nil {
		log.Fatal(err)
	}
	amount, _ := bridgesdk.ParseUnits("10", 18)

	q, err := c.Quote(ctx, bridgesdk.QuoteRequest{Route: bridgesdk.RouteBSCToOne, Amount: amount, Sender: from, Recipient: from})
	if err != nil {
		log.Fatal(err)
	}
	sent, err := c.Send(ctx, bridgesdk.NewKeyedSigner(key, big.NewInt(q.SourceChainID)), q, bridgesdk.SendOptions{})
	if err != nil {
		log.Fatal(err)
	}
	h, err := c.WaitHandle(ctx, sent)
	if err != nil {
		log.Fatal(err)
	}
	res, err := c.Track(ctx, h, bridgesdk.TrackOpts{})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(res.State)
}
```

### cast

```bash
cd cast
FROM=0xYourAddress AMOUNT=10 NETWORK=testnet ./r1.sh                          # 견적·잔고·시뮬레이션만 (전송 안 함)
FROM=0xYourAddress AMOUNT=10 NETWORK=testnet SEND=1 PRIVATE_KEY=... ./r1.sh   # 실제 전송
```

## 알아두면 좋은 것

- **`amount` 의 의미**: 단일 홉(R1–R4)은 도착 금액, 멀티홉(R5/R6)은 최종 도착 **최소** 금액, X1 은 보낼 USDT 금액, X3 는 스왑 입력 금액입니다. 수수료는 견적이 따로 계산해 더합니다.
- **경로마다 최소 전송액이 있습니다.** 운영 중에 바뀔 수 있으므로 화면에 고정하지 말고 견적으로 확인하세요.
- **견적은 바꿀 수 없습니다.** 금액이나 수령인을 바꾸려면 `quote()` 를 다시 호출하세요. 견적 객체를 고치면 `send()` 가 거부합니다.
- **`send()` 는 안전 점검을 강제합니다.** approve 전후로 브리지 정지·페어 등록·잔고·allowance 등을 확인하고, 문제가 있으면 아무것도 서명하지 않고 `PreflightError` 를 던집니다. 사유를 미리 보려면 `preflight()` 를 호출하세요.
- **자동 재전송을 하지 않습니다.** 전송 결과를 알 수 없으면 `BroadcastOutcomeUnknownError` 가 나옵니다. 다시 보내기 전에 `findSourceTransfer()` 로 이미 나갔는지 확인하세요. 같은 전송이 두 번 나가는 것을 막기 위해서입니다.
- **핸들을 저장하세요.** `TransferHandle` 은 순수 JSON 입니다. 트랜잭션 해시만 있어도 `handleFromTx()` 로 다시 만들 수 있습니다.
- ONE 체인에는 reorg 가 없어 컨펌 대기가 필요 없습니다. BSC · HyperEVM 에서 출발하면 출발 트랜잭션의 영수증만 기다립니다.

## 전송 상태

`track()` 이 돌려주는 주요 상태입니다. 전체 목록과 처리 방법은 [GUIDE.md §7](GUIDE.md#7-이벤트--상태-추적) 에 있습니다.

| 상태 | 의미 |
|---|---|
| `IN_FLIGHT` | 출발 체인에서 처리됨, 목적지 도착 대기 중 |
| `DELIVERED` | 목적지에 도착 |
| `DELIVERED_FALLBACK` | 요청한 후속 동작(스왑·HyperCore 입금)이 실패해 원래 토큰으로 도착 |
| `PENDING_ON_DEST` | 목적지에서 보류됨 (자동 또는 운영자 처리 후 지급) |
| `CORE_DEPOSIT_SUBMITTED` | X2: HyperCore 입금 요청까지 완료 |
| `SOURCE_FAILED` | 출발 트랜잭션 실패 (자금 이동 없음) |

## 예제

`js/examples/`, `go/examples/` 에 경로마다 예제가 있습니다. 기본은 **dry-run**(견적·사전 점검·시뮬레이션만)이고 `--send` / `-send` 를 줄 때만 실제로 전송합니다.

```bash
# TypeScript
cd js && npm ci && npm run build && npm run examples:build
node dist-examples/examples/r1-bsc-to-one.js --network testnet --sender 0xYourAddress

# Go
cd go && go run ./examples/r1-bsc-to-one -network testnet -sender 0xYourAddress
```

| 예제 | 경로 |
|---|---|
| `r1-bsc-to-one` · `r2-one-to-bsc` | R1 · R2 |
| `r3-one-to-hyperevm` · `r4-hyperevm-to-one` | R3 · R4 |
| `r5-bsc-to-hyperevm` · `r6-hyperevm-to-bsc` | R5 · R6 (멀티홉) |
| `x1-usdt-to-oneusd` · `x2-one-to-hypercore` · `x3-swap-bridge` | X1 · X2 · X3 |

## 문서

- [GUIDE.md](GUIDE.md) — 연동 가이드: 경로별 상세, 동작 원리, 수수료, 상태 추적, 에러 처리와 복구
- [js/README.md](js/README.md) — TypeScript API
- [go/README.md](go/README.md) — Go API
- [cast/README.md](cast/README.md) — 셸 스크립트와 환경변수
- [SOURCES.md](SOURCES.md) — 주소와 설정값의 출처, 직접 확인하는 방법

## 개발

```bash
# TypeScript
cd js && npm ci && npm run typecheck && npm test

# Go
cd go && go vet ./... && go test ./...
```

네트워크 설정은 루트의 `networks.json` 에서만 바꿉니다. 바꾼 뒤 `node tools/gen.mjs` 로 SDK 와 문서에 반영하고 `node tools/check-consistency.mjs` 로 확인합니다. 도구 설명은 [tools/README.md](tools/README.md) 에 있습니다.

## 보안

- 개인 키는 환경변수로만 넘기고 코드나 저장소에 넣지 마세요.
- 메인넷에서는 작은 금액으로 먼저 확인하세요.

## License

[MIT](LICENSE)
