#!/usr/bin/env bash
# X1  BSC USDT -> ONE chain ONEUSD (BridgeExecutor extraCall: exactInputSingle on the hub DEX: Uniswap V3, or Slipstream CL with DEX=cl).
# Outcome: ONEUSD to the recipient, or plain USDTx if the swap cannot run (slippage / deadline / pool) - never a loss.
# SLIPPAGE_BPS (default 50), DEADLINE_SEC (default 1800). AMOUNT = USDT value.
source "$(dirname "$0")/lib.sh"
ROUTE=X1; SRC_PREFIX=BSC
need BSC_BRIDGE; need BSC_USDT; need ONE_BRIDGE; need ONE_USDTX; need ONE_ONEUSD; need "$DEX_ROUTER_VAR"; need "$DEX_QUOTER_VAR"; need "$DEX_X1_POOL_VAR"; need "$DEX_X1_KEY_VAR"
X1_ROUTER=${!DEX_ROUTER_VAR}; X1_QUOTER=${!DEX_QUOTER_VAR}; X1_KEY=${!DEX_X1_KEY_VAR}   # X1_KEY: uint24 fee (V3) | int24 tickSpacing (CL)
X1_OUT_MIN=""; X1_DEADLINE=""
quote_route() {
  chain_vars BSC
  SRC_TOKEN=$BSC_USDT; TO_CHAIN_ID=$ONE_CHAIN_ID; TO1=$(rec_dest); VALUE=$(amount_wei)
  pair_ok "$C_RPC" "$C_BRIDGE" "$TO_CHAIN_ID" "$SRC_TOKEN"
  check_chain ONE
  local out; out=$(cast call "$X1_QUOTER" "quoteExactInputSingle((address,address,uint256,$DEX_KEY_TYPE,uint160))(uint256,uint160,uint32,uint256)" "($ONE_USDTX,$ONE_ONEUSD,$VALUE,$X1_KEY,0)" --rpc-url "$ONE_RPC")
  local expected; expected=$(echo "$out" | line 1)
  if [ "${FROZEN:-0}" = "1" ]; then
    # the floor and deadline approved at the first quote are NEVER recomputed
    ge_wei "$expected" "$X1_OUT_MIN" || die "re-quote: expected swap output $expected fell below the approved floor $X1_OUT_MIN. Nothing was sent."
  else
    X1_OUT_MIN=$(floor_bps "$expected" "${SLIPPAGE_BPS:-50}"); X1_DEADLINE=$(( $(date +%s) + ${DEADLINE_SEC:-1800} ))
  fi
  EXTRA=$(x1_extra "$X1_ROUTER" "$ONE_USDTX" "$ONE_ONEUSD" "$X1_KEY" "$(rec_dest)" "$X1_DEADLINE" "$VALUE" "$X1_OUT_MIN")
  chain_vars BSC
  fee_of "$C_RPC" "$C_BRIDGE" "$TO_CHAIN_ID" "$SRC_TOKEN" "$VALUE"
  ge_wei "$VALUE" "$F_MIN" || die "amount $VALUE is below minimumValue $F_MIN"
  NF=$F_NF; EF=$F_EF; TOTAL=$(add_wei "$VALUE" "$NF" "$EF"); MIN_DELIVERED=$VALUE; MSG_VALUE=0
  say "X1 ($DEX_LABEL): expected ONEUSD out=$expected  amountOutMinimum=$X1_OUT_MIN (slippage ${SLIPPAGE_BPS:-50} bps) deadline=$X1_DEADLINE"
}
run_bridge
