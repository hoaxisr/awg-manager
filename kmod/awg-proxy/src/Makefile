# Standalone out-of-tree build (for development/testing on host)
# For cross-compilation, use the SDK package or build.sh

AWG_PROXY_VERSION ?= 1.1.0

KDIR ?= /lib/modules/$(shell uname -r)/build

all:
	$(MAKE) -C $(KDIR) M=$(PWD) AWG_PROXY_VERSION=$(AWG_PROXY_VERSION) modules

clean:
	$(MAKE) -C $(KDIR) M=$(PWD) clean
