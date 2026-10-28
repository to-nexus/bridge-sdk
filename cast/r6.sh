#!/usr/bin/env bash
# R6  HyperEVM -> ONE hub -> BSC with ONE bridge request (first-time ERC20 approve is separate).  AMOUNT = minimum delivered on BSC (v2).
source "$(dirname "$0")/lib.sh"
ROUTE=R6; SRC_PREFIX=HYPEREVM; DEST_PREFIX=BSC
need HYPEREVM_BRIDGE; need HYPEREVM_ONE_TOKEN; need ONE_BRIDGE; need BSC_BRIDGE; need BSC_ONE_TOKEN
source "$KIT_DIR/multihop.inc.sh"
quote_route() { multihop_quote; }
run_bridge
