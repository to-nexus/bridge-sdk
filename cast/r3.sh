#!/usr/bin/env bash
# R3  ONE -> HyperEVM: native ONE becomes the ONE ERC20 on HyperEVM.
# X2  add CORE_RECIPIENT=0x.. to deposit straight to that HyperCore account (token || transferToCoreFor).
source "$(dirname "$0")/lib.sh"
ROUTE=R3; SRC_PREFIX=CROSS
need CROSS_BRIDGE; need HYPEREVM_BRIDGE; need HYPEREVM_ONE_TOKEN
[ -n "${CORE_RECIPIENT:-}" ] && ROUTE=X2
quote_route() {
  chain_vars CROSS
  SRC_TOKEN=$NATIVE; TO_CHAIN_ID=$HYPEREVM_CHAIN_ID; TO1=$(rec_dest); VALUE=$(amount_wei)
  pair_ok "$C_RPC" "$C_BRIDGE" "$TO_CHAIN_ID" "$SRC_TOKEN"
  fee_of "$C_RPC" "$C_BRIDGE" "$TO_CHAIN_ID" "$SRC_TOKEN" "$VALUE"
  ge_wei "$VALUE" "$F_MIN" || die "amount $VALUE is below minimumValue $F_MIN"
  NF=$F_NF; EF=$F_EF; EXTRA=0x
  if [ -n "${CORE_RECIPIENT:-}" ]; then
    need HYPEREVM_CORE_SYSTEM
    check_chain HYPEREVM; chain_vars HYPEREVM
    # the link / whitelist / amount checks are enforced by preflight_route (exit 4 before any approve or send); this line only informs
    [ "$(cast call "$HYPEREVM_ONE_TOKEN" "isCoreTokenIndexSet()(bool)" --rpc-url "$C_RPC")" = "true" ] || say "X2: WARNING the HyperEVM token is not linked to HyperCore (isCoreTokenIndexSet=false): preflight will refuse a send"
    unit=$(cast call "$HYPEREVM_ONE_TOKEN" "coreUnit()(uint256)" --rpc-url "$C_RPC" | first)
    say "X2: coreUnit=$unit -> HyperCore credit $(python3 -c "import sys;print(int(sys.argv[1])//int(sys.argv[2]))" "$VALUE" "$unit") core-wei, dust $(python3 -c "import sys;print(int(sys.argv[1])%int(sys.argv[2]))" "$VALUE" "$unit") wei returns to the recipient"
    EXTRA=$(core_extra "$HYPEREVM_ONE_TOKEN" "$(rec_core)" "$VALUE")
    CORE_AMOUNT=$VALUE
    chain_vars CROSS
  fi
  TOTAL=$(add_wei "$VALUE" "$NF" "$EF"); MIN_DELIVERED=$VALUE; MSG_VALUE=$TOTAL
}
run_bridge
