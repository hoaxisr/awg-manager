#!/bin/sh
# diagnose.sh — диагностика Susanin.Keenetic.
#
# Когда что-то работает не так — запустите этот скрипт: он проверит окружение,
# настройки, правила и списки, а в конце выдаст РЕКОМЕНДАЦИИ.
#
# Запуск:
#   sh /opt/susanin/tools/diagnose.sh [--etc DIR] [--dns IP]
#
# Для отправки полного отчёта (логи и состояние) используйте report.sh.
# POSIX sh (busybox ash compatible).
#
# ---------------------------------------------------------------------------
# Что проверяет и какие проблемы выявляет (по опыту проекта и разбору логов):
#
#  1) Окружение и версия
#     - нет susanin-agent (не установлен / неудачное обновление);
#     - нет пакетов iptables/ipset/conntrack/ip — правила не создаются вообще
#       (в логе это выглядело как «provisioning failed», пока не добавили
#       причину).
#  2) Конфиг
#     - egress_interface указывает на отсутствующий интерфейс (VPN удалён или
#       переименован) — демон «помнит» старое подключение, маршрут не встаёт;
#     - пустые lan_interfaces/lan_subnets — трафик LAN не анализируется;
#     - disk_mode=soft на флешке и наоборот (не критично, но полезно видеть);
#     - vpn_always_dns пусто (auto) — риск, что список резолвится через
#       подменяемый/неотвечающий DNS.
#  3) Правила (data plane)
#     - нет цепочки SUSANIN, нет ip rule на таблицу VPN, нет default в таблице —
#       трафик идёт мимо VPN (частый симптом «ничего не работает»);
#     - обнаружены правила политик Keenetic (fwmark 0xffffaXX) — клиенты,
#       привязанные к «Приоритетам подключений», Susanin не обрабатывает
#       (в логах это видно как mark=0xffffaXX в conntrack);
#     - наборы susanin_ok_* пусты — автообучение не работает.
#  4) Списки vpn_always / vpn_never
#     - домен без A-записей на apex — НЕ ошибка: для CDN это норма, демон сам
#       зондирует поддомены (resolve_dom: apex пуст → пробы susanin-XXXX.domain);
#       такие строки идут в отдельный счётчик «без A на apex», без списка
#       «проблем» и рекомендаций по каждой;
#     - одна и та же строка в обоих списках (конфликт; приоритет у «напрямую»).
#
# В конце печатается блок «Рекомендации» — что именно поправить.
# ---------------------------------------------------------------------------
set -u

# Прогресс показываем только в интерактивном терминале (stderr — tty), чтобы не
# засорять вывод при перенаправлении (например, в report.sh).
ttyst=0
[ -t 2 ] && ttyst=1

ETCDIR=""
DNS=""
while [ $# -gt 0 ]; do
    case "$1" in
        --etc) ETCDIR="$2"; shift ;;
        --dns) DNS="$2"; shift ;;
        -h|--help) echo "usage: $0 [--etc DIR] [--dns IP]"; exit 0 ;;
        *) echo "unknown arg: $1" >&2; exit 2 ;;
    esac
    shift
done

if [ -z "$ETCDIR" ]; then
    if [ -d /opt/susanin/etc ]; then
        ETCDIR=/opt/susanin/etc
    elif [ -d /etc/susanin ]; then
        ETCDIR=/etc/susanin
    else
        ETCDIR=/opt/susanin/etc
    fi
fi
CONF="$ETCDIR/susanin.conf"

cfg() { # cfg <key> — значение из susanin.conf (или пусто)
    [ -f "$CONF" ] || return 0
    sed -n "s/^$1=[ \t]*//p" "$CONF" | head -n1
}

RECS=""
rec() { RECS="${RECS}
  - $*"; }
sec() { echo; echo "== $* =="; }

# ------------------------------------------------------------------ окружение
sec "Окружение"
A=/opt/susanin/bin/susanin-agent
if [ ! -x "$A" ]; then
    A=$(command -v susanin-agent 2>/dev/null || true)
fi
if [ -n "$A" ] && [ -x "$A" ]; then
    echo "agent: $("$A" version 2>/dev/null)"
else
    echo "agent: НЕ НАЙДЕН"
            rec "Программа (susanin-agent) не найдена — проверьте установку или обновление."
fi

for t in iptables ipset conntrack ip; do
    if command -v "$t" >/dev/null 2>&1; then
        echo "tool $t: ok"
    else
        echo "tool $t: НЕТ"
        rec "Не хватает пакета '$t'. Установите: opkg update && opkg install $t"
    fi
done

# ------------------------------------------------------------------- конфиг
sec "Конфиг ($CONF)"
EGR=$(cfg egress_interface)
LAN=$(cfg lan_interfaces)
SUB=$(cfg lan_subnets)
TBL=$(cfg routing_table)
DM=$(cfg disk_mode)
DNS_CFG=$(cfg vpn_always_dns)
[ -n "$TBL" ] || TBL=100
echo "egress: ${EGR:-?}"
echo "lan: ${LAN:-?} / ${SUB:-?}"
echo "table: $TBL, disk_mode: ${DM:-?}, vpn_always_dns: ${DNS_CFG:-<auto>}"

if [ -n "$EGR" ]; then
    oldifs=$IFS; IFS=','
    for i in $EGR; do
        IFS=$oldifs
        i=$(printf '%s' "$i" | tr -d ' \t')
        if [ -e "/sys/class/net/$i" ]; then
            echo "egress $i: существует"
        else
            echo "egress $i: ОТСУТСТВУЕТ"
            rec "VPN-интерфейс '$i' не найден. Включите VPN-подключение или укажите верное имя в egress_interface."
        fi
        IFS=','
    done
    IFS=$oldifs
fi

# ------------------------------------------------------------ правила (data plane)
sec "Правила"
if command -v iptables >/dev/null 2>&1; then
    if iptables -t mangle -S SUSANIN >/dev/null 2>&1; then
        echo "цепочка SUSANIN: ok"
    else
        echo "цепочка SUSANIN: НЕТ"
        rec "Правила Susanin не созданы. Проверьте, что установлены ipset и iptables, затем: susanin.sh restart"
    fi
fi
if command -v ip >/dev/null 2>&1; then
    if ip rule show 2>/dev/null | grep -q "lookup $TBL"; then
        echo "ip rule -> table $TBL: ok"
    else
        echo "ip rule -> table $TBL: НЕТ"
        rec "Нет правила ip rule на таблицу $TBL — трафик идёт мимо VPN. Перезапустите: susanin.sh restart"
    fi
    if ip route show table "$TBL" 2>/dev/null | grep -q '^default'; then
        echo "default в table $TBL: ok"
    else
        echo "default в table $TBL: НЕТ"
        rec "В таблице $TBL нет маршрута по умолчанию — проверьте, что VPN-подключение включено."
    fi
    if ip rule show 2>/dev/null | grep -qE 'fwmark 0xffffa'; then
        echo "политики Keenetic (fwmark 0xffffaXX): есть"
        rec "У части устройств включён «Приоритет подключений» Keenetic. Для них маршрут выбирает Keenetic, а Susanin не участвует. Если устройство должно управляться Susanin — снимите у него политику (оставьте «по умолчанию»)."
    fi
fi
if command -v ipset >/dev/null 2>&1; then
    okc=$(ipset list susanin_ok_tcp 2>/dev/null | grep -cE '^[0-9]+\.')
    okn=$(ipset list susanin_ok_net 2>/dev/null | grep -cE '^[0-9]+\.')
    echo "наборы: ok_tcp=$okc, ok_net=$okn"
    if [ "${okc:-0}" -eq 0 ] && [ "${okn:-0}" -eq 0 ]; then
        rec "Список выученных адресов пуст — обучение не работает. Проверьте пакеты и правила и что через роутер идёт трафик."
    fi
fi

# --------------------------------------------------------------------- списки
ips_of() {
    if [ -n "$DNS" ]; then
        nslookup "$1" "$DNS" 2>&1
    else
        nslookup "$1" 2>&1
    fi | awk '/^Name:/{f=1; next}
              f && /^Address/ {
                  for (i = 1; i <= NF; i++)
                      if ($i ~ /^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$/) { print $i; break }
              }'
}
is_ip4()  { printf '%s' "$1" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$'; }
is_cidr() { printf '%s' "$1" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+/[0-9]+$'; }

# Домен без A на apex — не ошибка: для CDN это норма (работают поддомены), а
# демон с 0.3.10 сам зондирует поддомены (resolve_dom: apex пуст → пробы
# susanin-XXXX.domain). Поэтому такие строки считаем отдельно и НЕ заваливаем
# пользователя списком «проблем». Реально нерезолвимые имена видны по нулевому
# числу адресов и упоминаются в сводке.
check_list() {
    f="$1"; label="$2"
    if [ ! -f "$f" ]; then
        echo "$label: файла нет ($f)"
        return
    fi
    echo "$label ($f):"
    n_all=$(grep -vcE '^[[:space:]]*(#|$)' "$f" 2>/dev/null)
    [ -n "$n_all" ] || n_all=0
    if [ "$ttyst" = 1 ] && [ "$n_all" -gt 0 ]; then
        printf '  проверяю %s строк (DNS-запрос к каждому домену; может занять ~минуту, Ctrl+C — прервать)\n' \
               "$n_all" >&2
    fi
    tot=0; ok=0; zone=0; idx=0
    zex=""
    while IFS= read -r raw; do
        e=$(printf '%s' "$raw" | sed 's/#.*//' | tr -d ' \t\r')
        [ -z "$e" ] && continue
        tot=$((tot + 1))
        idx=$((idx + 1))
        [ "$ttyst" = 1 ] && printf '\r  [%d/%d] %-45s' "$idx" "$n_all" "$e" >&2
        if is_ip4 "$e" || is_cidr "$e"; then
            ok=$((ok + 1))
            continue
        fi
        name=$(printf '%s' "$e" | sed 's/^\*\.//')
        ips=$(ips_of "$name")
        if [ -n "$ips" ]; then
            ok=$((ok + 1))
        else
            zone=$((zone + 1))
            [ -z "$zex" ] && zex="$name"
        fi
    done < "$f"
    [ "$ttyst" = 1 ] && printf '\r                                                                      \r' >&2
    echo "  итог: строк $tot, с адресами $ok, без A на apex $zone"
    if [ "$zone" -gt 0 ]; then
        echo "  без A на apex: $zone (для CDN это норма, демон проверит поддомены; напр. $zex)"
    fi
}

sec "Списки"
check_list "$ETCDIR/vpn_always.txt" "vpn_always"
check_list "$ETCDIR/vpn_never.txt" "vpn_never"

if [ -f "$ETCDIR/vpn_always.txt" ] && [ -f "$ETCDIR/vpn_never.txt" ]; then
    conf=$(awk '
        { l = $0; sub(/#.*/, "", l); gsub(/[ \t\r]/, "", l);
          if (l == "") next;
          if (FNR == NR) { a[l] = 1; next }
          if (a[l]) print l }
    ' "$ETCDIR/vpn_always.txt" "$ETCDIR/vpn_never.txt")
    if [ -n "$conf" ]; then
        echo "  конфликты always/never (приоритет — «напрямую»):"
        printf '    %s\n' $conf
        rec "Один и тот же адрес есть и в vpn_always, и в vpn_never — побеждает «напрямую». Уберите лишнюю строку: $(printf '%s' "$conf" | head -n1)"
    fi
fi

if [ -z "$DNS_CFG" ] && [ -z "$DNS" ]; then
    echo "  DNS для списков: auto (системный, при 127.0.0.1 — адрес LAN-моста)"
fi

# ------------------------------------------------------------- рекомендации
sec "Рекомендации"
if [ -n "$RECS" ]; then
    printf '%s\n' "$RECS"
else
    echo "  проблем не найдено — настройки и правила в порядке"
fi
echo
echo "Для отправки отчёта с логами: sh /opt/susanin/tools/report.sh"
