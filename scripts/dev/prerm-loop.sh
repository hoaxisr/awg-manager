#!/bin/sh
# Проверка prerm (#854, не для CI — нужны user namespaces): run entware/control/prerm inside a user namespace with a
# throwaway dir bind-mounted over /opt, pre-seeded with the files the issue lists,
# then report which leftovers survived. Exit 1 = red (leftovers), 0 = green.
set -u
REPO=${1:-$(cd "$(dirname "$0")/../.." && pwd)}
FAKE=$(mktemp -d)
mkdir -p "$FAKE/etc/ndm/netfilter.d" "$FAKE/tmp" "$FAKE/var/run/awg-manager" "$FAKE/var/lock/awg-manager" "$FAKE/etc/awg-manager"
for h in 50-awgm-tproxy 51-awgm-tunnel-fw 52-awgm-policytun-dns 61-awgm-wdtt-forward 62-awgm-listen-ports; do echo '#!/bin/sh' > "$FAKE/etc/ndm/netfilter.d/$h.sh"; done
: > "$FAKE/tmp/awg-manager-upgrade.log"; : > "$FAKE/tmp/awg-manager-stderr.log"; : > "$FAKE/tmp/awg-manager_9.9.9_mipsel-3.4-kn.ipk"
: > "$FAKE/etc/awg-manager/settings.json"; : > "$FAKE/etc/awg-manager/tunnel-fw.list"
unshare -Urm sh -c "mount --bind '$FAKE' /opt && sh '$REPO/entware/control/prerm' remove" >/dev/null 2>&1
rc=0
check() { [ -e "/dev/null$1" ] 2>/dev/null; if [ -e "$FAKE$1" ]; then echo "LEFT  $1"; rc=1; else echo "gone  $1"; fi; }
for p in /etc/ndm/netfilter.d/50-awgm-tproxy.sh /etc/ndm/netfilter.d/51-awgm-tunnel-fw.sh /etc/ndm/netfilter.d/52-awgm-policytun-dns.sh /etc/ndm/netfilter.d/61-awgm-wdtt-forward.sh /etc/ndm/netfilter.d/62-awgm-listen-ports.sh /tmp/awg-manager-upgrade.log /tmp/awg-manager-stderr.log /tmp/awg-manager_9.9.9_mipsel-3.4-kn.ipk /var/run/awg-manager /var/lock/awg-manager /etc/awg-manager/tunnel-fw.list; do check "$p"; done
[ -e "$FAKE/etc/awg-manager/settings.json" ] && echo "kept  /etc/awg-manager/settings.json (expected)" || { echo "WRONG settings.json deleted"; rc=1; }
rm -rf "$FAKE"; exit $rc
