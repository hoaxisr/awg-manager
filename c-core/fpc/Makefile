CC      ?= cc
CFLAGS  ?= -O2 -Wall -Wextra -std=gnu11 -pthread
LDFLAGS ?= -lpthread

SRC := fpc.c ../xhttpd/crypto.c
HDR := ../xhttpd/xhttpd.h

.PHONY: all test clean

all: awgm-fpc

awgm-fpc: $(SRC) $(HDR)
	$(CC) $(CFLAGS) -o $@ $(SRC) $(LDFLAGS)

fpc_audit_test: tests/fpc_audit_test.c $(SRC) $(HDR)
	$(CC) $(CFLAGS) -Wno-unused-function -DFPC_NO_MAIN -I. -o $@ tests/fpc_audit_test.c ../xhttpd/crypto.c $(LDFLAGS)

test: fpc_audit_test
	./fpc_audit_test

clean:
	rm -f awgm-fpc fpc_audit_test
