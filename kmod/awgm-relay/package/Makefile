include $(TOPDIR)/rules.mk
include $(INCLUDE_DIR)/kernel.mk

PKG_NAME:=awgm-relay
# 0.1.1: cond_resched после пакета (F472).
# 0.2.0: таблица трансформаций (провод и /proc без изменений).
# 0.2.1: MEDIA без obfuscate-bytes = 16, как у сервера Phobos (F480).
PKG_VERSION:=0.2.1
PKG_LICENSE:=GPL-2.0
PKG_LICENSE_FILES:=COPYING
PKG_BUILD_DIR:=$(KERNEL_BUILD_DIR)/$(PKG_NAME)-$(PKG_VERSION)

include $(INCLUDE_DIR)/package.mk

define Build/Prepare
	mkdir -p $(PKG_BUILD_DIR)/src
	$(CP) ./src/* $(PKG_BUILD_DIR)/src/
	$(CP) ./COPYING $(PKG_BUILD_DIR)/COPYING
endef

define Build/Compile
	+$(MAKE) $(KERNEL_MAKEOPTS) M="$(PKG_BUILD_DIR)/src" \
		AWGM_RELAY_VERSION="$(PKG_VERSION)" modules
endef

define KernelPackage/awgm-relay
  SECTION:=kernel
  CATEGORY:=Kernel modules
  SUBMENU:=Network Support
  TITLE:=awgm_relay - kernel relay for obfuscated NativeWG tunnels
  # udp_tunnel: encap rx + udp_tunnel_xmit_skb; IPv4-only — без udptunnel6.
  DEPENDS:=+kmod-udptunnel4
  FILES:=$(PKG_BUILD_DIR)/src/awgm_relay.ko
endef

$(eval $(call KernelPackage,awgm-relay))
