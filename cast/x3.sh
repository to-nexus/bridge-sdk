#!/usr/bin/env bash
# X3  Swap on the ONE chain (Uniswap V3 via SwapBridgeRouter) and bridge the output in ONE tx. Atomic: any failure reverts everything.
#   AMOUNT = amountIn.  TOKEN_IN=<address>|native  TOKEN_OUT=<address>  POOL_FEE=<uint24, default 0>  TO_CHAIN=bsc|hyperevm (default bsc)
#   SLIPPAGE_BPS (default 50), DEADLINE_SEC (default 1800).
#   Defaults (assumption A6, when configured): ONEUSD -> USDTx -> BSC USDT.
# Two floors, both printed: the SWAP-OUTPUT floor (amountOutMinimum, enforced on chain) and the BRIDGE-DELIVERY floor (minBridgeValue,
# shown to you; it holds as long as bridge fees do not rise between quote and inclusion).
source "$(dirname "$0")/lib.sh"
ROUTE=X3
need ONE_BRIDGE; need ONE_SWAP_BRIDGE_ROUTER
need_env FROM
TO_CHAIN=${TO_CHAIN:-bsc}
case "$TO_CHAIN" in bsc) TO_ID=$BSC_CHAIN_ID; need BSC_BRIDGE;; hyperevm) TO_ID=$HYPEREVM_CHAIN_ID; need HYPEREVM_BRIDGE;; *) die "TO_CHAIN must be bsc or hyperevm";; esac
TOKEN_IN=${TOKEN_IN:-${ONE_ONEUSD:-}}; TOKEN_OUT=${TOKEN_OUT:-${ONE_USDTX:-}}
# quote-only demo on a network without ONEUSD: swap native ONE (via WCROSS) instead
if [ -z "$TOKEN_IN" ] && [ "${QUOTE_ONLY:-0}" = "1" ]; then TOKEN_IN=native; fi
[ -n "$TOKEN_IN" ] || die "TOKEN_IN is required on $NETWORK (no default: ONEUSD is not configured)"
[ -n "$TOKEN_OUT" ] || die "TOKEN_OUT is required"
POOL_FEE=${POOL_FEE:-0}
NATIVE_IN=0; TOKEN_IN_ADDR=$TOKEN_IN
if [ "$TOKEN_IN" = "native" ]; then need ONE_WCROSS; NATIVE_IN=1; TOKEN_IN_ADDR=$ONE_WCROSS; fi
check_chain ONE; chain_vars ONE; RPC=$C_RPC; ROUTER=$ONE_SWAP_BRIDGE_ROUTER
AMOUNT_IN=$(amount_wei)
SBR_STRUCT_SIG="swapBridgeExactInputSingle((address,address,uint24,uint256,uint256,uint160,(uint256,address,bytes)),uint256)"
[ $NATIVE_IN = 1 ] && SBR_STRUCT_SIG="swapBridgeExactInputSingleETH((address,address,uint24,uint256,uint256,uint160,(uint256,address,bytes)),uint256)"
FROZEN_MIN=""; FROZEN_BRIDGE_MIN=""; DEADLINE=""

quote_route() {
  local out status
  out=$(cast call --from "$FROM" "$ROUTER" "getAmountSwapBridgeOut(uint256,address,address,uint24,uint256)(uint8,uint256,uint256,uint256,uint256)" "$TO_ID" "$TOKEN_IN_ADDR" "$TOKEN_OUT" "$POOL_FEE" "$AMOUNT_IN" --rpc-url "$RPC") || die "SwapBridgeRouter quote reverted"
  status=$(echo "$out" | line 1); SWAP_OUT=$(echo "$out" | line 2); BRIDGE_VALUE=$(echo "$out" | line 3); NF=$(echo "$out" | line 4); EF=$(echo "$out" | line 5)
  [[ "$status" =~ ^[0-9]+$ ]] || die "router quote status could not be decoded: '$status'"
  if [ "$status" != "1" ]; then
    # exit 5 (00-quote.sh: SKIPPED_ACCEPTED) ONLY on positive evidence: the quote eth_call SUCCEEDED and returned status 0 (Invalid) or 5 (InvalidSwap)
    # on TESTNET = the known "no V3 pool for the pair" limitation. Any other status, any RPC error, any decode error is a plain failure (exit 1).
    if [ "${QUOTE_ONLY:-0}" = "1" ] && [ "$NETWORK" = "testnet" ] && { [ "$status" = "0" ] || [ "$status" = "5" ]; }; then
      say "ACCEPTED_SKIP X3 testnet quote-status=$status ($([ "$status" = 0 ] && echo Invalid || echo InvalidSwap)): the quote eth_call succeeded; the testnet router has no V3 pool for the pair (known limitation)"
      exit 5
    fi
    die "router quote status $status (1=Success 2=NoPair 3=InsufficientForFee 4=InsufficientValue 5=InvalidSwap)"
  fi
  if [ -z "$FROZEN_MIN" ]; then
    OUT_MIN=$(floor_bps "$SWAP_OUT" "${SLIPPAGE_BPS:-50}"); DEADLINE=$(( $(date +%s) + ${DEADLINE_SEC:-1800} ))
  else
    ge_wei "$SWAP_OUT" "$FROZEN_MIN" || die "re-quote: swap output $SWAP_OUT fell below the approved floor $FROZEN_MIN. Nothing was sent."
    OUT_MIN=$FROZEN_MIN
  fi
  # delivery floor under CURRENT fees, computed from the floor that will actually be in calldata
  local eb; eb=$(cast call "$ROUTER" "getExpectedBridgeAmount(uint256,address,uint256)(uint8,uint256,uint256,uint256)" "$TO_ID" "$TOKEN_OUT" "$OUT_MIN" --rpc-url "$RPC")
  [ "$(echo "$eb" | line 1)" = "1" ] || die "the delivery floor cannot be bridged (status $(echo "$eb" | line 1)); lower slippage or raise the amount"
  DELIVERY_AT_FLOOR=$(echo "$eb" | line 2)
  if [ -z "$FROZEN_MIN" ]; then MIN_BRIDGE=$DELIVERY_AT_FLOOR; else
    ge_wei "$DELIVERY_AT_FLOOR" "$FROZEN_BRIDGE_MIN" || die "re-quote: delivery at the ORIGINAL floor is now $DELIVERY_AT_FLOOR < approved $FROZEN_BRIDGE_MIN (bridge fees rose). Nothing was sent."
    MIN_BRIDGE=$FROZEN_BRIDGE_MIN
  fi
  PARAMS="($TOKEN_IN_ADDR,$TOKEN_OUT,$POOL_FEE,$AMOUNT_IN,$OUT_MIN,0,($TO_ID,$(rec_dest),0x))"
}
quote_route
say "X3 on $NETWORK: amountIn=$AMOUNT_IN swapOut=$SWAP_OUT -> bridge value=$BRIDGE_VALUE networkFee=$NF exFee=$EF"
say "  swap-output floor amountOutMinimum=$OUT_MIN (on chain)  bridge-delivery floor minBridgeValue=$MIN_BRIDGE (informational)  deadline=$DEADLINE"
if [ "${QUOTE_ONLY:-0}" = "1" ]; then say "RESULT X3 $NETWORK quote-only ok"; exit 0; fi
FROZEN_MIN=$OUT_MIN; FROZEN_BRIDGE_MIN=$MIN_BRIDGE; LIMIT_SPEND=$AMOUNT_IN

VAL_ARGS=""; [ $NATIVE_IN = 1 ] && VAL_ARGS="--value $AMOUNT_IN"
simulate() { # shellcheck disable=SC2086
  cast call --from "$FROM" $VAL_ARGS "$ROUTER" "$SBR_STRUCT_SIG" "$PARAMS" "$DEADLINE" --rpc-url "$RPC"; }
blocked=""
if [ $NATIVE_IN = 0 ]; then
  bal=$(balance_of "$RPC" "$TOKEN_IN_ADDR"); alw=$(allowance_of "$RPC" "$TOKEN_IN_ADDR" "$ROUTER"); say "  balance=$bal allowance(router)=$alw"
  ge_wei "$bal" "$AMOUNT_IN" || blocked="balance"; ge_wei "$alw" "$AMOUNT_IN" || blocked="${blocked:+$blocked,}allowance"
else
  nb=$(cast balance "$FROM" --rpc-url "$RPC"); ge_wei "$nb" "$AMOUNT_IN" || blocked="balance"
fi
if [ "${SEND:-0}" != "1" ]; then
  say "mode: diagnose (read-only). Nothing is sent."
  PF_REPORT_ONLY=1 preflight_route X3
  if [ -n "$blocked" ]; then
    [[ "$blocked" == *allowance* ]] && print_approve_cmds "$RPC" "$TOKEN_IN_ADDR" "$ROUTER" "$AMOUNT_IN"
    say "RESULT X3 $NETWORK encoding-verified simulation-blocked($blocked)"
  elif r=$(simulate 2>&1); then say "RESULT X3 $NETWORK encoding-verified simulation-passed"; else say "simulation reverted: $r"; say "RESULT X3 $NETWORK encoding-verified simulation-reverted"; fi
  say "call: cast send $ROUTER '$SBR_STRUCT_SIG' '$PARAMS' $DEADLINE --rpc-url $RPC $VAL_ARGS <signer flags>"
  exit 0
fi
signer_setup
preflight_route X3 pre-approve   # exit 4 when refused: nothing was approved or sent
nonce0=$(cast nonce "$FROM" --block pending --rpc-url "$RPC"); block0=$(cast block-number --rpc-url "$RPC")
[ $NATIVE_IN = 0 ] && ensure_allowance "$RPC" "$TOKEN_IN_ADDR" "$ROUTER" "$AMOUNT_IN"
say "re-quote under the approved floors (amountOutMinimum=$FROZEN_MIN minBridgeValue=$FROZEN_BRIDGE_MIN)"
quote_route
preflight_route X3 post-approve   # after the re-quote: exit 4 when refused, nothing is sent
simulate >/dev/null || die "final simulation reverted - nothing was sent"
say "checkpoint: from=$FROM chainId=$ONE_CHAIN_ID pendingNonceBefore=$nonce0 blockBefore=$block0 router=$ROUTER (recovery: SwapBridge(user=FROM,...) on the router, NOT BridgeInitiated.from)"
if [ $NATIVE_IN = 1 ]; then SV=$AMOUNT_IN; else SV=0; fi
h=$(SEND_VALUE=$SV send_tx "$RPC" "$ROUTER" "$SBR_STRUCT_SIG" "$PARAMS" "$DEADLINE")
say "SENT $h"; say "track: $KIT_DIR/track.sh $ONE_CHAIN_ID $h   (NETWORK=$NETWORK)"
