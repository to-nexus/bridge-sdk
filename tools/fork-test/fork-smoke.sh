#!/usr/bin/env bash
# Local anvil-fork smoke test of the JS and Go examples. Every transaction goes to a LOCAL anvil fork; nothing is sent to a real chain.
#   fork-smoke.sh <network> <route> [js|go|both] [pause-source-bridge|pause-dest-bridge]     one route, e.g.  fork-smoke.sh testnet R1 both
#   fork-smoke.sh --all     testnet R1 R2 R3 X2 R4 R5 R6, mainnet R1 R2 X1, plus the two refusal cases below
#
# Needs: anvil, cast, python3, node (with `npm run build && npm run examples:build` done in js/) and go. The forks read public RPCs, which may rate-limit.
#
# A plain case expects the example to send the transfer; the assertions (smoke_assert.py) are: example exit 0; a printed source tx hash; receipt
# status 1 on the fork; a BridgeInitiated from the source bridge; handle.srcTxHash == that hash; handle.hop1.index == the log's index; ERC20 source:
# balance delta == value+fees and the exact approve fully consumed; native source: tx value == value+fees.
# The two refusal cases change the fork (not a real chain) and expect the SDK to refuse before approving or sending anything:
#   pause-source-bridge  the source bridge is paused on the fork                  -> PreflightError BRIDGE_PAUSED
#   pause-dest-bridge    (R1) BSC source fork + a CROSS destination fork whose bridge is paused -> PreflightError DEST_BRIDGE_PAUSED
# Both set the OpenZeppelin Pausable storage slot of the proxy (no pauser account can be discovered from a fork) and read `paused()` back before the
# SDK runs. A refusal must exit 2, stop at pre-approve, print no broadcast/sign line, and leave the sender's nonce 0, balance and allowance unchanged.
# Exit code: 0 only if every assertion of every case held; otherwise 1. A public RPC that cannot be forked is a failure (exit 2) unless
# SMOKE_ALLOW_FORK_IMPOSSIBLE=1. A throw-away key is generated at run time (never written to disk); tokens are funded by impersonating the bridge.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"; KIT="$(cd "$HERE/../.." && pwd)"
WORK=${SMOKE_WORKDIR:-${TMPDIR:-/tmp}/bridge-smoke}; mkdir -p "$WORK"
FAILS=0; FAILED=()
RESULTS="$WORK/results.$$.tsv"; : > "$RESULTS"   # one line per sdk case: PASS|FAIL <network> <route> <sdk> <expectation>
record_fail() { FAILS=$((FAILS + 1)); FAILED+=("$1"); echo "SMOKE FAIL $1"; }

smoke_route() { # <network> <route> <which> [mutation]   (runs in a subshell; exit 0 = all assertions held)
  local NET=$1 ROUTE=$2 WHICH=$3 MUTATE=${4:-}
  # shellcheck source=/dev/null
  source "$KIT/cast/env.$NET.sh"
  local PORT=${SMOKE_PORT:-$((18600 + RANDOM % 300))} AMOUNT=${AMOUNT:-10}
  local SRC EX MODE TOKEN_VAR
  case "$ROUTE" in
    R1) SRC=BSC; EX=r1-bsc-to-one; MODE=erc20-locked; TOKEN_VAR=BSC_ONE_TOKEN;;
    R5) SRC=BSC; EX=r5-bsc-to-hyperevm; MODE=erc20-locked; TOKEN_VAR=BSC_ONE_TOKEN;;
    X1) SRC=BSC; EX=x1-usdt-to-oneusd; MODE=erc20-locked; TOKEN_VAR=BSC_USDT;;
    R4) SRC=HYPEREVM; EX=r4-hyperevm-to-one; MODE=erc20-minted; TOKEN_VAR=HYPEREVM_ONE_TOKEN;;
    R6) SRC=HYPEREVM; EX=r6-hyperevm-to-bsc; MODE=erc20-minted; TOKEN_VAR=HYPEREVM_ONE_TOKEN;;
    R2) SRC=CROSS; EX=r2-one-to-bsc; MODE=native;;
    R3) SRC=CROSS; EX=r3-one-to-hyperevm; MODE=native;;
    X2) SRC=CROSS; EX=x2-one-to-hypercore; MODE=native;;
    *) echo "SMOKE $ROUTE $NET unsupported (X3 cannot run end to end: the testnet router has no V3 pool for any pair and the mainnet router address is not confirmed)"; return 2;;
  esac
  local RPC_V="${SRC}_RPC" BR_V="${SRC}_BRIDGE"; local PUB_RPC="${!RPC_V}" BRIDGE="${!BR_V}"
  if [ -z "$BRIDGE" ]; then echo "SMOKE FAIL $ROUTE $NET: source bridge not configured"; return 1; fi
  local LOCAL="http://127.0.0.1:$PORT" CASE_FAILS=0
  anvil --fork-url "$PUB_RPC" --port "$PORT" --retries 15 --fork-retry-backoff 500 --silent > "$WORK/anvil-$ROUTE-$NET.log" 2>&1 &
  APID=$!
  APID2=""
  trap 'kill $APID $APID2 2>/dev/null || true; wait $APID 2>/dev/null || true; [ -z "$APID2" ] || wait $APID2 2>/dev/null || true' EXIT
  local i; for i in $(seq 1 40); do cast chain-id --rpc-url "$LOCAL" >/dev/null 2>&1 && break; sleep 1; done
  if ! cast chain-id --rpc-url "$LOCAL" >/dev/null 2>&1; then
    echo "SMOKE $ROUTE $NET fork impossible: $(head -c 300 "$WORK/anvil-$ROUTE-$NET.log")"
    [ "${SMOKE_ALLOW_FORK_IMPOSSIBLE:-0}" = 1 ] && return 0 || return 2
  fi
  local KJ ME PK
  KJ=$(cast wallet new --json); ME=$(echo "$KJ" | python3 -c 'import json,sys;print(json.load(sys.stdin)[0]["address"])'); PK=$(echo "$KJ" | python3 -c 'import json,sys;print(json.load(sys.stdin)[0]["private_key"])')
  cast rpc anvil_setBalance "$ME" "0x$(python3 -c 'print(format(10**24,"x"))')" --rpc-url "$LOCAL" >/dev/null
  local TOKEN="" FUND; FUND=$(python3 -c 'print(1000*10**18)')
  if [ "$MODE" != native ]; then
    TOKEN="${!TOKEN_VAR}"
    cast rpc anvil_impersonateAccount "$BRIDGE" --rpc-url "$LOCAL" >/dev/null
    cast rpc anvil_setBalance "$BRIDGE" "0x$(python3 -c 'print(format(10**20,"x"))')" --rpc-url "$LOCAL" >/dev/null
    local fn="transfer(address,uint256)(bool)"; [ "$MODE" = erc20-minted ] && fn="mint(address,uint256)(bool)"
    if ! cast send --unlocked --from "$BRIDGE" "$TOKEN" "$fn" "$ME" "$FUND" --rpc-url "$LOCAL" >/dev/null 2>&1; then echo "SMOKE FAIL $ROUTE $NET: funding $ME via $fn from the bridge failed"; return 1; fi
    local got; got=$(cast call "$TOKEN" "balanceOf(address)(uint256)" "$ME" --rpc-url "$LOCAL" | awk '{print $1}')
    [ "$got" = "$FUND" ] || { echo "SMOKE FAIL $ROUTE $NET: funded balance $got != $FUND"; return 1; }
  fi
  if [ "$MUTATE" = pause-source-bridge ]; then
    # fork-only state mutation: OZ PausableUpgradeable ERC-7201 slot (_paused = true) on the source bridge proxy. Nothing on a real chain changes.
    cast rpc anvil_setStorageAt "$BRIDGE" 0xcd5ed15c6e187e77e9aee88184c21f4f2182ab5827cb3b7e07fbedcd63f03300 0x0000000000000000000000000000000000000000000000000000000000000001 --rpc-url "$LOCAL" >/dev/null
    echo "SMOKE $ROUTE $NET fork state mutated: source bridge paused (local fork only)"
  fi
  local DEST_ENV=""
  if [ "$MUTATE" = pause-dest-bridge ]; then
    [ "$ROUTE" = R1 ] || { echo "SMOKE FAIL $ROUTE $NET: pause-dest-bridge is defined for R1 only (BSC source fork + CROSS destination fork)"; return 1; }
    local DPORT; DPORT=$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()')
    local DLOCAL="http://127.0.0.1:$DPORT"
    anvil --fork-url "$CROSS_RPC" --port "$DPORT" --retries 15 --fork-retry-backoff 500 --silent > "$WORK/anvil-$ROUTE-$NET-dest.log" 2>&1 &
    APID2=$!
    for i in $(seq 1 40); do cast chain-id --rpc-url "$DLOCAL" >/dev/null 2>&1 && break; sleep 1; done
    cast chain-id --rpc-url "$DLOCAL" >/dev/null 2>&1 || { echo "SMOKE $ROUTE $NET destination fork impossible: $(head -c 300 "$WORK/anvil-$ROUTE-$NET-dest.log")"; [ "${SMOKE_ALLOW_FORK_IMPOSSIBLE:-0}" = 1 ] && return 0 || return 2; }
    [ "$(cast call "$CROSS_BRIDGE" "paused()(bool)" --rpc-url "$DLOCAL")" = false ] || { echo "SMOKE FAIL $ROUTE $NET: the destination bridge is already paused before the mutation"; return 1; }
    # fork-only state mutation on the DESTINATION fork: OZ PausableUpgradeable ERC-7201 slot (_paused = true) on the destination bridge proxy
    cast rpc anvil_setStorageAt "$CROSS_BRIDGE" 0xcd5ed15c6e187e77e9aee88184c21f4f2182ab5827cb3b7e07fbedcd63f03300 0x0000000000000000000000000000000000000000000000000000000000000001 --rpc-url "$DLOCAL" >/dev/null
    # CONFIRM by a read before the SDK runs (a mutation that did not take is a FAIL, never a silent pass)
    [ "$(cast call "$CROSS_BRIDGE" "paused()(bool)" --rpc-url "$DLOCAL")" = true ] || { echo "SMOKE FAIL $ROUTE $NET: the destination pause did not take on the fork (paused() != true)"; return 1; }
    echo "SMOKE $ROUTE $NET fork state mutated: DESTINATION bridge $CROSS_BRIDGE paused on the destination fork $DLOCAL (read back: paused() == true); source fork $LOCAL (local forks only)"
    DEST_ENV="$DLOCAL"
  fi
  local EXPECT=send-success
  case "$MUTATE" in
    pause-source-bridge) EXPECT=preflight-refused:BRIDGE_PAUSED;;
    pause-dest-bridge) EXPECT=preflight-refused:DEST_BRIDGE_PAUSED;;
  esac
  export "RPC_$SRC=$LOCAL" PRIVATE_KEY="$PK" NETWORK="$NET" AMOUNT
  if [ -n "$DEST_ENV" ]; then export RPC_CROSS="$DEST_ENV"; fi
  local extra_js="" extra_go=""
  [ "$ROUTE" = X2 ] && { extra_js="--recipient-core $ME"; extra_go="-recipient-core $ME"; }

  run_case() { # <sdk>
    local sdk=$1; local out="$WORK/$sdk-$ROUTE-$NET${MUTATE:+-$MUTATE}.out" rc=0 snap before=0 after=0 allow=0 nonce=0
    snap=$(cast rpc evm_snapshot --rpc-url "$LOCAL" | tr -d '"')
    if [ -n "$TOKEN" ]; then before=$(cast call "$TOKEN" "balanceOf(address)(uint256)" "$ME" --rpc-url "$LOCAL" | awk '{print $1}'); else before=$(cast balance "$ME" --rpc-url "$LOCAL"); fi
    if [ "$sdk" = js ]; then
      # shellcheck disable=SC2086
      ( cd "$KIT/js" && node "dist-examples/examples/$EX.js" --network "$NET" --send --no-track $extra_js ) > "$out" 2>&1 || rc=$?
    else
      # shellcheck disable=SC2086
      # build then run the binary: `go run` maps every nonzero exit status to 1 and would hide the refusal exit code 2
      ( cd "$KIT/go" && GOWORK=off go build -o "$WORK/go-ex-$EX" "./examples/$EX" && "$WORK/go-ex-$EX" -network "$NET" -send -no-track $extra_go ) > "$out" 2>&1 || rc=$?
    fi
    if [ -n "$TOKEN" ]; then after=$(cast call "$TOKEN" "balanceOf(address)(uint256)" "$ME" --rpc-url "$LOCAL" | awk '{print $1}'); allow=$(cast call "$TOKEN" "allowance(address,address)(uint256)" "$ME" "$BRIDGE" --rpc-url "$LOCAL" | awk '{print $1}'); fi
    nonce=$(cast nonce "$ME" --rpc-url "$LOCAL")
    echo "SMOKE case $ROUTE $NET $sdk (expectation $EXPECT, example exit $rc)"
    local erc=0; [ -n "$TOKEN" ] && erc=1
    if python3 "$HERE/smoke_assert.py" "$out" "$rc" "$LOCAL" "$BRIDGE" "$erc" "$before" "$after" "$allow" "$EXPECT" "$nonce"; then
      echo "SMOKE PASS $ROUTE $NET $sdk ($EXPECT)"; echo "PASS $NET $ROUTE${MUTATE:+[$MUTATE]} $sdk $EXPECT" >> "$RESULTS"
    else
      echo "SMOKE FAIL $ROUTE $NET $sdk ($EXPECT)"; echo "FAIL $NET $ROUTE${MUTATE:+[$MUTATE]} $sdk $EXPECT" >> "$RESULTS"; tail -8 "$out" | sed 's/^/    | /'; CASE_FAILS=$((CASE_FAILS + 1))
    fi
    cast rpc evm_revert "$snap" --rpc-url "$LOCAL" >/dev/null
  }
  if [ "$WHICH" = js ] || [ "$WHICH" = both ]; then run_case js; fi
  if [ "$WHICH" = go ] || [ "$WHICH" = both ]; then run_case go; fi
  return $(( CASE_FAILS > 0 ))
}

run_route() { # <network> <route> <which> [mutation]
  local rc=0
  set +e
  ( set -e; smoke_route "$1" "$2" "$3" "${4:-}" )   # not in an ||/&& list, so `set -e` really applies inside
  rc=$?
  set -e
  if [ "$rc" -ne 0 ]; then record_fail "$2 on $1 (exit $rc)"; fi
}

summary() {
  local sent refused failed
  sent=$(grep -c '^PASS .* send-success$' "$RESULTS" || true); refused=$(grep -c '^PASS .* preflight-refused:' "$RESULTS" || true); failed=$(grep -c '^FAIL ' "$RESULTS" || true)
  echo "SMOKE SUMMARY: sent-success cases passed: $sent | preflight-refused cases passed: $refused (counted separately, never as send successes) | failed cases: $failed | failed case groups: $FAILS"
  grep '^PASS .* send-success$' "$RESULTS" | sed 's/^PASS /  SENT    /; s/ send-success$//' || true
  grep '^PASS .* preflight-refused:' "$RESULTS" | sed 's/^PASS /  REFUSED /' || true
  if [ "$FAILS" -gt 0 ]; then printf '  failed: %s\n' "${FAILED[@]}"; fi
  exit $(( FAILS > 0 ? 1 : 0 ))
}

case "${1:-}" in
  --all)
    for r in R1 R2 R3 X2 R4 R5 R6; do run_route testnet "$r" both; done
    for r in R1 R2 X1; do run_route mainnet "$r" both; done
    # refusal path end to end (JS + Go): a LOCAL fork whose source bridge is paused
    run_route testnet R1 both pause-source-bridge
    # the DESTINATION bridge is paused on a destination fork (source fork + destination fork, both loopback)
    run_route testnet R1 both pause-dest-bridge
    summary;;
  "") echo "usage: $0 <network> <route> [js|go|both] | --all" >&2; exit 64;;
  *) run_route "${1:?network}" "${2:?route}" "${3:-both}" "${4:-}"; summary;;
esac
