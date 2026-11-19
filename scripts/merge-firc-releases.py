#!/usr/bin/env python3
"""Merge per-arch JSON fragments and validate internal/firc/firc_releases.json."""
import argparse
import json
import pathlib
import re
import sys
import urllib.parse

EXPECTED_ARCHES = ("aarch64-3.10", "mips-3.4", "mipsel-3.4")
MAX_PACKAGE_BYTES = 32 << 20
VERSION_RE = re.compile(r"^[0-9][A-Za-z0-9.+~_-]{0,95}$")
SHA256_RE = re.compile(r"^[0-9a-f]{64}$")


def valid_https_url(raw: str) -> bool:
    parsed = urllib.parse.urlparse(raw)
    return (
        parsed.scheme == "https"
        and bool(parsed.netloc)
        and parsed.username is None
        and parsed.password is None
    )


def validate_manifest(
    manifest: object,
    expected_version: str = "",
    expected_repo: str = "",
) -> dict[str, dict[str, object]]:
    if not isinstance(manifest, dict):
        raise SystemExit("manifest must be a JSON object")

    got_arches = tuple(sorted(manifest.keys()))
    if got_arches != EXPECTED_ARCHES:
        raise SystemExit(
            f"manifest architectures mismatch: got {got_arches}, want {EXPECTED_ARCHES}"
        )

    versions = set()
    for arch in EXPECTED_ARCHES:
        entry = manifest[arch]
        if not isinstance(entry, dict):
            raise SystemExit(f"{arch}: entry must be a JSON object")

        version = entry.get("version")
        if not isinstance(version, str) or not VERSION_RE.fullmatch(version):
            raise SystemExit(f"{arch}: invalid version {version!r}")
        versions.add(version)
        if expected_version and version != expected_version:
            raise SystemExit(
                f"{arch}: version {version!r} does not match expected {expected_version!r}"
            )

        pkg_url = entry.get("packageUrl")
        if not isinstance(pkg_url, str) or not valid_https_url(pkg_url):
            raise SystemExit(f"{arch}: invalid HTTPS packageUrl {pkg_url!r}")
        if expected_repo:
            want_prefix = f"https://github.com/{expected_repo}/releases/download/"
            if not pkg_url.startswith(want_prefix):
                raise SystemExit(
                    f"{arch}: packageUrl {pkg_url!r} does not start with {want_prefix!r}"
                )

        mirror_url = entry.get("mirrorUrl", "")
        if mirror_url not in ("", None):
            if not isinstance(mirror_url, str) or not valid_https_url(mirror_url):
                raise SystemExit(f"{arch}: invalid HTTPS mirrorUrl {mirror_url!r}")

        sha = entry.get("packageSha256")
        if not isinstance(sha, str) or not SHA256_RE.fullmatch(sha):
            raise SystemExit(f"{arch}: invalid packageSha256 {sha!r}")

        size = entry.get("packageSize")
        if isinstance(size, bool) or not isinstance(size, int) or size <= 0 or size > MAX_PACKAGE_BYTES:
            raise SystemExit(f"{arch}: invalid packageSize {size!r}")

    if len(versions) != 1:
        raise SystemExit(f"inconsistent versions across arches: {sorted(versions)}")

    return manifest  # type: ignore[return-value]


def main() -> int:
    parser = argparse.ArgumentParser(description="Merge or verify FIRC release manifests.")
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument(
        "--merge",
        nargs="+",
        metavar="PATH",
        help="Output manifest path followed by one or more per-arch JSON fragment paths.",
    )
    mode.add_argument(
        "--verify",
        metavar="PATH",
        help="Validate an existing firc_releases.json manifest.",
    )
    parser.add_argument("--expected-version", default="", help="Require exact runtime version.")
    parser.add_argument(
        "--expected-repo",
        default="",
        help="Require GitHub release URL prefix for owner/repo.",
    )
    args = parser.parse_args()

    if args.merge:
        if len(args.merge) < 2:
            parser.error("--merge requires <out.json> and at least one fragment")
        out_path = pathlib.Path(args.merge[0])
        merged: dict[str, object] = {}
        for frag in args.merge[1:]:
            data = json.loads(pathlib.Path(frag).read_text(encoding="utf-8"))
            if not isinstance(data, dict) or not data:
                raise SystemExit(f"fragment {frag} must be a non-empty JSON object")
            for k, v in data.items():
                if k in merged:
                    raise SystemExit(f"duplicate architecture key {k!r} in fragments")
                merged[k] = v
        validated = validate_manifest(
            merged,
            expected_version=args.expected_version,
            expected_repo=args.expected_repo,
        )
        out_path.parent.mkdir(parents=True, exist_ok=True)
        out_path.write_text(
            json.dumps(validated, indent=2, sort_keys=True) + "\n",
            encoding="utf-8",
            newline="\n",
        )
        print(f"Wrote verified manifest for {len(validated)} arches to {out_path}")
        return 0

    manifest_path = pathlib.Path(args.verify)
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    validated = validate_manifest(
        manifest,
        expected_version=args.expected_version,
        expected_repo=args.expected_repo,
    )
    ver = next(iter(validated.values()))["version"]
    print(f"Verified FIRC release manifest ({ver}, {len(validated)} arches): {manifest_path}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
