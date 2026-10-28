#!/usr/bin/env bash
# Shared helpers for the cast cookbook. Sourced by every route script. Requires: cast (foundry) and python3.
#
# Environment contract (all scripts):
#   NETWORK=mainnet|testnet   (default testnet)      FROM=<your address>  (required)
#   AMOUNT=<decimal, e.g. 10> (default 10)           RECIPIENT=<address>  (default $FROM)
#   RECIPIENT_HUB / RECIPIENT_DEST / CORE_RECIPIENT  per-hop recipients (multihop / X2)
#   HOP2_BUFFER=<decimal>     multihop buffer put in the DECLARED exFee (default 0)
#   SEND=1                    really send (default: diagnose = read-only)
#   PRIVATE_KEY=<hex>  or  CAST_SIGNER_ARGS="--account <name>" | "--ledger"   (signer for SEND=1; never printed)
#   GAS_PRICE=<wei or 3gwei>  used for --legacy sends on BSC testnet (chain 97) (default 3gwei)
#   RPC_ONE / RPC_BSC / RPC_HYPEREVM   optional RPC overrides (same names as the SDKs; used to point the scripts at local forks)
# Nothing in this kit sends a transaction unless SEND=1.
# Exit codes: 0 ok | 1 error | 2 usage | 3 value not configured (unconfirmed) | 4 PREFLIGHT REFUSED (nothing approved or sent)
#             | 5 known testnet limitation (x3.sh with QUOTE_ONLY=1 only: the testnet router has no V3 pool for the pair)
set -euo pipefail
set +x

KIT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# QUOTE_ONLY=1 : read-only fee quote without a sender (used by 00-quote.sh)
if [ "${QUOTE_ONLY:-0}" = "1" ]; then : "${FROM:=0x000000000000000000000000000000000000dEaD}"; fi
: "${NETWORK:=testnet}"
case "$NETWORK" in mainnet|testnet) ;; *) echo "NETWORK must be mainnet or testnet" >&2; exit 2;; esac
# shellcheck source=/dev/null
source "$KIT_DIR/env.$NETWORK.sh"
# RPC overrides (the generated env file sets <CHAIN>_RPC unconditionally, so the override is applied AFTER it)
for _p in ONE BSC HYPEREVM; do _o="RPC_$_p"; if [ -n "${!_o:-}" ]; then export "${_p}_RPC=${!_o}"; fi; done

command -v cast >/dev/null || { echo "cast (foundry) is required" >&2; exit 2; }
command -v python3 >/dev/null || { echo "python3 is required (wei arithmetic exceeds bash 64-bit)" >&2; exit 2; }

die() { echo "ERROR: $*" >&2; exit 1; }
say() { echo "$*"; }

# need VAR : exit with the unconfirmed-value marker when a configured value is empty (null in networks.json)
need() {
  local n=$1 v todo
  v="${!n:-}"
  if [ -z "$v" ]; then
    todo="TODO_$n"; todo="${!todo:-TODO(UNCONFIRMED)}"
    echo "⚠ $n is not configured on $NETWORK: $todo" >&2
    exit 3
  fi
}
need_env() { local v="${!1:-}"; [ -n "$v" ] || die "$1 is required (see header of cast/lib.sh)"; }

# ---- wei arithmetic (python3) -------------------------------------------------------------------------------------------
add_wei() { python3 -c 'import sys;print(sum(int(x) for x in sys.argv[1:]))' "$@"; }
sub_wei() { python3 -c 'import sys;print(int(sys.argv[1])-int(sys.argv[2]))' "$1" "$2"; }
ge_wei() { python3 -c 'import sys;sys.exit(0 if int(sys.argv[1])>=int(sys.argv[2]) else 1)' "$1" "$2"; }
gt_wei() { python3 -c 'import sys;sys.exit(0 if int(sys.argv[1])>int(sys.argv[2]) else 1)' "$1" "$2"; }
floor_bps() { python3 -c 'import sys;print(int(sys.argv[1])*(10000-int(sys.argv[2]))//10000)' "$1" "$2"; }
to_wei() { cast to-wei "$1"; }
from_wei() { cast from-wei "$1"; }
# first token of a cast output line ("5000000000000000000 [5e18]" -> "5000000000000000000")
first() { awk '{print $1}'; }
line() { sed -n "${1}p" | awk '{print $1}'; }

# ---- chain selection ------------------------------------------------------------------------------------------------------
# chain_vars <ONE|BSC|HYPEREVM> sets C_ID C_RPC C_BRIDGE
chain_vars() {
  local p=$1 v
  v="${p}_CHAIN_ID"; C_ID="${!v}"
  v="${p}_RPC"; C_RPC="${!v}"
  v="${p}_BRIDGE"; C_BRIDGE="${!v:-}"
}
check_chain() { # <ONE|BSC|HYPEREVM>
  chain_vars "$1"
  local got; got=$(cast chain-id --rpc-url "$C_RPC") || die "cannot reach $C_RPC"
  [ "$got" = "$C_ID" ] || die "network mismatch on $1: RPC reports chainId $got, expected $C_ID ($NETWORK). Nothing was sent."
}

verifier_of() { cast call "$2" "bridgeVerifier()(address)" --rpc-url "$1"; }

# fee_of <rpc> <bridge> <toChainId> <token> <value>  -> sets F_MIN F_NF F_EF  (fees come from the SOURCE chain's verifier)
fee_of() {
  local v out; v=$(verifier_of "$1" "$2")
  out=$(cast call "$v" "calculateFee(uint256,address,uint256)(uint256,uint256,uint256)" "$3" "$4" "$5" --rpc-url "$1")
  F_MIN=$(echo "$out" | line 1); F_NF=$(echo "$out" | line 2); F_EF=$(echo "$out" | line 3)
  F_VERIFIER=$v
}

pair_ok() { # <rpc> <bridge> <toChainId> <token>
  local p; p=$(cast call "$2" "getTokenPair(uint256,address)((address,address,bool,bool,uint256,uint256,uint256))" "$3" "$4" --rpc-url "$1")
  echo "$p" | tr 'A-F' 'a-f' | grep -q "$(echo "$4" | tr 'A-F' 'a-f')" || die "token $4 has no registered pair to chain $3 (RegistryNotExistToken)"
  echo "$p" | grep -q "true, false, " || true
}

balance_of() { cast call "$2" "balanceOf(address)(uint256)" "$FROM" --rpc-url "$1" | first; }
allowance_of() { cast call "$2" "allowance(address,address)(uint256)" "$FROM" "$3" --rpc-url "$1" | first; }

# ---- extraData builders -----------------------------------------------------------------------------------------
# forward_extra <hub> <destChainId> <toDest> <v2> <nf2> <ef2'> <inner|0x>
forward_extra() {
  local call; call=$(cast calldata "bridgeTokenForwarded(uint256,address,uint256,uint256,uint256,bytes)" "$2" "$3" "$4" "$5" "$6" "$7")
  cast concat-hex "$1" "$call"
}
# core_extra <heToken> <coreRecipient> <value> : token || transferToCoreFor(coreRecipient,value)   (never transferToCore(uint256))
core_extra() { cast concat-hex "$1" "$(cast calldata "transferToCoreFor(address,uint256)" "$2" "$3")"; }
# x1_extra <router> <tokenIn> <tokenOut> <fee> <recipient> <deadline> <amountIn> <amountOutMin>
x1_extra() {
  cast concat-hex "$1" "$(cast calldata "exactInputSingle((address,address,uint24,address,uint256,uint256,uint256,uint160))" "($2,$3,$4,$5,$6,$7,$8,0)")"
}

# ---- signer ---------------------------------------------------------------------------------------------------------------
SIGNER_ARGS=""
signer_setup() {
  if [ -n "${PRIVATE_KEY:-}" ]; then SIGNER_ARGS="--private-key $PRIVATE_KEY"; else SIGNER_ARGS="${CAST_SIGNER_ARGS:-}"; fi
  [ -n "$SIGNER_ARGS" ] || die "SEND=1 needs PRIVATE_KEY or CAST_SIGNER_ARGS (e.g. --account <name>)"
  local addr; # shellcheck disable=SC2086
  addr=$(cast wallet address $SIGNER_ARGS) || die "cannot derive the signer address"
  [ "$(echo "$addr" | tr 'A-F' 'a-f')" = "$(echo "$FROM" | tr 'A-F' 'a-f')" ] || die "signer address $addr != FROM $FROM. Nothing was signed."
}

gas_args() { # BSC testnet needs a real gas price or the tx sits in the mempool
  if [ "${C_ID:-}" = "97" ]; then echo "--legacy --gas-price ${GAS_PRICE:-3gwei}"; fi
}

# send_tx <rpc> <to> <sig> <args...> [VALUE=wei]  -> echoes the tx hash; dies unless receipt status == 1
send_tx() {
  local rpc=$1 to=$2 sig=$3; shift 3
  local value="" args=()
  for a in "$@"; do args+=("$a"); done
  if [ -n "${SEND_VALUE:-}" ] && [ "${SEND_VALUE}" != "0" ]; then value="--value $SEND_VALUE"; fi
  local out; # shellcheck disable=SC2086
  out=$(cast send "$to" "$sig" "${args[@]}" --rpc-url "$rpc" $SIGNER_ARGS $(gas_args) $value --json) || die "cast send failed (NOT retried; see the recovery notes in GUIDE §8)"
  local hs; hs=$(echo "$out" | python3 -c 'import json,sys;r=json.load(sys.stdin);print(r["transactionHash"],int(r["status"],16))')
  local h=${hs% *} s=${hs#* }
  [ "$s" = "1" ] || die "transaction $h reverted (status 0)"
  echo "$h"
}

# ensure_allowance <rpc> <token> <spender> <needed> : exact approve, zero-first when 0 < allowance < needed
ensure_allowance() {
  local cur; cur=$(allowance_of "$1" "$2" "$3")
  if ge_wei "$cur" "$4"; then say "allowance ok ($cur >= $4)"; return 0; fi
  if gt_wei "$cur" 0; then say "allowance $cur < $4 and > 0: approve(0) first (USDT-style tokens)"; SEND_VALUE=0 send_tx "$1" "$2" "approve(address,uint256)" "$3" 0 >/dev/null; fi
  say "approve spender=$3 amount=$4"
  local h; h=$(SEND_VALUE=0 send_tx "$1" "$2" "approve(address,uint256)" "$3" "$4")
  say "  approve tx $h (status 1)"
}

print_approve_cmds() { # <rpc> <token> <spender> <needed>
  say "approve needed: spender=$3 amount=$4"
  say "  cast send $2 'approve(address,uint256)' $3 $4 --rpc-url $1 <signer flags>"
}

# human summary of the quote globals
print_quote() {
  say "route $ROUTE on $NETWORK: value=$VALUE networkFee=$NF exFee=$EF total spend=$TOTAL (source token)"
  say "  minimum delivered=$MIN_DELIVERED  msg.value=$MSG_VALUE"
}

CORE_AMOUNT=""; HOP2_INNER=0x   # set by the route scripts that build an X2 / multihop payload (read by preflight_route)

# ---- preflight_route <route> [stage] ------------------------------------------------------------------------------------------
# The cast equivalent of the SDK's BLOCKING preflight (read-only `cast call`s; exit 4 = refused, BEFORE any approve or send).
# It is called twice by every send script: stage "pre-approve" before the approve, and stage "post-approve" right before the bridge send,
# after the fees were re-read (the cast equivalent of a requote). With SEND unset (diagnose) it only REPORTS (PF_REPORT_ONLY=1) and returns 0.
# An RPC error / chain mismatch on any check is itself a refusal (PREFLIGHT_INCOMPLETE / NETWORK_MISMATCH): an unverified check never passes.
# Inputs (set by the route script / quote_route): ROUTE SRC_PREFIX [DEST_PREFIX] SRC_TOKEN TO_CHAIN_ID VALUE TOTAL MSG_VALUE EXTRA
#   [CORE_AMOUNT = the amount encoded in the transferToCoreFor payload] [HOP2_INNER = the inner extraData of a multihop forward]
#   X3: ROUTER TO_ID TOKEN_IN_ADDR NATIVE_IN TOKEN_OUT POOL_FEE AMOUNT_IN.
# Not checked here on purpose: the DESTINATION pair (needs a per-hop pair table); only the destination bridge pause is required.
PF_CODES=()
pf_issue() { PF_CODES+=("$1"); say "  preflight REFUSE $1: $2"; }
pf_note() { say "  preflight ok     $1"; }
pf_cv() { local v; v="${1}_CHAIN_ID"; PF_ID="${!v}"; v="${1}_RPC"; PF_RPC="${!v}"; v="${1}_BRIDGE"; PF_BR="${!v:-}"; }
# pf_read <check> <rpc> <to> <sig> [args...]  -> PF_OUT ; an RPC error becomes a PREFLIGHT_INCOMPLETE refusal (return 1)
pf_read() {
  local name=$1 rpc=$2 to=$3 sig=$4; shift 4
  PF_OUT=$(cast call ${PF_FROM:+--from "$PF_FROM"} "$to" "$sig" "$@" --rpc-url "$rpc" 2>/dev/null) && return 0
  local e; e=$(cast call ${PF_FROM:+--from "$PF_FROM"} "$to" "$sig" "$@" --rpc-url "$rpc" 2>&1 | head -c 160 | tr '\n' ' ') || true
  pf_issue PREFLIGHT_INCOMPLETE "$name could not be verified: $e"; return 1
}
# pf_chain <PREFIX> : 0 when that RPC reports the configured chainId (memoized for one preflight run)
pf_chain() {
  local v="PF_CHK_$1"
  if [ -n "${!v:-}" ]; then [ "${!v}" = ok ]; return; fi
  pf_cv "$1"; local got
  if ! got=$(cast chain-id --rpc-url "$PF_RPC" 2>/dev/null); then pf_issue PREFLIGHT_INCOMPLETE "chainId($1) could not be verified (RPC error)"; printf -v "$v" %s bad; return 1; fi
  if [ "$got" != "$PF_ID" ]; then pf_issue NETWORK_MISMATCH "$1 RPC reports chainId $got, expected $PF_ID"; printf -v "$v" %s bad; return 1; fi
  printf -v "$v" %s ok; pf_note "chainId($1) = $got"
}
pf_paused() { # <PREFIX> <CODE> <label>
  pf_chain "$1" || return 0
  pf_cv "$1"; pf_read "$3 paused()" "$PF_RPC" "$PF_BR" "paused()(bool)" || return 0
  if [ "$PF_OUT" = true ]; then pf_issue "$2" "$3 is paused"; else pf_note "$3 paused() == false"; fi
}
# pf_pair <PREFIX> <toChainId> <token> <CODE-prefix> <check-minted 0|1> <value> : registered / not paused / wrapped stock
pf_pair() {
  pf_chain "$1" || return 0
  pf_cv "$1"; pf_read "pair($1,$2)" "$PF_RPC" "$PF_BR" "getTokenPair(uint256,address)((address,address,bool,bool,uint256,uint256,uint256))" "$2" "$3" || return 0
  local f local_t origin paused minted
  f=$(printf '%s' "$PF_OUT" | python3 -c 'import re,sys;s=re.sub(r"\[[^\]]*\]","",sys.stdin.read()).strip().strip("()");f=[x.strip() for x in s.split(",")];print(f[0].lower(),f[2],f[3],f[6])') || { pf_issue PREFLIGHT_INCOMPLETE "pair($1,$2) output could not be decoded"; return 0; }
  read -r local_t origin paused minted <<<"$f"
  if [ "$local_t" != "$(echo "$3" | tr 'A-F' 'a-f')" ]; then pf_issue "${4}PAIR_NOT_REGISTERED" "$1 has no pair for $3 -> chain $2 (RegistryNotExistToken)"; return 0; fi
  pf_note "$1 pair($3 -> $2) registered"
  if [ "$paused" = true ]; then pf_issue "${4}PAIR_PAUSED" "the $1 pair is paused (RegistryTokenPaused)"; else pf_note "$1 pair not paused"; fi
  if [ "$5" = 1 ] && [ "$origin" != true ]; then
    if ge_wei "$minted" "$6"; then pf_note "wrapped stock minted=$minted >= value"; else pf_issue INSUFFICIENT_MINTED "minted $minted < value $6 (BaseBridgeInvalidValue)"; fi
  fi
}
# pf_maxlen <PREFIX> <bytes> <CODE> <label> : extraData length within the bridge limit (0 = unlimited)
pf_maxlen() {
  pf_chain "$1" || return 0
  pf_cv "$1"; pf_read "maxExtraDataLength($1)" "$PF_RPC" "$PF_BR" "maxExtraDataLength()(uint256)" || return 0
  local max; max=$(echo "$PF_OUT" | first)
  if [ "$max" = 0 ] || ge_wei "$max" "$2"; then pf_note "$4 length $2/$max"; else pf_issue "$3" "$4 $2 B > the $1 bridge max $max B (BaseBridgeExtraDataTooLong)"; fi
}
# pf_whitelist <PREFIX> <executor> <target> <selector> <label>
pf_whitelist() {
  pf_cv "$1"; local wl mc wm=true
  pf_read "executor isWhitelistedTarget" "$PF_RPC" "$2" "isWhitelistedTarget(address)(bool)" "$3" || return 0; wl=$PF_OUT
  pf_read "executor isMethodCheckEnabled" "$PF_RPC" "$2" "isMethodCheckEnabled(address)(bool)" "$3" || return 0; mc=$PF_OUT
  if [ "$mc" = true ]; then pf_read "executor isWhitelistedMethod" "$PF_RPC" "$2" "isWhitelistedMethod(address,bytes4)(bool)" "$3" "$4" || return 0; wm=$PF_OUT; fi
  if [ "$wl" = true ] && [ "$wm" = true ]; then pf_note "executor $2 whitelists $5 (target=$wl methodCheck=$mc method=$wm)"
  else pf_issue EXECUTOR_NOT_WHITELISTED "executor $2 does not whitelist $5 (target=$wl methodCheck=$mc method=$wm): BETargetNotWhitelisted / BEMethodNotWhitelisted"; fi
}
prefix_of_chain_id() { case "$1" in "$ONE_CHAIN_ID") echo ONE;; "$BSC_CHAIN_ID") echo BSC;; "$HYPEREVM_CHAIN_ID") echo HYPEREVM;; esac; }

preflight_route() { # <route> [pre-approve|post-approve]
  local route=$1 stage=${2:-pre-approve} kind=single core=0 src=${SRC_PREFIX:-ONE} dest="" hub=$ONE_BRIDGE
  PF_CODES=(); PF_CHK_ONE=""; PF_CHK_BSC=""; PF_CHK_HYPEREVM=""; PF_FROM=""
  case "$route" in R5|R6) kind=multihop;; R5+X2) kind=multihop; core=1;; X2) core=1;; X1) kind=x1;; X3) kind=x3; src=ONE;; esac
  case $kind in
    single) dest=$(prefix_of_chain_id "$TO_CHAIN_ID");;
    multihop) dest=$DEST_PREFIX;;
    x1) dest=ONE;;
    x3) dest=$(prefix_of_chain_id "$TO_ID");;
  esac
  say "preflight ($stage) $route on $NETWORK:"
  # 1. chain ids (source, hub, HyperEVM for X2); the destination RPC is asserted by its own check below
  pf_chain "$src" || true
  if [ "$kind" = multihop ] || [ "$kind" = x1 ]; then pf_chain ONE || true; fi
  if [ "$core" = 1 ]; then pf_chain HYPEREVM || true; fi
  # 2. source bridge paused (X3: the hub bridge the router calls)
  pf_paused "$src" BRIDGE_PAUSED "source bridge"
  # 3. source pair, extraData length (X3: the tokenOut pair on the hub)
  if [ "$kind" = x3 ]; then
    pf_pair ONE "$TO_ID" "$TOKEN_OUT" "" 0 0
    pf_cv ONE
    if pf_chain ONE; then
      pf_read "x3 router.bridge()" "$PF_RPC" "$ROUTER" "bridge()(address)" && {
        if [ "$(echo "$PF_OUT" | tr 'A-F' 'a-f')" = "$(echo "$hub" | tr 'A-F' 'a-f')" ]; then pf_note "router.bridge() == hub"; else pf_issue ROUTER_BRIDGE_MISMATCH "SwapBridgeRouter.bridge() is $PF_OUT, expected the hub $hub"; fi; }
      PF_FROM=$FROM
      pf_read "x3 quote status" "$PF_RPC" "$ROUTER" "getAmountSwapBridgeOut(uint256,address,address,uint24,uint256)(uint8,uint256,uint256,uint256,uint256)" "$TO_ID" "$TOKEN_IN_ADDR" "$TOKEN_OUT" "$POOL_FEE" "$AMOUNT_IN" && {
        local st; st=$(echo "$PF_OUT" | line 1)
        if [ "$st" = 1 ]; then pf_note "router quote status == Success"; else pf_issue "X3_QUOTE_STATUS_$st" "SwapBridgeRouter quote status $st (1=Success 2=NoPair 3=InsufficientForFee 4=InsufficientValue 5=InvalidSwap)"; fi; }
      PF_FROM=""
    fi
  else
    pf_pair "$src" "$TO_CHAIN_ID" "$SRC_TOKEN" "" 1 "$VALUE"
    pf_maxlen "$src" "$(( (${#EXTRA} - 2) / 2 ))" EXTRADATA_TOO_LONG_SOURCE "source extraData"
  fi
  # 4. hub: multihop / X1
  if [ "$kind" = multihop ] || [ "$kind" = x1 ]; then
    pf_paused ONE HUB_PAUSED "hub bridge"
    local hexec=""
    if pf_chain ONE; then
      pf_cv ONE
      if pf_read "hub bridgeExecutor()" "$PF_RPC" "$hub" "bridgeExecutor()(address)"; then
        hexec=$PF_OUT
        if [ "$hexec" = 0x0000000000000000000000000000000000000000 ]; then pf_issue HUB_EXECUTOR_UNSET "hub bridgeExecutor() is not set: extraData cannot be executed on the hub"; hexec=""; else pf_note "hub bridgeExecutor() = $hexec"; fi
      fi
    fi
    if [ "$kind" = multihop ]; then
      local dv="${DEST_PREFIX}_CHAIN_ID"
      pf_pair ONE "${!dv}" "$NATIVE" HUB_ 0 0
      pf_maxlen ONE "$(( (${#HOP2_INNER} - 2) / 2 ))" EXTRADATA_TOO_LONG_HUB "hub inner extraData"
      [ -z "$hexec" ] || pf_whitelist ONE "$hexec" "$hub" 0x55fd0776 "hub + bridgeTokenForwarded"
    else
      if [ -n "$hexec" ]; then pf_whitelist ONE "$hexec" "$ONE_V3_ROUTER" 0x414bf389 "V3 SwapRouter + exactInputSingle"; fi
      if pf_chain ONE; then
        pf_cv ONE
        if [ -n "${ONE_V3_FACTORY:-}" ] && pf_read "x1 factory.getPool" "$PF_RPC" "$ONE_V3_FACTORY" "getPool(address,address,uint24)(address)" "$ONE_USDTX" "$ONE_ONEUSD" "$ONE_X1_POOL_FEE"; then
          if [ "$(echo "$PF_OUT" | tr 'A-F' 'a-f')" = "$(echo "$ONE_X1_POOL" | tr 'A-F' 'a-f')" ]; then pf_note "factory.getPool(USDTx, ONEUSD, fee) == configured pool"; else pf_issue POOL_NOT_FOUND "factory has no pool $ONE_X1_POOL for (USDTx, ONEUSD, fee $ONE_X1_POOL_FEE): got $PF_OUT"; fi
        fi
        if pf_read "x1 pool liquidity" "$PF_RPC" "$ONE_X1_POOL" "liquidity()(uint128)"; then
          if gt_wei "$(echo "$PF_OUT" | first)" 0; then pf_note "pool liquidity > 0"; else pf_issue POOL_NO_LIQUIDITY "the swap pool has no liquidity"; fi
        fi
      fi
    fi
  fi
  # 5. the DESTINATION bridge (every bridge after the source; for X1 the destination is the hub, checked above)
  if [ "$kind" != x1 ] && [ "$dest" != "$src" ]; then pf_paused "$dest" DEST_BRIDGE_PAUSED "destination bridge ($dest)"; fi
  # 6. X2: HyperCore deposit checks on HyperEVM
  if [ "$core" = 1 ]; then
    if pf_chain HYPEREVM; then
      pf_cv HYPEREVM; local tok=$HYPEREVM_ONE_TOKEN he_rpc=$PF_RPC he_bridge=$PF_BR
      if pf_read "token isCoreTokenIndexSet()" "$he_rpc" "$tok" "isCoreTokenIndexSet()(bool)"; then
        if [ "$PF_OUT" = true ]; then pf_note "token.isCoreTokenIndexSet() == true"; else pf_issue CORE_NOT_LINKED "the HyperEVM token is not linked to HyperCore (isCoreTokenIndexSet=false)"; fi
      fi
      if pf_read "HyperEVM bridgeExecutor()" "$he_rpc" "$he_bridge" "bridgeExecutor()(address)"; then
        pf_whitelist HYPEREVM "$PF_OUT" "$tok" 0x9438568d "the ONE token + transferToCoreFor"
      fi
      if pf_read "coreTransferableAmount" "$he_rpc" "$tok" "coreTransferableAmount(uint256)(uint256)" "$CORE_AMOUNT"; then
        if gt_wei "$(echo "$PF_OUT" | first)" 0; then pf_note "coreTransferableAmount($CORE_AMOUNT) > 0"; else pf_issue CORE_AMOUNT_ZERO "amount is below 1 Core wei after flooring (CoreAmountBelowOneCoreWei)"; fi
      fi
    fi
  fi
  # 7. balance / allowance on the SOURCE chain (allowance is blocking only after the approve step)
  if pf_chain "$src"; then
    pf_cv "$src"; local btok="" need="" spender=""
    if [ "$kind" = x3 ]; then
      if [ "$NATIVE_IN" = 1 ]; then need=$AMOUNT_IN; else btok=$TOKEN_IN_ADDR; need=$AMOUNT_IN; spender=$ROUTER; fi
    elif [ "$SRC_TOKEN" != "$NATIVE" ]; then btok=$SRC_TOKEN; need=$TOTAL; spender=$PF_BR
    else need=$MSG_VALUE; fi
    if [ -n "$btok" ]; then
      if pf_read "balanceOf" "$PF_RPC" "$btok" "balanceOf(address)(uint256)" "$FROM"; then
        if ge_wei "$(echo "$PF_OUT" | first)" "$need"; then pf_note "token balance >= total ($need)"; else pf_issue INSUFFICIENT_BALANCE "balance $(echo "$PF_OUT" | first) < required $need (ERC20InsufficientBalance)"; fi
      fi
      if pf_read "allowance" "$PF_RPC" "$btok" "allowance(address,address)(uint256)" "$FROM" "$spender"; then
        if ge_wei "$(echo "$PF_OUT" | first)" "$need"; then pf_note "allowance >= total"
        elif [ "$stage" = post-approve ]; then pf_issue ALLOWANCE_INSUFFICIENT "allowance $(echo "$PF_OUT" | first) < $need after the approve step"
        else say "  preflight warn   allowance $(echo "$PF_OUT" | first) < $need: the script approves first"; fi
      fi
    else
      local nb; if nb=$(cast balance "$FROM" --rpc-url "$PF_RPC" 2>/dev/null); then
        if ge_wei "$nb" "$need"; then pf_note "native balance >= value ($need)"; else pf_issue INSUFFICIENT_BALANCE "native balance $nb < required $need (gas not included)"; fi
      else pf_issue PREFLIGHT_INCOMPLETE "native balance could not be verified (RPC error)"; fi
    fi
  fi
  if [ ${#PF_CODES[@]} -eq 0 ]; then say "preflight: ok ($stage)"; return 0; fi
  local codes; codes=$(printf '%s\n' "${PF_CODES[@]}" | sort -u | paste -sd, -)
  if [ "${PF_REPORT_ONLY:-0}" = 1 ]; then say "preflight: refused($codes) [diagnose: informational, nothing is sent]"; return 0; fi
  say "preflight: REFUSED($codes) at $stage - nothing was approved or sent after this point (exit 4)"
  exit 4
}

# run_bridge : generic flow around the route's quote_route() function.
#   quote_route must set: ROUTE VALUE NF EF TOTAL MIN_DELIVERED MSG_VALUE EXTRA TO1 SRC_PREFIX SRC_TOKEN TO_CHAIN_ID  (+ APPROVE_TOKEN when ERC20)
#   Order matches the SDK: check chain -> quote -> report -> [SEND=1] sender check -> PREFLIGHT (pre-approve) -> approve ->
#   RE-QUOTE under the limits captured at the first quote -> PREFLIGHT (post-approve) -> simulate -> send. NEVER an automatic resend.
run_bridge() {
  need_env FROM
  check_chain "$SRC_PREFIX"
  chain_vars "$SRC_PREFIX"; local rpc=$C_RPC bridge=$C_BRIDGE
  quote_route; chain_vars "$SRC_PREFIX"
  local limit_total=$TOTAL limit_min=$MIN_DELIVERED
  print_quote
  if [ "${QUOTE_ONLY:-0}" = "1" ]; then say "RESULT $ROUTE $NETWORK quote-only ok"; return 0; fi
  local erc20=0; [ "$SRC_TOKEN" != "$NATIVE" ] && erc20=1
  local blocked=""
  if [ $erc20 = 1 ]; then
    local bal alw; bal=$(balance_of "$rpc" "$SRC_TOKEN"); alw=$(allowance_of "$rpc" "$SRC_TOKEN" "$bridge")
    say "  balance=$bal allowance(bridge)=$alw"
    ge_wei "$bal" "$TOTAL" || blocked="balance"
    ge_wei "$alw" "$TOTAL" || { [ -z "$blocked" ] && blocked="allowance" || blocked="$blocked,allowance"; }
  else
    local nb; nb=$(cast balance "$FROM" --rpc-url "$rpc"); say "  native balance=$nb"
    ge_wei "$nb" "$MSG_VALUE" || blocked="balance"
  fi

  simulate() { # shellcheck disable=SC2086
    cast call --from "$FROM" ${MSG_VALUE:+--value "$MSG_VALUE"} "$bridge" "bridgeToken(uint256,address,address,uint256,uint256,uint256,bytes)(bool)" "$TO_CHAIN_ID" "$SRC_TOKEN" "$TO1" "$VALUE" "$NF" "$EF" "$EXTRA" --rpc-url "$rpc"
  }

  if [ "${SEND:-0}" != "1" ]; then
    say "mode: diagnose (read-only). Nothing is sent. Re-run with SEND=1 to broadcast."
    PF_REPORT_ONLY=1 preflight_route "$ROUTE"
    if [ -n "$blocked" ]; then
      [[ "$blocked" == *allowance* ]] && print_approve_cmds "$rpc" "$SRC_TOKEN" "$bridge" "$TOTAL"
      say "RESULT $ROUTE $NETWORK encoding-verified simulation-blocked($blocked)"
    elif r=$(simulate 2>&1); then say "simulation: $r"; say "RESULT $ROUTE $NETWORK encoding-verified simulation-passed"
    else say "simulation reverted: $r"; say "RESULT $ROUTE $NETWORK encoding-verified simulation-reverted"; fi
    say "bridge call:"; say "  cast send $bridge 'bridgeToken(uint256,address,address,uint256,uint256,uint256,bytes)' $TO_CHAIN_ID $SRC_TOKEN $TO1 $VALUE $NF $EF $EXTRA --rpc-url $rpc ${MSG_VALUE:+--value $MSG_VALUE} <signer flags>"
    return 0
  fi

  signer_setup
  preflight_route "$ROUTE" pre-approve   # exit 4 when refused: nothing was approved or sent
  local nonce0 block0; nonce0=$(cast nonce "$FROM" --block pending --rpc-url "$rpc"); block0=$(cast block-number --rpc-url "$rpc")
  if [ $erc20 = 1 ]; then ensure_allowance "$rpc" "$SRC_TOKEN" "$bridge" "$TOTAL"; fi
  say "re-quote under the limits of the first quote (maxTotalSpend=$limit_total minDelivered=$limit_min)"
  FROZEN=1 quote_route; chain_vars "$SRC_PREFIX"
  gt_wei "$TOTAL" "$limit_total" && die "re-quote would raise the total spend ($TOTAL > $limit_total). Nothing was sent; quote again."
  ge_wei "$MIN_DELIVERED" "$limit_min" || die "re-quote would lower the delivered floor ($MIN_DELIVERED < $limit_min). Nothing was sent; quote again."
  if [ $erc20 = 1 ]; then ge_wei "$(allowance_of "$rpc" "$SRC_TOKEN" "$bridge")" "$TOTAL" || ensure_allowance "$rpc" "$SRC_TOKEN" "$bridge" "$TOTAL"; fi
  preflight_route "$ROUTE" post-approve   # after the re-read fees: exit 4 when refused, nothing is sent
  simulate >/dev/null || die "final simulation reverted - nothing was sent"
  say "checkpoint: from=$FROM chainId=$C_ID pendingNonceBefore=$nonce0 blockBefore=$block0 bridge=$bridge   (keep it: recovery needs it)"
  local h
  h=$(SEND_VALUE="${MSG_VALUE:-0}" send_tx "$rpc" "$bridge" "bridgeToken(uint256,address,address,uint256,uint256,uint256,bytes)" "$TO_CHAIN_ID" "$SRC_TOKEN" "$TO1" "$VALUE" "$NF" "$EF" "$EXTRA")
  say "SENT $h"
  say "track: $KIT_DIR/track.sh $C_ID $h   (NETWORK=$NETWORK)"
}

# recipients
rec_dest() { echo "${RECIPIENT_DEST:-${RECIPIENT:-$FROM}}"; }
rec_hub() { echo "${RECIPIENT_HUB:-${RECIPIENT:-$FROM}}"; }
rec_core() { echo "${CORE_RECIPIENT:-${RECIPIENT:-$FROM}}"; }
amount_wei() { to_wei "${AMOUNT:-10}"; }
