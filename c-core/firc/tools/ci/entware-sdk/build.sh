#!/bin/sh
set -eu

keep_output() {
	for d in bin logs; do
		if [ -d "$d" ]; then mv "$d" /artifacts/; fi
	done
}
trap keep_output EXIT

tar xf /sdk/sdk.tar.zst --strip=1 --no-same-owner -C .
cp /sdk/feeds.conf feeds.conf
echo "src-link firc /feed/" >> feeds.conf
./scripts/feeds update -a
make defconfig
./scripts/feeds install -p firc -f firc
make package/firc/download BUILD_LOG=1 V=s
make package/firc/check BUILD_LOG=1 V=s
make package/firc/compile BUILD_LOG=1 V=s -j "$(nproc)"
