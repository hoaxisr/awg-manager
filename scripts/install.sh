#!/bin/sh
# AWG Manager — установщик для роутеров Keenetic
#
# Установка / обновление:
#   curl -sL https://raw.githubusercontent.com/hoaxisr/awg-manager/main/scripts/install.sh | sh
#   wget -qO- https://raw.githubusercontent.com/hoaxisr/awg-manager/main/scripts/install.sh | sh
#
# Скрипт добавляет репозиторий http://repo.hoaxisr.ru и делает
# `opkg update && opkg install awg-manager`. Повторный запуск —
# обновление до последней опубликованной версии.

set -e

ENTWARE_REPO="http://repo.hoaxisr.ru"
OPKG_CONF="/opt/etc/opkg/awg_manager.conf"

info()  { printf "\033[1;32m[+]\033[0m %s\n" "$1"; }
warn()  { printf "\033[1;33m[!]\033[0m %s\n" "$1"; }
error() { printf "\033[1;31m[-]\033[0m %s\n" "$1"; exit 1; }

# --- Определение архитектуры (нужно только для имени arch-директории репо) ---
detect_arch() {
    info "Определяю архитектуру..."
    ARCH=$(opkg print-architecture 2>/dev/null | grep '_kn' | awk '{print $2}' | sed 's/_kn.*//')
    [ -z "$ARCH" ] && error "Не удалось определить архитектуру. Это роутер Keenetic с Entware?"

    case "$ARCH" in
        mipsel-3.4|mips-3.4|aarch64-3.10) ;;
        *) error "Неподдерживаемая архитектура: $ARCH" ;;
    esac

    # aarch64-3.10 → aarch64-k3.10
    REPO_ARCH=$(echo "$ARCH" | sed 's/-\([0-9]\)/-k\1/')
    info "Архитектура: $ARCH (repo: $REPO_ARCH)"
}

# --- Добавить opkg репозиторий ---
add_repo() {
    REPO_LINE="src/gz hoaxisr ${ENTWARE_REPO}/${REPO_ARCH}"

    if [ -f "$OPKG_CONF" ] && grep -qF "$REPO_LINE" "$OPKG_CONF" 2>/dev/null; then
        return
    fi

    mkdir -p /opt/etc/opkg
    echo "$REPO_LINE" > "$OPKG_CONF"
    info "Репозиторий добавлен: ${ENTWARE_REPO}/${REPO_ARCH}"
}

# --- Установить / обновить пакет через opkg ---
install_awg_manager() {
    BEFORE=$(opkg list-installed 2>/dev/null | awk '/^awg-manager /{print $3}')

    info "Обновляю индекс пакетов (opkg update)..."
    if ! opkg update >/dev/null 2>&1; then
        warn "opkg update вернул ошибку — продолжаем, возможно другой репозиторий недоступен"
    fi

    info "opkg install awg-manager"
    opkg install awg-manager || error "Не удалось установить пакет. См. вывод opkg выше."

    AFTER=$(opkg list-installed 2>/dev/null | awk '/^awg-manager /{print $3}')
    [ -z "$AFTER" ] && error "awg-manager не установлен после opkg install"

    if [ -z "$BEFORE" ]; then
        info "Установлено: $AFTER"
    elif [ "$BEFORE" = "$AFTER" ]; then
        info "Версия не изменилась: $AFTER (уже актуальна, или opkg отказался от downgrade)"
    else
        info "Обновлено: $BEFORE → $AFTER"
    fi
}

# --- Запуск сервиса (belt-and-braces: post-install обычно уже запустил) ---
start_service() {
    info "Проверяю что сервис запущен..."
    /opt/etc/init.d/S99awg-manager restart 2>/dev/null \
        || /opt/bin/awg-manager --service start 2>/dev/null \
        || warn "Не удалось запустить автоматически. Запустите вручную: /opt/etc/init.d/S99awg-manager start"
}

# --- Проверка работоспособности ---
health_check() {
    PORT=$(sed -n 's/.*"port"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' \
        /opt/etc/awg-manager/settings.json 2>/dev/null)
    [ -z "$PORT" ] && PORT=2222

    info "Проверяю работоспособность (порт $PORT)..."

    attempts=0
    max_attempts=3
    while [ "$attempts" -lt "$max_attempts" ]; do
        attempts=$((attempts + 1))
        if curl -sf "http://127.0.0.1:${PORT}/api/health" >/dev/null 2>&1; then
            info "Сервис работает!"
            return 0
        fi
        [ "$attempts" -lt "$max_attempts" ] && sleep 2
    done

    warn "Сервис не отвечает на порту $PORT (может потребоваться больше времени для запуска)"
}

# --- Показать URL доступа ---
show_access_url() {
    PORT=$(sed -n 's/.*"port"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' \
        /opt/etc/awg-manager/settings.json 2>/dev/null)
    [ -z "$PORT" ] && PORT=2222

    IP=$(ip -4 addr show br0 2>/dev/null | awk '/inet /{sub(/\/.*/, "", $2); print $2; exit}')
    [ -z "$IP" ] && IP="192.168.1.1"

    echo ""
    info "========================================"
    info "  AWG Manager: http://${IP}:${PORT}"
    info "========================================"
    echo ""
}

# --- Main ---
detect_arch
add_repo
install_awg_manager
start_service
health_check
show_access_url
