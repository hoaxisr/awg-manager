CC      ?= cc
CFLAGS  ?= -O2 -Wall -Wextra -std=gnu11 -pthread
LDFLAGS ?= -lpthread

SRC := main.c crypto.c finiteproto.c sniff_and_dial.c vless_xhttp.c
HDR := xhttpd.h

.PHONY: all test clean

all: awgm-xhttpd

awgm-xhttpd: $(SRC) $(HDR)
	$(CC) $(CFLAGS) -o $@ $(SRC) $(LDFLAGS)

xhttpd_audit_test: tests/xhttpd_audit_test.c $(SRC) $(HDR)
	$(CC) $(CFLAGS) -Wno-unused-function -DXHTTPD_NO_MAIN -I. -o $@ tests/xhttpd_audit_test.c $(SRC) $(LDFLAGS)

test: xhttpd_audit_test
	./xhttpd_audit_test

clean:
	rm -f awgm-xhttpd xhttpd_audit_test
