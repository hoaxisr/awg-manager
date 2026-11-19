# tunvless — a TUN device with a VLESS outbound.
#
#   make                    build out/tunvless (fetches and builds wolfSSL on first run)
#   make test               unit and crypto tests (no root, no network)
#   make e2e                tunnel tests in network namespaces (root, iproute2, python3)
#   make interop            against real Xray-core / sing-box (XRAY=, SINGBOX=; skipped without)
#   make speed              Hysteria2 speed against Xray's own client on a netem line (XRAY=; about 95 min)
#   make install            DESTDIR, PREFIX (default /usr/local)
#
# Cross builds take the toolchain from the environment, e.g.
#   make CC=mipsel-openwrt-linux-gnu-gcc AR=mipsel-openwrt-linux-gnu-ar
# The Entware package is built this way by the Entware SDK (tools/ci/entware-package/Makefile).

VERSION     ?= dev
O           ?= out
CC          ?= cc
AR          ?= ar
CFLAGS      ?= -O2
LDFLAGS     ?=
WOLFSSL_SRC ?= third_party/wolfssl
ZLIB_SRC    ?= third_party/zlib
BROTLI_SRC  ?= third_party/brotli
ZSTD_SRC    ?= third_party/zstd
NGTCP2_SRC  ?= third_party/ngtcp2

INC      := -Isrc/lib -Isrc/tunnel -Isrc/proto/vless -Isrc/proto/transport -Isrc/proto/tls
WARN     := -Wall -Wextra
PREFIXMAP := -ffile-prefix-map=$(CURDIR)/=
ALL_CFLAGS = $(CFLAGS) $(WARN) $(INC) $(PREFIXMAP) -ffunction-sections -fdata-sections
DEFS     := -DTUNVLESS_VERSION='"$(VERSION)"'

TUNNEL_SRC    := src/tunnel/tun.c src/tunnel/stack.c src/tunnel/ibhs.c src/tunnel/rtx.c src/tunnel/ifcfg.c src/tunnel/pool.c src/tunnel/nodes.c src/control.c src/ctlmap.c
VLESS_SRC     := src/proto/vless/vldial.c src/proto/vless/client.c src/proto/vless/vless_proto.c src/proto/vless/vlink.c \
                 src/proto/vless/vision.c src/proto/vless/sublink.c src/proto/vless/hy2link.c src/proto/vless/sub.c src/proto/transport/fmask.c
TRANSPORT_SRC := src/proto/transport/transport.c src/proto/transport/trdial.c src/proto/transport/sockmark.c \
                 src/proto/transport/trsec.c src/proto/transport/trgrpc.c src/proto/transport/trxhttp.c \
                 src/proto/transport/trupgrade.c src/proto/transport/trws.c src/proto/transport/trpath.c \
                 src/proto/transport/trvenc.c src/proto/transport/trfp.c \
                 src/proto/transport/xhopts.c src/proto/transport/xhreq.c \
                 src/proto/transport/xconn.c src/proto/transport/trxmux.c src/proto/transport/xmux.c \
                 src/proto/transport/fmlink.c src/proto/transport/fmfrag.c src/proto/transport/fmhc.c \
                 src/proto/transport/fmsd.c src/proto/transport/fmsdtab.c src/lib/gorand.c
TLS_SRC       := src/proto/tls/tls13.c src/proto/tls/certverify.c src/proto/tls/reality.c \
                 src/proto/tls/h2.c src/proto/tls/h2wire.c src/proto/tls/h2mux.c src/proto/tls/hpack.c src/proto/tls/ech.c \
                 src/proto/tls/roots.c
LIB_SRC       := src/lib/osrand.c src/lib/tscratch.c src/lib/portpick.c
# The one file that sees wolfSSL headers: built with the library's own flags.
CRYPTO_SRC    := src/lib/scrypto.c
SRC           := src/main.c src/ctlnodes.c src/ctlinb.c $(TUNNEL_SRC) $(VLESS_SRC) $(TRANSPORT_SRC) $(TLS_SRC) $(LIB_SRC)
HDR           := $(wildcard src/*/*.h src/proto/*/*.h)

WOLFSSL_LIB   := $(O)/wolfssl/libwolfssl.a
WOLFSSL_FLAGS  = $(shell cat $(WOLFSSL_LIB).cflags 2>/dev/null)
ZLIB_LIB      := $(O)/zlib/libz.a
BROTLI_LIB    := $(O)/brotli/libbrotlidec.a
ZSTD_LIB      := $(O)/zstd/libzstd.a
CERTZIP_LIBS  := $(ZLIB_LIB) $(BROTLI_LIB) $(ZSTD_LIB)
NGTCP2_LIB    := $(O)/ngtcp2/libngtcp2.a
CERTZIP_FLAGS  = $(foreach l,$(CERTZIP_LIBS),$(shell cat $(l).cflags 2>/dev/null))

.PHONY: all fetch test unit-test crypto-test e2e interop speed install clean distclean

all: $(O)/tunvless

fetch: $(WOLFSSL_SRC)/wolfssl/wolfcrypt/settings.h $(ZLIB_SRC)/.steer-zlib $(BROTLI_SRC)/.steer-brotli $(ZSTD_SRC)/.steer-zstd $(NGTCP2_SRC)/.steer-ngtcp2

$(WOLFSSL_SRC)/wolfssl/wolfcrypt/settings.h:
	sh build/wolfssl/fetch.sh $(WOLFSSL_SRC)

$(ZLIB_SRC)/.steer-zlib: build/zlib/fetch.sh
	sh build/zlib/fetch.sh $(ZLIB_SRC)

$(BROTLI_SRC)/.steer-brotli: build/brotli/fetch.sh
	sh build/brotli/fetch.sh $(BROTLI_SRC)

$(ZSTD_SRC)/.steer-zstd: build/zstd/fetch.sh
	sh build/zstd/fetch.sh $(ZSTD_SRC)

$(NGTCP2_SRC)/.steer-ngtcp2: build/ngtcp2/fetch.sh
	sh build/ngtcp2/fetch.sh $(NGTCP2_SRC)

LIB_ENV = CC="$(CC)" AR="$(AR)" CFLAGS="$(CFLAGS) $(PREFIXMAP) -ffunction-sections -fdata-sections"

$(ZLIB_LIB): $(ZLIB_SRC)/.steer-zlib build/zlib/build.sh build/archive.sh FORCE
	@$(LIB_ENV) sh build/zlib/build.sh $(ZLIB_SRC) $@

$(BROTLI_LIB): $(BROTLI_SRC)/.steer-brotli build/brotli/build.sh build/archive.sh FORCE
	@$(LIB_ENV) sh build/brotli/build.sh $(BROTLI_SRC) $@

$(ZSTD_LIB): $(ZSTD_SRC)/.steer-zstd build/zstd/build.sh build/archive.sh FORCE
	@$(LIB_ENV) sh build/zstd/build.sh $(ZSTD_SRC) $@

$(NGTCP2_LIB): $(NGTCP2_SRC)/.steer-ngtcp2 build/ngtcp2/build.sh build/archive.sh $(WOLFSSL_LIB) FORCE
	@$(LIB_ENV) FIRC_CFLAGS="$(WARN)" sh build/ngtcp2/build.sh $(NGTCP2_SRC) $@ $(WOLFSSL_LIB)

$(O)/certzip.o: src/lib/certzip.c src/lib/certzip.h $(CERTZIP_LIBS)
	@mkdir -p $(O)
	$(CC) $(ALL_CFLAGS) $(CERTZIP_FLAGS) -c $< -o $@

# build.sh rebuilds only when the options, the compiler or the flags change.
$(WOLFSSL_LIB): $(WOLFSSL_SRC)/wolfssl/wolfcrypt/settings.h build/wolfssl/user_settings.h build/wolfssl/build.sh FORCE
	@CC="$(CC)" AR="$(AR)" CFLAGS="$(CFLAGS) $(PREFIXMAP) -ffunction-sections -fdata-sections" \
		sh build/wolfssl/build.sh $(WOLFSSL_SRC) $@

$(O)/scrypto.o: $(CRYPTO_SRC) src/lib/scrypto.h src/lib/blake3.h $(WOLFSSL_LIB)
	@mkdir -p $(O)
	$(CC) $(ALL_CFLAGS) $(WOLFSSL_FLAGS) -c $< -o $@

QUIC_FLAGS = -Isrc/proto/quic $(shell cat $(NGTCP2_LIB).cflags 2>/dev/null)

HY2_OBJS := $(O)/quic.o $(O)/qcssl.o $(O)/qcbrutal.o $(O)/hy2wire.o $(O)/hy2conn.o $(O)/hy2node.o $(O)/hy2run.o $(O)/hy2check.o $(O)/hy2dial.o $(O)/hy2loss.o

$(O)/quic.o: src/proto/quic/quic.c src/proto/quic/quic.h src/proto/quic/qcssl.h src/proto/quic/ngcc.h src/proto/quic/qcbrutal.h src/lib/osrand.h src/lib/tscratch.h src/lib/portpick.h $(NGTCP2_LIB)
	@mkdir -p $(O)
	$(CC) $(ALL_CFLAGS) $(QUIC_FLAGS) -c $< -o $@

$(O)/qcbrutal.o: src/proto/quic/qcbrutal.c src/proto/quic/qcbrutal.h
	@mkdir -p $(O)
	$(CC) $(ALL_CFLAGS) -c $< -o $@

$(O)/qcssl.o: src/proto/quic/qcssl.c src/proto/quic/qcssl.h src/proto/tls/certverify.h $(NGTCP2_LIB)
	@mkdir -p $(O)
	$(CC) $(ALL_CFLAGS) $(QUIC_FLAGS) -c $< -o $@

$(O)/hy2wire.o: src/proto/hysteria2/hy2wire.c src/proto/hysteria2/hy2wire.h src/proto/tls/hpack.h src/lib/scrypto.h
	@mkdir -p $(O)
	$(CC) $(ALL_CFLAGS) -Isrc/proto/hysteria2 -c $< -o $@

$(O)/hy2conn.o: src/proto/hysteria2/hy2conn.c src/proto/hysteria2/hy2conn.h src/proto/hysteria2/hy2wire.h src/proto/quic/quic.h src/proto/tls/reality.h src/lib/osrand.h
	@mkdir -p $(O)
	$(CC) $(ALL_CFLAGS) -Isrc/proto/quic -Isrc/proto/hysteria2 -c $< -o $@

HY2_CFLAGS = -Isrc/proto/quic -Isrc/proto/hysteria2

$(O)/hy2node.o: src/proto/hysteria2/hy2node.c $(HDR)
	@mkdir -p $(O)
	$(CC) $(ALL_CFLAGS) $(HY2_CFLAGS) -c $< -o $@

$(O)/hy2run.o: src/proto/hysteria2/hy2run.c $(HDR)
	@mkdir -p $(O)
	$(CC) $(ALL_CFLAGS) $(HY2_CFLAGS) -c $< -o $@

$(O)/hy2check.o: src/proto/hysteria2/hy2check.c $(HDR)
	@mkdir -p $(O)
	$(CC) $(ALL_CFLAGS) $(HY2_CFLAGS) -c $< -o $@

$(O)/hy2dial.o: src/proto/hysteria2/hy2dial.c $(HDR)
	@mkdir -p $(O)
	$(CC) $(ALL_CFLAGS) $(HY2_CFLAGS) -c $< -o $@

$(O)/hy2loss.o: src/proto/hysteria2/hy2loss.c src/proto/hysteria2/hy2loss.h
	@mkdir -p $(O)
	$(CC) $(ALL_CFLAGS) -c $< -o $@

$(O)/tunvless: $(SRC) $(HDR) $(HY2_OBJS) $(O)/scrypto.o $(O)/certzip.o $(NGTCP2_LIB) $(WOLFSSL_LIB) $(CERTZIP_LIBS)
	@mkdir -p $(O)
	$(CC) $(ALL_CFLAGS) $(HY2_CFLAGS) $(DEFS) -o $@ $(SRC) $(HY2_OBJS) $(O)/scrypto.o $(O)/certzip.o $(NGTCP2_LIB) $(WOLFSSL_LIB) \
		$(CERTZIP_LIBS) $(LDFLAGS) -Wl,--gc-sections -lpthread

install: $(O)/tunvless
	install -d $(DESTDIR)$(or $(PREFIX),/usr/local)/bin
	install -m 0755 $(O)/tunvless $(DESTDIR)$(or $(PREFIX),/usr/local)/bin/tunvless

# ---- tests ------------------------------------------------------------------------------
#
# Unit tests need neither the network nor wolfSSL: where a test needs TLS, it stubs it.
# Crypto tests link a wolfSSL built with the test-only options (certificate issuing and a TLS
# server for the peer side), in its own archive so the shipped one never carries them.

T        := $(O)/tests
T_CFLAGS  = -O1 -g $(WARN) -Wno-unused-function $(INC) -Itests $(if $(SANITIZE),-fsanitize=$(SANITIZE))
TW_LIB   := $(T)/wolfssl/libwolfssl.a
TW_FLAGS  = $(shell cat $(TW_LIB).cflags 2>/dev/null)

SCRATCH_SRC := src/lib/tscratch.c

FMLINK_SRC := src/proto/transport/fmlink.c src/proto/transport/fmask.c src/proto/transport/fmfrag.c src/proto/transport/fmhc.c src/lib/osrand.c \
              src/proto/transport/fmsd.c src/proto/transport/fmsdtab.c src/lib/gorand.c

UNIT := h2match h2muxmatch hpackmatch xhupmatch wsmatch pqfallbackmatch b3match visionmatch submatch subpq vlinkmatch tungromatch tunnamematch tunnelmatch rtxmatch grpcmatch ctlmatch nodesmatch ctlnodesmatch ctlmapmatch ctlinbmatch ibhsmatch dialmatch ratelogmatch xhoptsmatch xmuxmatch tun6match vreqmatch addr6match fmaskmatch fmlinkmatch tstackmatch tscratchmatch gorandmatch brutalmatch portpickmatch hy2lossmatch
CRYPTO_TESTS := scryptomatch hellofreeze echmatch vlessmatch takematch certzipmatch xudpmatch probematch xconnmatch fmsdmatch quicmatch hy2wirematch hy2connmatch hy2checkmatch hy2dialmatch fpmatch

test: unit-test crypto-test $(O)/tunvless
	sh tests/tlssize.sh $(O)/tunvless
	@echo "all tests passed"

unit-test: $(addprefix $(T)/,$(UNIT))
	@set -e; for t in $(UNIT); do echo "== $$t"; $(T)/$$t; done

crypto-test: $(addprefix $(T)/,$(CRYPTO_TESTS))
	@set -e; for t in $(CRYPTO_TESTS); do echo "== $$t"; $(T)/$$t; done

$(T)/h2match: tests/h2match.c src/proto/tls/h2.c src/proto/tls/h2wire.c $(SCRATCH_SRC) $(HDR)
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -o $@ tests/h2match.c src/proto/tls/h2wire.c $(SCRATCH_SRC) -lpthread

$(T)/hpackmatch: tests/hpackmatch.c tests/hpackread.h src/proto/tls/hpack.c $(HDR)
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -o $@ tests/hpackmatch.c src/proto/tls/hpack.c

H2MUX_SRC := src/proto/tls/h2mux.c src/proto/tls/hpack.c src/proto/tls/h2wire.c src/proto/tls/h2.c $(SCRATCH_SRC)
$(T)/h2muxmatch: tests/h2muxmatch.c tests/hpackread.h $(H2MUX_SRC) $(HDR)
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -Wl,--wrap=calloc -o $@ tests/h2muxmatch.c $(H2MUX_SRC) -lpthread

XHUP_SRC := src/proto/tls/h2.c src/proto/tls/h2wire.c src/proto/transport/transport.c src/proto/transport/trsec.c \
            src/proto/transport/trdial.c src/proto/transport/sockmark.c src/proto/transport/trgrpc.c src/proto/tls/roots.c \
            src/proto/transport/trws.c src/proto/transport/trupgrade.c src/proto/transport/trpath.c \
            src/proto/transport/xhopts.c src/proto/transport/xhreq.c src/lib/osrand.c \
            src/proto/transport/xconn.c src/proto/tls/h2mux.c src/proto/tls/hpack.c src/proto/transport/trxmux.c src/proto/transport/xmux.c \
            src/proto/transport/fmlink.c src/proto/transport/fmfrag.c src/proto/transport/fmhc.c src/proto/transport/fmask.c \
            src/proto/transport/fmsd.c src/proto/transport/fmsdtab.c src/lib/gorand.c \
            $(SCRATCH_SRC)
$(T)/xhupmatch: tests/xhupmatch.c tests/trvenc-stub.c src/proto/transport/trxhttp.c $(XHUP_SRC) $(T)/scrypto.o $(HDR)
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -Wl,--wrap=calloc -Wl,--wrap=xconn_io -o $@ tests/xhupmatch.c tests/trvenc-stub.c $(XHUP_SRC) $(T_FMCRYPTO) -lpthread

WS_SRC := src/proto/transport/trws.c src/proto/transport/trupgrade.c src/proto/transport/trpath.c \
          src/proto/transport/transport.c src/proto/transport/trsec.c src/proto/transport/trdial.c src/proto/transport/sockmark.c \
          src/proto/transport/trgrpc.c src/proto/transport/trxhttp.c src/proto/tls/h2.c \
          src/proto/tls/h2wire.c src/proto/transport/xhopts.c src/proto/transport/xhreq.c src/proto/tls/roots.c src/lib/osrand.c \
          src/proto/transport/xconn.c src/proto/tls/h2mux.c src/proto/tls/hpack.c src/proto/transport/trxmux.c src/proto/transport/xmux.c \
          src/proto/transport/fmlink.c src/proto/transport/fmfrag.c src/proto/transport/fmhc.c src/proto/transport/fmask.c \
          src/proto/transport/fmsd.c src/proto/transport/fmsdtab.c src/lib/gorand.c \
          $(SCRATCH_SRC)
$(T)/wsmatch: tests/wsmatch.c tests/trvenc-stub.c $(WS_SRC) $(T)/scrypto.o $(HDR)
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -o $@ tests/wsmatch.c tests/trvenc-stub.c $(WS_SRC) $(T_FMCRYPTO) -lpthread

$(T)/xhoptsmatch: tests/xhoptsmatch.c src/proto/transport/xhopts.c src/proto/transport/xhreq.c src/lib/osrand.c $(HDR)
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -o $@ tests/xhoptsmatch.c src/proto/transport/xhopts.c src/proto/transport/xhreq.c src/lib/osrand.c

XMUX_SRC := $(XHUP_SRC) src/proto/transport/trxhttp.c
$(T)/xmuxmatch: tests/xmuxmatch.c tests/trvenc-stub.c $(XMUX_SRC) $(T)/scrypto.o $(HDR)
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -o $@ tests/xmuxmatch.c tests/trvenc-stub.c $(XMUX_SRC) $(T_FMCRYPTO) -lpthread

$(T)/pqfallbackmatch: tests/pqfallbackmatch.c src/proto/transport/trsec.c $(FMLINK_SRC) $(T)/scrypto.o $(HDR)
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -o $@ tests/pqfallbackmatch.c src/proto/transport/trsec.c $(FMLINK_SRC) $(T_FMCRYPTO) -lpthread

$(T)/b3match: tests/b3match.c src/lib/blake3.h
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -o $@ tests/b3match.c

$(T)/visionmatch: tests/visionmatch.c src/proto/vless/vision.c src/lib/osrand.c $(HDR)
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -Wl,--wrap=os_getrandom -o $@ tests/visionmatch.c src/proto/vless/vision.c src/lib/osrand.c -lpthread

SUB_SRC := src/proto/vless/sublink.c src/proto/vless/hy2link.c src/proto/vless/vlink.c src/proto/transport/trpath.c src/proto/transport/xhopts.c \
           src/proto/transport/fmask.c
$(T)/submatch: tests/submatch.c src/proto/vless/sub.c src/proto/vless/vless_proto.c $(SUB_SRC) $(HDR)
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -o $@ tests/submatch.c $(SUB_SRC)

$(T)/subpq: tests/subpq.c tests/sub-pq-samples.h src/proto/vless/sub.c src/proto/vless/vless_proto.c $(SUB_SRC) $(HDR)
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -o $@ tests/subpq.c src/proto/vless/sub.c src/proto/vless/vless_proto.c $(SUB_SRC)

$(T)/vlinkmatch: tests/vlinkmatch.c tests/sub-pq-samples.h src/proto/vless/sub.c src/proto/vless/vless_proto.c $(SUB_SRC) $(HDR)
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -o $@ tests/vlinkmatch.c src/proto/vless/sub.c src/proto/vless/vless_proto.c $(SUB_SRC)

$(T)/tungromatch: tests/tungromatch.c src/tunnel/tun.c $(HDR)
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -o $@ tests/tungromatch.c

$(T)/vreqmatch: tests/vreqmatch.c src/proto/vless/vless_proto.c $(HDR)
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -o $@ tests/vreqmatch.c src/proto/vless/vless_proto.c

$(T)/tun6match: tests/tun6match.c src/tunnel/tun.c $(HDR)
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -o $@ tests/tun6match.c src/tunnel/tun.c

$(T)/addr6match: tests/addr6match.c src/tunnel/ifcfg.c $(HDR)
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -o $@ tests/addr6match.c src/tunnel/ifcfg.c

$(T)/fmaskmatch: tests/fmaskmatch.c src/proto/transport/fmask.c $(HDR)
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -o $@ tests/fmaskmatch.c src/proto/transport/fmask.c

$(T)/fmlinkmatch: tests/fmlinkmatch.c tests/sdpeer.h $(FMLINK_SRC) $(T)/scrypto.o $(HDR)
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -o $@ tests/fmlinkmatch.c $(filter-out src/proto/transport/fmlink.c src/lib/osrand.c,$(FMLINK_SRC)) $(T_CRYPTO)

$(T)/tunnamematch: tests/tunnamematch.c src/tunnel/tun.c $(HDR)
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -o $@ tests/tunnamematch.c src/tunnel/tun.c

# grpcmatch includes trgrpc.c (its framing functions are static) and links the rest for real.
GRPC_TEST_SRC := $(filter-out src/proto/transport/trgrpc.c,$(XHUP_SRC)) src/proto/transport/trxhttp.c
$(T)/grpcmatch: tests/grpcmatch.c tests/trvenc-stub.c src/proto/transport/trgrpc.c $(GRPC_TEST_SRC) $(T)/scrypto.o $(HDR)
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -Wl,--wrap=calloc -o $@ tests/grpcmatch.c tests/trvenc-stub.c $(GRPC_TEST_SRC) $(T_FMCRYPTO) -lpthread

$(T)/ctlmatch: tests/ctlmatch.c src/control.c src/control.h
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -o $@ tests/ctlmatch.c src/control.c -lpthread

$(T)/ctlmapmatch: tests/ctlmapmatch.c src/ctlmap.c src/ctlmap.h
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -o $@ tests/ctlmapmatch.c src/ctlmap.c -lpthread

$(T)/nodesmatch: tests/nodesmatch.c src/tunnel/nodes.c src/proto/transport/trdial.c src/proto/transport/sockmark.c $(HDR)
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -o $@ tests/nodesmatch.c src/tunnel/nodes.c src/proto/transport/sockmark.c -lpthread

$(T)/ratelogmatch: tests/ratelogmatch.c src/lib/ratelog.h
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -o $@ tests/ratelogmatch.c

$(T)/tstackmatch: tests/tstackmatch.c src/lib/tstack.h
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -o $@ tests/tstackmatch.c -lpthread

$(T)/gorandmatch: tests/gorandmatch.c src/lib/gorand.c src/lib/gorand.h tests/unit.h
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -o $@ tests/gorandmatch.c src/lib/gorand.c

$(T)/brutalmatch: tests/brutalmatch.c src/proto/quic/qcbrutal.c src/proto/quic/qcbrutal.h tests/unit.h
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -o $@ tests/brutalmatch.c src/proto/quic/qcbrutal.c

$(T)/portpickmatch: tests/portpickmatch.c src/lib/portpick.c src/lib/portpick.h tests/unit.h
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -o $@ tests/portpickmatch.c src/lib/portpick.c

$(T)/hy2lossmatch: tests/hy2lossmatch.c src/proto/hysteria2/hy2loss.c src/proto/hysteria2/hy2loss.h tests/unit.h
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -o $@ tests/hy2lossmatch.c src/proto/hysteria2/hy2loss.c

$(T)/tscratchmatch: tests/tscratchmatch.c src/lib/tscratch.c src/lib/tscratch.h tests/unit.h
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -Wl,--wrap=calloc -Wl,--wrap=free -Wl,--wrap=pthread_key_create -Wl,--wrap=pthread_setspecific -o $@ tests/tscratchmatch.c src/lib/tscratch.c -lpthread

$(T)/dialmatch: tests/dialmatch.c src/proto/transport/trdial.c src/proto/transport/sockmark.c $(HDR)
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -Wl,--wrap=setsockopt -Wl,--wrap=__setsockopt64 -o $@ tests/dialmatch.c -lpthread

CTLN_SRC := src/ctlnodes.c src/ctlmap.c src/ctlinb.c src/control.c src/tunnel/nodes.c src/proto/vless/sub.c \
            src/proto/vless/vless_proto.c src/proto/transport/sockmark.c $(SUB_SRC)
$(T)/ctlnodesmatch: tests/ctlnodesmatch.c tests/sub-pq-samples.h src/proto/transport/trdial.c $(CTLN_SRC) $(HDR)
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -o $@ tests/ctlnodesmatch.c $(CTLN_SRC) -lpthread

$(T)/ctlinbmatch: tests/ctlinbmatch.c src/proto/transport/trdial.c $(CTLN_SRC) $(HDR)
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -o $@ tests/ctlinbmatch.c $(CTLN_SRC) -lpthread

$(T)/ibhsmatch: tests/ibhsmatch.c src/tunnel/ibhs.c src/tunnel/ibhs.h src/ctlinb.h
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -o $@ tests/ibhsmatch.c src/tunnel/ibhs.c

$(T)/rtxmatch: tests/rtxmatch.c src/tunnel/rtx.c src/tunnel/rtx.h
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -o $@ tests/rtxmatch.c src/tunnel/rtx.c

TUNNEL_TEST_SRC := src/control.c src/ctlmap.c src/tunnel/tun.c src/tunnel/rtx.c src/tunnel/ifcfg.c src/proto/vless/vless_proto.c \
                   src/proto/vless/vision.c src/proto/vless/vldial.c src/lib/osrand.c src/tunnel/ibhs.c src/tunnel/nodes.c \
                   src/ctlinb.c src/ctlnodes.c src/proto/vless/sub.c src/proto/transport/trdial.c src/proto/transport/sockmark.c \
                   $(SUB_SRC)
$(T)/tunnelmatch: tests/tunnelmatch.c src/tunnel/stack.c src/tunnel/pool.c $(TUNNEL_TEST_SRC) $(HDR)
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -o $@ tests/tunnelmatch.c $(TUNNEL_TEST_SRC) -lpthread -ldl

# -- crypto tests: the test wolfSSL and the crypto layer built against it --

$(TW_LIB): $(WOLFSSL_SRC)/wolfssl/wolfcrypt/settings.h build/wolfssl/user_settings.h build/wolfssl/build.sh FORCE
	@CC="$(CC)" AR="$(AR)" CFLAGS="-O2 -g" \
		STEER_WOLFSSL_DEFS="-DWOLFSSL_CERT_GEN -DWOLFSSL_CERT_EXT -DSTEER_WOLFSSL_SERVER" \
		sh build/wolfssl/build.sh $(WOLFSSL_SRC) $@

$(T)/scrypto.o: $(CRYPTO_SRC) src/lib/scrypto.h src/lib/blake3.h $(TW_LIB)
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) $(TW_FLAGS) -c $< -o $@

$(T)/certgen.o: tests/certgen.c tests/certgen.h $(TW_LIB)
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) $(TW_FLAGS) -c $< -o $@

T_CRYPTO = $(T)/scrypto.o src/lib/osrand.c $(TW_LIB) -lpthread

TN_LIB   := $(T)/ngtcp2/libngtcp2.a
TN_FLAGS  = $(shell cat $(TN_LIB).cflags 2>/dev/null)
QC_TFLAGS = -Isrc/proto/quic -DQC_WITH_SERVER

$(TN_LIB): $(NGTCP2_SRC)/.steer-ngtcp2 build/ngtcp2/build.sh build/archive.sh $(TW_LIB) FORCE
	@CC="$(CC)" AR="$(AR)" CFLAGS="-O2 -g" FIRC_CFLAGS="$(WARN) -DQC_WITH_SERVER$(if $(SANITIZE), -fsanitize=$(SANITIZE))" \
		sh build/ngtcp2/build.sh $(NGTCP2_SRC) $@ $(TW_LIB)

$(T)/quic.o: src/proto/quic/quic.c src/proto/quic/quic.h src/proto/quic/qcssl.h src/proto/quic/ngcc.h src/proto/quic/qcbrutal.h src/lib/osrand.h src/lib/tscratch.h src/lib/portpick.h $(TN_LIB)
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) $(QC_TFLAGS) $(TN_FLAGS) -c $< -o $@

$(T)/qcbrutal.o: src/proto/quic/qcbrutal.c src/proto/quic/qcbrutal.h
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -c $< -o $@

$(T)/qcssl.o: src/proto/quic/qcssl.c src/proto/quic/qcssl.h src/proto/tls/certverify.h $(TN_LIB)
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) $(QC_TFLAGS) $(TN_FLAGS) -c $< -o $@

$(T)/quicmatch: tests/quicmatch.c tests/hoppair.h tests/certgen.h tests/unit.h $(T)/quic.o $(T)/qcssl.o $(T)/qcbrutal.o $(T)/certgen.o $(T)/scrypto.o $(HDR)
	$(CC) $(T_CFLAGS) $(QC_TFLAGS) -Wl,--wrap=connect -Wl,--wrap=getsockopt -Wl,--wrap=sendmsg -Wl,--wrap=sendmmsg -Wl,--wrap=recvmmsg \
		-Wl,--wrap=__getsockopt64 -Wl,--wrap=__sendmsg64 -Wl,--wrap=__sendmmsg64 -Wl,--wrap=__recvmmsg64 -o $@ tests/quicmatch.c $(T)/quic.o $(T)/qcssl.o $(T)/qcbrutal.o $(T)/certgen.o src/lib/portpick.c $(SCRATCH_SRC) $(TN_LIB) $(T_CRYPTO)

T_FMCRYPTO = -Wl,--wrap=sc_wipe $(T)/scrypto.o $(TW_LIB)

$(T)/certzip.o: src/lib/certzip.c src/lib/certzip.h $(CERTZIP_LIBS)
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) $(CERTZIP_FLAGS) -c $< -o $@

T_CERTZIP = $(T)/certzip.o $(CERTZIP_LIBS)

$(T)/certzipmatch: tests/certzipmatch.c tests/certzip-samples.h $(T)/certzip.o
	$(CC) $(T_CFLAGS) -Wl,--wrap=malloc -o $@ tests/certzipmatch.c $(T_CERTZIP)
CLIENT_SRC := src/proto/vless/client.c src/proto/vless/vless_proto.c src/proto/vless/vision.c \
              src/proto/vless/sub.c src/proto/vless/sublink.c src/proto/vless/hy2link.c src/proto/vless/vlink.c src/proto/transport/fmask.c \
              src/proto/transport/transport.c src/proto/transport/trsec.c src/proto/transport/trgrpc.c \
              src/proto/transport/trxhttp.c src/proto/transport/trupgrade.c src/proto/transport/trws.c \
              src/proto/transport/trpath.c src/proto/transport/trvenc.c src/proto/transport/trfp.c \
              src/proto/transport/xhopts.c src/proto/transport/xhreq.c src/proto/transport/xconn.c \
              src/proto/transport/trxmux.c src/proto/transport/xmux.c \
              src/proto/transport/fmlink.c src/proto/transport/fmfrag.c src/proto/transport/fmhc.c \
              src/proto/transport/fmsd.c src/proto/transport/fmsdtab.c src/lib/gorand.c \
              src/proto/tls/tls13.c src/proto/tls/certverify.c src/proto/tls/reality.c src/proto/tls/h2.c \
              src/proto/tls/h2wire.c src/proto/tls/h2mux.c src/proto/tls/hpack.c src/proto/tls/ech.c \
              $(SCRATCH_SRC)

FMSD_SRC := src/proto/transport/fmsd.c src/proto/transport/fmsdtab.c src/proto/transport/fmask.c src/lib/gorand.c
$(T)/fmsdmatch: tests/fmsdmatch.c tests/sdpeer.h $(FMSD_SRC) src/proto/transport/fmsd.h src/lib/gorand.h $(T)/scrypto.o $(HDR)
	$(CC) $(T_CFLAGS) -Wl,--wrap=go_rand_seed -o $@ tests/fmsdmatch.c $(FMSD_SRC) $(T_CRYPTO)

$(T)/hy2wirematch: tests/hy2wirematch.c tests/hy2-frozen.h tests/unit.h src/proto/hysteria2/hy2wire.c src/proto/tls/hpack.c $(T)/scrypto.o $(HDR)
	$(CC) $(T_CFLAGS) -Isrc/proto/hysteria2 -o $@ tests/hy2wirematch.c src/proto/hysteria2/hy2wire.c src/proto/tls/hpack.c $(T_CRYPTO)

HY2C_SRC := src/lib/portpick.c src/proto/hysteria2/hy2conn.c src/proto/hysteria2/hy2wire.c src/proto/tls/hpack.c src/proto/tls/reality.c
$(T)/hy2connmatch: tests/hy2connmatch.c tests/hy2peer.h tests/hy2-frozen.h tests/certgen.h tests/unit.h $(HY2C_SRC) $(T)/quic.o $(T)/qcssl.o $(T)/qcbrutal.o $(T)/certgen.o $(T)/scrypto.o $(HDR)
	$(CC) $(T_CFLAGS) $(QC_TFLAGS) -Isrc/proto/hysteria2 -Wl,--wrap=qc_datagram_slots -o $@ tests/hy2connmatch.c $(HY2C_SRC) $(T)/quic.o $(T)/qcssl.o $(T)/qcbrutal.o $(T)/certgen.o $(SCRATCH_SRC) $(TN_LIB) $(T_CRYPTO)

HY2CHK_SRC := src/lib/portpick.c src/proto/hysteria2/hy2node.c src/proto/hysteria2/hy2run.c src/proto/hysteria2/hy2check.c \
              src/proto/hysteria2/hy2conn.c src/proto/hysteria2/hy2wire.c src/proto/transport/sockmark.c src/proto/tls/roots.c \
              $(CLIENT_SRC)
HY2CHK_WRAP := -Wl,--wrap=tr_sock_prepare -Wl,--wrap=connect -Wl,--wrap=timerfd_settime -Wl,--wrap=__timerfd_settime64 \
               -Wl,--wrap=timerfd_create \
               -Wl,--wrap=qcssl_ctx_client -Wl,--wrap=os_rand_seed -Wl,--wrap=qc_open
$(T)/hy2checkmatch: tests/hy2checkmatch.c tests/hy2peer.h tests/hy2-frozen.h tests/certgen.h tests/unit.h $(HY2CHK_SRC) \
                    src/proto/transport/trdial.c $(T)/quic.o $(T)/qcssl.o $(T)/qcbrutal.o $(T)/certgen.o $(T)/scrypto.o $(T)/certzip.o $(HDR)
	$(CC) $(T_CFLAGS) $(QC_TFLAGS) -Isrc/proto/hysteria2 $(HY2CHK_WRAP) -o $@ tests/hy2checkmatch.c $(HY2CHK_SRC) \
		$(T)/quic.o $(T)/qcssl.o $(T)/qcbrutal.o $(T)/certgen.o $(T_CERTZIP) $(TN_LIB) $(T_CRYPTO)

HY2DIAL_SRC := src/proto/hysteria2/hy2dial.c src/proto/hysteria2/hy2loss.c $(filter-out src/proto/hysteria2/hy2run.c src/proto/hysteria2/hy2check.c,$(HY2CHK_SRC))
$(T)/hy2dialmatch: tests/hy2dialmatch.c tests/hy2peer.h tests/hy2-frozen.h tests/certgen.h tests/unit.h $(HY2DIAL_SRC) \
                   src/proto/transport/trdial.c $(T)/quic.o $(T)/qcssl.o $(T)/qcbrutal.o $(T)/certgen.o $(T)/scrypto.o $(T)/certzip.o $(HDR)
	$(CC) $(T_CFLAGS) $(QC_TFLAGS) -Isrc/proto/hysteria2 -Wl,--wrap=tr_sock_prepare -Wl,--wrap=connect \
		-Wl,--wrap=qc_stream_consumed -Wl,--wrap=qc_conn_consumed -o $@ tests/hy2dialmatch.c \
		$(HY2DIAL_SRC) $(T)/quic.o $(T)/qcssl.o $(T)/qcbrutal.o $(T)/certgen.o $(T_CERTZIP) $(TN_LIB) $(T_CRYPTO)

$(T)/scryptomatch: tests/scryptomatch.c tests/scrypto-pki.h tests/scrypto-pq.h $(T)/scrypto.o
	$(CC) $(T_CFLAGS) -o $@ tests/scryptomatch.c $(T_CRYPTO)

$(T)/hellofreeze: tests/hellofreeze.c tests/chello-frozen.h tests/chello-frozen-pq.h tests/chello-frozen-firefox.h src/proto/tls/reality.c src/proto/tls/reality.h src/lib/scrypto.h $(T)/scrypto.o
	$(CC) $(T_CFLAGS) -o $@ tests/hellofreeze.c $(T_CRYPTO)

$(T)/echmatch: tests/echmatch.c src/proto/tls/ech.c src/proto/tls/reality.c $(T)/scrypto.o $(HDR)
	$(CC) $(T_CFLAGS) -o $@ tests/echmatch.c src/proto/tls/ech.c src/proto/tls/reality.c $(T_CRYPTO)

# vlessmatch includes trdial.c, roots.c and trsec.c (it reaches their static seams).
VLM_SRC := $(filter-out src/proto/transport/trsec.c src/proto/tls/certverify.c,$(CLIENT_SRC)) src/proto/transport/sockmark.c
$(T)/vlessmatch: tests/vlessmatch.c tests/fmrelay.h tests/sdpeer.h $(CLIENT_SRC) src/proto/transport/trdial.c src/proto/transport/sockmark.c src/proto/tls/roots.c \
                 $(T)/certgen.o $(T)/scrypto.o $(T)/certzip.o $(HDR)
	$(CC) $(T_CFLAGS) -DSTEER_HAVE_X509WRITE -Wl,--wrap=sc_wipe -Wl,--wrap=time -Wl,--wrap=__time64 -o $@ tests/vlessmatch.c $(VLM_SRC) $(T)/certgen.o $(T_CERTZIP) $(T_CRYPTO)

$(T)/xconnmatch: tests/xconnmatch.c tests/vlessmatch.c tests/fmrelay.h tests/sdpeer.h $(CLIENT_SRC) src/proto/transport/trdial.c src/proto/transport/sockmark.c \
                 src/proto/tls/roots.c $(T)/certgen.o $(T)/scrypto.o $(T)/certzip.o $(HDR)
	$(CC) $(T_CFLAGS) -DSTEER_HAVE_X509WRITE -Wl,--wrap=sc_wipe -Wl,--wrap=send -Wl,--wrap=fm_grow -Wl,--wrap=malloc -Wl,--wrap=calloc -Wl,--wrap=time -Wl,--wrap=__time64 -o $@ tests/xconnmatch.c \
		$(VLM_SRC) $(T)/certgen.o $(T_CERTZIP) $(T_CRYPTO)

$(T)/probematch: tests/probematch.c $(CLIENT_SRC) src/proto/transport/trdial.c src/proto/transport/sockmark.c src/proto/tls/roots.c $(T)/scrypto.o $(T)/certzip.o $(HDR)
	$(CC) $(T_CFLAGS) -o $@ tests/probematch.c $(CLIENT_SRC) src/proto/transport/sockmark.c src/proto/tls/roots.c $(T_CERTZIP) $(T_CRYPTO)

FP_TEST_SRC := $(filter-out src/proto/transport/trfp.c,$(CLIENT_SRC)) src/proto/transport/trdial.c src/proto/transport/sockmark.c
$(T)/fpmatch: tests/fpmatch.c src/proto/transport/trfp.c $(FP_TEST_SRC) src/proto/tls/roots.c $(T)/scrypto.o $(T)/certzip.o $(HDR)
	$(CC) $(T_CFLAGS) -o $@ tests/fpmatch.c $(FP_TEST_SRC) src/proto/tls/roots.c $(T_CERTZIP) $(T_CRYPTO)

$(T)/takematch: tests/takematch.c src/proto/vless/vldial.c $(CLIENT_SRC) src/proto/transport/sockmark.c $(T)/scrypto.o $(T)/certzip.o $(HDR)
	$(CC) $(T_CFLAGS) -o $@ tests/takematch.c src/proto/vless/vldial.c $(TUNNEL_SRC) $(CLIENT_SRC) \
		src/ctlinb.c src/ctlnodes.c src/proto/transport/trdial.c src/proto/transport/sockmark.c src/proto/tls/roots.c $(T_CERTZIP) $(T_CRYPTO)

$(T)/xudpmatch: tests/xudpmatch.c src/proto/vless/vldial.c $(CLIENT_SRC) src/proto/transport/sockmark.c $(T)/scrypto.o $(T)/certzip.o $(HDR)
	$(CC) $(T_CFLAGS) -o $@ tests/xudpmatch.c $(TUNNEL_SRC) $(CLIENT_SRC) \
		src/ctlinb.c src/ctlnodes.c src/proto/transport/trdial.c src/proto/transport/sockmark.c src/proto/tls/roots.c $(T_CERTZIP) $(T_CRYPTO)

# -- probes for the interop scripts (a real server on the other side) --

$(T)/vencprobe: tests/vencprobe.c $(CLIENT_SRC) src/proto/transport/sockmark.c $(T)/scrypto.o $(T)/certzip.o $(HDR)
	$(CC) $(T_CFLAGS) -o $@ tests/vencprobe.c $(CLIENT_SRC) src/proto/transport/trdial.c src/proto/transport/sockmark.c \
		src/proto/tls/roots.c $(T_CERTZIP) $(T_CRYPTO)

$(T)/xudpprobe: tests/xudpprobe.c src/proto/vless/vldial.c $(CLIENT_SRC) src/proto/transport/sockmark.c $(T)/scrypto.o $(T)/certzip.o $(HDR)
	$(CC) $(T_CFLAGS) -o $@ tests/xudpprobe.c src/proto/vless/vldial.c $(CLIENT_SRC) \
		src/proto/transport/trdial.c src/proto/transport/sockmark.c src/proto/tls/roots.c $(T_CERTZIP) $(T_CRYPTO)

$(T)/h2tap: tests/h2tap.c tests/hpackread.h $(T)/scrypto.o
	$(CC) $(T_CFLAGS) -o $@ tests/h2tap.c $(T_CRYPTO)

HY2P_SRC := src/lib/portpick.c src/proto/tls/hpack.c src/proto/tls/reality.c src/lib/osrand.c src/proto/transport/sockmark.c $(SCRATCH_SRC)
$(T)/hy2probe: tests/hy2probe.c $(O)/quic.o $(O)/qcssl.o $(O)/qcbrutal.o $(O)/hy2wire.o $(O)/hy2conn.o $(O)/hy2run.o $(O)/scrypto.o $(HY2P_SRC) $(HDR)
	@mkdir -p $(T)
	$(CC) $(T_CFLAGS) -Isrc/proto/quic -Isrc/proto/hysteria2 -o $@ tests/hy2probe.c $(O)/quic.o $(O)/qcssl.o $(O)/qcbrutal.o \
		$(O)/hy2wire.o $(O)/hy2conn.o $(O)/hy2run.o $(HY2P_SRC) $(O)/scrypto.o $(NGTCP2_LIB) $(WOLFSSL_LIB) $(LDFLAGS) -lpthread

interop: $(T)/vencprobe $(T)/xudpprobe $(T)/h2tap $(T)/hy2probe $(O)/tunvless
	BUILD=$(T) sh tests/venc.sh
	BUILD=$(T) sh tests/xudp.sh
	BUILD=$(T) sh tests/ech.sh
	TUNVLESS=$(O)/tunvless H2TAP=$(T)/h2tap sh tests/run-xray.sh
	HY2PROBE=$(T)/hy2probe sh tests/hy2.sh
	TUNVLESS=$(O)/tunvless sh tests/hy2-tun.sh

speed: $(T)/hy2probe $(O)/tunvless
	TUNVLESS=$(O)/tunvless HY2PROBE=$(T)/hy2probe sh tests/hy2-speed.sh

e2e: $(O)/tunvless
	TUNVLESS=$(O)/tunvless sh tests/run-tunnel.sh
	TUNVLESS=$(O)/tunvless sh tests/run-tunnel.sh 3
	TUNVLESS=$(O)/tunvless sh tests/run-udp.sh
	TUNVLESS=$(O)/tunvless sh tests/run-tunnel-fin.sh
	TUNVLESS=$(O)/tunvless sh tests/rcvwnd.sh
	TUNVLESS=$(O)/tunvless sh tests/tun-mss.sh
	TUNVLESS=$(O)/tunvless sh tests/sigpipe.sh
	SIGPIPE_CHAIN=1 TUNVLESS=$(O)/tunvless sh tests/sigpipe.sh
	TUNVLESS=$(O)/tunvless sh tests/chain-cache.sh
	TUNVLESS=$(O)/tunvless sh tests/routes.sh
	TUNVLESS=$(O)/tunvless sh tests/failover.sh
	TUNVLESS=$(O)/tunvless sh tests/refill.sh
	TUNVLESS=$(O)/tunvless sh tests/control.sh
	TUNVLESS=$(O)/tunvless sh tests/tlsrss.sh
	sh tests/run-tun-gso.sh

clean:
	rm -rf $(O)

distclean: clean
	rm -rf $(WOLFSSL_SRC) $(ZLIB_SRC) $(BROTLI_SRC) $(ZSTD_SRC) $(NGTCP2_SRC)

FORCE:
