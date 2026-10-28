#!/usr/bin/env bash
# track.sh <srcChainId> <txhash>   (NETWORK=mainnet|testnet)   -- read-only
# Reads the source receipt, prints the hop-1 transfer(s) and, for each, what the destination bridge says:
#   IN_FLIGHT / PENDING_ON_DEST / FINALIZED(+ the keyed events). It is the shell view of the SDK's tracker; the SDKs additionally
#   resolve extraCall outcomes, hub forwards (hop 2), releases and fallbacks (GUIDE §7, events and status tracking).
source "$(dirname "$0")/lib.sh"
SRC_ID=${1:?usage: track.sh <srcChainId> <txhash>}; TX=${2:?usage: track.sh <srcChainId> <txhash>}
pfx=""; for p in ONE BSC HYPEREVM; do v="${p}_CHAIN_ID"; [ "${!v}" = "$SRC_ID" ] && pfx=$p; done
[ -n "$pfx" ] || die "chain $SRC_ID is not part of $NETWORK"
check_chain "$pfx"; chain_vars "$pfx"; SRC_RPC=$C_RPC; SRC_BRIDGE=$C_BRIDGE
TOPIC_INIT=$(cast keccak "BridgeInitiated(uint256,uint256,address,address,address,address,uint256,uint256,uint256,bytes,uint256)")
rec=$(cast receipt "$TX" --json --rpc-url "$SRC_RPC") || die "no receipt yet for $TX on chain $SRC_ID (SOURCE_SUBMITTED)"
status=$(echo "$rec" | python3 -c 'import json,sys;print(int(json.load(sys.stdin)["status"],16))')
[ "$status" = "1" ] || { say "SOURCE_FAILED: $TX reverted - no transfer was created"; exit 0; }
list=$(python3 - "$SRC_BRIDGE" "$TOPIC_INIT" "$rec" <<'PY'
import json,sys
bridge=sys.argv[1].lower(); topic=sys.argv[2].lower(); rec=json.loads(sys.argv[3])
for l in rec["logs"]:
    if l["address"].lower()==bridge and l["topics"][0].lower()==topic:
        print(int(l["topics"][1],16), int(l["topics"][2],16))
PY
)
n=0
while read -r to idx; do
  [ -n "$to" ] || continue
  n=$((n+1))
  # map toChainId -> prefix
  dpfx=""; for p in ONE BSC HYPEREVM; do v="${p}_CHAIN_ID"; [ "${!v}" = "$to" ] && dpfx=$p; done
  [ -n "$dpfx" ] || { say "transfer #$n: toChainID=$to index=$idx (destination chain not in $NETWORK config)"; continue; }
  check_chain "$dpfx"; chain_vars "$dpfx"
  say "transfer #$n: $pfx($SRC_ID) -> $dpfx($to) index=$idx"
  next=$(cast call "$C_BRIDGE" "getNextFinalizeIndex(uint256)(uint256)" "$SRC_ID" --rpc-url "$C_RPC" | first)
  pend=$(cast call "$C_BRIDGE" "allPendingIndex(uint256)(uint256[])" "$SRC_ID" --rpc-url "$C_RPC")
  if echo "$pend" | tr -d '[]' | tr ',' '\n' | awk '{print $1}' | grep -qx "$idx"; then say "  PENDING_ON_DEST (held in the destination pending queue)"; continue; fi
  if ! ge_wei "$next" "$((idx+1))"; then say "  IN_FLIGHT (destination next finalize index $next <= $idx)"; continue; fi
  say "  index advanced on $dpfx (next finalize index $next). Keyed events for (fromChainID=$SRC_ID, index=$idx):"
  topic0s=$(for e in "BridgeFinalized(uint256,uint256,address,address,uint256,uint256)" "ExtraCallExecuted(uint256,uint256,address,bytes4,bool,uint256,bytes)" "ForwardInitiated(uint256,uint256,uint256,address,address,uint256,uint256,uint256,bytes32)" "ForwardFailed(uint256,uint256,uint8,bytes32)" "ManualReleased(uint256,uint256)" "PendingRemoved(uint256,uint256)" "BridgePending(uint256,uint256,address,address,uint256,uint256,uint8)"; do cast keccak "$e"; done | python3 -c 'import sys,json;print(json.dumps([l.strip() for l in sys.stdin]))')
  p32() { python3 -c 'import sys;print("0x"+format(int(sys.argv[1]),"064x"))' "$1"; }
  head=$(cast block-number --rpc-url "$C_RPC"); from=$(python3 -c 'import sys;print(max(0,int(sys.argv[1])-20000))' "$head")
  logs=$(cast rpc eth_getLogs "{\"address\":\"$C_BRIDGE\",\"fromBlock\":\"$(printf '0x%x' "$from")\",\"toBlock\":\"latest\",\"topics\":[$topic0s,\"$(p32 "$SRC_ID")\",\"$(p32 "$idx")\"]}" --rpc-url "$C_RPC") || { say "  UNKNOWN: logs unavailable on this RPC (ONE nodes may not serve old logs). Check the explorer."; continue; }
  echo "$logs" | python3 -c '
import json,sys
names={}
for l in json.load(sys.stdin):
    print("   ", l["topics"][0][:10], "tx", l["transactionHash"], "block", int(l["blockNumber"],16))
' || true
  say "  -> decode with: cast receipt <tx> --json | the event signatures above; the SDK tracker maps them to DELIVERED / DELIVERED_FALLBACK / FORWARDED / HUB_FALLBACK / RELEASED_FROM_PENDING"
done <<< "$list"
[ $n -gt 0 ] || say "no BridgeInitiated event from $SRC_BRIDGE in $TX"
