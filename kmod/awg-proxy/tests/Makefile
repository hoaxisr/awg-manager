# Host-only unit tests for kmod/awg-proxy/src/.
#
# Build/run on a regular dev machine — NO Linux kernel headers required.
# We provide minimal stubs (in stubs/) for the few <linux/*> and <asm/*>
# includes that the source files pull in, and shim.h supplies the integer
# types, byte-order macros, and deterministic get_random_bytes / ktime_*.
#
# Targets:
#   make           run all tests (default)
#   make test      run all tests
#   make clean     remove built artifacts
#
# To keep determinism, every test seeds the PRNG via shim_set_random_seed()
# and pins the clock via shim_set_fixed_time(). Byte-exact assertions are
# reproducible across machines / compiler revisions.

CC      ?= gcc
CFLAGS  := -O0 -g3 -std=c11 -Wall -Wextra -Werror \
           -Wno-pointer-sign -Wno-unused-parameter \
           -I . -I stubs -include shim.h
LDFLAGS :=

SRC     := ../src

.PHONY: all test clean

all: test

test: test_cps test_transform test_tunnel test_proxy_recv test_proxy_loop test_hp
	./test_cps
	./test_transform
	./test_tunnel
	./test_proxy_recv
	./test_proxy_loop
	./test_hp

test_cps: test_cps.c shim.c shim.h $(SRC)/cps.c
	$(CC) $(CFLAGS) -o $@ test_cps.c shim.c $(SRC)/cps.c $(LDFLAGS)

# test_hp covers the header-protection ChaCha20 primitive (awg_chacha20_xor
# in cookie.c) against an independent OpenSSL-generated golden vector.
test_hp: test_hp.c shim.c shim.h $(SRC)/cookie.c
	$(CC) $(CFLAGS) -o $@ test_hp.c shim.c $(SRC)/cookie.c $(LDFLAGS)

test_transform: test_transform.c shim.c shim.h $(SRC)/transform.c $(SRC)/blake2s.c $(SRC)/cps.c $(SRC)/cookie.c
	$(CC) $(CFLAGS) -o $@ test_transform.c shim.c $(SRC)/transform.c $(SRC)/blake2s.c $(SRC)/cps.c $(SRC)/cookie.c $(LDFLAGS)

test_tunnel: test_tunnel.c shim.c shim.h $(SRC)/tunnel.c $(SRC)/transform.c $(SRC)/blake2s.c $(SRC)/cps.c $(SRC)/cookie.c
	$(CC) $(CFLAGS) -o $@ test_tunnel.c shim.c $(SRC)/tunnel.c $(SRC)/transform.c $(SRC)/blake2s.c $(SRC)/cps.c $(SRC)/cookie.c $(LDFLAGS)

# test_proxy_recv covers src/proxy_recv.h — the dual-mode worker recv-loop
# classifier — and src/endpoint.h — the pure RX source filter
# (awg_src_matches) used by awg_encap_rcv for both address families.
# Both headers are header-only (static inline), so no extra source files.
test_proxy_recv: test_proxy_recv.c shim.c shim.h $(SRC)/proxy_recv.h $(SRC)/endpoint.h
	$(CC) $(CFLAGS) -o $@ test_proxy_recv.c shim.c $(LDFLAGS)

# test_proxy_loop replays the worker recv-loop's control flow on top of
# awg_classify_recv (from proxy_recv.h) with a scripted recv source and
# a settable should-stop flag — proves the loop converges under stop
# and does not break early on zero-length-recv floods (issue #234).
test_proxy_loop: test_proxy_loop.c shim.c shim.h $(SRC)/proxy_recv.h
	$(CC) $(CFLAGS) -o $@ test_proxy_loop.c shim.c $(LDFLAGS)

clean:
	rm -f test_cps test_transform test_tunnel test_proxy_recv test_proxy_loop test_hp *.o
