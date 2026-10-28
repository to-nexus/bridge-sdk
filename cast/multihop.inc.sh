# shared by r5.sh / r6.sh: multihop backward computation. Needs SRC_PREFIX, SRC_TOKEN_VAR, DEST_PREFIX.
multihop_quote() {
  chain_vars CROSS; local hub_rpc=$C_RPC hub=$C_BRIDGE
  chain_vars "$DEST_PREFIX"; local dest_id=$C_ID dest_token_var="${DEST_PREFIX}_ONE_TOKEN"
  chain_vars "$SRC_PREFIX"; local src_rpc=$C_RPC src_bridge=$C_BRIDGE src_id=$C_ID
  local tv="${SRC_PREFIX}_ONE_TOKEN"; SRC_TOKEN="${!tv}"; TO_CHAIN_ID=$CROSS_CHAIN_ID
  local hubv out; hubv=$(verifier_of "$hub_rpc" "$hub")
  V2=$(amount_wei)
  # 1) hop-2 fee from the HUB verifier (destination = final chain, token = native)
  out=$(cast call "$hubv" "calculateFee(uint256,address,uint256)(uint256,uint256,uint256)" "$dest_id" "$NATIVE" "$V2" --rpc-url "$hub_rpc")
  local mv2 nf2 ef2; mv2=$(echo "$out" | line 1); nf2=$(echo "$out" | line 2); ef2=$(echo "$out" | line 3)
  ge_wei "$V2" "$mv2" || die "amount $V2 is below the hop-2 minimumValue $mv2"
  # 2) the buffer goes into the DECLARED exFee, never into value
  local buffer ef2p; buffer=$(to_wei "${HOP2_BUFFER:-0}"); ef2p=$(add_wei "$ef2" "$buffer")
  # 3) hop-1 value (strict equality on the hub): v1 = v2 + nf2 + ef2'
  VALUE=$(add_wei "$V2" "$nf2" "$ef2p")
  local inner=0x; HOP2_KIND=plain
  if [ -n "${CORE_RECIPIENT:-}" ] && [ "$DEST_PREFIX" = "HYPEREVM" ]; then
    need HYPEREVM_ONE_TOKEN; inner=$(core_extra "$HYPEREVM_ONE_TOKEN" "$(rec_core)" "$V2"); HOP2_KIND=x2; CORE_AMOUNT=$V2
  fi
  HOP2_INNER=$inner
  EXTRA=$(forward_extra "$hub" "$dest_id" "$(rec_dest)" "$V2" "$nf2" "$ef2p" "$inner")
  # 4) hop-1 fee from the SOURCE verifier, on v1
  pair_ok "$src_rpc" "$src_bridge" "$TO_CHAIN_ID" "$SRC_TOKEN"
  fee_of "$src_rpc" "$src_bridge" "$TO_CHAIN_ID" "$SRC_TOKEN" "$VALUE"
  ge_wei "$VALUE" "$F_MIN" || die "hop-1 value $VALUE is below the source minimumValue $F_MIN"
  NF=$F_NF; EF=$F_EF; TO1=$(rec_hub); TOTAL=$(add_wei "$VALUE" "$NF" "$EF"); MIN_DELIVERED=$V2; MSG_VALUE=0
  say "hop2 (hub verifier): minimumValue=$mv2 networkFee=$nf2 exFee=$ef2 buffer=$buffer -> declared exFee'=$ef2p ; hop1 value v1=$VALUE"
  say "extraData length: $(( ($(echo -n "$EXTRA" | wc -c) - 2) / 2 )) bytes (must be <= maxExtraDataLength of source and hub)"
  chain_vars "$SRC_PREFIX"
}
