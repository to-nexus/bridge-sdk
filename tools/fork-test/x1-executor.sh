#!/usr/bin/env bash
# X1 executor test on a ONE-MAINNET anvil fork (local only). Impersonates the hub bridge (the EXECUTOR_ROLE holder), gives it USDTx and calls
# BridgeExecutor.executeExtraCall DIRECTLY with the SDK-built extraData (so the receipt has no hub ExtraCallExecuted event; the success assertion is
# the executeExtraCall tx status 1 plus the balance / Transfer evidence below). Needs anvil, cast, python3 and node (js/ built).
#   x1-executor.sh    exits 0 only if every assertion held
# SUCCESS case (valid amountOutMinimum): tx status 1 | ONEUSD delta >= amountOutMinimum | ONEUSD delta == QuoterV2 quote at the same fork block |
#   hub USDTx decreased by exactly `value` | executor holds 0 USDTx afterwards | receipt has a USDTx Transfer executor -> (V3 router | pool) of exactly `value`.
# FAILURE case (impossible amountOutMinimum): the call reverts with selector 0xe2ef9c2b (BETargetCallFailed) - on a real finalize the bridge then
#   pays plain USDTx. Any other selector, an RPC error, an unexpected success, or a failed prerequisite (impersonate / mint / approve / balance) fails.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"; KIT="$(cd "$HERE/../.." && pwd)"
# shellcheck source=/dev/null
source "$KIT/cast/env.mainnet.sh"
PORT=${SMOKE_PORT:-$((18900 + RANDOM % 90))}; LOCAL="http://127.0.0.1:$PORT"
WORK=${SMOKE_WORKDIR:-${TMPDIR:-/tmp}/bridge-smoke}; mkdir -p "$WORK"
FAILS=0
fail() { echo "SMOKE FAIL X1-executor $*"; FAILS=$((FAILS + 1)); }
okay() { echo "  ok   $*"; }
need_eq() { if [ "$1" = "$2" ]; then okay "$3 ($1)"; else fail "$3: got $1, expected $2"; return 1; fi; }
ge() { python3 -c 'import sys;sys.exit(0 if int(sys.argv[1])>=int(sys.argv[2]) else 1)' "$1" "$2"; }
sub() { python3 -c 'import sys;print(int(sys.argv[1])-int(sys.argv[2]))' "$1" "$2"; }
num() { awk '{print $1}'; }

anvil --fork-url "$ONE_RPC" --port "$PORT" --retries 15 --fork-retry-backoff 500 --silent > "$WORK/anvil-x1-exec.log" 2>&1 & APID=$!
trap 'kill $APID 2>/dev/null || true; wait $APID 2>/dev/null || true' EXIT
for _ in $(seq 1 40); do cast chain-id --rpc-url "$LOCAL" >/dev/null 2>&1 && break; sleep 1; done
if ! cast chain-id --rpc-url "$LOCAL" >/dev/null 2>&1; then echo "SMOKE FAIL X1-executor fork impossible: $(head -c 300 "$WORK/anvil-x1-exec.log")"; exit 2; fi

HUB=$ONE_BRIDGE; EXEC=$(cast call "$HUB" "bridgeExecutor()(address)" --rpc-url "$LOCAL")
TO=0x00000000000000000000000000000000000a11ce
VALUE=$(python3 -c 'print(100*10**18)')
SEL_EXPECT=0xe2ef9c2b   # BETargetCallFailed()
TRANSFER=$(cast keccak "Transfer(address,address,uint256)")
POOL=$ONE_X1_POOL

bal() { cast call "$1" "balanceOf(address)(uint256)" "$2" --rpc-url "$LOCAL" | num; }
lower() { echo "$1" | tr 'A-F' 'a-f'; }

cast rpc anvil_impersonateAccount "$HUB" --rpc-url "$LOCAL" >/dev/null || { fail "prerequisite: anvil_impersonateAccount failed"; exit 1; }
cast rpc anvil_setBalance "$HUB" "0x$(python3 -c 'print(format(10**20,"x"))')" --rpc-url "$LOCAL" >/dev/null || { fail "prerequisite: anvil_setBalance failed"; exit 1; }
Q=$(cast call "$ONE_QUOTER_V2" "quoteExactInputSingle((address,address,uint256,uint24,uint160))(uint256,uint160,uint32,uint256)" "($ONE_USDTX,$ONE_ONEUSD,$VALUE,$ONE_X1_POOL_FEE,0)" --rpc-url "$LOCAL" | head -1 | num) || { fail "prerequisite: QuoterV2 quote failed"; exit 1; }
MIN_OK=$(python3 -c "import sys;print(int(sys.argv[1])*9950//10000)" "$Q"); MIN_BAD=$(python3 -c "import sys;print(int(sys.argv[1])*2)" "$Q")
DEADLINE=$(( $(date +%s) + 1800 ))

# prepare <floor> : snapshot, build extraData with the SDK, mint USDTx to the hub, approve the executor (each prerequisite checked on its own)
prepare() {
  SNAP=$(cast rpc evm_snapshot --rpc-url "$LOCAL" | tr -d '"')
  EXTRA=$(node "$HERE/x1-extradata.mjs" "$ONE_V3_ROUTER" "$ONE_USDTX" "$ONE_ONEUSD" "$ONE_X1_POOL_FEE" "$TO" "$DEADLINE" "$VALUE" "$1") || { fail "prerequisite: SDK extraData build failed"; return 1; }
  local b0; b0=$(bal "$ONE_USDTX" "$HUB")
  cast send --unlocked --from "$HUB" "$ONE_USDTX" "mint(address,uint256)(bool)" "$HUB" "$VALUE" --rpc-url "$LOCAL" >/dev/null 2>&1 || { fail "prerequisite: minting USDTx to the impersonated hub failed"; return 1; }
  need_eq "$(bal "$ONE_USDTX" "$HUB")" "$(python3 -c 'import sys;print(int(sys.argv[1])+int(sys.argv[2]))' "$b0" "$VALUE")" "prerequisite: hub USDTx balance after mint" || return 1
  cast send --unlocked --from "$HUB" "$ONE_USDTX" "approve(address,uint256)(bool)" "$EXEC" "$VALUE" --rpc-url "$LOCAL" >/dev/null 2>&1 || { fail "prerequisite: approve(executor) failed"; return 1; }
  need_eq "$(cast call "$ONE_USDTX" "allowance(address,address)(uint256)" "$HUB" "$EXEC" --rpc-url "$LOCAL" | num)" "$VALUE" "prerequisite: allowance(hub -> executor)" || return 1
}

case_success() { # <label> <floor>
  echo "--- $1 (amountOutMinimum $2)"
  prepare "$2" || { cast rpc evm_revert "$SNAP" --rpc-url "$LOCAL" >/dev/null; return; }
  local hub0 one0 out rc=0
  hub0=$(bal "$ONE_USDTX" "$HUB"); one0=$(bal "$ONE_ONEUSD" "$TO")
  out=$(cast send --unlocked --from "$HUB" "$EXEC" "executeExtraCall(uint256,uint256,address,address,uint256,bytes)(uint256,bytes)" 56 0 "$ONE_USDTX" "$TO" "$VALUE" "$EXTRA" --rpc-url "$LOCAL" --json 2>&1) || rc=$?
  if [ "$rc" -ne 0 ]; then fail "$1: executeExtraCall reverted/failed: $(echo "$out" | head -c 300)"; cast rpc evm_revert "$SNAP" --rpc-url "$LOCAL" >/dev/null; return; fi
  echo "$out" > "$WORK/x1-exec-receipt.json"
  need_eq "$(python3 -c 'import json,sys;print(int(json.load(open(sys.argv[1]))["status"],16))' "$WORK/x1-exec-receipt.json")" 1 "executeExtraCall tx status" || true
  local one1 hub1 exec1 delta; one1=$(bal "$ONE_ONEUSD" "$TO"); hub1=$(bal "$ONE_USDTX" "$HUB"); exec1=$(bal "$ONE_USDTX" "$EXEC"); delta=$(sub "$one1" "$one0")
  if ge "$delta" "$2"; then okay "ONEUSD delta $delta >= amountOutMinimum $2"; else fail "$1: ONEUSD delta $delta < amountOutMinimum $2"; fi
  need_eq "$delta" "$Q" "ONEUSD delta == QuoterV2 quote at the same fork block" || echo "       (difference: $(sub "$delta" "$Q"))"
  need_eq "$(sub "$hub0" "$hub1")" "$VALUE" "hub USDTx decreased by exactly value" || true
  need_eq "$exec1" 0 "executor holds 0 USDTx after the call" || true
  local tl; tl=$(python3 - "$WORK/x1-exec-receipt.json" "$ONE_USDTX" "$TRANSFER" "$EXEC" "$ONE_V3_ROUTER" "$POOL" "$VALUE" <<'PY'
import json, sys
rec = json.load(open(sys.argv[1])); usdtx, topic, execa, router, pool, value = sys.argv[2:8]
addr = lambda t: "0x" + t[-40:].lower()
hits = [l for l in rec["logs"] if l["address"].lower() == usdtx.lower() and l["topics"][0].lower() == topic.lower() and addr(l["topics"][1]) == execa.lower()
        and addr(l["topics"][2]) in (router.lower(), pool.lower()) and int(l["data"], 16) == int(value)]
print(len(hits))
PY
  )
  need_eq "$tl" 1 "USDTx Transfer executor -> (router|pool) of exactly value (matching receipt logs)" || true
  cast rpc evm_revert "$SNAP" --rpc-url "$LOCAL" >/dev/null
}

case_failure() { # <label> <floor>
  echo "--- $1 (amountOutMinimum $2)"
  prepare "$2" || { cast rpc evm_revert "$SNAP" --rpc-url "$LOCAL" >/dev/null; return; }
  local out rc=0 sel
  out=$(cast call --from "$HUB" "$EXEC" "executeExtraCall(uint256,uint256,address,address,uint256,bytes)(uint256,bytes)" 56 0 "$ONE_USDTX" "$TO" "$VALUE" "$EXTRA" --rpc-url "$LOCAL" 2>&1) || rc=$?
  if [ "$rc" -eq 0 ]; then fail "$1: expected a revert but the call SUCCEEDED (unexpected success)"; cast rpc evm_revert "$SNAP" --rpc-url "$LOCAL" >/dev/null; return; fi
  sel=$(echo "$out" | python3 -c 'import re,sys;m=re.search(r"(?:custom error|data:?)\s*\"?(0x[0-9a-fA-F]{8})",sys.stdin.read());print(m.group(1).lower() if m else "")')
  if [ -z "$sel" ]; then fail "$1: reverted but no 4-byte selector could be decoded (RPC error?): $(echo "$out" | head -c 300)"
  elif [ "$sel" != "$SEL_EXPECT" ]; then fail "$1: reverted with selector $sel, expected $SEL_EXPECT (BETargetCallFailed)"
  else okay "revert selector == $SEL_EXPECT (BETargetCallFailed) -> on a real finalize the bridge falls back to a plain USDTx payout"; fi
  cast rpc evm_revert "$SNAP" --rpc-url "$LOCAL" >/dev/null
}

case_success valid-minimum "$MIN_OK"
case_failure impossible-minimum "$MIN_BAD"
echo "SMOKE X1-executor SUMMARY: $FAILS failed assertion(s)"
exit $(( FAILS > 0 ? 1 : 0 ))
