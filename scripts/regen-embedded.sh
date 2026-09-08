#!/usr/bin/env bash
# Регенерирует internal/singbox/installer/embedded.go из РЕЛИЗА ФОРКА
# hoaxisr/amnezia-box (наш sing-box = base + AmneziaWG/AWG3 + mieru + xhttp).
# Сборку owns форк (его CI release-entware.yml); awg-manager только ПОТРЕБЛЯЕТ.
# Компиляции здесь нет — только скачивание ассетов, sha256/size, запись embedded.go.
#
# Usage:
#   ./scripts/regen-embedded.sh <release-tag>
#   (release-tag = VERSION, напр. 1.14.0-alpha.48-awg3-xhttp-mieru)
# Env:
#   SINGBOX_FORK_REPO   (default hoaxisr/amnezia-box)
#   SINGBOX_URL_BASE    базовый URL для embedded.go (default — GitHub-релиз форка).
#                       Для прод-зеркала: http://repo.hoaxisr.ru/singbox/<tag>
# Requires: gh CLI authenticated, sha256sum, stat, python3, gofmt.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

VERSION="${1:?usage: regen-embedded.sh <release-tag>}"
FORK_REPO="${SINGBOX_FORK_REPO:-hoaxisr/amnezia-box}"
URL_BASE="${SINGBOX_URL_BASE:-https://github.com/${FORK_REPO}/releases/download/${VERSION}}"
EMBEDDED_GO="$PROJECT_ROOT/internal/singbox/installer/embedded.go"
ARCHES=(mipsel-3.4 mips-3.4 aarch64-3.10)
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "Regenerating embedded.go from fork release $FORK_REPO@$VERSION"

# RequiredVersion = release-тег.
python3 - <<PY
import pathlib, re
p = pathlib.Path("$EMBEDDED_GO")
text = p.read_text()
text = re.sub(r'const RequiredVersion = "[^"]*"',
              f'const RequiredVersion = "$VERSION"', text, count=1)
p.write_text(text)
PY

for arch in "${ARCHES[@]}"; do
    asset="singbox-${VERSION}-${arch}"
    dest="$TMP/$asset"

    echo "  Downloading $asset from $FORK_REPO release $VERSION..."
    gh release download "$VERSION" --repo "$FORK_REPO" --pattern "$asset" --dir "$TMP"
    if [[ ! -f "$dest" ]]; then
        echo "ERROR: $asset not present in $FORK_REPO release $VERSION. Run release-entware CI first." >&2
        exit 1
    fi

    sha="$(sha256sum "$dest" | awk '{print $1}')"
    size="$(stat -c '%s' "$dest")"
    url="${URL_BASE}/${asset}"

    URL="$url" SHA="$sha" SIZE="$size" ARCH="$arch" EMBEDDED_GO="$EMBEDDED_GO" python3 - <<'PY'
import os, pathlib, re, sys
p = pathlib.Path(os.environ["EMBEDDED_GO"])
arch = os.environ["ARCH"]
text = p.read_text()
pattern = re.compile(
    rf'(\t"{re.escape(arch)}":\s*)'
    r'\{Version: RequiredVersion, URL: "[^"]*", SHA256: "[^"]*"(?:, Size: \d+)?\},'
)
replacement = (
    rf'\1{{Version: RequiredVersion, URL: "{os.environ["URL"]}", SHA256: "{os.environ["SHA"]}", Size: {os.environ["SIZE"]}}},'
)
text, n = pattern.subn(replacement, text, count=1)
if n != 1:
    sys.stderr.write(f"ERROR: failed to update embedded.go entry for {arch}\n")
    sys.exit(1)
p.write_text(text)
PY

    echo "  Updated $arch (sha=${sha:0:12}..., size=$size)"
done

# RequiredTags: теги сборки читаются из mipsel-ассета под qemu-user.
# Без qemu константа остаётся прежней — предупреждаем, правится руками.
mipsel_bin="$TMP/singbox-${VERSION}-mipsel-3.4"
if command -v qemu-mipsel-static >/dev/null 2>&1; then
    chmod +x "$mipsel_bin"
    tags="$(qemu-mipsel-static "$mipsel_bin" version 2>/dev/null | sed -n 's/^Tags: *//p' | head -1)"
    if [[ -n "$tags" ]]; then
        TAGS="$tags" EMBEDDED_GO="$EMBEDDED_GO" python3 - <<'PY'
import os, pathlib, re, sys
p = pathlib.Path(os.environ["EMBEDDED_GO"])
text = p.read_text()
items = ", ".join(f'"{t.strip()}"' for t in os.environ["TAGS"].split(",") if t.strip())
text, n = re.subn(r'var RequiredTags = \[\]string\{[^}]*\}',
                  f'var RequiredTags = []string{{{items}}}', text, count=1)
if n != 1:
    sys.stderr.write("ERROR: RequiredTags not found in embedded.go\n")
    sys.exit(1)
p.write_text(text)
PY
        echo "  Updated RequiredTags: $tags"
    else
        echo "WARNING: qemu-mipsel-static printed no Tags — update RequiredTags in embedded.go by hand" >&2
    fi
else
    echo "WARNING: qemu-mipsel-static not found — update RequiredTags in embedded.go by hand" >&2
fi

gofmt -w "$EMBEDDED_GO"
echo "Done. Diff:"
git diff --stat "$EMBEDDED_GO" || true
