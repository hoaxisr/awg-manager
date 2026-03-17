#!/bin/bash
# Dev release: build all architectures and publish to awg-manager-dev repo.
# Does NOT bump VERSION — uses current version with -dev.N suffix.
#
# Usage:
#   ./scripts/dev-release.sh              # auto-increment dev number
#   ./scripts/dev-release.sh "test notes" # with notes

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
DEV_REPO="$(dirname "$PROJECT_ROOT")/awg-manager-dev"

# All 3 target architectures (built sequentially — shared build/bin/ dir)
ARCHITECTURES="mipsel-3.4 mips-3.4 aarch64-3.10"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

log()   { echo -e "${GREEN}[DEV]${NC} $1"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }
step()  { echo -e "\n${CYAN}${BOLD}==> $1${NC}"; }

# --- Validation ---
validate() {
    step "Validating"

    if [[ ! -d "$DEV_REPO" ]]; then
        error "Dev repo not found at $DEV_REPO\nClone it: git clone https://github.com/hoaxisr/awg-manager-dev.git $(dirname "$PROJECT_ROOT")/awg-manager-dev"
    fi

    if [[ ! -d "$DEV_REPO/.git" ]]; then
        error "$DEV_REPO is not a git repository"
    fi

    log "Dev repo: $DEV_REPO"
}

# --- Version ---
resolve_dev_version() {
    step "Resolving dev version"

    local base_version
    base_version=$(cat "$PROJECT_ROOT/VERSION")

    # Find highest existing dev number for this base version
    local max_dev=0
    shopt -s nullglob
    for arch_dir in "$DEV_REPO"/*/; do
        for ipk in "$arch_dir"awg-manager_${base_version}-dev.*_*.ipk; do
            local name
            name=$(basename "$ipk")
            local dev_num
            dev_num=$(echo "$name" | sed -n "s/.*-dev\.\([0-9]*\)_.*/\1/p")
            if [[ -n "$dev_num" && "$dev_num" -gt "$max_dev" ]]; then
                max_dev=$dev_num
            fi
        done
    done
    shopt -u nullglob

    DEV_NUM=$((max_dev + 1))
    DEV_VERSION="${base_version}-dev.${DEV_NUM}"

    log "Base version: $base_version"
    log "Dev version: ${BOLD}$DEV_VERSION${NC}"
}

# --- Build ---
build_all() {
    step "Cleaning dist/"
    rm -f "$PROJECT_ROOT/dist/"*.ipk

    step "Building all architectures (sequentially)"
    for arch in $ARCHITECTURES; do
        log "Building ${BOLD}$arch${NC}..."
        "$SCRIPT_DIR/build-ipk.sh" "$DEV_VERSION" "$arch"
        echo ""
    done

    log "Built packages:"
    ls -lh "$PROJECT_ROOT/dist/"*.ipk
}

# --- Publish ---
publish() {
    step "Publishing to awg-manager-dev"

    cd "$DEV_REPO"
    git pull --rebase origin main 2>/dev/null || true

    # Remove old dev versions for this base version (keep repo clean)
    local base_version
    base_version=$(cat "$PROJECT_ROOT/VERSION")
    local removed=()
    shopt -s nullglob
    for arch in $ARCHITECTURES; do
        local arch_dir="${arch}-kn"
        for old_ipk in "$DEV_REPO/$arch_dir"/awg-manager_${base_version}-dev.*_*.ipk; do
            rm -f "$old_ipk"
            removed+=("$arch_dir/$(basename "$old_ipk")")
        done
    done
    shopt -u nullglob
    if [[ ${#removed[@]} -gt 0 ]]; then
        log "Removed ${#removed[@]} old dev packages"
    fi

    local files_added=()
    for arch in $ARCHITECTURES; do
        local arch_dir="${arch}-kn"
        local ipk="$PROJECT_ROOT/dist/awg-manager_${DEV_VERSION}_${arch_dir}.ipk"
        if [[ -f "$ipk" ]]; then
            cp "$ipk" "$DEV_REPO/$arch_dir/"
            files_added+=("$arch_dir/$(basename "$ipk")")
            log "Copied to $arch_dir/"
        else
            warn "Missing: $ipk"
        fi
    done

    if [[ ${#files_added[@]} -eq 0 ]]; then
        error "No packages to publish"
    fi

    # Stage both removals and additions
    for arch in $ARCHITECTURES; do
        git add -A "${arch}-kn/"
    done
    git commit -m "awg-manager ${DEV_VERSION}"
    git push origin main

    log "Pushed to GitHub"

    step "Triggering reindex"
    gh workflow run "Update Entware Repository Indexes" --repo hoaxisr/awg-manager-dev 2>/dev/null || warn "Workflow trigger failed (will run on push anyway)"
}

# --- Summary ---
print_summary() {
    local notes="${1:-}"

    step "Dev release ${DEV_VERSION} published!"

    echo ""
    echo -e "${BOLD}Feed URLs:${NC}"
    for arch in $ARCHITECTURES; do
        echo "  https://hoaxisr.github.io/awg-manager-dev/${arch}-kn"
    done

    if [[ -n "$notes" ]]; then
        echo ""
        echo -e "${BOLD}Notes:${NC} $notes"
    fi

    echo ""
}

# --- Main ---
NOTES="${1:-}"

validate
resolve_dev_version
build_all
publish
print_summary "$NOTES"
