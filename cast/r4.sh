#!/usr/bin/env bash
# R4  HyperEVM -> ONE: the ONE ERC20 on HyperEVM becomes native ONE (burn path; approve spender = the HyperEVM bridge).
source "$(dirname "$0")/lib.sh"
ROUTE=R4; SRC_PREFIX=HYPEREVM
need HYPEREVM_BRIDGE; need HYPEREVM_ONE_TOKEN; need CROSS_BRIDGE
quote_route() {
  chain_vars HYPEREVM
  SRC_TOKEN=$HYPEREVM_ONE_TOKEN; TO_CHAIN_ID=$CROSS_CHAIN_ID; TO1=$(rec_dest); VALUE=$(amount_wei)
  pair_ok "$C_RPC" "$C_BRIDGE" "$TO_CHAIN_ID" "$SRC_TOKEN"
  fee_of "$C_RPC" "$C_BRIDGE" "$TO_CHAIN_ID" "$SRC_TOKEN" "$VALUE"
  ge_wei "$VALUE" "$F_MIN" || die "amount $VALUE is below minimumValue $F_MIN"
  NF=$F_NF; EF=$F_EF; EXTRA=0x; TOTAL=$(add_wei "$VALUE" "$NF" "$EF"); MIN_DELIVERED=$VALUE; MSG_VALUE=0
}
run_bridge
