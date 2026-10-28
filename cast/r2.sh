#!/usr/bin/env bash
# R2  ONE -> BSC: native ONE becomes the CROSS ERC20 on BSC. msg.value = value + networkFee + exFee exactly.
source "$(dirname "$0")/lib.sh"
ROUTE=R2; SRC_PREFIX=CROSS
need CROSS_BRIDGE; need BSC_BRIDGE; need BSC_ONE_TOKEN
quote_route() {
  chain_vars CROSS
  SRC_TOKEN=$NATIVE; TO_CHAIN_ID=$BSC_CHAIN_ID; TO1=$(rec_dest); VALUE=$(amount_wei)
  pair_ok "$C_RPC" "$C_BRIDGE" "$TO_CHAIN_ID" "$SRC_TOKEN"
  fee_of "$C_RPC" "$C_BRIDGE" "$TO_CHAIN_ID" "$SRC_TOKEN" "$VALUE"
  ge_wei "$VALUE" "$F_MIN" || die "amount $VALUE is below minimumValue $F_MIN"
  NF=$F_NF; EF=$F_EF; EXTRA=0x; TOTAL=$(add_wei "$VALUE" "$NF" "$EF"); MIN_DELIVERED=$VALUE; MSG_VALUE=$TOTAL
}
run_bridge
