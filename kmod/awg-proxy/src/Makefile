# Standalone out-of-tree build (for development/testing on host)
# For cross-compilation, use the SDK package or build.sh

# 1.4.0: AWG 3.1 — header protection (HP_KEY) + random trailers (RT).
# 1.3.0: IPv6 endpoint support — "[v6]:port" accepted by /proc add/del,
# printed in /proc list; per-family remote socket + udp_tunnel6 TX path.
# Keep in sync with package/Makefile PKG_VERSION.
AWG_PROXY_VERSION ?= 1.4.0

KDIR ?= /lib/modules/$(shell uname -r)/build

all:
	$(MAKE) -C $(KDIR) M=$(PWD) AWG_PROXY_VERSION=$(AWG_PROXY_VERSION) modules

clean:
	$(MAKE) -C $(KDIR) M=$(PWD) clean
