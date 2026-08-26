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
# Точный describe, а не --abbrev=0: коммиты поверх тега — это уже другой
# декодер, и схема описала бы не тот бинарь, что вшит. Расхождение фатально:
# молча записанная схема из будущей версии хуже отсутствия схемы.
forked="$(cd "$FORK" && git describe --tags 2>/dev/null || echo '?')"
if [ "$pinned" != "$forked" ]; then
    ahead="$(cd "$FORK" && git log --oneline "${pinned}..HEAD" 2>/dev/null || true)"
    if [ -z "$ahead" ] || [ "${SCHEMA_ALLOW_AHEAD:-}" != "1" ]; then
        echo "ОШИБКА: пин $pinned, форк на $forked — схема описала бы не тот бинарь." >&2
        if [ -n "$ahead" ]; then
            echo "Поверх тега $pinned лежит:" >&2
            echo "$ahead" >&2
            echo "Если эти коммиты не меняют декодирование (только schema/DescribeSchema)," >&2
            echo "повторите с SCHEMA_ALLOW_AHEAD=1." >&2
        else
            echo "Переведите чекаут форка на тег $pinned и повторите." >&2
        fi
        exit 1
    fi
    echo "ВНИМАНИЕ: форк впереди тега $pinned, разрешено явно (SCHEMA_ALLOW_AHEAD=1):" >&2
    echo "$ahead" >&2
fi

echo "Генерация схемы из $FORK ($forked)"
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT
(cd "$FORK" && go run ./cmd/sing-box schema) > "$tmp"
# Версия внутри схемы — то, что связывает её с пином: без штампа забытый
# regen при бампе оставил бы тест проверять старый контракт, и поле,
# удалённое форком, прошло бы зелёным (ровно класс #806).
python3 - "$tmp" "$OUT" "$pinned" <<'EOF'
import json, sys
src, dst, version = sys.argv[1], sys.argv[2], sys.argv[3]
with open(src) as f:
    schema = json.load(f)
schema["x-singbox-version"] = version
with open(dst, "w") as f:
    json.dump(schema, f, indent=2, ensure_ascii=False)
    f.write("\n")
EOF
echo "Записано: $OUT ($(wc -c < "$OUT") байт), версия $pinned"
