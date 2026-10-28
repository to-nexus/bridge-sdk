#!/usr/bin/env bash
# Read-only on-chain probes that back every value in networks.json.
# Usage: bash tools/probe.sh            (prints "PROBE <net> <label> => <result>" lines)
# Public RPCs only. No transaction is ever sent (cast call / cast code / cast storage / cast chain-id).
set -uo pipefail

IMPL_SLOT=0x360894a13ba1a3210667c828492db98dca3e2076cc3735a920a3ca505d382bbc
NATIVE=0x0000000000000000000000000000000000000001
PAIR_T='(address,address,bool,bool,uint256,uint256,uint256)'

RPC_ONE_MAIN=https://mainnet.onechain.nexus:22001
RPC_BSC_MAIN=https://bsc-mainnet-public.cross-api.in
RPC_HYPE_MAIN=https://rpc.hyperliquid.xyz/evm
RPC_ONE_TEST=https://testnet.onechain.nexus:22001
RPC_BSC_TEST=https://bsc-testnet-public.cross-api.in
RPC_HYPE_TEST=https://rpc.hyperliquid-testnet.xyz/evm

p() { # net rpc label addr sig args...
  local net=$1 rpc=$2 label=$3 addr=$4 sig=$5; shift 5
  local out; out=$(timeout 30 cast call "$addr" "$sig" "$@" --rpc-url "$rpc" 2>&1 | tr '\n' ' ' | head -c 700)
  echo "PROBE $net $label => $out"
}
code() { # net rpc label addr [selector]
  local net=$1 rpc=$2 label=$3 addr=$4 sel=${5:-}
  local c; c=$(timeout 30 cast code "$addr" --rpc-url "$rpc" 2>&1)
  local n=$(( (${#c} - 2) / 2 )); [ "$n" -lt 0 ] && n=0
  local extra=""
  if [ -n "$sel" ]; then
    if echo "$c" | grep -qi "${sel#0x}"; then extra=" selector $sel present"; else extra=" selector $sel ABSENT"; fi
  fi
  echo "PROBE $net $label => code bytes=$n$extra"
}
impl() { local net=$1 rpc=$2 addr=$3; echo "PROBE $net impl-slot($addr) => $(timeout 30 cast storage "$addr" $IMPL_SLOT --rpc-url "$rpc" 2>&1 | tr '\n' ' ')"; }

echo "# probe run $(date -u +%FT%TZ)"
for n in "one-mainnet $RPC_ONE_MAIN" "bsc-mainnet $RPC_BSC_MAIN" "hyperevm-mainnet $RPC_HYPE_MAIN" "one-testnet $RPC_ONE_TEST" "bsc-testnet $RPC_BSC_TEST" "hyperevm-testnet $RPC_HYPE_TEST"; do
  set -- $n; echo "PROBE $1 chain-id => $(timeout 30 cast chain-id --rpc-url "$2" 2>&1)"
done

# ---------------------------------------------------------------- generic bridge probe
bridge_probe() { # net rpc bridge
  local net=$1 rpc=$2 b=$3
  impl "$net" "$rpc" "$b"
  p "$net" "$rpc" "paused()" "$b" "paused()(bool)"
  p "$net" "$rpc" "allChainIDs()" "$b" "allChainIDs()(uint256[])"
  p "$net" "$rpc" "bridgeVerifier()" "$b" "bridgeVerifier()(address)"
  p "$net" "$rpc" "bridgeExecutor()" "$b" "bridgeExecutor()(address)"
  p "$net" "$rpc" "maxExtraDataLength()" "$b" "maxExtraDataLength()(uint256)"
}
tok() { # net rpc addr
  p "$1" "$2" "symbol($3)" "$3" "symbol()(string)"
  p "$1" "$2" "decimals($3)" "$3" "decimals()(uint8)"
}
pairs() { # net rpc bridge chainid
  p "$1" "$2" "allTokenPairs($4)" "$3" "allTokenPairs(uint256)($PAIR_T[])" "$4"
}
fee() { # net rpc bridge chainid token value
  local v; v=$(timeout 30 cast call "$3" "bridgeVerifier()(address)" --rpc-url "$2" 2>&1)
  p "$1" "$2" "calculateFee($4,$5,$6)@$v" "$v" "calculateFee(uint256,address,uint256)(uint256,uint256,uint256)" "$4" "$5" "$6"
}
exec_probe() { # net rpc bridge target...
  local net=$1 rpc=$2 b=$3; shift 3
  local e; e=$(timeout 30 cast call "$b" "bridgeExecutor()(address)" --rpc-url "$rpc" 2>&1)
  for t in "$@"; do
    p "$net" "$rpc" "executor.isWhitelistedTarget($t)" "$e" "isWhitelistedTarget(address)(bool)" "$t"
    p "$net" "$rpc" "executor.isMethodCheckEnabled($t)" "$e" "isMethodCheckEnabled(address)(bool)" "$t"
  done
}

HUB=0xb81d6e000000000000000000000000000000C0de
FWD_SEL=0x55fd0776

# ================================================================ ONE mainnet 612055
N=one-mainnet; R=$RPC_ONE_MAIN
bridge_probe $N $R $HUB
code $N $R "hub proxy" $HUB
IMPL=$(cast storage $HUB $IMPL_SLOT --rpc-url $R | sed 's/0x000000000000000000000000/0x/')
code $N $R "hub impl $IMPL" "$IMPL" $FWD_SEL
for c in 56 999; do pairs $N $R $HUB $c; done
USDTX_M=0x1c37d4F44ed3eC86d96868001CfA89e97112a85C
p $N $R "getTokenPair(56,NATIVE)" $HUB "getTokenPair(uint256,address)($PAIR_T)" 56 $NATIVE
p $N $R "getTokenPair(56,USDTx)" $HUB "getTokenPair(uint256,address)($PAIR_T)" 56 $USDTX_M
p $N $R "getTokenPair(999,NATIVE)" $HUB "getTokenPair(uint256,address)($PAIR_T)" 999 $NATIVE
fee $N $R $HUB 56 $NATIVE 10000000000000000000
fee $N $R $HUB 999 $NATIVE 10000000000000000000
fee $N $R $HUB 56 $USDTX_M 10000000000000000000
EXEC_M=$(cast call $HUB "bridgeExecutor()(address)" --rpc-url $R)
V3R_M=0x0e36011c70e28DF14B7Eb39Aab23F8e9F3fa39Ba
exec_probe $N $R $HUB $HUB $V3R_M
p $N $R "executor.isWhitelistedMethod(hub,fwd)" $EXEC_M "isWhitelistedMethod(address,bytes4)(bool)" $HUB $FWD_SEL
p $N $R "executor.isWhitelistedMethod(v3router,0x414bf389)" $EXEC_M "isWhitelistedMethod(address,bytes4)(bool)" $V3R_M 0x414bf389
tok $N $R $USDTX_M
ONEUSD=0xff42c13d927a6b9265236619161accb227b6d452
tok $N $R $ONEUSD
V3F_M=0x4658026B6C0f9751E6d9F312718FaCFa12Dfb61D; QV2_M=0x9FBa55a11f845Fe029546f6Fe1CA7f6B2a3e07eb; WC_M=0x731A104a4461aE45BFEf520468B43c262d35E682
POOL_M=0x298c360039de417A1b12aFAc64b9175Dbdd0DbfE
tok $N $R $WC_M
code $N $R "v3 swapRouter" $V3R_M 0x414bf389
p $N $R "v3Router.factory()" $V3R_M "factory()(address)"
p $N $R "v3Router.WETH9()" $V3R_M "WETH9()(address)"
code $N $R "quoterV2" $QV2_M
p $N $R "quoterV2.factory()" $QV2_M "factory()(address)"
p $N $R "quoterV2.WETH9()" $QV2_M "WETH9()(address)"
p $N $R "factory.getPool(USDTx,ONEUSD,0)" $V3F_M "getPool(address,address,uint24)(address)" $USDTX_M $ONEUSD 0
p $N $R "pool.liquidity()" $POOL_M "liquidity()(uint128)"
p $N $R "pool.fee()" $POOL_M "fee()(uint24)"
p $N $R "quoterV2.quoteExactInputSingle(USDTx->ONEUSD,1e18,fee0)" $QV2_M "quoteExactInputSingle((address,address,uint256,uint24,uint160))(uint256,uint160,uint32,uint256)" "($USDTX_M,$ONEUSD,1000000000000000000,0,0)"
p $N $R "quoterV2.quoteExactInputSingle(ONEUSD->USDTx,1e18,fee0)" $QV2_M "quoteExactInputSingle((address,address,uint256,uint24,uint160))(uint256,uint160,uint32,uint256)" "($ONEUSD,$USDTX_M,1000000000000000000,0,0)"
SBR_M=0xB298C340816fee45cfB39B1e6EE4DB4D5739e357
code $N $R "SwapBridgeRouter" $SBR_M
p $N $R "router.bridge()" $SBR_M "bridge()(address)"
p $N $R "router.swapRouter()" $SBR_M "swapRouter()(address)"
p $N $R "router.quoter()" $SBR_M "quoter()(address)"

# ================================================================ BSC mainnet 56
N=bsc-mainnet; R=$RPC_BSC_MAIN; B56=0xa6a1a0c1e6ABa5FC6C84E856dCF81506e2bE06c5
bridge_probe $N $R $B56
code $N $R "bridge proxy" $B56
BIMPL=$(cast storage $B56 $IMPL_SLOT --rpc-url $R | sed 's/0x000000000000000000000000/0x/')
code $N $R "bridge impl $BIMPL" "$BIMPL"
pairs $N $R $B56 612055
BSC_ONE_M=0x6bf62ca91e397B5A7d1D6bCe97D9092065d7A510; BSC_USDT_M=0x55d398326f99059fF775485246999027B3197955
tok $N $R $BSC_ONE_M; tok $N $R $BSC_USDT_M
p $N $R "getTokenPair(612055,ONE)" $B56 "getTokenPair(uint256,address)($PAIR_T)" 612055 $BSC_ONE_M
p $N $R "getTokenPair(612055,USDT)" $B56 "getTokenPair(uint256,address)($PAIR_T)" 612055 $BSC_USDT_M
fee $N $R $B56 612055 $BSC_ONE_M 10000000000000000000
fee $N $R $B56 612055 $BSC_USDT_M 10000000000000000000

# ================================================================ HyperEVM mainnet 999
N=hyperevm-mainnet; R=$RPC_HYPE_MAIN; H_TOK_M=0xFaA92e0e9BF90102ff1F731e7DeBD89CD7D1Ebe5
tok $N $R $H_TOK_M
p $N $R "token.totalSupply()" $H_TOK_M "totalSupply()(uint256)"
p $N $R "token.isCoreTokenIndexSet()" $H_TOK_M "isCoreTokenIndexSet()(bool)"
p $N $R "token.coreUnit()" $H_TOK_M "coreUnit()(uint256)"
code $N $R "token" $H_TOK_M
B999_M=0xd8A89e931E504Bdf7775640F800fe46528D097E5
bridge_probe $N $R $B999_M
impl $N $R $B999_M
p $N $R "getTokenPair(612055,ONE)" $B999_M "getTokenPair(uint256,address)($PAIR_T)" 612055 $H_TOK_M
fee $N $R $B999_M 612055 $H_TOK_M 10000000000000000000

# ================================================================ ONE testnet 612044
N=one-testnet; R=$RPC_ONE_TEST
bridge_probe $N $R $HUB
IMPL=$(cast storage $HUB $IMPL_SLOT --rpc-url $R | sed 's/0x000000000000000000000000/0x/')
code $N $R "hub impl $IMPL" "$IMPL" $FWD_SEL
for c in 97 998; do pairs $N $R $HUB $c; done
USDTX_T=0x9F85c7B5D7637E18f946cc8AF9C131318c6833d9
p $N $R "getTokenPair(97,NATIVE)" $HUB "getTokenPair(uint256,address)($PAIR_T)" 97 $NATIVE
p $N $R "getTokenPair(97,USDTx)" $HUB "getTokenPair(uint256,address)($PAIR_T)" 97 $USDTX_T
p $N $R "getTokenPair(998,NATIVE)" $HUB "getTokenPair(uint256,address)($PAIR_T)" 998 $NATIVE
tok $N $R $USDTX_T
fee $N $R $HUB 97 $NATIVE 10000000000000000000
fee $N $R $HUB 998 $NATIVE 10000000000000000000
fee $N $R $HUB 97 $USDTX_T 10000000000000000000
EXEC_T=$(cast call $HUB "bridgeExecutor()(address)" --rpc-url $R)
exec_probe $N $R $HUB $HUB
p $N $R "executor.isWhitelistedMethod(hub,fwd)" $EXEC_T "isWhitelistedMethod(address,bytes4)(bool)" $HUB $FWD_SEL
SBR_T=0xc9b23a5e14747cc2acc3dca7237d3174ce1bcf5b
code $N $R "SwapBridgeRouter" $SBR_T
for f in "bridge()(address)" "swapRouter()(address)" "quoter()(address)" "WETH9()(address)"; do p $N $R "SwapBridgeRouter.$f" $SBR_T "$f"; done
V3R_T=0x61745D6Dc4342cF01bE2C145218CE1952260cd05; QV2_T=0x19D62F16FbFc200F8a1eFc521f10b54A3801D10A; WC_T=0xda37a3B60E6b97E8CB44b460130d40331efc566b
code $N $R "v3 swapRouter" $V3R_T 0x414bf389
p $N $R "v3Router.factory()" $V3R_T "factory()(address)"
p $N $R "v3Router.WETH9()" $V3R_T "WETH9()(address)"
p $N $R "quoterV2.factory()" $QV2_T "factory()(address)"
tok $N $R $WC_T
# mainnet-only addresses are absent on the testnet (no USDTx/ONEUSD pool known there)
for a in $V3R_M $QV2_M $ONEUSD $POOL_M; do code $N $R "mainnet address $a on testnet" $a; done

# ================================================================ BSC testnet 97
N=bsc-testnet; R=$RPC_BSC_TEST; B97=0x192FFaCAf26A53b439C33F3d2C166ceB0A26bf2d
bridge_probe $N $R $B97
BSC_ONE_T=0x26ca2A92ea9659e073621bb85b9117Dc5944Cff1; BSC_USDT_T=0xBB188503c32FA7F61E019c3b3Ae61ae6B0b3B492
pairs $N $R $B97 612044
tok $N $R $BSC_ONE_T; tok $N $R $BSC_USDT_T
p $N $R "getTokenPair(612044,ONE)" $B97 "getTokenPair(uint256,address)($PAIR_T)" 612044 $BSC_ONE_T
p $N $R "getTokenPair(612044,tUSDt)" $B97 "getTokenPair(uint256,address)($PAIR_T)" 612044 $BSC_USDT_T
fee $N $R $B97 612044 $BSC_ONE_T 10000000000000000000
fee $N $R $B97 612044 $BSC_USDT_T 10000000000000000000

# ================================================================ HyperEVM testnet 998
N=hyperevm-testnet; R=$RPC_HYPE_TEST; B998=0xeF7F79dA2281704647306267E7E1C72848768c22; H_TOK_T=0xDD937e58A0fc5094CB9677Cf37754c58602166bB
bridge_probe $N $R $B998
pairs $N $R $B998 612044
p $N $R "getTokenPair(612044,ONE)" $B998 "getTokenPair(uint256,address)($PAIR_T)" 612044 $H_TOK_T
tok $N $R $H_TOK_T
for f in "isCoreTokenIndexSet()(bool)" "coreUnit()(uint256)" "coreSystemAddress()(address)" "coreTransferableAmount(uint256)(uint256)"; do
  if [ "$f" = "coreTransferableAmount(uint256)(uint256)" ]; then p $N $R "token.$f" $H_TOK_T "$f" 1000000000000000042; else p $N $R "token.$f" $H_TOK_T "$f"; fi
done
fee $N $R $B998 612044 $H_TOK_T 10000000000000000000
EXEC_H=$(cast call $B998 "bridgeExecutor()(address)" --rpc-url $R)
exec_probe $N $R $B998 $H_TOK_T
p $N $R "executor.isWhitelistedMethod(ONE,transferToCoreFor 0x9438568d)" $EXEC_H "isWhitelistedMethod(address,bytes4)(bool)" $H_TOK_T 0x9438568d
echo "selector transferToCoreFor => $(cast sig 'transferToCoreFor(address,uint256)')"
echo "selector bridgeTokenForwarded => $(cast sig 'bridgeTokenForwarded(uint256,address,uint256,uint256,uint256,bytes)')"
