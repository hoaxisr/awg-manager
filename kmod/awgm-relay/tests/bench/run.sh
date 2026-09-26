#!/bin/sh
# Прогон на роутере: одно направление c2s через релей на loopback.
# usage: run.sh process|kernel PPS   (PPS=0 — без лимита, потолок)
# process: /tmp/obfb (Phobos, threads=2) слушает 39095 -> 127.0.0.1:51950
# kernel:  слот awgm_relay 39095 -> 127.0.0.1:51950 (модуль загружен заранее)
# CPU: общий (/proc/stat, busy/total) и отдельно релея — процесс по pid,
# модуль по kthread'ам awgmr_*; сравнивать общий (спека §7.4).
MODE=$1; PPS=$2
cpu(){ awk '/^cpu /{print $2+$3+$4+$6+$7+$8, $5}' /proc/stat; }
ticks(){ if [ "$MODE" = process ]; then awk '{print $14+$15}' /proc/$(pidof obfb)/stat;
         else t=0; for p in $(pgrep awgmr_); do t=$((t + $(awk '{print $14+$15}' /proc/$p/stat))); done; echo $t; fi; }
if [ "$MODE" = process ]; then
  printf "[main]\nsource-if = 127.0.0.1\nsource-lport = 39095\ntarget = 127.0.0.1:51950\nkey = benchkey-0123456789\nmasking = NONE\nmax-dummy = 4\nthreads = 2\nverbose = ERROR\n" > /tmp/rb.conf
  start-stop-daemon -S -b -x /tmp/obfb -- --config /tmp/rb.conf; sleep 1
else
  echo "127.0.0.1:39095 127.0.0.1:51950 transform=phobos key=$(printf %s benchkey-0123456789 | hexdump -ve '1/1 "%02x"') masking=none max-dummy=4" > /proc/awgm_relay/add
fi
set -- $(cpu); B0=$1; I0=$2; T0=$(ticks)
R=$(/tmp/relaybench 39095 51950 10 1412 $PPS)
set -- $(cpu); B1=$1; I1=$2; T1=$(ticks)
echo "$MODE pps=$PPS $R cpu_total=$(( 100*(B1-B0)/((B1-B0)+(I1-I0)) ))% relay=$(( (T1-T0)*10/105 ))%core"
if [ "$MODE" = process ]; then killall obfb; while pidof obfb >/dev/null; do sleep 1; done; else echo 127.0.0.1:39095 > /proc/awgm_relay/del; fi
