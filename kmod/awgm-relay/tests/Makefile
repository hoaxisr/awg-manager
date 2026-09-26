# Хост-тесты kmod/awgm-relay/src (без заголовков ядра).
#   make          — test_phobos + test_config
#   make compat PHOBOS_SRC=/path/to/wg-obfuscator — совместимость с эталоном
#                 Phobos (GPL-3): эталон берётся снаружи, в репозиторий не
#                 кладётся, бинарь не распространяется (спека §3.8).
CC     ?= gcc
CFLAGS := -O0 -g3 -std=c11 -Wall -Wextra -Werror
SRC    := ../src

.PHONY: all test compat clean
all: test

test: test_phobos test_config
	./test_phobos
	./test_config

test_phobos: test_phobos.c $(SRC)/phobos.c $(SRC)/phobos.h
	$(CC) $(CFLAGS) -o $@ test_phobos.c $(SRC)/phobos.c

test_config: test_config.c $(SRC)/config.c $(SRC)/config.h $(SRC)/phobos.c
	$(CC) $(CFLAGS) -o $@ test_config.c $(SRC)/config.c $(SRC)/phobos.c

compat: compat_phobos.c $(SRC)/phobos.c
	@test -n "$(PHOBOS_SRC)" || { echo "PHOBOS_SRC не задан"; exit 1; }
	$(CC) -O0 -g -std=gnu11 -Wall -I$(PHOBOS_SRC) -o compat_phobos compat_phobos.c $(SRC)/phobos.c
	./compat_phobos

clean:
	rm -f test_phobos test_config compat_phobos
