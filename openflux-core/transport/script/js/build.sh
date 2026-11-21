#!/usr/bin/env bash
# Rebuilds the bundled transports from src/ and, given the official signing key,
# signs them. The signature is what the apps check against OfficialKeyHex.
#
#   transport/script/js/build.sh                       # rebuild only (signatures go stale)
#   transport/script/js/build.sh ~/oflx-keys/script-signing.key
#
# Rebuilt without a key, the bundles no longer match their .sig files:
# PENDING_SIGNATURE says so (the signature test skips while it exists) and the
# apps would reject the bundled scripts as unsigned, so the scripts are not
# shipped until someone with the key runs this with it.
set -euo pipefail
cd "$(dirname "$0")/../../.."          # repository root
js=transport/script/js
key=${1:-}

for entry in yandex vyandex boards oneme-webrtc oneme-iceinject; do
  go run ./transport/script/cmd/scriptbundle -entry "$js/src/$entry.js" -out "$js/$entry.js"
done

if [ -z "$key" ]; then
  echo "bundles rebuilt, NOT signed: pass the official key to sign (see PENDING_SIGNATURE)" >&2
  exit 0
fi

for f in yandex vyandex boards mailru cupsonline oneme-webrtc oneme-iceinject; do
  go run ./transport/script/cmd/scriptsign sign "$key" "$js/$f.js"
done
rm -f "$js/PENDING_SIGNATURE"
echo "signed; shipped signatures now verify under the official key"
