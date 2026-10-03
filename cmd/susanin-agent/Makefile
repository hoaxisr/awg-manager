CC ?= cc
CFLAGS ?= -O2 -std=c11 -Wall -Wextra -Wpedantic
LDFLAGS ?=
PREFIX ?= /opt
BINDIR = $(PREFIX)/susanin/bin
CONFDIR = $(PREFIX)/susanin/etc
VARDIR = $(PREFIX)/susanin/var

SRCS = src/main.c src/config.c src/discover.c src/conntrack.c src/state.c \
       src/backend.c src/classifier.c src/health.c src/engine.c src/log.c src/ops.c \
       src/vpn_always.c src/vpn_never.c
OBJS = $(SRCS:.c=.o)

TARGET = susanin-agent

.PHONY: all clean install

all: $(TARGET)

$(TARGET): $(OBJS)
	$(CC) $(CFLAGS) -o $@ $(OBJS) $(LDFLAGS)

%.o: %.c
	$(CC) $(CFLAGS) -c -o $@ $<

install: $(TARGET)
	install -d $(DESTDIR)$(BINDIR) $(DESTDIR)$(CONFDIR) $(DESTDIR)$(VARDIR)
	install -m 0755 $(TARGET) $(DESTDIR)$(BINDIR)/$(TARGET)

clean:
	rm -f $(TARGET) $(OBJS)
