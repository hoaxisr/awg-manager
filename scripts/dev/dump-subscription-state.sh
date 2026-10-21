#!/usr/bin/env bash
# Dump subscription state: API view (what the UI list uses) vs router outbounds
# (what sb-router expert shows). Compare to on-disk files on the router:
#   /opt/etc/awg-manager/subscriptions.json
#   /opt/etc/awg-manager/singbox/config.d/13-subscriptions.json
#
# Usage:
#   API_KEY='your-key' ./scripts/dev/dump-subscription-state.sh
#   HOST=192.168.1.1:2222 API_KEY='...' ./scripts/dev/dump-subscription-state.sh

set -euo pipefail

HOST="${HOST:-192.168.1.1:2222}"
API_KEY="${API_KEY:-}"

auth=()
if [[ -n "$API_KEY" ]]; then
	auth=(-H "Authorization: Bearer ${API_KEY}")
fi

base="http://${HOST}/api"

echo "=== GET /singbox/subscriptions (subscriptions.json → UI tab) ==="
curl -sS "${auth[@]}" "${base}/singbox/subscriptions" | python3 -m json.tool 2>/dev/null || \
	curl -sS "${auth[@]}" "${base}/singbox/subscriptions"
echo

echo "=== GET /singbox/router/outbounds/list (13-subscriptions slot composites) ==="
curl -sS "${auth[@]}" "${base}/singbox/router/outbounds/list" | python3 -m json.tool 2>/dev/null || \
	curl -sS "${auth[@]}" "${base}/singbox/router/outbounds/list"
echo

echo "=== On router (SSH) ==="
echo "  cat /opt/etc/awg-manager/subscriptions.json"
echo "  cat /opt/etc/awg-manager/singbox/config.d/13-subscriptions.json"
echo "  ls -la /opt/etc/awg-manager/singbox/config.d/disabled/ 2>/dev/null || true"
