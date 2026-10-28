#!/usr/bin/env bash
# R1  BSC -> ONE (CROSS): the CROSS ERC20 on BSC becomes native ONE on the CROSS chain.
#   FROM=0x.. AMOUNT=10 NETWORK=testnet ./r1.sh            # diagnose (read-only)
#   FROM=0x.. AMOUNT=10 NETWORK=testnet SEND=1 PRIVATE_KEY=.. ./r1.sh
source "$(dirname "$0")/lib.sh"
ROUTE=R1; SRC_PREFIX=BSC
need BSC_BRIDGE; need BSC_ONE_TOKEN; need CROSS_BRIDGE
quote_route() {
  chain_vars BSC
  SRC_TOKEN=$BSC_ONE_TOKEN; TO_CHAIN_ID=$CROSS_CHAIN_ID; TO1=$(rec_dest); VALUE=$(amount_wei)
  pair_ok "$C_RPC" "$C_BRIDGE" "$TO_CHAIN_ID" "$SRC_TOKEN"
  fee_of "$C_RPC" "$C_BRIDGE" "$TO_CHAIN_ID" "$SRC_TOKEN" "$VALUE"
  ge_wei "$VALUE" "$F_MIN" || die "amount $VALUE is below minimumValue $F_MIN"
  NF=$F_NF; EF=$F_EF; EXTRA=0x; TOTAL=$(add_wei "$VALUE" "$NF" "$EF"); MIN_DELIVERED=$VALUE; MSG_VALUE=0
}
run_bridge
