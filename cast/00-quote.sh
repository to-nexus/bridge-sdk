#!/usr/bin/env bash
# 00-quote.sh [R1|R2|R3|X2|R4|R5|R6|X1|X3|all]   -- READ-ONLY fee quote for every configured route on NETWORK (default: all routes).
#   NETWORK=mainnet ./00-quote.sh          AMOUNT=20 NETWORK=testnet ./00-quote.sh R5
# Never sends a transaction (QUOTE_ONLY=1). Every route ends in exactly ONE of four classes, printed in a summary table:
#   OK                    the script exited 0 AND printed its `RESULT <route> <network> quote-only ok` line
#   SKIPPED_UNCONFIGURED  the script exited 3 AND printed the `is not configured on <network>` marker (a value is null in networks.json)
#   SKIPPED_ACCEPTED      the script exited 5 = the known testnet X3 limitation: the quote call SUCCEEDED and returned status 0 (Invalid) or 5 (InvalidSwap)
#                         because the testnet router has no V3 pool for the pair; it prints `ACCEPTED_SKIP X3 testnet quote-status=<n>`. Only X3 on testnet.
#   FAILED                anything else: an RPC error, a timeout, an unexpected status, a decode error, a script error, a missing evidence line
# Exit code: 1 if ANY route is FAILED (whatever its position), otherwise 0.
here="$(cd "$(dirname "$0")" && pwd)"
want=${1:-all}
export QUOTE_ONLY=1
NET=${NETWORK:-testnet}
LABELS=(); CLASSES=(); DETAILS=()
run() { # <label> <script> [env...]
  local label=$1 script=$2; shift 2
  if [ "$want" != all ] && [ "$want" != "$label" ]; then return 0; fi
  echo "---- $label"
  local out rc cls detail
  out=$(env "$@" "$here/$script" 2>&1); rc=$?
  echo "$out" | sed 's/^/  /'
  case $rc in
    0) if echo "$out" | grep -Eq "^RESULT [^ ]+ $NET quote-only ok\$"; then cls=OK; detail="quote ok"; else cls=FAILED; detail="exit 0 without the RESULT quote-only line"; fi;;
    3) if echo "$out" | grep -q "is not configured on $NET"; then cls=SKIPPED_UNCONFIGURED; detail=$(echo "$out" | grep "is not configured on $NET" | head -1 | cut -c1-110); else cls=FAILED; detail="exit 3 without the not-configured marker"; fi;;
    5) if [ "$label" = X3 ] && [ "$NET" = testnet ] && echo "$out" | grep -Eq '^ACCEPTED_SKIP X3 testnet quote-status=(0|5) '; then
         cls=SKIPPED_ACCEPTED; detail=$(echo "$out" | grep '^ACCEPTED_SKIP' | head -1 | sed -E 's/^ACCEPTED_SKIP X3 testnet //; s/:.*//')
       else cls=FAILED; detail="exit 5 without the evidence line (only X3 on testnet with quote status 0/5 qualifies)"; fi;;
    *) cls=FAILED; detail="exit $rc: $(echo "$out" | tail -1 | cut -c1-100)";;
  esac
  echo "  => $cls"
  LABELS+=("$label"); CLASSES+=("$cls"); DETAILS+=("$detail")
}
run R1 r1.sh
run R2 r2.sh
run R3 r3.sh
run X2 r3.sh "CORE_RECIPIENT=${CORE_RECIPIENT:-0x000000000000000000000000000000000000dEaD}"
run R4 r4.sh
run R5 r5.sh
run R6 r6.sh
run X1 x1.sh
run X3 x3.sh

echo
echo "SUMMARY $NET"
printf '  %-6s %-20s %s\n' ROUTE CLASS DETAIL
failed=0; nok=0; nunc=0; nacc=0
for i in "${!LABELS[@]}"; do
  printf '  %-6s %-20s %s\n' "${LABELS[$i]}" "${CLASSES[$i]}" "${DETAILS[$i]}"
  case "${CLASSES[$i]}" in
    OK) nok=$((nok + 1));; SKIPPED_UNCONFIGURED) nunc=$((nunc + 1));; SKIPPED_ACCEPTED) nacc=$((nacc + 1));; FAILED) failed=$((failed + 1));;
  esac
done
echo "  OK=$nok SKIPPED_UNCONFIGURED=$nunc SKIPPED_ACCEPTED=$nacc FAILED=$failed"
if [ "$failed" -gt 0 ]; then echo "00-quote: $failed route(s) FAILED -> exit 1"; exit 1; fi
echo "00-quote: no route failed -> exit 0"
exit 0
