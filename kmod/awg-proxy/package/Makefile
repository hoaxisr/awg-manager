include $(TOPDIR)/rules.mk
include $(INCLUDE_DIR)/kernel.mk

PKG_NAME:=awg-proxy
PKG_VERSION:=1.1.0

PKG_LICENSE:=GPL-2.0
PKG_LICENSE_FILES:=COPYING

PKG_BUILD_DIR:=$(KERNEL_BUILD_DIR)/$(PKG_NAME)-$(PKG_VERSION)

include $(INCLUDE_DIR)/package.mk

define Package/awg-proxy/Default
  SECTION:=net
  CATEGORY:=Network
  SUBMENU:=VPN
  MAINTAINER:=hoaxisr
endef

define Package/awg-proxy/Default/description
  AWG Proxy is a netfilter kernel module that transparently transforms
  WireGuard packets into AmneziaWG format on the fly. This allows using
  the native kernel WireGuard implementation (nwg) at full speed while
  adding AWG obfuscation without userspace overhead.
endef

define Package/awg-proxy
  $(call Package/awg-proxy/Default)
  TITLE:=AWG Proxy meta-package
  DEPENDS:=+kmod-awg-proxy
endef

include $(INCLUDE_DIR)/package-defaults.mk

define Build/Prepare
	mkdir -p $(PKG_BUILD_DIR)/src
	$(CP) ../src/* $(PKG_BUILD_DIR)/src/
endef

define Build/Compile
	+$(MAKE) $(KERNEL_MAKEOPTS) M="$(PKG_BUILD_DIR)/src" \
		AWG_PROXY_VERSION="$(PKG_VERSION)" modules
endef

define Package/awg-proxy/install
  true
endef

define Package/awg-proxy/description
  $(call Package/awg-proxy/Default/description)
endef

define KernelPackage/awg-proxy
  SECTION:=kernel
  CATEGORY:=Kernel modules
  SUBMENU:=Network Support
  TITLE:=AWG Proxy netfilter module
  FILES:=$(PKG_BUILD_DIR)/src/awg_proxy.ko
endef

define KernelPackage/awg-proxy/description
  $(call Package/awg-proxy/Default/description)

  This package provides the kernel module for AWG Proxy.
endef

$(eval $(call BuildPackage,awg-proxy))
$(eval $(call KernelPackage,awg-proxy))
