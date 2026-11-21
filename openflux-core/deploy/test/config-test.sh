#!/bin/sh
# node-install.sh's config, its checks, the node.conf it writes and the .flux
# packages it installs, without a server: sourced without its command
# dispatch. $1: the installer; $2: a scratch directory. provision runs it
# (TestInstallerConfig).
set -u
lib="$2/lib.sh"
sed '/^case "\${1:-}" in/,$d' "$1" > "$lib"
. "$lib"
STATE_ROOT="$2/state"; CONF_ROOT="$2/conf"; NODE_USER=$(id -un)
chown() { :; }   # no node user here

pass=0; failn=0
ok() { pass=$((pass + 1)); }
bad() { failn=$((failn + 1)); printf 'FAIL: %s\n' "$1"; }

b64u() { printf '%s' "$1" | base64 | tr -d '\n=' | tr '/+' '_-'; }
KEY64=$(printf 'ab%.0s' $(seq 32))
PKG=$(printf 'PK-fake-flux' | base64 | tr -d '\n')

config='channel=t1
key='$KEY64'
port=40000
mailru=https://cloud.mail.ru/public/z5iM/XFh98DW3b
telemost=https://telemost.yandex.ru/j/88936358186517
avito=WyJhIiwiYiJd
script=mtslink,'$KEY64','$(b64u "https://my.mts-link.ru/j/a,b")',eyJ4IjoiMSJ9,'$PKG'
script=bitrix,'$KEY64',,,'$PKG'
'
read_config <<EOF
$config
EOF
CHANNEL=t1
( check_transports ) >/dev/null && ok || bad "a good config is refused"

conf=$(write_node_conf)
case "$conf" in *"[Transport telemost]"*"URL = https://telemost.yandex.ru/j/88936358186517"*) ok ;; *) bad "telemost section" ;; esac
case "$conf" in *"[Transport avito]"*"Priority = 75"*"URL = WyJhIiwiYiJd"*) ok ;; *) bad "avito section" ;; esac
case "$conf" in *"[Transport script]"*"Name = mtslink"*"Path = $STATE_ROOT/t1/scripts/mtslink.flux"*"URL = https://my.mts-link.ru/j/a,b"*"Params = eyJ4IjoiMSJ9"*) ok ;; *) bad "first script section: $conf" ;; esac
case "$conf" in *"[Transport script-2]"*"Name = bitrix"*) ok ;; *) bad "second script section" ;; esac
case "$conf" in *"ScriptAutoUpdate = yes"*) ok ;; *) bad "auto-update line" ;; esac
[ "$(session_context)" = "https://cloud.mail.ru/public/z5iM/XFh98DW3b" ] && ok || bad "context: mailru first"
names=$(transport_names)
[ "$names" = "Mail.ru Документ, Яндекс Телемост, Avito, JS: mtslink, JS: bitrix" ] && ok || bad "names: $names"

# The context falls to Telemost, then Avito, then the first script with a parameter.
MAILRU=""; [ "$(session_context)" = "https://telemost.yandex.ru/j/88936358186517" ] && ok || bad "context: telemost"
TELEMOST=""; [ "$(session_context)" = "WyJhIiwiYiJd" ] && ok || bad "context: avito"
AVITO=""; [ "$(session_context)" = "https://my.mts-link.ru/j/a,b" ] && ok || bad "context: script"

# Packages land in the state directory; a line without one keeps the file there.
mkdir -p "$STATE_ROOT/t1"
install_scripts && [ "$(cat "$STATE_ROOT/t1/scripts/mtslink.flux")" = "PK-fake-flux" ] && ok || bad "install: ${SCRIPT_ERROR:-}"
SCRIPTS='mtslink,'$KEY64',,,
'
install_scripts && ok || bad "a kept package: ${SCRIPT_ERROR:-}"
prune_scripts
[ -f "$STATE_ROOT/t1/scripts/mtslink.flux" ] && [ ! -f "$STATE_ROOT/t1/scripts/bitrix.flux" ] && ok || bad "prune"
SCRIPTS='other,'$KEY64',,,
'
install_scripts && bad "a missing package is accepted" || ok

# Bad lines are refused.
for line in 'BAD,'$KEY64',,,' 'x,nothex,,,' 'x,'$KEY64','$(b64u "a#b")',,' 'x,'$KEY64',,se;t,' 'x,'$KEY64',,,***'; do
    SCRIPTS="$line
"
    ( check_transports ) >/dev/null 2>&1 && bad "accepted: $line" || ok
done
SCRIPTS="x,$KEY64,,,
x,$KEY64,,,
"
( check_transports ) >/dev/null 2>&1 && bad "a package twice" || ok
SCRIPTS=""; TELEMOST="https://example.com/j/1"
( check_transports ) >/dev/null 2>&1 && bad "a bad Telemost link" || ok
TELEMOST=""; AVITO="not a list"
( check_transports ) >/dev/null 2>&1 && bad "a bad Avito list" || ok

# A sign-in for one document never wipes the other document's (cookies.json
# holds both): a core with cookie-inbox gets it in its inbox; an older core's
# store is replaced only when it holds no other document.
YA=https://disk.yandex.ru/i/ya; MR=https://cloud.mail.ru/public/aa/bb
BIN_DIR="$2/bin"; mkdir -p "$BIN_DIR" "$STATE_ROOT/t1"
old_core() { printf '#!/bin/sh\nexit 2\n' > "$BIN_DIR/openflux"; chmod +x "$BIN_DIR/openflux"; }
new_core() { printf '#!/bin/sh\n[ "$1" = --features ] && printf "cookie-inbox\\nsignin-state\\n"\n' > "$BIN_DIR/openflux"; chmod +x "$BIN_DIR/openflux"; }
store="$STATE_ROOT/t1/cookies.json"
COOKIES=$(printf '{"%s":{"Session_id":"y"}}' "$YA" | base64 | tr -d '\n')
old_core; rm -f "$store" "$store.incoming"
write_cookies && [ "$COOKIES_QUEUED" = 0 ] && grep -q '"Session_id":"y"' "$store" && ok || bad "a first sign-in makes the store"
printf '{\n  "%s": {\n    "Mpop": "m"\n  },\n  "%s": {\n    "Session_id": "old"\n  }\n}' "$MR" "$YA" > "$store"
write_cookies && bad "an old core overwrote the Mail.ru sign-in" || ok
grep -q '"Mpop": "m"' "$store" && [ ! -f "$store.incoming" ] && ok || bad "the store was touched: $(cat "$store")"
case "$COOKIES_ERROR" in *"обновите ноду"*) ok ;; *) bad "error: $COOKIES_ERROR" ;; esac
new_core
write_cookies && [ "$COOKIES_QUEUED" = 1 ] && grep -q '"Session_id":"y"' "$store.incoming" && grep -q '"Mpop": "m"' "$store" && ok || bad "the inbox: $COOKIES_QUEUED"
old_core; rm -f "$store.incoming"
printf '{"%s":{"Session_id":"older"}}' "$YA" > "$store"
write_cookies && grep -q '"Session_id":"y"' "$store" && ok || bad "an old core, the same document only, is replaced"

# show's signin list: the core's state lines, anything else dropped.
printf 'login 1760000000 %s\ndown 1760000300 %s\nbogus 1 x\nlogin notanumber %s\nlogin 5\n' "$YA" "$MR" "$YA" > "$STATE_ROOT/t1/signin-state"
got=$(signin_json)
[ "$got" = '[{"state":"login","since":1760000000,"doc":"'"$YA"'"},{"state":"down","since":1760000300,"doc":"'"$MR"'"}]' ] && ok || bad "signin_json: $got"
rm -f "$STATE_ROOT/t1/signin-state"
[ "$(signin_json)" = '[]' ] && ok || bad "signin_json without a file"

printf 'passed %d, failed %d\n' "$pass" "$failn"
[ "$failn" = 0 ]
