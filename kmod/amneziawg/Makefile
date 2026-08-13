include $(TOPDIR)/rules.mk
include $(INCLUDE_DIR)/kernel.mk

PKG_NAME:=amneziawg
PKG_VERSION:=3.1.20260812

PKG_SOURCE_URL:=https://github.com/amnezia-vpn/amneziawg-linux-kernel-module.git
PKG_SOURCE_VERSION:=v$(PKG_VERSION)
PKG_SOURCE_PROTO:=git

PKG_LICENSE:=GPL-2.0
PKG_LICENSE_FILES:=COPYING

PKG_BUILD_DIR:=$(KERNEL_BUILD_DIR)/$(PKG_NAME)-$(PKG_VERSION)

# AmneziaWG's makefile needs this to know where to build the kernel module
export KERNELDIR:=$(LINUX_DIR)

include $(INCLUDE_DIR)/package.mk

define Package/amneziawg/Default
  SECTION:=net
  CATEGORY:=Network
  SUBMENU:=VPN
  URL:=https://amnezia.org/
  MAINTAINER:=Amnezia VPN <admin@amnezia.org>
endef

define Package/amneziawg/Default/description
  AmneziaWG is a modified WireGuard VPN protocol with traffic obfuscation
  capabilities designed to bypass DPI (Deep Packet Inspection) filters.
  It maintains WireGuard's cryptographic security while adding dynamic
  headers, handshake randomization, junk packets, and protocol masking.
  AWG 2.0 adds H1-H4 header ranges, S3-S4 prefixes, and I1-I5 concealment packets.
endef

define Package/amneziawg
  $(call Package/amneziawg/Default)
  TITLE:=AmneziaWG meta-package
  DEPENDS:=+kmod-amneziawg
endef

include $(INCLUDE_DIR)/package-defaults.mk

# Used by Build/Compile/Default
MAKE_PATH:=src

define Build/Compile
	+$(MAKE) $(KERNEL_MAKEOPTS) M="$(PKG_BUILD_DIR)/src" modules
endef

define Package/amneziawg/install
  true
endef

define Package/amneziawg/description
  $(call Package/amneziawg/Default/description)
endef

define KernelPackage/amneziawg
  SECTION:=kernel
  CATEGORY:=Kernel modules
  SUBMENU:=Network Support
  TITLE:=AmneziaWG kernel module
  URL:=https://amnezia.org/
  DEPENDS:=+kmod-udptunnel6 +kmod-udptunnel4
  FILES:=$(PKG_BUILD_DIR)/src/amneziawg.ko
endef

define KernelPackage/amneziawg/description
  $(call Package/amneziawg/Default/description)

  This package provides the kernel module for AmneziaWG.
endef

$(eval $(call BuildPackage,amneziawg))
$(eval $(call KernelPackage,amneziawg))
