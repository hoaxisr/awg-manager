#!/bin/bash
# Сборка модуля ядра (awg-proxy | awgm-relay) под каждую группу ядер Keenetic:
#   ./scripts/build-kmods.sh awgm-relay
#   ONLY_MODELS="KN-1810" ./scripts/build-kmods.sh awg-proxy
#
# По одной сборке на SoC-группу. Чужая сборка при совпавшем vermagic
# (CONFIG_MODVERSIONS выключен) грузится и ходит по структурам ядра с неверными
# смещениями — F342 (KN-2112 ушёл в ребут), поэтому arch-default за другой SoC
# не выдаётся.
#
#   модель    -> файл                  -> что покрывает
#   KN-1812   -> <ko>-arm64.ko          mt7988 + aarch64 default
#   KN-1811   -> <ko>-mt7622.ko         mt7622
#   KN-3811   -> <ko>-mt7981.ko         mt7981
#   KN-1810   -> <ko>-mt7621.ko         mt7621 mipsel SMP + mipsel default
#   KN-1212   -> <ko>-mt7628.ko         mt7628 mipsel non-SMP
#   KN-1912   -> <ko>-en7528.ko         en7528
#   KN-2010   -> <ko>-mips.ko           en7512 + mips BE default
#   KN-2112   -> <ko>-en7516.ko         en7516
#   KN-1011   -> <ko>-KN-1011.ko        mt7621 HIGHMEM
#
# Пакет SDK (package/kernel/<модуль>/) пересобирается из kmod/<модуль>/ ЭТОГО
# чекаута на каждом запуске; результат — kmod/<модуль>/out/ этого же чекаута,
# откуда его берёт scripts/build-ipk.sh (F477: прежний скрипт awg_proxy жил
# в SDK, собирал из своей копии пакета и писал в основной чекаут).

set -e
MODULE="${1:?usage: $0 awg-proxy|awgm-relay}"
case "$MODULE" in
    awg-proxy|awgm-relay) ;;
    *) echo "unknown module: $MODULE" >&2; exit 2 ;;
esac
KO="${MODULE//-/_}"
REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
SDK_DIR="${SDK_DIR:-/home/hoaxisr/buthole/keenetic-sdk}"
VENDORDIR="$REPO_DIR/kmod/$MODULE/out"
LOGFILE="$VENDORDIR/$MODULE-build.log"
REPORT="$VENDORDIR/$MODULE-report.txt"
mkdir -p "$VENDORDIR"

log() { echo "[$(date '+%H:%M:%S')] $*" | tee -a "$LOGFILE"; }

# модель файл
TARGETS=(
    "KN-1812 $KO-arm64.ko"
    "KN-1811 $KO-mt7622.ko"
    "KN-3811 $KO-mt7981.ko"
    "KN-1810 $KO-mt7621.ko"
    "KN-1212 $KO-mt7628.ko"
    "KN-1912 $KO-en7528.ko"
    "KN-2010 $KO-mips.ko"
    "KN-2112 $KO-en7516.ko"
    "KN-1011 $KO-KN-1011.ko"
)

# ONLY_MODELS="KN-2112 KN-1912" — собрать лишь эти модели. Остальные .ko в
# $VENDORDIR не трогаются, поэтому лог и отчёт дописываются, а не обнуляются.
if [ -n "${ONLY_MODELS:-}" ]; then
    keep=()
    for entry in "${TARGETS[@]}"; do
        case " $ONLY_MODELS " in *" ${entry%% *} "*) keep+=("$entry") ;; esac
    done
    TARGETS=("${keep[@]}")
else
    : > "$LOGFILE"
    : > "$REPORT"
fi

# Пакет в SDK: Makefile в корне, src/ и COPYING рядом (Build/Prepare берёт ./src).
PKG_DIR="$SDK_DIR/package/kernel/$MODULE"
rm -rf "$PKG_DIR"
mkdir -p "$PKG_DIR"
cp -r "$REPO_DIR/kmod/$MODULE/package/Makefile" \
      "$REPO_DIR/kmod/$MODULE/src" \
      "$REPO_DIR/kmod/$MODULE/COPYING" "$PKG_DIR/"

cd "$SDK_DIR"

total=${#TARGETS[@]}
count=0
failed=0

log "=== Building $KO.ko for $total Keenetic kernel-ABI groups ==="
log "Output: $VENDORDIR"

for entry in "${TARGETS[@]}"; do
    model="${entry%% *}"
    out_name="${entry##* }"
    count=$((count + 1))
    log ""
    log "[$count/$total] Building $model -> $out_name"

    if ! ./configure.sh "$model" >> "$LOGFILE" 2>&1; then
        log "  FAILED: configure.sh $model"
        echo "FAIL $model -> $out_name (configure)" >> "$REPORT"
        failed=$((failed + 1))
        continue
    fi

    echo "CONFIG_PACKAGE_kmod-$MODULE=m" >> .config
    make defconfig >> "$LOGFILE" 2>&1

    # Ядро собирается, если его ещё нет. Исходник 4.9 зовёт идентификатор
    # constexpr, GCC 13+ его отвергает — патч scripts/unifdef.c и повтор.
    SOC=$(grep '^CONFIG_TARGET_BOARD=' .config | cut -d'"' -f2)
    KERNEL_DIR=$(ls -d build_dir/target-*/linux-${SOC}_${model}/linux-4.9 2>/dev/null || true)
    if [ -z "$KERNEL_DIR" ] || [ ! -f "$KERNEL_DIR/.modules" ]; then
        log "  Kernel not ready, compiling (≈6min first time per SoC)…"
        if ! make target/linux/compile V=s >> "$LOGFILE" 2>&1; then
            KERNEL_DIR=$(ls -d build_dir/target-*/linux-${SOC}_${model}/linux-4.9 2>/dev/null)
            if [ -n "$KERNEL_DIR" ] && [ -f "$KERNEL_DIR/scripts/unifdef.c" ]; then
                log "  Applying constexpr patch and retrying…"
                sed -i 's/\bconstexpr\b/is_constexpr/g' "$KERNEL_DIR/scripts/unifdef.c"
                if ! make target/linux/compile V=s >> "$LOGFILE" 2>&1; then
                    log "  FAILED: kernel compile for $model"
                    echo "FAIL $model -> $out_name (kernel compile)" >> "$REPORT"
                    failed=$((failed + 1))
                    continue
                fi
            else
                log "  FAILED: kernel compile for $model (no unifdef.c to patch)"
                echo "FAIL $model -> $out_name (kernel compile)" >> "$REPORT"
                failed=$((failed + 1))
                continue
            fi
        fi
    fi

    # Версия — из Makefile пакета: build_dir хранит каталоги прошлых версий,
    # и глоб взял бы не тот.
    PKG_VERSION=$(awk -F= '/^PKG_VERSION:=/ {print $2; exit}' \
        "package/kernel/$MODULE/Makefile" | tr -d ' ')
    if [ -z "$PKG_VERSION" ]; then
        log "  FAILED: cannot read PKG_VERSION from $MODULE/Makefile"
        echo "FAIL $model -> $out_name (no PKG_VERSION)" >> "$REPORT"
        failed=$((failed + 1))
        continue
    fi

    rm -rf build_dir/target-*/linux-${SOC}_${model}/$MODULE-* 2>/dev/null || true

    make "package/kernel/$MODULE/clean" V=s >> "$LOGFILE" 2>&1 || true
    if ! make "package/kernel/$MODULE/compile" V=s >> "$LOGFILE" 2>&1; then
        log "  FAILED: $MODULE compile for $model"
        echo "FAIL $model -> $out_name (compile)" >> "$REPORT"
        failed=$((failed + 1))
        continue
    fi

    KO_PATH=$(ls build_dir/target-*/linux-${SOC}_${model}/$MODULE-${PKG_VERSION}/src/$KO.ko 2>/dev/null | head -1)
    if [ -z "$KO_PATH" ] || [ ! -f "$KO_PATH" ]; then
        log "  FAILED: built .ko not found for $model"
        echo "FAIL $model -> $out_name (missing artifact)" >> "$REPORT"
        failed=$((failed + 1))
        continue
    fi
    cp "$KO_PATH" "$VENDORDIR/$out_name"
    SIZE=$(stat -c%s "$VENDORDIR/$out_name")
    VER=$(strings "$VENDORDIR/$out_name" | grep '^version=' | head -1 | cut -d= -f2)
    SHA=$(sha256sum "$VENDORDIR/$out_name" | awk '{print substr($1, 1, 12)}')
    log "  OK  $out_name  size=$SIZE  version=$VER  sha256=$SHA"
    echo "OK   $model -> $out_name  ${SIZE}b  v${VER}  sha=${SHA}" >> "$REPORT"
done

log ""
log "=== Summary ==="
log "Built: $((total - failed))/$total"
if [ "$failed" -gt 0 ]; then
    log "Failed: $failed (see $REPORT)"
fi
cat "$REPORT" | tee -a "$LOGFILE"

# Совпавший sha у двух файлов = лишняя копия в IPK (не ошибка, а место).
log ""
log "=== sha256 dedupe audit ==="
(cd "$VENDORDIR" && sha256sum "$KO"-*.ko 2>/dev/null | sort | uniq -c -w 64 | tee -a "$LOGFILE")

exit "$failed"
