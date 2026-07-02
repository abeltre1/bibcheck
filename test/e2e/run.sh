#!/usr/bin/env bash
# Copyright 2025 National Technology and Engineering Solutions of Sandia
# SPDX-License-Identifier: BSD-3-Clause
#
# End-to-end test of bibcheck's parallel analysis pipeline and MCP server,
# driven by a mock OpenRouter-compatible LLM (test/e2e/mockllm) and a fixture
# document whose bibliography holds real convergence-computing citations
# (test/e2e/convergence_computing.txt).
#
# Asserts:
#   1. --concurrency 1 and --concurrency 8 produce byte-identical JSON output
#      (parallelism does not change results or their order).
#   2. The mock LLM observes at most 1 in-flight request when --concurrency=1,
#      and >1 but <= the bound when --concurrency=8 (the pool works and is
#      bounded).
#   3. The MCP server (bibcheck mcp) returns the same 12 entries, in order,
#      for the check_bibliography tool.
#
# External metadata sources (doi.org, Crossref, ...) are NOT mocked; in a
# network-restricted environment those lookups fail identically in every run,
# which the byte-identical-output assertion tolerates by design.
set -euo pipefail

cd "$(dirname "$0")/../.."
WORK="$(mktemp -d)"
trap 'kill $MOCK_PID 2>/dev/null || true; rm -rf "$WORK"' EXIT

MOCK_ADDR="localhost:9811"
export OPENROUTER_API_KEY="sk-mock-e2e"
export OPENROUTER_BASE_URL="http://$MOCK_ADDR"
unset SHIRTY_API_KEY 2>/dev/null || true

echo "== build =="
go build -o "$WORK/bibcheck" .
go build -o "$WORK/mockllm" ./test/e2e/mockllm
go run ./test/e2e/genpdf test/e2e/convergence_computing.txt "$WORK/convergence_computing.pdf"

echo "== start mock LLM =="
"$WORK/mockllm" --addr "$MOCK_ADDR" >"$WORK/mockllm.log" 2>&1 &
MOCK_PID=$!
for _ in $(seq 1 50); do
    curl -sf "http://$MOCK_ADDR/stats" >/dev/null 2>&1 && break
    sleep 0.1
done
curl -sf "http://$MOCK_ADDR/stats" >/dev/null

run_cli() { # $1=concurrency $2=output
    curl -sf -X POST "http://$MOCK_ADDR/stats/reset" >/dev/null
    local start end
    start=$(date +%s.%N)
    "$WORK/bibcheck" --format json --concurrency "$1" "$WORK/convergence_computing.pdf" \
        >"$2" 2>"$2.stderr"
    end=$(date +%s.%N)
    echo "$(echo "$end $start" | awk '{printf "%.1f", $1-$2}')s"
}

echo "== run: --concurrency 1 =="
T1=$(run_cli 1 "$WORK/serial.json")
S1=$(curl -sf "http://$MOCK_ADDR/stats")
echo "  wall $T1, mock stats: $S1"

echo "== run: --concurrency 8 =="
T8=$(run_cli 8 "$WORK/parallel.json")
S8=$(curl -sf "http://$MOCK_ADDR/stats")
echo "  wall $T8, mock stats: $S8"

echo "== assert: identical output regardless of concurrency =="
diff "$WORK/serial.json" "$WORK/parallel.json"
echo "  OK: byte-identical JSON"

echo "== assert: concurrency observed at the LLM =="
python3 - "$S1" "$S8" <<'EOF'
import json, sys
serial, parallel = json.loads(sys.argv[1]), json.loads(sys.argv[2])
assert serial["max_in_flight"] == 1, f"serial run overlapped: {serial}"
assert 2 <= parallel["max_in_flight"] <= 8, f"parallel bound violated: {parallel}"
print(f"  OK: serial max_in_flight=1, parallel max_in_flight={parallel['max_in_flight']} (bound 8)")
EOF

echo "== assert: analyzed entries =="
python3 - "$WORK/serial.json" <<'EOF'
import json, sys
doc = json.load(open(sys.argv[1]))
entries = doc["entries"]
assert doc["total_entries"] == 12, doc["total_entries"]
assert [e["number"] for e in entries] == list(range(1, 13)), "entries out of order"
assert all(e["original_text"] for e in entries), "missing entry text"
assert "Edge computing: Vision and challenges" in entries[0]["original_text"]
assert "MobileNets" in entries[7]["original_text"]
print("  OK: 12 entries, in order, with extracted text")
EOF

echo "== MCP server: check_bibliography over stdio =="
curl -sf -X POST "http://$MOCK_ADDR/stats/reset" >/dev/null
python3 - "$WORK/bibcheck" "$WORK/convergence_computing.pdf" <<'EOF'
import json, subprocess, sys

proc = subprocess.Popen([sys.argv[1], "mcp"], stdin=subprocess.PIPE,
                        stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)

def send(msg):
    proc.stdin.write((json.dumps(msg) + "\n").encode())
    proc.stdin.flush()

def recv(want_id):
    while True:
        line = proc.stdout.readline()
        if not line:
            raise SystemExit("MCP server closed stdout")
        msg = json.loads(line)
        if msg.get("id") == want_id:
            return msg

send({"jsonrpc": "2.0", "id": 1, "method": "initialize",
      "params": {"protocolVersion": "2025-06-18", "capabilities": {},
                 "clientInfo": {"name": "e2e", "version": "0"}}})
recv(1)
send({"jsonrpc": "2.0", "method": "notifications/initialized"})
send({"jsonrpc": "2.0", "id": 2, "method": "tools/call",
      "params": {"name": "check_bibliography",
                 "arguments": {"pdf_path": sys.argv[2], "concurrency": 6}}})
result = recv(2)["result"]
assert not result.get("isError"), result
doc = result["structuredContent"]
assert doc["total_entries"] == 12, doc["total_entries"]
assert [e["number"] for e in doc["entries"]] == list(range(1, 13))
print(f"  OK: MCP returned {doc['total_entries']} entries, in order")

send({"jsonrpc": "2.0", "id": 3, "method": "tools/call",
      "params": {"name": "check_entry",
                 "arguments": {"text": doc["entries"][0]["original_text"]}}})
result = recv(3)["result"]
assert not result.get("isError"), result
single = result["structuredContent"]
assert single["total_entries"] == 1
print("  OK: MCP check_entry analyzed a single citation")

proc.stdin.close()
proc.wait(timeout=10)
EOF
SMCP=$(curl -sf "http://$MOCK_ADDR/stats")
python3 -c "
import json, sys
s = json.loads('$SMCP')
assert 2 <= s['max_in_flight'] <= 6, s
print(f\"  OK: MCP run parallel too (max_in_flight={s['max_in_flight']}, bound 6)\")
"

echo
echo "E2E PASS (serial $T1 vs parallel $T8)"
