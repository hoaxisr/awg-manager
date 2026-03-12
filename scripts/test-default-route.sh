#!/bin/sh
# Test: verify forced restart recovers a DEAD tunnel
#
# Simulates real DEAD state by blocking the WireGuard endpoint with iptables.
# PingCheck detects connectivity loss → marks DEAD → forced restart fires →
# stopInternal + startInternal → tunnel recovers.
#
# Timeline (with default settings interval=45s, threshold=3, deadInterval=120s):
#   ~135s to mark DEAD + ~120s to forced restart + ~15s verify = ~4-5 min total
#
# After test, verifies pingcheck logs:
#   - failCount increments correctly (no 0/3 on FAIL)
#   - grace period shows "grace" stateChange (if tunnel was freshly started)
#   - forced_restart / alive entries present
#
# Usage: ./test-default-route.sh

IFACE="opkgtun10"
NDMS_NAME="OpkgTun10"
TUNNEL_ID="awg10"
API="http://localhost:2222/api"
BOLD="\033[1m"
GREEN="\033[32m"
RED="\033[31m"
YELLOW="\033[33m"
CYAN="\033[36m"
RESET="\033[0m"

ENDPOINT_IP=""
IPTABLES_ADDED=false

pass() { printf "${GREEN}✓ PASS${RESET}: %s\n" "$1"; }
fail() { printf "${RED}✗ FAIL${RESET}: %s\n" "$1"; }
info() { printf "${BOLD}→ %s${RESET}\n" "$1"; }
warn() { printf "${YELLOW}⚠ %s${RESET}\n" "$1"; }
timer() { printf "${CYAN}  ⏱ %s${RESET}\n" "$1"; }
separator() { echo "────────────────────────────────────────"; }

cleanup() {
    if [ "$IPTABLES_ADDED" = "true" ] && [ -n "$ENDPOINT_IP" ]; then
        iptables -D OUTPUT -d "$ENDPOINT_IP" -p udp -j DROP 2>/dev/null
        info "Cleanup: iptables rule removed"
    fi
}
trap cleanup EXIT INT TERM

check_connectivity() {
    /opt/bin/curl -s -o /dev/null -w "%{http_code}" --max-time 5 --interface "$IFACE" \
        "http://connectivitycheck.gstatic.com/generate_204" 2>/dev/null
}

get_tunnel_json() {
    curl -s "$API/tunnels/$TUNNEL_ID" 2>/dev/null
}

is_dead_by_monitoring() {
    get_tunnel_json | grep -q '"isDeadByMonitoring" *: *true'
}

get_status() {
    get_tunnel_json | grep -o '"status" *: *"[^"]*"' | head -1 | sed 's/"//g; s/status *: *//'
}

show_tunnel_info() {
    JSON=$(get_tunnel_json)
    STATUS=$(echo "$JSON" | grep -o '"status" *: *"[^"]*"' | head -1)
    DEAD=$(echo "$JSON" | grep -o '"isDeadByMonitoring" *: *[a-z]*' | head -1)
    echo "  API: $STATUS, $DEAD"
    if awg show "$IFACE" >/dev/null 2>&1; then
        echo "  WireGuard: active"
        HANDSHAKE=$(awg show "$IFACE" 2>/dev/null | grep "latest handshake" | sed 's/.*: //')
        [ -n "$HANDSHAKE" ] && echo "  Handshake: $HANDSHAKE"
    else
        echo "  WireGuard: inactive"
    fi
    ndmc -c "show interface $NDMS_NAME" 2>/dev/null | grep -E "(state|link|conf)" | sed 's/^/    /'
}

echo ""
echo "╔══════════════════════════════════════════════╗"
echo "║  Forced Restart Test (real DEAD simulation)  ║"
echo "╚══════════════════════════════════════════════╝"
echo ""

# ─── Step 1: Pre-checks ───
info "Step 1: Pre-checks"

if ! curl -s "$API/tunnels" >/dev/null 2>&1; then
    fail "awg-manager API not responding at $API"
    exit 1
fi
pass "API OK"

if ! awg show "$IFACE" >/dev/null 2>&1; then
    fail "Tunnel $IFACE not running — start it first"
    exit 1
fi
pass "Tunnel running"

CODE=$(check_connectivity)
if [ "$CODE" = "204" ]; then
    pass "Connectivity OK (HTTP 204)"
else
    fail "No connectivity through tunnel (HTTP $CODE) — fix before testing"
    exit 1
fi

# Get endpoint IP
ENDPOINT_IP=$(awg show "$IFACE" endpoints 2>/dev/null | awk '{print $2}' | cut -d: -f1)
if [ -z "$ENDPOINT_IP" ]; then
    fail "Cannot determine endpoint IP from awg show"
    exit 1
fi
pass "Endpoint: $ENDPOINT_IP"

show_tunnel_info
separator

# ─── Step 2: Block endpoint (simulate connectivity loss) ───
info "Step 2: Blocking endpoint $ENDPOINT_IP (iptables DROP)"
iptables -I OUTPUT -d "$ENDPOINT_IP" -p udp -j DROP
IPTABLES_ADDED=true
pass "iptables rule added — tunnel should lose connectivity"

CODE=$(check_connectivity)
if [ "$CODE" = "204" ]; then
    warn "Connectivity still works immediately after block?! Waiting..."
else
    pass "Connectivity lost (HTTP $CODE)"
fi
separator

# ─── Step 3: Wait for PingCheck to mark DEAD ───
info "Step 3: Waiting for PingCheck to mark tunnel DEAD..."
echo "  (default: 3 failures × 45s interval = ~135s)"
echo ""

TIMEOUT=300
ELAPSED=0
while [ $ELAPSED -lt $TIMEOUT ]; do
    if is_dead_by_monitoring; then
        echo ""
        pass "Tunnel marked DEAD after ${ELAPSED}s"
        show_tunnel_info
        break
    fi
    sleep 10
    ELAPSED=$((ELAPSED + 10))
    timer "Waiting... ${ELAPSED}s / ${TIMEOUT}s (status: $(get_status))"
done

if ! is_dead_by_monitoring; then
    fail "Tunnel not marked DEAD within ${TIMEOUT}s"
    exit 1
fi
separator

# ─── Step 4: Unblock endpoint ───
info "Step 4: Unblocking endpoint (remove iptables rule)"
iptables -D OUTPUT -d "$ENDPOINT_IP" -p udp -j DROP
IPTABLES_ADDED=false
pass "iptables rule removed — forced restart should now succeed"
separator

# ─── Step 5: Wait for forced restart + recovery ───
info "Step 5: Waiting for forced restart to recover the tunnel..."
echo "  (default: deadInterval=120s, then stopInternal + startInternal)"
echo ""

TIMEOUT=600
ELAPSED=0
RECOVERED=false
while [ $ELAPSED -lt $TIMEOUT ]; do
    # Check if tunnel recovered (not dead + connectivity works)
    if ! is_dead_by_monitoring; then
        CODE=$(check_connectivity)
        if [ "$CODE" = "204" ]; then
            echo ""
            pass "Tunnel RECOVERED after ${ELAPSED}s!"
            RECOVERED=true
            break
        fi
    fi
    sleep 10
    ELAPSED=$((ELAPSED + 10))
    DEAD_FLAG=""
    is_dead_by_monitoring && DEAD_FLAG=" [DEAD]"
    timer "Waiting... ${ELAPSED}s / ${TIMEOUT}s (status: $(get_status)${DEAD_FLAG})"
done

if [ "$RECOVERED" = "true" ]; then
    pass "Forced restart works! stopInternal + startInternal recovered the tunnel."
else
    fail "Tunnel did NOT recover within ${TIMEOUT}s"
fi
separator

# ─── Step 6: Verify pingcheck logs ───
info "Step 6: Checking pingcheck logs for correct behavior"

LOGS=$(curl -s "$API/pingcheck/logs" 2>/dev/null)
TUNNEL_LOGS=$(echo "$LOGS" | python3 -c "
import json, sys
data = json.load(sys.stdin)
logs = data if isinstance(data, list) else data.get('logs', [])
for l in logs:
    if l.get('tunnelId') == '$TUNNEL_ID':
        fc = l.get('failCount', 0)
        th = l.get('threshold', 3)
        sc = l.get('stateChange', '')
        ok = l.get('success', False)
        result = 'OK' if ok else 'FAIL'
        if sc == 'dead': result = 'DEAD'
        elif sc == 'alive': result = 'RECOVERED'
        elif sc == 'forced_restart': result = 'RESTART OK' if ok else 'RESTART FAIL'
        elif sc == 'grace': result = 'FAIL (grace)'
        ts = l.get('timestamp', '')[:19]
        print(f'{ts}  {result:15s}  {fc}/{th}  {sc}')
" 2>/dev/null)

if [ -n "$TUNNEL_LOGS" ]; then
    echo "$TUNNEL_LOGS" | head -20
    echo ""

    # Check: no FAIL with 0/failCount (grace period bug)
    BAD_ZERO=$(echo "$TUNNEL_LOGS" | grep "FAIL " | grep " 0/" | grep -v "grace")
    if [ -n "$BAD_ZERO" ]; then
        fail "Found FAIL entries with 0/N counter (grace period bug still present)"
        echo "$BAD_ZERO"
    else
        pass "No FAIL 0/N entries — counter is correct"
    fi

    # Check: DEAD entry exists
    if echo "$TUNNEL_LOGS" | grep -q "DEAD"; then
        pass "DEAD state change logged"
    else
        fail "No DEAD entry in logs"
    fi

    # Check: forced_restart or alive entry exists (recovery)
    if echo "$TUNNEL_LOGS" | grep -qE "RESTART|RECOVERED"; then
        pass "Recovery entry logged (forced_restart or alive)"
    else
        warn "No recovery entry in logs yet (may need more time)"
    fi
else
    warn "Could not parse pingcheck logs (python3 missing?)"
fi
separator

# ─── Final state ───
echo ""
info "Final state:"
show_tunnel_info
echo ""
