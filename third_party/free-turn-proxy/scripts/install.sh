#!/usr/bin/env bash
#
# Free Turn Proxy — установщик сервера (gum UI, Material-палитра).
#
#   sudo bash install.sh              интерактивный мастер
#   sudo bash install.sh -y [опции]   non-interactive (CI / автоматизация)
#   sudo bash install.sh --help       все опции
#
# Работает через `curl -fsSL .../install.sh | sudo bash` — интерактивный ввод
# читается из /dev/tty. UI рендерит gum (ставится автоматически); при недоступности
# gum скрипт молча падает в текстовый режим.

set -Eeuo pipefail

# ───────────────────────────────────────────────────────────────────────────
# Константы
# ───────────────────────────────────────────────────────────────────────────
readonly REPO="samosvalishe/free-turn-proxy"
readonly IMAGE="ghcr.io/${REPO}"
readonly APP_DIR="/opt/free-turn-proxy"
readonly CONF_FILE="${APP_DIR}/install.conf"
readonly SERVICE="free-turn-proxy.service"
readonly UNIT_FILE="/etc/systemd/system/${SERVICE}"
readonly COMPOSE_FILE="${APP_DIR}/docker-compose.yml"
readonly CONTAINER="free-turn-proxy"
readonly GUM_VERSION="0.17.0"   # pinned; при сбое скачивания — fallback на latest API

# WireGuard bootstrap (опциональный, только mode=udp)
readonly WG_DIR="/etc/wireguard"
readonly WG_IFACE="wg0"
readonly WG_CONF="${WG_DIR}/${WG_IFACE}.conf"
readonly WG_NET="10.13.13"          # /24, .1 = сервер, .2 = первый пир
readonly WG_CLIENT_CONF="${APP_DIR}/wireguard-client.conf"

# ───────────────────────────────────────────────────────────────────────────
# Material Design 3 — тональная палитра (читаемые на тёмном фоне тона)
# ───────────────────────────────────────────────────────────────────────────
readonly MD_PRIMARY="#D0BCFF"    # акцент: заголовки, курсор, prompt
readonly MD_SECONDARY="#CCC2DC"  # подписи, dim-текст
readonly MD_TERTIARY="#EFB8C8"   # курсор выбора
readonly MD_SUCCESS="#81C784"    # ok
readonly MD_ERROR="#F2B8B5"      # error
# Фиолетовый градиент баннера (Material primary tonal P90→P30).
readonly BANNER_GRADIENT=("#EADDFF" "#D0BCFF" "#B69DF8" "#9A82DB" "#7F67BE" "#6750A4")

# ANSI для plain-fallback (без gum).
readonly C_RED='\033[0;31m' C_GREEN='\033[0;32m' C_YELLOW='\033[1;33m' C_CYAN='\033[0;36m' C_NC='\033[0m'

# ───────────────────────────────────────────────────────────────────────────
# Конфиг установки (перекрывается load_config → мастером → CLI-overrides)
# ───────────────────────────────────────────────────────────────────────────
INSTALL_METHOD="docker"  # docker | systemd
VERSION="latest"
PROVIDER="vk"            # vk (источник TURN-creds на клиенте; сервер provider-agnostic)
PROXY_MODE="udp"         # udp | tcp
BACKEND_PORT=""
LISTEN_PORT="56000"
OBF_PROFILE="rtpopus"    # rtpopus | none
OBF_KEY=""
CLIENTS_FILE_CONF=""     # пусто = auth выключен
WG_SETUP=0               # 1 = поднять WireGuard-сервер на BACKEND_PORT
WG_ENDPOINT="127.0.0.1:9000"  # адрес локального free-turn-proxy client (-listen) для WG-app

# Состояние рантайма
HAS_GUM=0
GOARCH=""
WG_PORT=""
NONINTERACTIVE=0
OPEN_FIREWALL=""         # ""=спросить 1=да 0=нет
PURGE=0
ACTION=""               # ""|update|reconfigure|uninstall
OVERRIDES=()

# ───────────────────────────────────────────────────────────────────────────
# Валидаторы
# ───────────────────────────────────────────────────────────────────────────
valid_port()     { [[ "$1" =~ ^[0-9]+$ ]] && [ "$1" -ge 1 ] && [ "$1" -le 65535 ]; }
valid_hex64()    { [[ "$1" =~ ^[0-9a-fA-F]{64}$ ]]; }
valid_endpoint() { [[ "$1" =~ ^[^[:space:]/]+:[0-9]{1,5}$ ]]; }  # host:port

# ═══════════════════════════════════════════════════════════════════════════
# UI-слой: тонкие обёртки над gum + plain-fallback.
# Контракт: каждая ui_*-функция работает одинаково в обоих режимах.
# ═══════════════════════════════════════════════════════════════════════════
log_info()    { if [ "$HAS_GUM" = 1 ]; then gum log --level info  -- "$1"; else echo -e "${C_CYAN}[*]${C_NC} $1"; fi; }
log_warn()    { if [ "$HAS_GUM" = 1 ]; then gum log --level warn  -- "$1"; else echo -e "${C_YELLOW}[!]${C_NC} $1" >&2; fi; }
log_error()   { if [ "$HAS_GUM" = 1 ]; then gum log --level error -- "$1"; else echo -e "${C_RED}[x]${C_NC} $1" >&2; fi; }
log_success() { if [ "$HAS_GUM" = 1 ]; then gum style --foreground "$MD_SUCCESS" "✔ $1"; else echo -e "${C_GREEN}[+]${C_NC} $1"; fi; }

die()      { log_error "$1"; exit 1; }
ui_abort() { log_info "Отменено."; exit 0; }

# Градиент-баннер из ASCII-арта.
ui_banner() {
    local art=(
'███████╗██████╗ ███████╗███████╗████████╗██╗   ██╗██████╗ ███╗   ██╗'
'██╔════╝██╔══██╗██╔════╝██╔════╝╚══██╔══╝██║   ██║██╔══██╗████╗  ██║'
'█████╗  ██████╔╝█████╗  █████╗     ██║   ██║   ██║██████╔╝██╔██╗ ██║'
'██╔══╝  ██╔══██╗██╔══╝  ██╔══╝     ██║   ██║   ██║██╔══██╗██║╚██╗██║'
'██║     ██║  ██║███████╗███████╗   ██║   ╚██████╔╝██║  ██║██║ ╚████║'
'╚═╝     ╚═╝  ╚═╝╚══════╝╚══════╝   ╚═╝    ╚═════╝ ╚═╝  ╚═╝╚═╝  ╚═══╝')
    printf '\n'
    if [ "$HAS_GUM" = 1 ]; then
        local i lines=()
        for i in "${!art[@]}"; do
            lines+=("$(gum style --foreground "${BANNER_GRADIENT[$i]}" "${art[$i]}")")
        done
        gum join --vertical "${lines[@]}"
        gum style --foreground "$MD_SECONDARY" --italic --margin "0 0 1 1" \
            "TURN-relay туннель  ·  установщик сервера"
    else
        echo -e "${C_CYAN}"; printf '%s\n' "${art[@]}"; echo -e "${C_NC}"
        echo "  TURN-relay туннель · установщик сервера"; echo
    fi
}

# Информационный бокс. ui_note TITLE TEXT
ui_note() {
    if [ "$HAS_GUM" = 1 ]; then
        gum style --border rounded --border-foreground "$MD_PRIMARY" --padding "0 1" --margin "1 0" \
            "$(gum style --foreground "$MD_PRIMARY" --bold "$1")" "$2"
    else
        echo; log_warn "$1: $2"
    fi
}

# Текстовый ввод с дефолтом. ui_input VAR PROMPT [DEFAULT]
ui_input() {
    local __var="$1" __prompt="$2" __def="${3:-}" __ans
    if [ "$HAS_GUM" = 1 ]; then
        __ans=$(gum input --prompt "$__prompt: " --prompt.foreground "$MD_PRIMARY" \
            --cursor.foreground "$MD_TERTIARY" --value "$__def" </dev/tty) || ui_abort
    else
        if [ -n "$__def" ]; then read -r -p "$__prompt [$__def]: " __ans </dev/tty
        else read -r -p "$__prompt: " __ans </dev/tty; fi
    fi
    printf -v "$__var" '%s' "${__ans:-$__def}"
}

# Да/нет. ui_yesno PROMPT [DEFAULT(Y|N)] -> код 0(да)/1(нет)
ui_yesno() {
    local __prompt="$1" __def="${2:-Y}" __ans
    if [ "$HAS_GUM" = 1 ]; then
        local flags=(--selected.background "$MD_PRIMARY" --selected.foreground "#1C1B1F")
        [ "$__def" = "N" ] && flags+=(--default=false)
        gum confirm "${flags[@]}" "$__prompt" </dev/tty
        return $?
    fi
    local hint; [ "$__def" = "Y" ] && hint="Y/n" || hint="y/N"
    read -r -p "$__prompt [$hint]: " __ans </dev/tty
    [[ "${__ans:-$__def}" =~ ^[Yy]$ ]]
}

# Меню. ui_menu VAR PROMPT DEFAULT_TAG  tag label [tag label ...]
# Показывает label'ы, возвращает выбранный tag.
ui_menu() {
    local __var="$1" __prompt="$2" __def_tag="$3"; shift 3
    local tags=() labels=()
    while [ $# -gt 0 ]; do tags+=("$1"); labels+=("$2"); shift 2; done

    if [ "$HAS_GUM" = 1 ]; then
        local i def_label="" sel
        for i in "${!tags[@]}"; do [ "${tags[$i]}" = "$__def_tag" ] && def_label="${labels[$i]}"; done
        sel=$(gum choose --header "$__prompt" --header.foreground "$MD_PRIMARY" \
            --cursor "❯ " --cursor.foreground "$MD_TERTIARY" \
            --selected.foreground "$MD_PRIMARY" --selected "$def_label" \
            "${labels[@]}" </dev/tty) || ui_abort
        for i in "${!labels[@]}"; do
            [ "${labels[$i]}" = "$sel" ] && { printf -v "$__var" '%s' "${tags[$i]}"; return; }
        done
        ui_abort
    else
        local i sel
        echo; log_info "$__prompt"
        for i in "${!tags[@]}"; do
            if [ "${tags[$i]}" = "$__def_tag" ]; then echo -e "  ${C_GREEN}${tags[$i]}${C_NC}) ${labels[$i]}"
            else echo "  ${tags[$i]}) ${labels[$i]}"; fi
        done
        while :; do
            read -r -p "Выбор [${__def_tag}]: " sel </dev/tty
            sel="${sel:-$__def_tag}"
            for i in "${!tags[@]}"; do
                [ "$sel" = "${tags[$i]}" ] && { printf -v "$__var" '%s' "$sel"; return; }
            done
            log_warn "Неверный выбор: $sel"
        done
    fi
}

# Порт с повтором при ошибке. ask_port VAR PROMPT DEFAULT
ask_port() {
    local __var="$1" __prompt="$2" __def="$3"
    while :; do
        ui_input "$__var" "$__prompt" "$__def"
        valid_port "${!__var}" && break
        ui_note "Ошибка" "Порт — число 1–65535. Получено: '${!__var}'"
    done
}

# Спиннер вокруг долгой команды. ui_spin TITLE CMD ARGS...
# Возвращает код команды; на ошибке печатает хвост вывода в боксе.
ui_spin() {
    local title="$1"; shift
    local rc=0 log; log="$(mktemp)"
    if [ "$HAS_GUM" = 1 ]; then
        # Запускаем команду через bash -c, чтобы перенаправить её вывод в лог,
        # а спиннер gum оставить чистым ($0=лог, $@=команда).
        # if/else, а не `cmd; rc=$?` — иначе set -e убьёт скрипт до захвата кода.
        # shellcheck disable=SC2016  # $@/$0 должны раскрыться внутри bash -c, не здесь
        if gum spin --spinner dot --spinner.foreground "$MD_PRIMARY" --title "$title" \
            -- bash -c '"$@" >"$0" 2>&1' "$log" "$@"; then rc=0; else rc=$?; fi
        if [ "$rc" -eq 0 ]; then log_success "$title"
        else
            log_error "$title — ошибка (код $rc)"
            tail -n 40 "$log" | gum style --border rounded --border-foreground "$MD_ERROR" --padding "0 1"
        fi
    elif [ -t 1 ]; then
        ( "$@" >"$log" 2>&1 ) &
        local pid=$! i=0
        local ch='|/-'$'\\'   # кадры спиннера: | / - \
        while kill -0 "$pid" 2>/dev/null; do
            i=$(((i + 1) % 4)); printf "\r${C_CYAN}[*]${C_NC} %s %s" "$title" "${ch:$i:1}"; sleep 0.2
        done
        wait "$pid" && rc=0 || rc=$?
        if [ "$rc" -eq 0 ]; then printf "\r${C_GREEN}[+]${C_NC} %s\033[K\n" "$title"
        else printf "\r${C_RED}[x]${C_NC} %s\033[K\n" "$title"; tail -n 40 "$log" >&2; fi
    else
        log_info "$title..."
        "$@" >"$log" 2>&1 && rc=0 || rc=$?
        [ "$rc" -ne 0 ] && tail -n 40 "$log" >&2
    fi
    rm -f "$log"
    return "$rc"
}

# ═══════════════════════════════════════════════════════════════════════════
# Система / зависимости
# ═══════════════════════════════════════════════════════════════════════════
detect_arch() {
    case "$(uname -m)" in
        x86_64 | amd64)  GOARCH="amd64" ;;
        aarch64 | arm64) GOARCH="arm64" ;;
        *) die "Неподдерживаемая архитектура: $(uname -m)" ;;
    esac
}

pkg_install() {
    if command -v apt-get >/dev/null 2>&1; then
        # noninteractive: debconf/needrestart рисуют диалог в /dev/tty и виснут под спиннером.
        # Lock::Timeout: unattended-upgrades на свежей системе держит dpkg-lock.
        export DEBIAN_FRONTEND=noninteractive NEEDRESTART_SUSPEND=1
        apt-get -o DPkg::Lock::Timeout=300 update -y -qq >/dev/null 2>&1 || true
        apt-get -o DPkg::Lock::Timeout=300 install -y -qq "$@" </dev/null || true
    elif command -v dnf >/dev/null 2>&1; then dnf install -y -q "$@" </dev/null || true
    elif command -v yum >/dev/null 2>&1; then yum install -y -q "$@" </dev/null || true
    fi
}
export -f pkg_install

ensure_base_deps() {
    local missing=() b
    for b in curl jq openssl tar; do command -v "$b" >/dev/null 2>&1 || missing+=("$b"); done
    [ "${#missing[@]}" -eq 0 ] && return 0
    ui_spin "Установка зависимостей: ${missing[*]}" pkg_install "${missing[@]}"
    missing=()
    for b in curl jq openssl tar; do command -v "$b" >/dev/null 2>&1 || missing+=("$b"); done
    [ "${#missing[@]}" -ne 0 ] && die "Не удалось установить: ${missing[*]}. Поставьте вручную."
    return 0
}

# Скачать конкретную версию gum в /usr/local/bin. gum_download VERSION -> 0/1
gum_download() {
    local ver="$1" arch tmp bin
    case "$GOARCH" in amd64) arch="x86_64" ;; arm64) arch="arm64" ;; *) return 1 ;; esac
    local url="https://github.com/charmbracelet/gum/releases/download/v${ver}/gum_${ver}_Linux_${arch}.tar.gz"
    tmp="$(mktemp -d)"
    if curl -fsSL --max-time 30 "$url" | tar -xz -C "$tmp" 2>/dev/null; then
        bin="$(find "$tmp" -name gum -type f 2>/dev/null | head -n1 || true)"
        [ -n "$bin" ] && install -m 0755 "$bin" /usr/local/bin/gum 2>/dev/null
    fi
    rm -rf "$tmp"
    command -v gum >/dev/null 2>&1
}

ensure_gum() {
    command -v gum >/dev/null 2>&1 && { HAS_GUM=1; return; }
    log_info "Установка gum ${GUM_VERSION}..."
    if gum_download "$GUM_VERSION"; then HAS_GUM=1; return; fi
    # Fallback: последний релиз (если пин недоступен/битый).
    local latest
    latest="$(curl -s --max-time 10 'https://api.github.com/repos/charmbracelet/gum/releases/latest' \
        | jq -r '.tag_name // empty' | sed 's/^v//' || true)"
    if [ -n "$latest" ] && gum_download "$latest"; then HAS_GUM=1; return; fi
    HAS_GUM=0
    log_warn "gum недоступен — текстовый режим."
}

detect_wg_port() {
    WG_PORT=""
    command -v wg >/dev/null 2>&1 || return 0
    if wg show all listen-port >/dev/null 2>&1; then
        WG_PORT="$(wg show all listen-port 2>/dev/null | head -n1 | awk '{print $2}' || true)"
    fi
    if [ -z "$WG_PORT" ] && ls /etc/wireguard/*.conf >/dev/null 2>&1; then
        WG_PORT="$(grep -i ListenPort /etc/wireguard/*.conf 2>/dev/null | head -n1 | awk -F= '{print $2}' | tr -d ' ' || true)"
    fi
}

# ═══════════════════════════════════════════════════════════════════════════
# GitHub API
# ═══════════════════════════════════════════════════════════════════════════
gh_latest_version()  { curl -s --max-time 10 "https://api.github.com/repos/${REPO}/releases/latest" | jq -r '.tag_name // empty'; }
gh_recent_versions() { curl -s --max-time 10 "https://api.github.com/repos/${REPO}/releases?per_page=6" | jq -r '.[].tag_name'; }

resolve_asset_url() {  # VERSION ASSET_NAME
    local api
    [ "$1" = "latest" ] && api="https://api.github.com/repos/${REPO}/releases/latest" \
                        || api="https://api.github.com/repos/${REPO}/releases/tags/$1"
    curl -s --max-time 15 "$api" | jq -r --arg n "$2" '.assets[] | select(.name==$n) | .browser_download_url'
}

image_tag() { [ "$VERSION" = "latest" ] && echo "latest" || echo "$VERSION"; }

# ═══════════════════════════════════════════════════════════════════════════
# Конфиг: load / save / validate / overrides
# ═══════════════════════════════════════════════════════════════════════════
is_installed() { [ -f "$CONF_FILE" ] || [ -f "$COMPOSE_FILE" ] || [ -f "$UNIT_FILE" ]; }
# shellcheck disable=SC1090  # путь к конфигу динамический, по дизайну
load_config()  { [ -f "$CONF_FILE" ] && . "$CONF_FILE" || true; }

save_config() {
    mkdir -p "$APP_DIR"
    cat > "$CONF_FILE" <<EOF
INSTALL_METHOD="$INSTALL_METHOD"
VERSION="$VERSION"
PROVIDER="$PROVIDER"
PROXY_MODE="$PROXY_MODE"
BACKEND_PORT="$BACKEND_PORT"
LISTEN_PORT="$LISTEN_PORT"
OBF_PROFILE="$OBF_PROFILE"
OBF_KEY="$OBF_KEY"
CLIENTS_FILE_CONF="$CLIENTS_FILE_CONF"
WG_SETUP="$WG_SETUP"
WG_ENDPOINT="$WG_ENDPOINT"
EOF
    chmod 600 "$CONF_FILE"
}

apply_overrides() {  # CLI бьёт install.conf
    local kv k v
    for kv in "${OVERRIDES[@]+"${OVERRIDES[@]}"}"; do
        k="${kv%%=*}"; v="${kv#*=}"; printf -v "$k" '%s' "$v"
    done
}

connect_addr() { echo "127.0.0.1:${BACKEND_PORT}"; }

validate_config() {
    case "$INSTALL_METHOD" in docker | systemd) ;; *) die "method: docker|systemd, а не '$INSTALL_METHOD'" ;; esac
    case "$PROVIDER"       in vk) ;; *) die "provider: vk, а не '$PROVIDER'" ;; esac
    case "$PROXY_MODE"     in udp | tcp) ;; *) die "mode: udp|tcp, а не '$PROXY_MODE'" ;; esac
    valid_port "$LISTEN_PORT" || die "listen-port невалиден: '$LISTEN_PORT'"
    [ -z "$BACKEND_PORT" ] && { [ "$PROXY_MODE" = "udp" ] && BACKEND_PORT="51820" || BACKEND_PORT="443"; }
    valid_port "$BACKEND_PORT" || die "backend-port невалиден: '$BACKEND_PORT'"
    case "$OBF_PROFILE" in
        rtpopus)
            [ -z "$OBF_KEY" ] && { OBF_KEY="$(openssl rand -hex 32)"; log_info "Сгенерирован ключ обфускации."; }
            valid_hex64 "$OBF_KEY" || die "obf-key — ровно 64 hex-символа" ;;
        none) OBF_KEY="" ;;
        *) die "obf: rtpopus|none, а не '$OBF_PROFILE'" ;;
    esac
    if [ "$WG_SETUP" = "1" ]; then
        [ "$PROXY_MODE" = "udp" ] || die "WireGuard bootstrap доступен только при -mode udp."
        valid_endpoint "$WG_ENDPOINT" || die "wg-endpoint невалиден (host:port): '$WG_ENDPOINT'"
    fi
    return 0
}

# ═══════════════════════════════════════════════════════════════════════════
# Интерактивный мастер
# ═══════════════════════════════════════════════════════════════════════════
wizard_method() {
    ui_menu INSTALL_METHOD "Метод установки сервера:" "$INSTALL_METHOD" \
        docker  "Docker Compose  (рекомендуется)" \
        systemd "Systemd  (бинарь напрямую)"
}

wizard_mode() {
    ui_menu PROXY_MODE "Режим туннеля:" "$PROXY_MODE" \
        udp "UDP-relay  ·  WireGuard / AmneziaWG" \
        tcp "TCP-forward  ·  Xray / sing-box"
}

wizard_provider() {
    ui_menu PROVIDER "Провайдер TURN-creds (клиент):" "$PROVIDER" \
        vk "VK Calls API"
}

wizard_ports() {
    detect_wg_port
    local def_backend="$BACKEND_PORT" label="Порт вашего VPN / бэкенда"
    if [ -z "$def_backend" ]; then
        if [ "$PROXY_MODE" = "udp" ] && [ -n "$WG_PORT" ]; then
            def_backend="$WG_PORT"; log_info "Найден WireGuard на порту $WG_PORT."
        elif [ "$PROXY_MODE" = "udp" ]; then def_backend="51820"
        else def_backend="443"; fi
    fi
    if [ "$PROXY_MODE" = "tcp" ]; then
        label="Порт TCP-бэкенда (Xray / sing-box inbound)"
        ui_note "TCP-режим" "Backend — ваш Xray/sing-box inbound на 127.0.0.1. Сервис поднимаете отдельно; инсталлятор его не ставит."
    fi
    ask_port BACKEND_PORT "$label" "$def_backend"
    ask_port LISTEN_PORT  "Внешний порт (приём Free Turn Proxy)" "${LISTEN_PORT:-56000}"
}

wizard_wireguard() {
    [ "$PROXY_MODE" = "udp" ] || { WG_SETUP=0; return; }
    if [ -n "$WG_PORT" ]; then
        log_info "Обнаружен WireGuard (порт $WG_PORT) — использую существующий."
        WG_SETUP=0; return
    fi
    if ui_yesno "WireGuard не найден. Установить и настроить WG-сервер на порту ${BACKEND_PORT}?" "N"; then
        WG_SETUP=1
        ui_note "Endpoint WG-клиента" \
            "Адрес, куда WG-приложение шлёт трафик = ваш локальный free-turn-proxy client (-listen). На том же ПК: 127.0.0.1:9000. Если WG-app на телефоне, а client на ПК — LAN-IP ПК:порт. Подставлю в конфиг и QR."
        while :; do
            ui_input WG_ENDPOINT "Endpoint для WG-клиента (host:port)" "${WG_ENDPOINT:-127.0.0.1:9000}"
            valid_endpoint "$WG_ENDPOINT" && break
            ui_note "Ошибка" "Формат host:port, напр. 127.0.0.1:9000"
        done
    else
        WG_SETUP=0
    fi
}

wizard_obfuscation() {
    ui_menu OBF_PROFILE "Профиль обфускации:" "$OBF_PROFILE" \
        rtpopus "rtpopus  ·  RTP/opus + ChaCha20-Poly1305  (рекомендуется)" \
        none    "none  ·  без обфускации"
    if [ "$OBF_PROFILE" = "none" ]; then
        OBF_KEY=""; return
    fi
    if [ -n "$OBF_KEY" ]; then
        ui_yesno "Сгенерировать НОВЫЙ ключ? (нет — оставить текущий)" "N" && OBF_KEY="$(openssl rand -hex 32)"
    elif ui_yesno "Сгенерировать случайный ключ?" "Y"; then
        OBF_KEY="$(openssl rand -hex 32)"
    else
        while :; do
            ui_input OBF_KEY "64-hex ключ обфускации" ""
            valid_hex64 "$OBF_KEY" && break
            ui_note "Ошибка" "Ключ — ровно 64 hex-символа."
        done
    fi
}

wizard_auth() {
    local def="N"; [ -n "$CLIENTS_FILE_CONF" ] && def="Y"
    if ui_yesno "Включить авторизацию по Client ID?" "$def"; then
        CLIENTS_FILE_CONF="${APP_DIR}/clients/clients.json"
    else
        CLIENTS_FILE_CONF=""
    fi
}

wizard_version() {
    log_info "Получение списка версий с GitHub..."
    local latest others tag def="${VERSION:-latest}"
    latest="$(gh_latest_version || true)"
    others="$([ -n "$latest" ] && gh_recent_versions 2>/dev/null | grep -vx "$latest" | head -4 || true)"

    if [ "$HAS_GUM" = 1 ] && [ -n "$latest" ]; then
        local labels=("latest  (= $latest)") tags=("latest") sel
        while IFS= read -r tag; do [ -n "$tag" ] && { labels+=("$tag"); tags+=("$tag"); }; done <<< "$others"
        labels+=("ввести тег вручную"); tags+=("custom")
        sel=$(gum choose --header "Версия:" --header.foreground "$MD_PRIMARY" \
            --cursor "❯ " --cursor.foreground "$MD_TERTIARY" "${labels[@]}" </dev/tty) || ui_abort
        local i picked="latest"
        for i in "${!labels[@]}"; do [ "${labels[$i]}" = "$sel" ] && picked="${tags[$i]}"; done
        if [ "$picked" = "custom" ]; then ui_input VERSION "Тег (latest или vX.Y.Z)" "$def"; else VERSION="$picked"; fi
    else
        [ -n "$latest" ] && log_info "Последний релиз: $latest"
        ui_input VERSION "Версия (latest или vX.Y.Z)" "$def"
    fi
}

wizard_firewall() {
    ui_yesno "Открыть порт ${LISTEN_PORT}/udp в UFW/iptables?" "Y" && OPEN_FIREWALL=1 || OPEN_FIREWALL=0
}

# Сводка выбранного перед применением.
review_config() {
    local obf_line="выключена"
    [ "$OBF_PROFILE" != "none" ] && obf_line="$OBF_PROFILE"
    local auth_line="выключена"; [ -n "$CLIENTS_FILE_CONF" ] && auth_line="Client ID"
    local wg_line="нет"; [ "$WG_SETUP" = "1" ] && wg_line="установить + настроить"

    if [ "$HAS_GUM" = 1 ]; then
        gum format <<EOF | gum style --border double --border-foreground "$MD_PRIMARY" --padding "1 2" --margin "1 0"
# Проверьте настройки

| Параметр       | Значение |
| -------------- | -------- |
| Метод          | $INSTALL_METHOD |
| Версия         | $VERSION |
| Провайдер      | $PROVIDER |
| Режим          | $PROXY_MODE |
| Порт бэкенда   | 127.0.0.1:$BACKEND_PORT |
| Внешний порт   | 0.0.0.0:$LISTEN_PORT |
| Обфускация     | $obf_line |
| Авторизация    | $auth_line |
| WireGuard      | $wg_line |
EOF
    else
        echo; log_info "Настройки: method=$INSTALL_METHOD version=$VERSION provider=$PROVIDER mode=$PROXY_MODE backend=$BACKEND_PORT listen=$LISTEN_PORT obf=$obf_line auth=$auth_line wireguard=$wg_line"
    fi
    ui_yesno "Применить эти настройки?" "Y" || ui_abort
}

wizard() {
    wizard_method
    wizard_mode
    wizard_provider
    wizard_ports
    wizard_wireguard
    wizard_obfuscation
    wizard_auth
    wizard_version
    wizard_firewall
}

# ═══════════════════════════════════════════════════════════════════════════
# Применение конфигурации
# ═══════════════════════════════════════════════════════════════════════════
init_clients_file() {
    [ -z "$CLIENTS_FILE_CONF" ] && return 0
    mkdir -p "$(dirname "$CLIENTS_FILE_CONF")"
    [ ! -f "$CLIENTS_FILE_CONF" ] && echo '{"clients":{}}' > "$CLIENTS_FILE_CONF" || true
}

# ── WireGuard bootstrap (опциональный) ──────────────────────────────────────
# Поднимает WG-сервер на 127.0.0.1+0.0.0.0:BACKEND_PORT — бэкенд, в который
# free-turn-proxy форвардит расшифрованный UDP. Публичный порт WG в файрвол НЕ
# открываем: трафик приходит только через free-turn-proxy.

wg_install() {
    command -v wg >/dev/null 2>&1 && command -v wg-quick >/dev/null 2>&1 && return 0
    ui_spin "Установка WireGuard" pkg_install wireguard-tools
    command -v wg >/dev/null 2>&1 || { ui_spin "Установка WireGuard (wireguard)" pkg_install wireguard; }
    command -v wg >/dev/null 2>&1 || die "Не удалось установить WireGuard (wg/wg-quick)."
}

wg_detect_wan() {  # имя WAN-интерфейса для NAT
    ip route show default 2>/dev/null | awk '/default/ {print $5; exit}' || true
}

wg_enable_forwarding() {
    echo 'net.ipv4.ip_forward = 1' > /etc/sysctl.d/99-free-turn-proxy-wg.conf
    sysctl -q -w net.ipv4.ip_forward=1 >/dev/null 2>&1 || true
}

# Генерирует server+client ключи и пишет wg0.conf. Идемпотентно: если конфиг
# уже есть — не перетирает (ключи стабильны).
wireguard_bootstrap() {
    [ "$WG_SETUP" = "1" ] || return 0
    wg_install

    if [ -f "$WG_CONF" ]; then
        log_info "WireGuard уже настроен ($WG_CONF) — переустанавливаю только службу."
        systemctl enable --now "wg-quick@${WG_IFACE}" >/dev/null 2>&1 || \
            ui_spin "Запуск wg-quick@${WG_IFACE}" wg-quick up "$WG_IFACE" || true
        return 0
    fi

    wg_enable_forwarding
    local wan; wan="$(wg_detect_wan)"
    [ -z "$wan" ] && { log_warn "WAN-интерфейс не определён — NAT пропущен, настройте вручную."; }

    ( umask 077; wg genkey > "${WG_DIR}/server.key"; wg genkey > "${WG_DIR}/client.key" )
    local srv_priv srv_pub cli_priv cli_pub
    srv_priv="$(cat "${WG_DIR}/server.key")"; srv_pub="$(wg pubkey < "${WG_DIR}/server.key")"
    cli_priv="$(cat "${WG_DIR}/client.key")"; cli_pub="$(wg pubkey < "${WG_DIR}/client.key")"

    local postup="" postdown=""
    if [ -n "$wan" ]; then
        postup="PostUp = iptables -A FORWARD -i %i -j ACCEPT; iptables -A FORWARD -o %i -j ACCEPT; iptables -t nat -A POSTROUTING -o ${wan} -j MASQUERADE"
        postdown="PostDown = iptables -D FORWARD -i %i -j ACCEPT; iptables -D FORWARD -o %i -j ACCEPT; iptables -t nat -D POSTROUTING -o ${wan} -j MASQUERADE"
    fi

    ( umask 077
      cat > "$WG_CONF" <<EOF
[Interface]
Address = ${WG_NET}.1/24
ListenPort = ${BACKEND_PORT}
PrivateKey = ${srv_priv}
${postup}
${postdown}

[Peer]
# первый клиент (ключи в ${WG_CLIENT_CONF})
PublicKey = ${cli_pub}
AllowedIPs = ${WG_NET}.2/32
EOF
    )

    systemctl enable --now "wg-quick@${WG_IFACE}" >/dev/null 2>&1 \
        || ui_spin "Запуск wg-quick@${WG_IFACE}" wg-quick up "$WG_IFACE" \
        || die "Не удалось поднять ${WG_IFACE}."
    log_success "WireGuard ${WG_IFACE} поднят (ListenPort ${BACKEND_PORT})."
    log_warn "WG слушает 0.0.0.0:${BACKEND_PORT}. Порт в firewall НЕ открывается (трафик идёт через free-turn-proxy). Если firewall отсутствует — WG доступен напрямую; закройте ${BACKEND_PORT}/udp вручную."

    # Клиентский конфиг. Endpoint = ЛОКАЛЬНЫЙ free-turn-proxy client (не IP VPS),
    # т.к. WG-приложение подключается к туннелю, а не напрямую к серверу.
    ( umask 077
      cat > "$WG_CLIENT_CONF" <<EOF
[Interface]
PrivateKey = ${cli_priv}
Address = ${WG_NET}.2/32
DNS = 1.1.1.1

[Peer]
PublicKey = ${srv_pub}
AllowedIPs = 0.0.0.0/0
# Endpoint = локальный free-turn-proxy client (-listen), задан при установке.
Endpoint = ${WG_ENDPOINT}
PersistentKeepalive = 25
EOF
    )
    log_info "Клиентский WG-конфиг: ${WG_CLIENT_CONF} (Endpoint ${WG_ENDPOINT}, готов к импорту)."
}

# Рендер QR клиентского WG-конфига в терминал (qrencode).
wg_render_qr() {
    [ "$WG_SETUP" = "1" ] && [ -f "$WG_CLIENT_CONF" ] || return 0
    command -v qrencode >/dev/null 2>&1 || ui_spin "Установка qrencode" pkg_install qrencode
    command -v qrencode >/dev/null 2>&1 || { log_warn "qrencode недоступен — QR пропущен."; return 0; }
    echo
    if [ "$HAS_GUM" = 1 ]; then gum style --foreground "$MD_PRIMARY" --bold "QR клиентского WireGuard-конфига (Endpoint ${WG_ENDPOINT}):"
    else echo "QR клиентского WireGuard-конфига (Endpoint ${WG_ENDPOINT}):"; fi
    qrencode -t ansiutf8 < "$WG_CLIENT_CONF"
    log_info "Сканируйте в приложении WireGuard — конфиг готов к работе."
}

firewall_open() {
    if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
        ufw allow "${LISTEN_PORT}/udp" >/dev/null 2>&1 || log_warn "ufw allow не удался."
        log_success "Порт ${LISTEN_PORT}/udp открыт (UFW)."
    elif command -v iptables >/dev/null 2>&1; then
        if ! iptables -C INPUT -p udp --dport "$LISTEN_PORT" -j ACCEPT 2>/dev/null; then
            iptables -I INPUT -p udp --dport "$LISTEN_PORT" -j ACCEPT 2>/dev/null || log_warn "iptables -I не удался."
            command -v netfilter-persistent >/dev/null 2>&1 && netfilter-persistent save >/dev/null 2>&1 || true
        fi
        log_success "Порт ${LISTEN_PORT}/udp открыт (iptables)."
    else
        log_warn "Брандмауэр не найден — откройте ${LISTEN_PORT}/udp вручную."
    fi
}

ensure_docker() {
    command -v docker >/dev/null 2>&1 && return 0
    if [ "$NONINTERACTIVE" != 1 ] && ! ui_yesno "Docker не найден. Установить?" "Y"; then
        die "Для метода docker нужен Docker."
    fi
    ui_spin "Установка Docker" sh -c 'curl -fsSL https://get.docker.com | sh' || die "Установка Docker не удалась."
    command -v docker >/dev/null 2>&1 || die "Docker не появился в PATH."
}

healthcheck_docker() {
    sleep 2
    [ "$(docker inspect -f '{{.State.Running}}' "$CONTAINER" 2>/dev/null || echo false)" = "true" ] \
        && { log_success "Контейнер работает."; return; }
    log_warn "Контейнер не запущен. Логи:"
    docker logs --tail 40 "$CONTAINER" 2>&1 || true
    die "$CONTAINER не поднялся."
}

apply_docker() {
    ensure_docker
    {
        echo "services:"
        echo "  free-turn-proxy:"
        echo "    image: ${IMAGE}:$(image_tag)"
        echo "    container_name: ${CONTAINER}"
        echo "    network_mode: \"host\""
        echo "    restart: unless-stopped"
        echo "    environment:"
        echo "      - CONNECT_ADDR=$(connect_addr)"
        echo "      - LISTEN_ADDR=0.0.0.0:${LISTEN_PORT}"
        echo "      - MODE=${PROXY_MODE}"
        echo "      - OBF_PROFILE=${OBF_PROFILE}"
        [ "$OBF_PROFILE" != "none" ] && echo "      - OBF_KEY=${OBF_KEY}"
        if [ -n "$CLIENTS_FILE_CONF" ]; then
            local cdir; cdir="$(dirname "$CLIENTS_FILE_CONF")"
            echo "      - CLIENTS_FILE=${CLIENTS_FILE_CONF}"
            echo "    volumes:"
            echo "      - ${cdir}:${cdir}"
        fi
    } > "$COMPOSE_FILE"
    chmod 600 "$COMPOSE_FILE"   # содержит OBF_KEY: не отдавать non-root

    ( cd "$APP_DIR" && ui_spin "Загрузка образа ${IMAGE}:$(image_tag)" docker compose pull ) || die "docker compose pull не удался."
    ( cd "$APP_DIR" && ui_spin "Запуск контейнера" docker compose up -d ) || die "docker compose up не удался."
    healthcheck_docker
}

download_binary() {
    local url
    url="$(resolve_asset_url "$VERSION" "server-linux-${GOARCH}" || true)"
    [ -z "$url" ] && die "Не найден server-linux-${GOARCH} в релизе ${VERSION}."
    ui_spin "Скачивание server-linux-${GOARCH} (${VERSION})" \
        curl -fL -o "${APP_DIR}/server.new" "$url" || die "Не удалось скачать $url"
    chmod +x "${APP_DIR}/server.new"
    mv -f "${APP_DIR}/server.new" "${APP_DIR}/server"
}

healthcheck_systemd() {
    sleep 1
    systemctl is-active --quiet "$SERVICE" && { log_success "Служба активна."; return; }
    log_warn "Служба не активна. Логи:"
    journalctl -u "$SERVICE" --no-pager -n 40 2>&1 || true
    die "$SERVICE не запустилась."
}

apply_systemd() {
    download_binary
    local args; args="-listen 0.0.0.0:${LISTEN_PORT} -connect $(connect_addr) -mode ${PROXY_MODE}"
    [ "$OBF_PROFILE" != "none" ] && args="$args -obf-profile ${OBF_PROFILE} -obf-key ${OBF_KEY}"
    [ -n "$CLIENTS_FILE_CONF" ] && args="$args -clients-file ${CLIENTS_FILE_CONF}"
    cat > "$UNIT_FILE" <<EOF
[Unit]
Description=Free TURN Proxy Server
After=network.target

[Service]
Type=simple
ExecStart=${APP_DIR}/server ${args}
Restart=always
RestartSec=5
User=nobody
Group=nogroup

[Install]
WantedBy=multi-user.target
EOF
    chmod 640 "$UNIT_FILE"   # ExecStart содержит -obf-key: не отдавать non-root
    systemctl daemon-reload
    systemctl enable "$SERVICE" >/dev/null 2>&1 || true
    systemctl restart "$SERVICE" || die "systemctl restart не удался."
    healthcheck_systemd
}

apply() {
    mkdir -p "$APP_DIR"
    init_clients_file
    wireguard_bootstrap
    if [ "$INSTALL_METHOD" = "docker" ]; then apply_docker; else apply_systemd; fi
    [ "$OPEN_FIREWALL" = "1" ] && firewall_open
    save_config
}

print_summary() {
    local ext_ip; ext_ip="$(curl -s --max-time 5 ifconfig.me || echo "IP_СЕРВЕРА")"
    local add_cmd
    if [ "$INSTALL_METHOD" = "docker" ]; then
        add_cmd="docker exec -it ${CONTAINER} /app/server clients add <id>"
    else
        add_cmd="${APP_DIR}/server -clients-file ${CLIENTS_FILE_CONF} clients add <id>"
    fi

    if [ "$HAS_GUM" = 1 ]; then
        {
            echo "# Готово ✓"
            echo
            echo "| | |"
            echo "|---|---|"
            echo "| **Сервер (peer)** | \`${ext_ip}:${LISTEN_PORT}\` |"
            echo "| **Провайдер** | ${PROVIDER} (клиент: \`-provider ${PROVIDER}\`) |"
            echo "| **Режим** | ${PROXY_MODE} |"
            echo "| **Версия** | ${VERSION} |"
            echo "| **Метод** | ${INSTALL_METHOD} |"
            [ "$OBF_PROFILE" != "none" ] && echo "| **Обфускация** | ${OBF_PROFILE} |"
            [ "$OBF_PROFILE" != "none" ] && echo "| **Ключ** | \`${OBF_KEY}\` |"
            echo
            if [ -n "$CLIENTS_FILE_CONF" ]; then
                echo "Client ID auth включён. Добавить клиента:"
                echo
                echo '```'
                echo "$add_cmd"
                echo '```'
            fi
            if [ "$WG_SETUP" = "1" ]; then
                echo "WireGuard поднят. Клиентский конфиг (\`Endpoint ${WG_ENDPOINT}\`, готов к импорту):"
                echo
                echo '```'
                echo "$WG_CLIENT_CONF"
                echo '```'
            fi
            echo "Документация: https://github.com/${REPO}"
        } | gum format | gum style --border rounded --border-foreground "$MD_SUCCESS" --padding "1 2" --margin "1 0"
    else
        echo; log_success "Готово!"
        echo "--------------------------------------------------------"
        echo "Сервер (peer): ${ext_ip}:${LISTEN_PORT}"
        echo "Провайдер (клиент): -provider ${PROVIDER}"
        echo "Режим: ${PROXY_MODE} | Версия: ${VERSION} | Метод: ${INSTALL_METHOD}"
        [ "$OBF_PROFILE" != "none" ] && echo "Обфускация: ${OBF_PROFILE} | Ключ: ${OBF_KEY}"
        [ -n "$CLIENTS_FILE_CONF" ] && { echo "Client ID auth включён. Добавить клиента:"; echo "  $add_cmd"; }
        [ "$WG_SETUP" = "1" ] && echo "WireGuard поднят. Клиентский конфиг: $WG_CLIENT_CONF (Endpoint $WG_ENDPOINT, готов)."
        echo "--------------------------------------------------------"
        echo "Документация: https://github.com/${REPO}"
    fi
    wg_render_qr
}

# ═══════════════════════════════════════════════════════════════════════════
# Сценарии
# ═══════════════════════════════════════════════════════════════════════════
flow_install() { wizard; validate_config; review_config; apply; print_summary; }

flow_update() {
    [ -f "$CONF_FILE" ] || { log_warn "Конфиг не найден — переконфигурация."; flow_install; return; }
    wizard_version
    [ -z "$OPEN_FIREWALL" ] && OPEN_FIREWALL=0
    validate_config; apply; print_summary
}

do_uninstall() {  # REMOVE_DIR(0|1)
    if [ "$INSTALL_METHOD" = "docker" ]; then
        if [ -f "$COMPOSE_FILE" ]; then
            ( cd "$APP_DIR" && docker compose down ) || log_warn "docker compose down — ошибка."
        fi
    else
        systemctl disable --now "$SERVICE" >/dev/null 2>&1 || true
        rm -f "$UNIT_FILE"; systemctl daemon-reload || true
    fi
    log_success "Служба остановлена и удалена."
    if [ "$1" = "1" ]; then rm -rf "$APP_DIR"; log_success "Каталог $APP_DIR удалён."
    else log_info "Каталог сохранён: $APP_DIR"; fi
}

flow_uninstall() {
    ui_yesno "Удалить Free Turn Proxy Server?" "N" || ui_abort
    local rm_dir=0
    ui_yesno "Удалить каталог ${APP_DIR} (ключи, clients.json, конфиг)?" "N" && rm_dir=1
    do_uninstall "$rm_dir"
}

menu_existing() {
    local choice
    ui_menu choice "Установка найдена (метод: ${INSTALL_METHOD}, версия: ${VERSION:-?}). Действие:" "update" \
        update      "Обновить версию  (конфиг сохранён)" \
        reconfigure "Переконфигурировать" \
        uninstall   "Удалить" \
        exit        "Выход"
    case "$choice" in
        update)      flow_update ;;
        reconfigure) flow_install ;;
        uninstall)   flow_uninstall ;;
        exit)        ui_abort ;;
    esac
}

run_noninteractive() {
    is_installed && load_config || true
    apply_overrides
    if [ "${ACTION:-install}" = "uninstall" ]; then do_uninstall "$PURGE"; return; fi
    if [ "${ACTION:-}" = "update" ] && ! is_installed; then
        die "--update: установка не найдена. Запустите без --update."
    fi
    validate_config
    [ -z "$OPEN_FIREWALL" ] && OPEN_FIREWALL=1
    log_info "Применяю конфигурацию (метод: ${INSTALL_METHOD})..."
    apply; print_summary
}

# ═══════════════════════════════════════════════════════════════════════════
# CLI
# ═══════════════════════════════════════════════════════════════════════════
usage() {
    cat <<EOF
Free Turn Proxy — установщик сервера.

  sudo bash install.sh                 интерактивный мастер (gum)
  sudo bash install.sh -y [опции]      non-interactive

Опции:
  -y, --yes, --non-interactive   без вопросов
  --method docker|systemd        метод (default docker)
  --provider vk                  провайдер TURN-creds для клиента (default vk)
  --mode   udp|tcp               режим (default udp)
  --backend-port N               порт бэкенда (default udp→51820 / tcp→443)
  --listen-port N                внешний порт (default 56000)
  --obf rtpopus|none             обфускация (default rtpopus)
  --obf-key HEX64                ключ (нет → сгенерируется)
  --clients-auth | --no-clients-auth   авторизация Client ID (default off)
  --wireguard | --no-wireguard   поднять WG-сервер на backend-порту (udp; default off)
  --wg-endpoint HOST:PORT        Endpoint для WG-клиента (адрес локального client; default 127.0.0.1:9000)
  --version latest|vX.Y.Z        версия (default latest)
  --firewall | --no-firewall     открывать порт (NI default: да)
  --update                       обновить версию (конфиг сохранён)
  --uninstall [--purge]          удалить (--purge — снести каталог)
  -h, --help

Примеры:
  sudo bash install.sh -y --method docker --mode udp --backend-port 51820
  sudo bash install.sh -y --update --version v1.2.3
  sudo bash install.sh -y --uninstall --purge
EOF
}

parse_args() {
    while [ $# -gt 0 ]; do
        case "$1" in
            -y | --yes | --non-interactive) NONINTERACTIVE=1 ;;
            --method)
                case "${2:-}" in
                    docker | systemd) OVERRIDES+=("INSTALL_METHOD=${2}") ;;
                    *) die "--method: docker|systemd" ;;
                esac; shift ;;
            --provider)
                case "${2:-}" in
                    vk) OVERRIDES+=("PROVIDER=${2}") ;;
                    *) die "--provider: vk" ;;
                esac; shift ;;
            --mode)            OVERRIDES+=("PROXY_MODE=${2:-}"); shift ;;
            --backend-port)    OVERRIDES+=("BACKEND_PORT=${2:-}"); shift ;;
            --listen-port)     OVERRIDES+=("LISTEN_PORT=${2:-}"); shift ;;
            --obf)             OVERRIDES+=("OBF_PROFILE=${2:-}"); shift ;;
            --obf-key)         OVERRIDES+=("OBF_KEY=${2:-}"); shift ;;
            --clients-auth)    OVERRIDES+=("CLIENTS_FILE_CONF=${APP_DIR}/clients/clients.json") ;;
            --no-clients-auth) OVERRIDES+=("CLIENTS_FILE_CONF=") ;;
            --wireguard)       OVERRIDES+=("WG_SETUP=1") ;;
            --no-wireguard)    OVERRIDES+=("WG_SETUP=0") ;;
            --wg-endpoint)     OVERRIDES+=("WG_ENDPOINT=${2:-}"); shift ;;
            --version)         OVERRIDES+=("VERSION=${2:-}"); shift ;;
            --firewall)        OPEN_FIREWALL=1 ;;
            --no-firewall)     OPEN_FIREWALL=0 ;;
            --update)          ACTION="update"; NONINTERACTIVE=1 ;;
            --uninstall)       ACTION="uninstall"; NONINTERACTIVE=1 ;;
            --purge)           PURGE=1 ;;
            -h | --help)       usage; exit 0 ;;
            *) die "Неизвестный аргумент: $1 (см. --help)" ;;
        esac
        shift
    done
}

# ═══════════════════════════════════════════════════════════════════════════
# main
# ═══════════════════════════════════════════════════════════════════════════
main() {
    parse_args "$@"
    [ "$(id -u)" -ne 0 ] && die "Запустите от root (sudo)."
    ensure_base_deps
    detect_arch

    if [ "$NONINTERACTIVE" = 1 ]; then
        run_noninteractive
        return
    fi

    ensure_gum
    ui_banner
    if is_installed; then load_config; menu_existing; else flow_install; fi
}

main "$@"
