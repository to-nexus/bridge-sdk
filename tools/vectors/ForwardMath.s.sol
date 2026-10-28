// SPDX-License-Identifier: MIT
pragma solidity ^0.8.28;

// Golden-vector generator for the multihop (R5/R6) hub re-settlement math.
//
// Runs on a LOCAL EVM only (forge script, no RPC, nothing is sent anywhere):
//   tools/vectors/build-vectors.mjs copies this file into a bridge-contracts checkout (BRIDGE_CONTRACTS_DIR) and runs it with `forge script`
// Every line starting with "VEC " is one JSON object; tools/vectors/build-vectors.mjs collects them
// into vectors.json. The numbers come from the REAL contracts:
//   - CrossBridge._resolveForwardAmounts (via the harness below)  -> hop-2 settlement on the hub
//   - BridgeVerifier.calculateFee / getTokenConfig                -> fee quotes + the post-settlement check

import {Script, console} from "forge-std/Script.sol";

import {BridgeVerifier} from "src/BridgeVerifier.sol";
import {CrossBridge} from "src/CrossBridge.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";

/// Exposes the internal pure settlement function of the hub bridge.
contract ForwardAmountsHarness is CrossBridge {
    function resolveForwardAmounts(uint ctxValue, uint quotedValue, uint networkFee, uint rate, uint denom)
        external
        pure
        returns (uint value_, uint exFee_)
    {
        return _resolveForwardAmounts(ctxValue, quotedValue, networkFee, rate, denom);
    }
}

/// Minimal price feed: every token and every chain's native token costs 1 USD (18 decimals), so
/// networkFee = finalizeBridgeGas * gasPrice and minimumValue = minimumTokenValue.
contract FlatPriceFeed {
    function getNativeTokenPrice(uint) external view returns (bool, uint, uint) {
        return (true, 1e18, block.timestamp);
    }

    function getTokenPriceInDollars(address) external view returns (bool, uint, uint) {
        return (true, 1e18, block.timestamp);
    }
}

contract ForwardMathVectors is Script {
    address internal constant NATIVE = address(1);
    uint internal constant FINALIZE_GAS = 100_000;
    bytes32 internal constant EDITOR = keccak256("EDITOR_ROLE");
    bytes32 internal constant PRICER = keccak256("PRICER_ROLE");

    ForwardAmountsHarness internal harness;
    FlatPriceFeed internal feed;

    struct Case {
        string name;
        string route; // "R5" (BSC->ONE->HyperEVM) / "R6" (HyperEVM->ONE->BSC)
        uint destChainId; // hop-2 destination chain (hub verifier key)
        uint v2; // minimum delivered on the destination
        uint hubNf; // hub networkFee at quote time
        uint hubRate; // hub exFeeRate (bps) at quote time
        uint hubMin; // hub minimumValue at quote time
        uint buffer; // hop2FeeBuffer, placed in the declared exFee term
        uint srcNf; // source (hop-1) networkFee
        uint srcRate; // source exFeeRate (bps)
        uint execNf; // hub networkFee at hub execution time
        uint execRate; // hub exFeeRate at execution time
        uint execMin; // hub minimumValue at execution time
    }

    function run() external {
        harness = new ForwardAmountsHarness();
        feed = new FlatPriceFeed();

        _rawCases();

        Case[] memory cs = new Case[](16);
        uint n;
        cs[n++] = Case("r5-baseline-unchanged", "R5", 999, 10e18, 0.18e18, 0, 5e18, 0, 0, 0, 0.18e18, 0, 5e18);
        cs[n++] = Case("r5-nf-down", "R5", 999, 10e18, 0.18e18, 0, 5e18, 0, 0, 0, 0.1e18, 0, 5e18);
        cs[n++] = Case("r5-nf-up-no-buffer-reverts", "R5", 999, 10e18, 0.18e18, 0, 5e18, 0, 0, 0, 0.2e18, 0, 5e18);
        cs[n++] = Case("r5-nf-up-covered-by-buffer", "R5", 999, 10e18, 0.18e18, 0, 5e18, 0.05e18, 0, 0, 0.2e18, 0, 5e18);
        cs[n++] = Case("r5-buffer-exactly-consumed", "R5", 999, 10e18, 0.18e18, 0, 5e18, 0.05e18, 0, 0, 0.23e18, 0, 5e18);
        cs[n++] = Case("r5-buffer-exceeded-minimally", "R5", 999, 10e18, 0.18e18, 0, 5e18, 0.05e18, 0, 0, 0.23e18 + FINALIZE_GAS, 0, 5e18);
        cs[n++] = Case("r5-rate30-unchanged", "R5", 999, 10e18, 0.18e18, 30, 5e18, 0, 0, 0, 0.18e18, 30, 5e18);
        cs[n++] = Case("r5-rate-up-30-to-50-no-buffer", "R5", 999, 10e18, 0.18e18, 30, 5e18, 0, 0, 0, 0.18e18, 50, 5e18);
        cs[n++] = Case("r5-rate-up-30-to-50-with-buffer", "R5", 999, 10e18, 0.18e18, 30, 5e18, 0.03e18, 0, 0, 0.18e18, 50, 5e18);
        cs[n++] = Case("r5-rate-down-30-to-0", "R5", 999, 10e18, 0.18e18, 30, 5e18, 0, 0, 0, 0.18e18, 0, 5e18);
        cs[n++] = Case("r5-rounding-odd-amount-rate33", "R5", 999, 5000000000000000042, 0.18e18, 33, 5e18, 0, 0, 0, 0.18e18, 33, 5e18);
        cs[n++] = Case("r5-v2-equals-minimum-unchanged", "R5", 999, 5e18, 0.18e18, 0, 5e18, 0, 0, 0, 0.18e18, 0, 5e18);
        cs[n++] = Case("r5-v2-equals-minimum-min-rises", "R5", 999, 5e18, 0.18e18, 0, 5e18, 0, 0, 0, 0.18e18, 0, 5.1e18);
        cs[n++] = Case("r6-baseline-src-fees", "R6", 56, 10e18, 0.18e18, 0, 5e18, 0, 0.01e18, 10, 0.18e18, 0, 5e18);
        cs[n++] = Case("r6-nf-up-covered-rate-up", "R6", 56, 7e18, 0.18e18, 20, 5e18, 0.1e18, 0, 0, 0.25e18, 35, 5e18);
        cs[n++] = Case("r6-nf-down-rate30", "R6", 56, 7e18, 0.18e18, 30, 5e18, 0, 0, 0, 0.05e18, 30, 5e18);
        for (uint i = 0; i < n; i++) _scenario(cs[i]);
    }

    // -------------------------------------------------------------- raw _resolveForwardAmounts cases
    function _rawCases() internal view {
        _raw("raw-exact-no-fee", 10.18e18, 10e18, 0.18e18, 0);
        _raw("raw-rate30-exact-inverse", 10.21e18, 10e18, 0.18e18, 30);
        _raw("raw-floor-wins", 10.2e18, 10e18, 0.18e18, 50);
        _raw("raw-quoted-plus-nf-eq-ctx", 10.18e18, 10e18, 0.18e18, 30);
        _raw("raw-staged-insufficient", 10.17e18, 10e18, 0.18e18, 0);
        _raw("raw-rounding-1wei", 1000000000000000001, 1000000000000000000, 0, 10000);
        _raw("raw-large", 123456789012345678901234567, 100000000000000000000000000, 1e18, 77);
        _raw("raw-zero-quoted", 5e18, 0, 0.18e18, 30);
    }

    function _raw(string memory name, uint ctx, uint quoted, uint nf, uint rate) internal view {
        string memory head = string.concat(
            '{"kind":"raw","name":"', name, '","ctxValue":"', vm.toString(ctx), '","quotedValue":"', vm.toString(quoted),
            '","networkFee":"', vm.toString(nf), '","rate":"', vm.toString(rate), '","denominator":"10000",'
        );
        try harness.resolveForwardAmounts(ctx, quoted, nf, rate, 10000) returns (uint v, uint e) {
            console.log(string.concat("VEC ", head, '"reverted":false,"value":"', vm.toString(v), '","exFee":"', vm.toString(e), '"}'));
        } catch (bytes memory err) {
            console.log(string.concat("VEC ", head, '"reverted":true,"selector":"', vm.toString(bytes4(err)), '"}'));
        }
    }

    // -------------------------------------------------------------- full multihop scenarios
    function _verifier(uint nf, uint rate, uint min, uint destChainId) internal returns (BridgeVerifier v) {
        v = new BridgeVerifier(address(this), address(this), address(feed), FINALIZE_GAS, 1e18, 0, min, 0, 0, 0);
        v.grantRole(EDITOR, address(this));
        v.grantRole(PRICER, address(this));
        v.updateGasPrice(destChainId, nf / FINALIZE_GAS);
        if (rate != 0) v.setExFeeRate(IERC20(NATIVE), rate);
    }

    function _scenario(Case memory c) internal {
        // quote time
        BridgeVerifier hubV = _verifier(c.hubNf, c.hubRate, c.hubMin, c.destChainId);
        BridgeVerifier srcV = _verifier(c.srcNf, c.srcRate, 5e18, 612055);

        (uint mv2, uint nf2, uint ef2) = hubV.calculateFee(c.destChainId, IERC20(NATIVE), c.v2);
        uint ef2p = ef2 + c.buffer;
        uint v1 = c.v2 + nf2 + ef2p;
        (uint mv1, uint nf1, uint ef1) = srcV.calculateFee(612055, IERC20(NATIVE), v1);

        string memory q = string.concat(
            '"hubQuote":{"minimumValue":"', vm.toString(mv2), '","networkFee":"', vm.toString(nf2), '","exFee":"', vm.toString(ef2),
            '"},"ef2Declared":"', vm.toString(ef2p), '","v1":"', vm.toString(v1), '","srcQuote":{"minimumValue":"', vm.toString(mv1),
            '","networkFee":"', vm.toString(nf1), '","exFee":"', vm.toString(ef1), '"},"totalSpend":"', vm.toString(v1 + nf1 + ef1), '",'
        );

        // execution time on the hub: fee configuration may have moved
        hubV.updateGasPrice(c.destChainId, c.execNf / FINALIZE_GAS);
        hubV.setExFeeRate(IERC20(NATIVE), c.execRate == 0 ? type(uint).max : c.execRate); // max = exempt
        hubV.setMinimumTokenValue(c.execMin);
        (uint minX, uint nfX, uint rateX) = hubV.getTokenConfig(c.destChainId, IERC20(NATIVE));
        uint denom = hubV.denominator();

        string memory head = string.concat(
            '{"kind":"multihop","name":"', c.name, '","route":"', c.route, '","destChainId":', vm.toString(c.destChainId),
            ',"v2":"', vm.toString(c.v2), '","buffer":"', vm.toString(c.buffer), '",'
        );
        string memory ex = string.concat(
            '"exec":{"minimumValue":"', vm.toString(minX), '","networkFee":"', vm.toString(nfX), '","rate":"', vm.toString(rateX),
            '","denominator":"', vm.toString(denom), '"},'
        );

        string memory settle;
        try harness.resolveForwardAmounts(v1, c.v2, nfX, rateX, denom) returns (uint value_, uint exFee_) {
            (uint mvC, uint nfC, uint exC) = hubV.calculateFee(c.destChainId, IERC20(NATIVE), value_);
            bool ok = value_ >= mvC && value_ != 0 && nfX >= nfC && exFee_ >= exC;
            settle = string.concat(
                '"settle":{"reverted":false,"value":"', vm.toString(value_), '","exFee":"', vm.toString(exFee_),
                '"},"verifierCheck":{"ok":', ok ? "true" : "false", ',"minimumValue":"', vm.toString(mvC), '","networkFee":"',
                vm.toString(nfC), '","exFee":"', vm.toString(exC), '"}}'
            );
        } catch (bytes memory err) {
            settle = string.concat('"settle":{"reverted":true,"selector":"', vm.toString(bytes4(err)), '"}}');
        }
        console.log(string.concat("VEC ", head, q, ex, settle));
    }
}
