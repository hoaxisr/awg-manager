#!/bin/sh
set -u

MODEL_PATH="$1"
MODEL_NAME="$2"
WORK_DIR="${3:-/opt/storage/ai/bench/results}"
PORT="${4:-11436}"
LLAMA_SERVER="${LLAMA_SERVER:-/opt/bin/llama-server}"
PROMPT_DIR="${PROMPT_DIR:-/opt/storage/ai/bench/prompts}"
jinja_arg=""
[ "${ENABLE_JINJA:-0}" = "1" ] && jinja_arg="--jinja"
RUN_DIR="$WORK_DIR/$MODEL_NAME"

mkdir -p "$RUN_DIR"
rm -f "$RUN_DIR"/response-*.json "$RUN_DIR"/server.log "$RUN_DIR"/summary.txt

server_pid=""
monitor_pid=""
cleanup() {
	if [ -n "$monitor_pid" ]; then kill "$monitor_pid" 2>/dev/null || true; fi
	if [ -n "$server_pid" ]; then
		kill "$server_pid" 2>/dev/null || true
		wait "$server_pid" 2>/dev/null || true
	fi
}
trap cleanup EXIT INT TERM

before_kb=$(awk '/MemAvailable:/ {print $2}' /proc/meminfo)
start_s=$(date +%s)
if command -v taskset >/dev/null 2>&1; then
	taskset -c 1,2 "$LLAMA_SERVER" -m "$MODEL_PATH" -c 1536 -t 2 -b 64 --parallel 1 \
		--host 127.0.0.1 --port "$PORT" $jinja_arg >"$RUN_DIR/server.log" 2>&1 &
else
	"$LLAMA_SERVER" -m "$MODEL_PATH" -c 1536 -t 2 -b 64 --parallel 1 \
		--host 127.0.0.1 --port "$PORT" $jinja_arg >"$RUN_DIR/server.log" 2>&1 &
fi
server_pid=$!

max_rss_kb=0
min_available_kb=$before_kb
(
	while kill -0 "$server_pid" 2>/dev/null; do
		rss=$(awk '/VmRSS:/ {print $2}' "/proc/$server_pid/status" 2>/dev/null || echo 0)
		available=$(awk '/MemAvailable:/ {print $2}' /proc/meminfo)
		[ "$rss" -gt "$(cat "$RUN_DIR/max-rss-kb" 2>/dev/null || echo 0)" ] && echo "$rss" >"$RUN_DIR/max-rss-kb"
		[ "$available" -lt "$(cat "$RUN_DIR/min-available-kb" 2>/dev/null || echo "$before_kb")" ] && echo "$available" >"$RUN_DIR/min-available-kb"
		sleep 1
	done
) &
monitor_pid=$!

ready=0
i=0
while [ "$i" -lt 120 ]; do
	if ! kill -0 "$server_pid" 2>/dev/null; then break; fi
	if /opt/bin/curl -fsS --max-time 1 "http://127.0.0.1:$PORT/health" >/dev/null 2>&1; then
		ready=1
		break
	fi
	i=$((i + 1))
	sleep 1
done
load_s=$(( $(date +%s) - start_s ))

if [ "$ready" -ne 1 ]; then
	printf 'model=%s\nready=false\nload_seconds=%s\n' "$MODEL_NAME" "$load_s" >"$RUN_DIR/summary.txt"
	tail -n 40 "$RUN_DIR/server.log" >>"$RUN_DIR/summary.txt"
	exit 1
fi

n=1
for prompt in "$PROMPT_DIR"/prompt-*.json; do
	prompt_start=$(date +%s)
	/opt/bin/curl -fsS --max-time 300 -H 'Content-Type: application/json' \
		--data-binary "@$prompt" "http://127.0.0.1:$PORT/v1/chat/completions" \
		>"$RUN_DIR/response-$n.json" || echo '{"benchmarkError":"request failed"}' >"$RUN_DIR/response-$n.json"
	prompt_s=$(( $(date +%s) - prompt_start ))
	echo "$prompt_s" >"$RUN_DIR/response-$n-seconds"
	n=$((n + 1))
done

sleep 2
max_rss_kb=$(cat "$RUN_DIR/max-rss-kb" 2>/dev/null || echo 0)
min_available_kb=$(cat "$RUN_DIR/min-available-kb" 2>/dev/null || echo 0)
{
	printf 'model=%s\nready=true\nload_seconds=%s\npid=%s\n' "$MODEL_NAME" "$load_s" "$server_pid"
	printf 'mem_available_before_kb=%s\nmax_rss_kb=%s\nmin_mem_available_kb=%s\n' "$before_kb" "$max_rss_kb" "$min_available_kb"
	printf 'response_seconds='
	for seconds in "$RUN_DIR"/response-*-seconds; do printf '%s ' "$(cat "$seconds")"; done
	printf '\n'
} >"$RUN_DIR/summary.txt"

cleanup
server_pid=""
monitor_pid=""
sleep 2
{
	printf 'mem_available_after_kb=%s\n' "$(awk '/MemAvailable:/ {print $2}' /proc/meminfo)"
	printf 'awg_status='; /opt/etc/init.d/S99awg-manager status 2>&1 | head -n 1
	printf 'mihomo_pid=%s\n' "$(pidof mihomo 2>/dev/null || true)"
	printf 'singbox_pid=%s\n' "$(pidof sing-box 2>/dev/null || true)"
	printf 'https_health='; /opt/bin/curl -fsS --max-time 10 https://1.1.1.1/cdn-cgi/trace 2>/dev/null | head -n 1
} >>"$RUN_DIR/summary.txt"
cat "$RUN_DIR/summary.txt"
