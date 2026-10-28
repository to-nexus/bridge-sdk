#!/usr/bin/env python3
"""Assertions for one fork-smoke case. Exit 0 only when EVERY assertion holds; prints one line per assertion.

usage: smoke_assert.py <example-output-file> <example-exit-code> <local-rpc> <source-bridge> <erc20:0|1> <balance-before> <balance-after> <allowance-after> <expectation> <account-nonce-after>

<expectation> is `send-success` or `preflight-refused:CODE[,CODE]` (given by fork-smoke.sh for the case).
 - send-success: the example sent the transfer and every on-chain assertion below holds.
 - preflight-refused:<codes>: the example stopped on PreflightError (exit code 2) with EXACTLY those codes at stage pre-approve, printed no
   broadcast / signature line, and NOTHING reached the fork: account nonce 0 (no approve, no bridge tx), balance unchanged, allowance 0.
"""
import json, re, subprocess, sys

out_file, rc, rpc, bridge, erc20, bal_before, bal_after, allow_after, expect, nonce_after = sys.argv[1:11]
text = open(out_file, encoding="utf-8", errors="replace").read()
fails = []


def chk(ok, msg):
    print(("  ok   " if ok else "  FAIL ") + msg)
    if not ok:
        fails.append(msg)


def cast(*a):
    return subprocess.run(["cast", *a], capture_output=True, text=True)


if expect.startswith("preflight-refused:"):
    want = sorted(set(expect.split(":", 1)[1].split(",")))
    chk(rc == "2", f"example exit code is 2 = refused by PreflightError (got {rc})")
    res = [l for l in text.splitlines() if l.startswith("RESULT ") and "preflight-refused:" in l and "stage=" in l]
    chk(len(res) == 1, "the example printed a send-time RESULT ... preflight-refused:<codes> stage=<stage> line")
    if res:
        got = sorted(set(re.search(r"preflight-refused:([A-Za-z_,]+)", res[0]).group(1).split(",")))
        chk(got == want, f"PreflightError codes {got} == expected {want}")
        chk("stage=pre-approve" in res[0], "refused at stage pre-approve (before any approve)")
    chk(re.search(r"broadcast 0x[0-9a-fA-F]{64}", text) is None, "no broadcast line was printed")
    chk("signed, persist before broadcast" not in text, "nothing was signed for the bridge tx")
    chk(int(nonce_after) == 0, f"nothing reached the fork: account nonce {nonce_after} == 0 (no approve, no bridge tx)")
    if erc20 == "1":
        chk(int(bal_before) == int(bal_after), f"ERC20 balance unchanged ({bal_before} -> {bal_after})")
        chk(int(allow_after) == 0, f"no allowance was granted (allowance after = {allow_after})")
    sys.exit(1 if fails else 0)
chk(expect == "send-success", f"expectation is send-success (got {expect})")
chk(rc == "0", f"example exit code is 0 (got {rc})")
m = re.search(r"broadcast (0x[0-9a-fA-F]{64})", text)
chk(m is not None, "the example printed a source tx hash")
handle = None
for line in text.splitlines():
    if "TransferHandle (persist):" in line:
        try:
            handle = json.loads(line.split("TransferHandle (persist):", 1)[1].strip())
        except Exception as e:  # noqa: BLE001
            chk(False, f"printed TransferHandle is valid JSON ({e})")
chk(handle is not None, "the example printed a TransferHandle")
if not (m and handle):
    sys.exit(1)
h = m.group(1).lower()
chk(handle["srcTxHash"].lower() == h, f"TransferHandle.srcTxHash == printed hash ({h[:12]}…)")

r = cast("receipt", h, "--json", "--rpc-url", rpc)
chk(r.returncode == 0, "cast receipt succeeds on the fork")
rec = json.loads(r.stdout) if r.returncode == 0 else {"status": "0x0", "logs": []}
chk(int(rec["status"], 16) == 1, "receipt status == 1")
topic = cast("keccak", "BridgeInitiated(uint256,uint256,address,address,address,address,uint256,uint256,uint256,bytes,uint256)").stdout.strip().lower()
logs = [l for l in rec["logs"] if l["address"].lower() == bridge.lower() and l["topics"][0].lower() == topic]
chk(len(logs) >= 1, "receipt has >= 1 BridgeInitiated emitted by the source bridge")
if not logs:
    sys.exit(1)
ev = next((l for l in logs if int(l["topics"][2], 16) == int(handle["hop1"]["index"])), None)
chk(ev is not None, f"a BridgeInitiated log has indexed index == handle.hop1.index ({handle['hop1']['index']})")
if ev is None:
    sys.exit(1)
chk(int(ev["topics"][1], 16) == handle["hop1"]["destChainId"], "BridgeInitiated.toChainID == handle.hop1.destChainId")
words = [int(ev["data"][2 + 64 * i : 2 + 64 * (i + 1)], 16) for i in range(6)]
value, nf, ef = words[3], words[4], words[5]
total = value + nf + ef
if erc20 == "1":
    delta = int(bal_before) - int(bal_after)
    chk(delta == total, f"ERC20 balance delta {delta} == value+fees {total}")
    chk(int(allow_after) == 0, f"exact approve fully consumed (allowance after = {allow_after})")
else:
    t = cast("tx", h, "--json", "--rpc-url", rpc)
    tv = int(json.loads(t.stdout)["value"], 16) if t.returncode == 0 else -1
    chk(tv == total, f"native tx value {tv} == value+fees {total}")
sys.exit(1 if fails else 0)
