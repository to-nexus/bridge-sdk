#!/usr/bin/env bash
# R5  BSC -> ONE hub -> HyperEVM with ONE bridge request (first-time ERC20 approve is separate).  AMOUNT = minimum delivered on HyperEVM (v2).
# Optional: RECIPIENT_HUB (hop-1 `to`, receives native ONE on the hub if hop 2 fails), RECIPIENT_DEST, HOP2_BUFFER (ONE),
#           CORE_RECIPIENT (X2: HyperCore deposit as the innermost extraData).
source "$(dirname "$0")/lib.sh"
ROUTE=R5; SRC_PREFIX=BSC; DEST_PREFIX=HYPEREVM
need BSC_BRIDGE; need BSC_ONE_TOKEN; need CROSS_BRIDGE; need HYPEREVM_BRIDGE; need HYPEREVM_ONE_TOKEN
[ -n "${CORE_RECIPIENT:-}" ] && ROUTE=R5+X2
source "$KIT_DIR/multihop.inc.sh"
quote_route() { multihop_quote; }
run_bridge
