ZIG     ?= ../../.cache/toolchains/zig/zig
CC      ?= cc
CFLAGS  ?= -std=c11 -D_GNU_SOURCE -Os -Wall -Wextra -Wno-unused-parameter \
           -ffunction-sections -fdata-sections -fno-asynchronous-unwind-tables
LDFLAGS ?= -static -s -Wl,--gc-sections

SRC := src/main.c src/wgcrypto.c src/wg.c src/netpkt.c src/json.c vendor/monocypher.c
HDR := src/wgcrypto.h src/wg.h src/netpkt.h src/json.h src/platform.h src/selftest_vectors.h vendor/monocypher.h
TEST_SRC := tests/test_main.c src/wgcrypto.c src/wg.c src/netpkt.c vendor/monocypher.c

.PHONY: all host cross test clean vectors

all: cross

host: out/awgm-warpscout

out/awgm-warpscout: $(SRC) $(HDR)
	@mkdir -p out
	$(CC) $(CFLAGS) -o $@ $(SRC)

out/awgm-warpscout-mipsle: $(SRC) $(HDR)
	@mkdir -p out
	$(ZIG) cc -target mipsel-linux-musl -msoft-float $(CFLAGS) $(LDFLAGS) -o $@ $(SRC)

out/awgm-warpscout-mips: $(SRC) $(HDR)
	@mkdir -p out
	$(ZIG) cc -target mips-linux-musl -msoft-float $(CFLAGS) $(LDFLAGS) -o $@ $(SRC)

out/awgm-warpscout-arm64: $(SRC) $(HDR)
	@mkdir -p out
	$(ZIG) cc -target aarch64-linux-musl $(CFLAGS) $(LDFLAGS) -o $@ $(SRC)

cross: out/awgm-warpscout-mipsle out/awgm-warpscout-mips out/awgm-warpscout-arm64

out/warpscout_test: $(TEST_SRC) $(HDR)
	@mkdir -p out
	$(CC) $(CFLAGS) -o $@ $(TEST_SRC)

test: out/awgm-warpscout out/warpscout_test
	./out/awgm-warpscout --selftest
	./out/warpscout_test

# regenerate src/selftest_vectors.h from Go x/crypto
vectors:
	cd tools/wgcheck && go run . vectors > ../../src/selftest_vectors.h

clean:
	rm -f out/awgm-warpscout out/awgm-warpscout-* out/warpscout_test out/*.exe
