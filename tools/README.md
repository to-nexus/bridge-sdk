# tools/

| 파일 | 용도 |
|---|---|
| `gen.mjs` | `networks.json` → `js/src/networks.generated.ts`, `go/bridgesdk/networks.json`, `cast/env.*.sh`, GUIDE 의 네트워크 표. `--check` 는 낡은 복사본이 있으면 exit 1 |
| `gen-abi.mjs` | `bridge-contracts` 의 forge 산출물(`out/`)에서 ABI → `js/src/abi/*.ts`, Go 용 `abi_extra.json`, **revert 선택자 표**(`errors.generated.ts`, `errors_table.json`). `error-hints.json` 이 힌트 문구. **`BRIDGE_CONTRACTS_DIR` 필요** |
| `check-consistency.mjs` | 정합성 검사: 복사본 일치 · 벡터 동일성 · provenance↔SOURCES.md · 미확인 값 목록 · stray 0x 주소/URL · 9개 경로 커버리지. 선택: `tools/consistency-deny.local.json`(git 에 넣지 않는 `[정규식, 라벨]` 배열)로 추가 금지 패턴 |
| `vectors/ForwardMath.s.sol` | 허브 재정산 수학을 **실제 컨트랙트 바이트코드**(CrossBridge `_resolveForwardAmounts`, BridgeVerifier)로 로컬 EVM 에서 생성 (`forge script`, RPC 없음) |
| `vectors/build-vectors.mjs` | 위 하네스 + `cast` 인코딩 → `vectors/vectors.json` → `js/test/`, `go/bridgesdk/testdata/` 로 복사. **`BRIDGE_CONTRACTS_DIR` 필요** |
| `probe.sh` | 모든 값의 온체인 프로브(읽기 전용 `cast call/code/storage`) — SOURCES.md 근거 |
| `fork-test/` | 선택 사항: 로컬 anvil fork 종단 테스트 (`fork-test/README.md`) |

## BRIDGE_CONTRACTS_DIR

`gen-abi.mjs` 와 `vectors/build-vectors.mjs` 는 컨트랙트 소스·빌드 산출물이 필요하다. 환경변수 `BRIDGE_CONTRACTS_DIR` 에
**`github.com/to-nexus/bridge-contracts` 의 체크아웃**(`forge build` 로 `out/` 이 만들어진 상태)을 지정한다. 없으면 두 스크립트는 안내 메시지와 함께 종료코드 2 로 끝난다.
`build-vectors.mjs` 는 하네스를 그 체크아웃의 `script/` 에 임시로 복사해 `forge script` 를 돌리고(빌드 결과는 임시 디렉터리) 끝나면 지운다.

```bash
export BRIDGE_CONTRACTS_DIR=/경로/bridge-contracts
node tools/gen.mjs && node tools/gen-abi.mjs && node tools/vectors/build-vectors.mjs && node tools/check-consistency.mjs
```

일반 사용(SDK·예제·테스트)에는 필요 없다 — 생성된 파일이 저장소에 들어 있다.
