#!/usr/bin/env bash
# Регенерирует internal/singbox/vlink/testdata/singbox-schema.json из ИСХОДНИКОВ
# форка (hoaxisr/amnezia-box). Схема — машинно-читаемый контракт того sing-box,
# в который мы собираем конфигурацию: по ней тест ловит поля, исчезнувшие или
# переименованные при бампе версии.
#
# Запускать ПОСЛЕ regen-embedded.sh, из чекаута форка на том же теге.
#
# Usage:
#   ./scripts/regen-singbox-schema.sh [path-to-fork-checkout]
# Env:
#   SINGBOX_FORK_DIR   чекаут форка (default ~/buthole/amnezia-box-awg14)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
FORK="${1:-${SINGBOX_FORK_DIR:-$HOME/buthole/amnezia-box-awg14}}"
OUT="$PROJECT_ROOT/internal/singbox/vlink/testdata/singbox-schema.json"

[ -d "$FORK" ] || { echo "нет чекаута форка: $FORK" >&2; exit 1; }

pinned="$(sed -n 's/^const RequiredVersion = "\(.*\)"$/\1/p' \
    "$PROJECT_ROOT/internal/singbox/installer/embedded.go")"
forked="$(cd "$FORK" && git describe --tags --abbrev=0 2>/dev/null || echo '?')"
if [ "$pinned" != "$forked" ]; then
    echo "ВНИМАНИЕ: пин $pinned, а форк на $forked — схема опишет не тот бинарь" >&2
fi

echo "Генерация схемы из $FORK ($forked)"
(cd "$FORK" && go run ./cmd/sing-box schema) > "$OUT"
echo "Записано: $OUT ($(wc -c < "$OUT") байт)"
