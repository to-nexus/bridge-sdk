# fork-test/ — 선택적 로컬 fork 종단 테스트

SDK 예제와 X1 executor 호출을 **로컬 anvil fork** 에서 끝까지 실행해 본다. 모든 트랜잭션은 로컬 anvil 안에서만 일어나고 실제 체인에는 아무것도 보내지 않는다.
**선택 사항**이다: 단위 테스트(`js`, `go`)와 정합성 검사에는 필요 없다.

필요: `anvil`·`cast`(foundry), `python3`, `node`(`js/` 에서 `npm run build && npm run examples:build` 완료), `go`.
fork 는 **공개 RPC** 에서 상태를 읽으므로 rate limit 에 걸려 실패할 수 있다 — 잠시 뒤 다시 실행한다.

| 파일 | 내용 |
|---|---|
| `fork-smoke.sh <net> <route> [js\|go\|both] [pause-source-bridge\|pause-dest-bridge]` | 예제를 `--send` 로 실행하고 `smoke_assert.py` 로 확인(영수증 status, `BridgeInitiated`, 핸들 일치, 잔고 증감·approve 소진). `--all` 은 testnet R1 R2 R3 X2 R4 R5 R6, mainnet R1 R2 X1 + 두 거절 케이스 |
| `x1-executor.sh` | CROSS 메인넷 fork 에서 허브를 impersonate 해 `executeExtraCall` 을 직접 호출: 유효한 `amountOutMinimum` 은 ONEUSD 도착, 불가능한 값은 `BETargetCallFailed` revert(실제 finalize 에서는 평범한 USDTx 지급으로 폴백) |
| `smoke_assert.py`, `x1-extradata.mjs` | 위 스크립트의 보조 도구 |

거절 케이스(`pause-source-bridge`, `pause-dest-bridge`)는 fork 의 OpenZeppelin Pausable 저장 슬롯을 바꿔 브리지를 정지시킨 뒤(`paused()` 를 읽어 확인) SDK 가 approve·전송 전에
`BRIDGE_PAUSED` / `DEST_BRIDGE_PAUSED` 로 거절하는지, 그리고 보낸이의 nonce·잔고·allowance 가 그대로인지 확인한다.

X3 는 종단 실행이 불가능해 포함하지 않는다(테스트넷 라우터에 V3 풀이 있는 쌍이 없고, 메인넷 라우터 주소는 미확인).
