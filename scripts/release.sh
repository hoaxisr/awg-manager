#!/bin/bash
# Full release script: bump version, build all architectures, tag, push, create GitHub Release,
# publish to entware-repo.
#
# Usage:
#   ./scripts/release.sh patch "release notes"
#   ./scripts/release.sh minor "new features"
#   ./scripts/release.sh major "breaking changes"
#   ./scripts/release.sh 2.1.0 "first stable release"
#   ./scripts/release.sh 2.1.0-rc.1 "release candidate"
#   ./scripts/release.sh patch              # notes default to "release: vX.Y.Z"

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
ENTWARE_REPO="$(dirname "$PROJECT_ROOT")/entware-repo"

# All 3 target architectures (built sequentially — shared build/bin/ dir)
ARCHITECTURES="mipsel-3.4 mips-3.4 aarch64-3.10"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

log()   { echo -e "${GREEN}[RELEASE]${NC} $1"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }
step()  { echo -e "\n${CYAN}${BOLD}==> $1${NC}"; }

# --- Usage ---
usage() {
    cat <<EOF
Usage: $0 [patch|minor|major|VERSION] [release notes]

Version bump:
  patch  - 2.0.9 → 2.0.10   (bugfixes)
  minor  - 2.0.9 → 2.1.0    (new features)
  major  - 2.0.9 → 3.0.0    (breaking changes)

Explicit version:
  2.1.0       - set exact version
  2.1.0-rc.1  - pre-release (auto-detected for GitHub)

Examples:
  $0 patch "fix: tunnel restart race condition"
  $0 2.1.0 "first stable release"
  $0 minor
EOF
    exit 1
}

# --- Validation ---
validate_prerequisites() {
    step "Validating prerequisites"

    # Check gh CLI
    if ! command -v gh &>/dev/null; then
        error "GitHub CLI (gh) not found. Install: https://cli.github.com/"
    fi

    if ! gh auth status &>/dev/null; then
        error "Not authenticated with GitHub. Run: gh auth login"
    fi
    log "GitHub CLI: authenticated"

    # Check git remote
    if ! git -C "$PROJECT_ROOT" remote get-url origin &>/dev/null; then
        error "No git remote 'origin' configured. Run: git remote add origin git@github.com:hoaxisr/awg-manager.git"
    fi
    log "Git remote: $(git -C "$PROJECT_ROOT" remote get-url origin)"

    # Check entware-repo
    if [[ ! -d "$ENTWARE_REPO/.git" ]]; then
        error "Entware repo not found at $ENTWARE_REPO\nClone it: git clone https://github.com/hoaxisr/entware-repo.git $(dirname "$PROJECT_ROOT")/entware-repo"
    fi
    log "Entware repo: $ENTWARE_REPO"

    # Check clean working tree (allow untracked)
    if ! git -C "$PROJECT_ROOT" diff --quiet HEAD 2>/dev/null; then
        warn "Working tree has uncommitted changes"
        git -C "$PROJECT_ROOT" status --short
        echo ""
        read -rp "Continue anyway? [y/N] " answer
        [[ "$answer" =~ ^[Yy]$ ]] || exit 1
    fi
}

# --- Version handling ---
# Strip pre-release suffix: 2.0.9-rc.1 → 2.0.9
strip_prerelease() {
    echo "$1" | sed 's/-.*//'
}

bump_version() {
    local current="$1"
    local bump_type="$2"

    # Strip any pre-release suffix before bumping
    local base
    base=$(strip_prerelease "$current")

    IFS='.' read -r major minor patch <<< "$base"

    case "$bump_type" in
        patch) patch=$((patch + 1)) ;;
        minor) minor=$((minor + 1)); patch=0 ;;
        major) major=$((major + 1)); minor=0; patch=0 ;;
    esac

    echo "${major}.${minor}.${patch}"
}

resolve_version() {
    local input="$1"
    local current
    current=$(cat "$PROJECT_ROOT/VERSION")

    log "Current version: $current"

    case "$input" in
        patch|minor|major)
            NEW_VERSION=$(bump_version "$current" "$input")
            ;;
        [0-9]*)
            # Explicit version — basic validation
            if ! [[ "$input" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.]+)?$ ]]; then
                error "Invalid version format: $input (expected: X.Y.Z or X.Y.Z-suffix)"
            fi
            NEW_VERSION="$input"
            ;;
        *)
            usage
            ;;
    esac

    log "New version: ${BOLD}$NEW_VERSION${NC}"

    # Check if tag already exists
    if git -C "$PROJECT_ROOT" tag -l "v${NEW_VERSION}" | grep -q .; then
        error "Tag v${NEW_VERSION} already exists"
    fi
}

# --- Build ---
build_all() {
    step "Cleaning dist/"
    rm -f "$PROJECT_ROOT/dist/"*.ipk

    step "Writing VERSION file"
    echo "$NEW_VERSION" > "$PROJECT_ROOT/VERSION"

    step "Building all architectures (sequentially)"
    for arch in $ARCHITECTURES; do
        log "Building ${BOLD}$arch${NC}..."
        "$SCRIPT_DIR/build-ipk.sh" "$arch"
        echo ""
    done

    log "Built packages:"
    ls -lh "$PROJECT_ROOT/dist/"*.ipk
}

# --- Sync main branch (public, no source code) ---
sync_main_branch() {
    step "Syncing public main branch"

    # main branch on GitHub contains only README + install.sh (no source code).
    # Tags point to main commits so GitHub's auto-generated source archives are minimal.

    local worktree
    worktree=$(mktemp -d)

    git -C "$PROJECT_ROOT" fetch origin main
    git -C "$PROJECT_ROOT" worktree add "$worktree" origin/main --detach

    # Sync install.sh to main
    cp "$PROJECT_ROOT/scripts/install.sh" "$worktree/scripts/install.sh"

    cd "$worktree"

    if ! git diff --quiet; then
        git add -A
        git commit -m "update install.sh for v${NEW_VERSION}"
        log "install.sh updated on main"
    else
        log "install.sh unchanged"
    fi

    # Tag this commit on main
    git tag "v${NEW_VERSION}"
    log "Tagged: v${NEW_VERSION}"

    # Push main + tag (NEVER push master — no source code on GitHub)
    git push origin HEAD:main
    git push origin "v${NEW_VERSION}"

    cd "$PROJECT_ROOT"
    git worktree remove "$worktree" --force 2>/dev/null || true
}

# --- Release ---
create_release() {
    step "Committing version locally"
    cd "$PROJECT_ROOT"
    git add VERSION
    git commit -m "release: v${NEW_VERSION}" || log "Nothing to commit"

    sync_main_branch

    step "Creating GitHub Release"

    # Auto-detect pre-release
    local prerelease_flag=""
    if [[ "$NEW_VERSION" =~ -(rc|beta|alpha|dev) ]]; then
        prerelease_flag="--prerelease"
        log "Detected pre-release version"
    fi

    local notes="${RELEASE_NOTES:-release: v${NEW_VERSION}}"

    # shellcheck disable=SC2086
    gh release create "v${NEW_VERSION}" \
        "$PROJECT_ROOT/dist/"*.ipk \
        --title "v${NEW_VERSION}" \
        --notes "$notes" \
        $prerelease_flag

    log "GitHub Release created"
}

# --- Publish to entware-repo ---
publish_entware_repo() {
    step "Publishing to entware-repo"

    cd "$ENTWARE_REPO"
    git pull --rebase origin master 2>/dev/null || true

    # Remove old awg-manager versions (keep other packages like sing-box)
    local removed=()
    shopt -s nullglob
    for arch in $ARCHITECTURES; do
        local arch_dir="${arch}-kn"
        for old_ipk in "$ENTWARE_REPO/$arch_dir"/awg-manager_*_*.ipk; do
            rm -f "$old_ipk"
            removed+=("$arch_dir/$(basename "$old_ipk")")
        done
    done
    shopt -u nullglob
    if [[ ${#removed[@]} -gt 0 ]]; then
        log "Removed ${#removed[@]} old awg-manager packages"
        for f in "${removed[@]}"; do
            echo "  - $f"
        done
    fi

    # Copy new packages
    local files_added=()
    for arch in $ARCHITECTURES; do
        local arch_dir="${arch}-kn"
        local ipk="$PROJECT_ROOT/dist/awg-manager_${NEW_VERSION}_${arch_dir}.ipk"
        if [[ -f "$ipk" ]]; then
            cp "$ipk" "$ENTWARE_REPO/$arch_dir/"
            files_added+=("$arch_dir/$(basename "$ipk")")
            log "Copied to $arch_dir/"
        else
            warn "Missing: $ipk"
        fi
    done

    if [[ ${#files_added[@]} -eq 0 ]]; then
        error "No packages to publish"
    fi

    # Commit and push
    for arch in $ARCHITECTURES; do
        git add -A "${arch}-kn/"
    done
    git commit -m "awg-manager ${NEW_VERSION}"
    git push origin master

    log "Pushed to GitHub"

    step "Triggering reindex"
    gh workflow run "Update Entware Repository Indexes" --repo hoaxisr/entware-repo 2>/dev/null \
        || warn "Workflow trigger failed (will run on push anyway)"
}

# --- Summary ---
print_summary() {
    step "Release v${NEW_VERSION} complete!"

    echo ""
    echo -e "${BOLD}GitHub Release:${NC}"
    echo "  https://github.com/hoaxisr/awg-manager/releases/tag/v${NEW_VERSION}"

    echo ""
    echo -e "${BOLD}Entware repo (opkg install):${NC}"
    for arch in $ARCHITECTURES; do
        echo "  https://hoaxisr.github.io/entware-repo/${arch}-kn"
    done

    echo ""
    echo -e "${BOLD}Direct download:${NC}"
    for ipk in "$PROJECT_ROOT/dist/"*.ipk; do
        local name
        name=$(basename "$ipk")
        echo "  https://github.com/hoaxisr/awg-manager/releases/download/v${NEW_VERSION}/${name}"
    done
    echo ""
}

# --- Main ---
[[ $# -lt 1 ]] && usage

BUMP_OR_VERSION="${1}"
RELEASE_NOTES="${2:-}"

validate_prerequisites
resolve_version "$BUMP_OR_VERSION"
build_all
create_release
publish_entware_repo
print_summary
