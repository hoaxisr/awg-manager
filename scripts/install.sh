#!/bin/sh
# AWG Manager — установщик для роутеров Keenetic
#
# Установка (последняя версия):
#   curl -sL https://raw.githubusercontent.com/hoaxisr/awg-manager/main/scripts/install.sh | sh
#   wget -qO- https://raw.githubusercontent.com/hoaxisr/awg-manager/main/scripts/install.sh | sh
#
# Установка конкретной версии:
#   curl -sL .../install.sh | sh -s -- 2.1.0
#
# Обновление: запустите ту же команду повторно

set -e

REPO="hoaxisr/awg-manager"
TMP_DIR="/tmp/awg-manager-install"

# Очистка при ошибке
cleanup() {
    rm -rf "$TMP_DIR"
}
trap cleanup EXIT

info()  { printf "\033[1;32m[+]\033[0m %s\n" "$1"; }
warn()  { printf "\033[1;33m[!]\033[0m %s\n" "$1"; }
error() { printf "\033[1;31m[-]\033[0m %s\n" "$1"; exit 1; }

# --- Убедиться что curl установлен ---
ensure_curl() {
    if command -v curl >/dev/null 2>&1; then
        return
    fi

    info "curl не найден, устанавливаю..."
    opkg update >/dev/null 2>&1 || error "Не удалось выполнить opkg update"
    opkg install curl >/dev/null 2>&1 || error "Не удалось установить curl"

    if ! command -v curl >/dev/null 2>&1; then
        error "curl установлен, но не найден в PATH"
    fi
    info "curl установлен"
}

# --- Определение архитектуры ---
detect_arch() {
    info "Определяю архитектуру..."
    ARCH=$(opkg print-architecture 2>/dev/null | awk '/_kn/{sub(/_kn.*/, "", $2); print $2}')
    [ -z "$ARCH" ] && error "Не удалось определить архитектуру. Это роутер Keenetic с Entware?"

    case "$ARCH" in
        mipsel-3.4|mips-3.4|aarch64-3.10) ;;
        *) error "Неподдерживаемая архитектура: $ARCH" ;;
    esac

    info "Архитектура: $ARCH"
}

# --- Проверка текущей установки ---
check_existing() {
    INSTALLED_VERSION=$(opkg list-installed 2>/dev/null | awk '/^awg-manager /{print $3}')
    if [ -n "$INSTALLED_VERSION" ]; then
        info "Установлена версия: $INSTALLED_VERSION"
        IS_UPDATE=1
    else
        IS_UPDATE=0
    fi
}

# --- Получить версию с GitHub ---
fetch_version() {
    # Если версия передана аргументом
    if [ -n "${TARGET_VERSION:-}" ]; then
        VERSION="$TARGET_VERSION"
        info "Запрошена версия: $VERSION"
        return
    fi

    info "Получаю последнюю версию с GitHub..."

    # Метод 1: Location header из redirect /releases/latest (только stable)
    VERSION=$(curl -sI "https://github.com/$REPO/releases/latest" 2>/dev/null \
        | sed -n 's/^[Ll]ocation:.*\/v\([^ \t\r]*\).*/\1/p' | tr -d '\r\n')

    # Метод 2: GitHub API /releases/latest (только stable)
    if [ -z "$VERSION" ]; then
        VERSION=$(curl -sL "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null \
            | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"v\([^"]*\)".*/\1/p')
    fi

    # Метод 3: все релизы, включая pre-release (первый = самый новый)
    if [ -z "$VERSION" ]; then
        VERSION=$(curl -sL "https://api.github.com/repos/$REPO/releases" 2>/dev/null \
            | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"v\([^"]*\)".*/\1/p' | head -1)
    fi

    [ -z "$VERSION" ] && error "Не удалось получить версию с GitHub. Проверьте подключение к интернету."
    info "Последняя версия: $VERSION"
}

# --- Скачать и установить ---
install_package() {
    PKG_NAME="awg-manager_${VERSION}_${ARCH}-kn.ipk"
    URL="https://github.com/$REPO/releases/download/v${VERSION}/${PKG_NAME}"

    mkdir -p "$TMP_DIR"

    if [ "$IS_UPDATE" = "1" ] && [ "$INSTALLED_VERSION" = "$VERSION" ]; then
        warn "Версия $VERSION уже установлена"
        printf "Переустановить? [y/N] "
        # stdin может быть занят пайпом (curl | sh), читаем из /dev/tty
        answer=""
        read -r answer < /dev/tty 2>/dev/null || answer="y"
        case "$answer" in
            [Yy]*) ;;
            *) info "Отменено"; exit 0 ;;
        esac
    fi

    if [ "$IS_UPDATE" = "1" ]; then
        info "Обновляю: $INSTALLED_VERSION -> $VERSION"
    else
        info "Устанавливаю версию $VERSION"
    fi

    info "Скачиваю $PKG_NAME..."
    curl -fL -o "$TMP_DIR/$PKG_NAME" "$URL" \
        || error "Ошибка загрузки. Проверьте, что версия $VERSION существует: https://github.com/$REPO/releases"

    info "Устанавливаю пакет..."
    opkg install "$TMP_DIR/$PKG_NAME" \
        || error "Ошибка установки пакета"

    info "Пакет установлен"
}

# --- Запуск сервиса ---
start_service() {
    info "Запускаю сервис..."
    /opt/etc/init.d/S80awg-manager restart 2>/dev/null \
        || /opt/bin/awg-manager --service start 2>/dev/null \
        || warn "Не удалось запустить автоматически. Запустите вручную: /opt/etc/init.d/S80awg-manager start"
}

# --- Проверка работоспособности ---
health_check() {
    # Извлечь порт из settings.json (без grep -P, совместимо с BusyBox)
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
    if [ "$IS_UPDATE" = "1" ]; then
        info "  Обновление завершено!"
    else
        info "  Установка завершена!"
    fi
    info "  AWG Manager: http://${IP}:${PORT}"
    info "========================================"
    echo ""
}

# --- Main ---
TARGET_VERSION="${1:-}"

ensure_curl
detect_arch
check_existing
fetch_version
install_package
start_service
health_check
show_access_url
