#!/bin/sh
# /opt/etc/ndm/ifstatechanged.d/99-proxy-watchdog.sh
# chmod +x 99-proxy-watchdog.sh
# Проверяем, что скрипт вызван системным хуком
[ "$1" = "hook" ] || exit 0

UPSTREAMS_FILE="/opt/etc/upstreams.list"

# Укажите все нужные интерфейсы через пробел: "Proxy0 Wireguard1 Wireguard2 OpenVPN0"
# ndmc -c "show running-config" чтобы узнать имена интерфейсов
TUNNELS="Proxy1 Wireguard1"

if [ "$id" = "GigabitEthernet1" ]; then

    # 1. Защита от дребезга: убиваем предыдущий таймер сохранения, если он был запущен
    if [ -f /tmp/proxy_saver.pid ]; then
        kill -9 $(cat /tmp/proxy_saver.pid) 2>/dev/null
        rm -f /tmp/proxy_saver.pid
    fi

    if [ "$connected" = "no" ]; then
        logger "Proxy Watchdog: ISP is DOWN (PingCheck failed). Stopping tunnels."
        # Моментально выключаем все указанные туннели в оперативной памяти
        for tun in $TUNNELS; do
            ndmc -c "interface $tun down"
        done
        
        # Динамическое сохранение текущих апстримов
        # Сохраняем во временный файл, чтобы не затереть основной список при двойном срабатывании хука
        ndmc -c "show running-config" \
          | sed -n '/^dns-proxy/,/^!/p' \
          | grep -E "^\s+(tls|https) upstream" \
          | sed 's/^[[:space:]]*/dns-proxy /' \
          > /tmp/current_upstreams.list

        # Обновляем основной файл только если найдены апстримы (файл не пустой)
        if [ -s /tmp/current_upstreams.list ]; then
            mv /tmp/current_upstreams.list "$UPSTREAMS_FILE"
        fi
        rm -f /tmp/current_upstreams.list        

        # Выгружаем апстримы из памяти
        if [ -f "$UPSTREAMS_FILE" ]; then
            while read -r line; do
                [ -z "$line" ] && continue
                # Обрезаем всё после адреса для команды no
                cmd=$(echo "$line" | sed -E 's/^(dns-proxy (tls|https) upstream [^ ]+).*/\1/')
                ndmc -c "no $cmd"
            done < "$UPSTREAMS_FILE"
        fi
        
        # Запускаем фоновый таймер на 10 секунд для сохранения конфига (синхронизации веб-интерфейса)
        sh -c 'sleep 10; ndmc -c "system configuration save"; logger "Proxy Watchdog: UI synced (Tuns is OFF)."' &        
        # Запоминаем PID фонового процесса
        echo $! > /tmp/proxy_saver.pid
        
    elif [ "$connected" = "yes" ]; then
        logger "Proxy Watchdog: ISP is UP (PingCheck passed). Starting tunnels."        
        # Моментально включаем все указанные туннели в оперативной памяти
        for tun in $TUNNELS; do
            ndmc -c "interface $tun up"
        done

        # Вгружаем апстримы обратно
        if [ -f "$UPSTREAMS_FILE" ]; then
            while read -r line; do
		[ -z "$line" ] && continue
                ndmc -c "$line"
            done < "$UPSTREAMS_FILE"
        fi
        
        # Запускаем фоновый таймер на 10 секунд для сохранения конфига
        sh -c 'sleep 10; ndmc -c "system configuration save"; logger "Proxy Watchdog: UI synced (Tuns is ON)."' &        
        # Запоминаем PID фонового процесса
        echo $! > /tmp/proxy_saver.pid
    fi
fi