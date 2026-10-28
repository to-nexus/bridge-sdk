#!/usr/bin/env bash
# X1  BSC USDT -> ONE chain ONEUSD (BridgeExecutor extraCall: Uniswap V3 exactInputSingle on the hub).
# Outcome: ONEUSD to the recipient, or plain USDTx if the swap cannot run (slippage / deadline / pool) - never a loss.
# SLIPPAGE_BPS (default 50), DEADLINE_SEC (default 1800). AMOUNT = USDT value.
source "$(dirname "$0")/lib.sh"
ROUTE=X1; SRC_PREFIX=BSC
need BSC_BRIDGE; need BSC_USDT; need CROSS_BRIDGE; need CROSS_USDTX; need CROSS_ONEUSD; need CROSS_V3_ROUTER; need CROSS_QUOTER_V2; need CROSS_X1_POOL; need CROSS_X1_POOL_FEE
X1_OUT_MIN=""; X1_DEADLINE=""
quote_route() {
  chain_vars BSC
  SRC_TOKEN=$BSC_USDT; TO_CHAIN_ID=$CROSS_CHAIN_ID; TO1=$(rec_dest); VALUE=$(amount_wei)
  pair_ok "$C_RPC" "$C_BRIDGE" "$TO_CHAIN_ID" "$SRC_TOKEN"
  check_chain CROSS
  local out; out=$(cast call "$CROSS_QUOTER_V2" "quoteExactInputSingle((address,address,uint256,uint24,uint160))(uint256,uint160,uint32,uint256)" "($CROSS_USDTX,$CROSS_ONEUSD,$VALUE,$CROSS_X1_POOL_FEE,0)" --rpc-url "$CROSS_RPC")
  local expected; expected=$(echo "$out" | line 1)
  if [ "${FROZEN:-0}" = "1" ]; then
    # the floor and deadline approved at the first quote are NEVER recomputed
    ge_wei "$expected" "$X1_OUT_MIN" || die "re-quote: expected swap output $expected fell below the approved floor $X1_OUT_MIN. Nothing was sent."
  else
    X1_OUT_MIN=$(floor_bps "$expected" "${SLIPPAGE_BPS:-50}"); X1_DEADLINE=$(( $(date +%s) + ${DEADLINE_SEC:-1800} ))
  fi
  EXTRA=$(x1_extra "$CROSS_V3_ROUTER" "$CROSS_USDTX" "$CROSS_ONEUSD" "$CROSS_X1_POOL_FEE" "$(rec_dest)" "$X1_DEADLINE" "$VALUE" "$X1_OUT_MIN")
  chain_vars BSC
  fee_of "$C_RPC" "$C_BRIDGE" "$TO_CHAIN_ID" "$SRC_TOKEN" "$VALUE"
  ge_wei "$VALUE" "$F_MIN" || die "amount $VALUE is below minimumValue $F_MIN"
  NF=$F_NF; EF=$F_EF; TOTAL=$(add_wei "$VALUE" "$NF" "$EF"); MIN_DELIVERED=$VALUE; MSG_VALUE=0
  say "X1: expected ONEUSD out=$expected  amountOutMinimum=$X1_OUT_MIN (slippage ${SLIPPAGE_BPS:-50} bps) deadline=$X1_DEADLINE"
}
run_bridge
