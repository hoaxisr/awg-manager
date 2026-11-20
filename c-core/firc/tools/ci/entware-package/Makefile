include $(TOPDIR)/rules.mk

include ./version.mk

PKG_NAME:=firc
PKG_VERSION:=$(FIRC_VERSION)
PKG_RELEASE:=$(FIRC_RELEASE)
PKG_LICENSE:=GPL-3.0-or-later
PKG_LICENSE_FILES:=LICENSE
PKG_MAINTAINER:=hoaxisr <https://github.com/hoaxisr>
PKG_SOURCE:=firc-source.tar.gz
PKG_SOURCE_URL:=file://$(CURDIR)
PKG_HASH:=skip
PKG_MIRROR_HASH:=skip
PKG_BUILD_DIR:=$(BUILD_DIR)/$(PKG_NAME)-$(PKG_VERSION)
PKG_BUILD_PARALLEL:=1
PKGARCH:=$(FIRC_TARGET)

include $(INCLUDE_DIR)/package.mk

# The SDK ships without toolchain stamps; restore them so packaging does not rebuild GCC.
FIRC_TOOLCHAIN_DIR:=$(BUILD_DIR)/toolchain
FIRC_TOOLCHAIN_PREPARED:=.prepared_ad8c94d059fbcd3e4fe92dada0211bba_18f1e190c5d53547fed41a3eaa76e9e9
FIRC_TOOLCHAIN_CONFIGURED:=.configured_68b329da9893e34099c7d8ad5cb9c940
$(shell mkdir -p $(FIRC_TOOLCHAIN_DIR) && \
	touch \
		$(FIRC_TOOLCHAIN_DIR)/$(FIRC_TOOLCHAIN_PREPARED)_check \
		$(FIRC_TOOLCHAIN_DIR)/$(FIRC_TOOLCHAIN_PREPARED) \
		$(FIRC_TOOLCHAIN_DIR)/$(FIRC_TOOLCHAIN_CONFIGURED) \
		$(FIRC_TOOLCHAIN_DIR)/.built)

FIRC_DYNAMIC_LINKER:=ld.so.1
ifneq ($(findstring aarch64,$(FIRC_TARGET)),)
FIRC_DYNAMIC_LINKER:=ld-linux-aarch64.so.1
endif

define Package/firc
  SECTION:=net
  CATEGORY:=Network
  TITLE:=DNS-based routing application
  URL:=https://github.com/hoaxisr/firc
  DEPENDS:=+libyaml +libpcre2 +libmnl +libcurl +zlib +cJSON +socat +ca-bundle
  CONFLICTS:=tunvless
endef

define Package/firc/description
 DNS-based routing application for Entware.
endef

define Package/firc/conffiles
/opt/etc/firc/firc.conf
/opt/etc/firc/groups.yaml
endef

# Mirrors files/entware/_ipk/control/postinst with every `$` doubled; CI diffs the two.
define Package/firc/postinst
#!/bin/sh

# Strip fetched list rules from an old groups.yaml before start: a huge file OOM-kills fircd on load.
GROUPS=/opt/etc/firc/groups.yaml

# Smaller files keep their subscriptions' rules: the daemon loads them.
MIGRATE_MIN_BYTES=$${MIGRATE_MIN_BYTES:-262144}

migrate_groups() {
    f=$$1
    [ -f "$$f" ] || return 0
    strip=1
    if [ "$${FIRC_MIGRATE_FORCE:-0}" != 1 ]; then
        size=$$(wc -c < "$$f" 2>/dev/null) || return 0
        [ "$$size" -ge "$$MIGRATE_MIN_BYTES" ] || strip=0
    fi
    # A YAML sequence may sit at its key's own indent, so the skip also runs over `-` items at that indent.
    LC_ALL=C awk -v countfile="$$f.off" -v strip="$$strip" '
        BEGIN {
            for (i = 1; i < 256; i++) { ORD[sprintf("%c", i)] = i }
            HEX = "0123456789abcdef"
            Q = "\047"
        }
        function hexval(h,   v, i) {
            v = 0
            for (i = 1; i <= length(h); i++) { v = v * 16 + index(HEX, tolower(substr(h, i, 1))) - 1 }
            return v
        }
        function utf8(cp) {
            if (cp < 32 || (cp >= 127 && cp < 160) || cp == 8232 || cp == 8233 || (cp >= 55296 && cp < 57344) || cp > 1114111) { return " " }
            if (cp < 128) { return sprintf("%c", cp) }
            if (cp < 2048) { return sprintf("%c%c", 192 + int(cp / 64), 128 + cp % 64) }
            if (cp < 65536) { return sprintf("%c%c%c", 224 + int(cp / 4096), 128 + int(cp / 64) % 64, 128 + cp % 64) }
            return sprintf("%c%c%c%c", 240 + int(cp / 262144), 128 + int(cp / 4096) % 64, 128 + int(cp / 64) % 64, 128 + cp % 64)
        }
        function unquote(v,   out, i, c, e, n) {
            c = substr(v, 1, 1)
            if (c == Q) {
                out = ""
                for (i = 2; i <= length(v); i++) {
                    c = substr(v, i, 1)
                    if (c == Q) {
                        if (substr(v, i + 1, 1) != Q) { break }
                        i++
                    }
                    out = out c
                }
                return out
            }
            if (c == "\"") {
                out = ""
                for (i = 2; i <= length(v); i++) {
                    c = substr(v, i, 1)
                    if (c == "\"") { break }
                    if (c != "\\") { out = out c; continue }
                    e = substr(v, ++i, 1)
                    n = (e == "x") ? 2 : (e == "u") ? 4 : (e == "U") ? 8 : 0
                    if (n > 0) {
                        out = out utf8(hexval(substr(v, i + 1, n)))
                        i += n
                    } else if (e == "\\" || e == "\"" || e == "/") {
                        out = out e
                    } else if (e == " ") {
                        out = out " "
                    } else if (e == "_") {
                        out = out utf8(160)
                    } else {
                        out = out " "
                    }
                }
                return out
            }
            sub(/[ \t]+#.*$$/, "", v)
            return v
        }
        function clean(v,   out, i, b, k) {
            out = ""
            for (i = 1; i <= length(v); i++) {
                b = ORD[substr(v, i, 1)]
                if (b < 32 || b == 127) { out = out " "; continue }
                if (b == 194 && ORD[substr(v, i + 1, 1)] >= 128 && ORD[substr(v, i + 1, 1)] < 160) { out = out " "; i++; continue }
                if (b == 226 && ORD[substr(v, i + 1, 1)] == 128 && (ORD[substr(v, i + 2, 1)] == 168 || ORD[substr(v, i + 2, 1)] == 169)) { out = out " "; i += 2; continue }
                out = out substr(v, i, 1)
            }
            sub(/^ +/, "", out)
            sub(/ +$$/, "", out)
            if (length(out) > 256) {
                k = 256
                while (k > 0 && ORD[substr(out, k + 1, 1)] >= 128 && ORD[substr(out, k + 1, 1)] < 192) { k-- }
                out = substr(out, 1, k)
                sub(/ +$$/, "", out)
            }
            return out
        }
        function key_of(line) {
            if (substr(line, 1, kcol) !~ /^ *(- *)?$$/) { return "" }
            line = substr(line, kcol + 1)
            if (line !~ /^[A-Za-z_][A-Za-z0-9_]*:([ \t]|$$)/) { return "" }
            return substr(line, 1, index(line, ":") - 1)
        }
        function value_of(i,   v, j, t, block) {
            v = substr(L[i], kcol + 1)
            v = substr(v, index(v, ":") + 1)
            sub(/^[ \t]+/, "", v)
            sub(/[ \t]+$$/, "", v)
            block = substr(v, 1, 1)
            if (block == "|" || block == ">") { v = "" }
            for (j = i + 1; j <= n && key_of(L[j]) == ""; j++) {
                t = L[j]
                sub(/^[ \t]+/, "", t)
                sub(/[ \t]+$$/, "", t)
                v = v (v == "" ? "" : " ") t
            }
            last = j - 1
            return (block == "|" || block == ">") ? v : unquote(v)
        }
        function flush_item(   i, name_i, rule_i, has_comment, k, nm, rv, end_i, out) {
            if (n == 0) { return }
            name_i = rule_i = has_comment = 0
            for (i = 1; i <= n; i++) {
                k = key_of(L[i])
                if (k == "name") { name_i = i } else if (k == "rule") { rule_i = i } else if (k == "comment") { has_comment = 1 }
            }
            if (name_i > 0 && !has_comment) {
                rv = rule_i > 0 ? value_of(rule_i) : ""
                nm = clean(value_of(name_i))
                end_i = last
                out = ""
                if (nm != "" && nm != rv) {
                    gsub(Q, Q Q, nm)
                    out = substr(L[name_i], 1, kcol) "comment: " Q nm Q
                } else if (name_i == 1 && end_i < n) {
                    L[end_i + 1] = substr(L[1], 1, kcol) substr(L[end_i + 1], kcol + 1)
                }
                for (i = 1; i <= n; i++) {
                    if (i == name_i && out != "") { print out }
                    if (i < name_i || i > end_i) { print L[i] }
                }
            } else {
                for (i = 1; i <= n; i++) { print L[i] }
            }
            n = 0
        }
        {
            if (in_rules) {
                if ($$0 ~ /^[ \t]*(#.*)?$$/) {
                    if (n > 0) { L[++n] = $$0 } else { print }
                    next
                }
                match($$0, /^[ \t]*/)
                c = substr($$0, RLENGTH + 1, 1)
                if (RLENGTH > rules_indent || (RLENGTH == rules_indent && c == "-")) {
                    if (c == "-" && (n == 0 || RLENGTH == item_indent)) {
                        flush_item()
                        item_indent = RLENGTH
                        match($$0, /^[ \t]*-[ \t]*/)
                        kcol = RLENGTH
                    }
                    L[++n] = $$0
                    next
                }
                flush_item()
                in_rules = 0
            }
        }
        /^[^ \t#-]/ { in_subs = ($$0 ~ /^subscriptions:[[:space:]]*$$/); in_groups = ($$0 ~ /^groups:[[:space:]]*$$/); skip = 0 }
        {
            if (skip) {
                if ($$0 ~ /^[[:space:]]*$$/) { next }
                match($$0, /^[ \t]*/)
                if (RLENGTH > skip_indent) {
                    if ($$0 ~ /^[ \t]*enable:[[:space:]]*false[[:space:]]*$$/) { off++ }
                    next
                }
                if (RLENGTH == skip_indent && substr($$0, RLENGTH + 1, 1) == "-") { next }
                skip = 0
            }
            if (strip && in_subs && $$0 ~ /^[ \t]*rules:/) {
                match($$0, /^[ \t]*/)
                skip_indent = RLENGTH
                skip = 1
                next
            }
            if (in_groups && $$0 ~ /^[ \t]*(-[ \t]*)?rules:[ \t]*$$/) {
                rules_indent = index($$0, "rules:") - 1
                in_rules = 1
                n = 0
            }
            print
        }
        END { flush_item(); print off + 0 > countfile }
    ' "$$f" > "$$f.new" || return 1

    if [ ! -s "$$f.new" ]; then
        rm -f "$$f.new" "$$f.off"
        return 1
    fi
    if cmp -s "$$f" "$$f.new"; then
        rm -f "$$f.new" "$$f.off"
        return 0
    fi
    [ -e "$$f.pre-0027.bak" ] || cp "$$f" "$$f.pre-0027.bak" || { rm -f "$$f.new"; return 1; }
    mv "$$f.new" "$$f" || return 1

    off=$$(cat "$$f.off" 2>/dev/null) || off=0
    rm -f "$$f.off"
    if [ "$${off:-0}" -gt 0 ]; then
        echo "firc: $$f was too large to load, so its subscriptions' rules were" >&2
        echo "firc: dropped and will be refetched. $$off rule(s) you had switched off" >&2
        echo "firc: could not be carried over; the old file is at $$f.pre-0027.bak" >&2
    fi
    return 0
}

CONF=$${FIRC_CONF:-/opt/etc/firc/firc.conf}
SOCK=$${FIRC_SOCK:-/tmp/firc/firc.sock}

conf_web_port() {
    awk '/^  httpWeb:/ { w = 1; next } /^  [^ ]/ { w = 0 } w && /^ +port:/ { p = $$2; exit } END { print (p == "" ? 666 : p) }' "$$CONF" 2>/dev/null
}

report_dns() {
    dns=$$(printf '%s' "$$ans" |
        sed -n -E 's/.*"dnsProxy":\{"listening":false,"port":([0-9]*),"proto":"([a-z]*)","error":"(([^"\\]|\\.)*)".*/\1\/\2 \3/p' |
        sed 's/\\\(.\)/\1/g')
    [ -n "$$dns" ] || return 0
    echo "firc: the DNS proxy is not running: $${dns#* }" >&2
    echo "firc: until port $${dns%% *} is free, firc answers no DNS: no new name gets a fake address" >&2
    echo "firc: and name rules do not apply; subnet rules and addresses already handed out still route." >&2
    echo "firc: firc takes the port by itself once it is free, or set app.dnsProxy.host.port in $$CONF and restart" >&2
}

report_web() {
    ans=$$(printf 'GET /api/v1/system/settings HTTP/1.0\r\n\r\n' |
        socat -t10 - UNIX-CONNECT:"$$SOCK" 2>/dev/null)
    web=$$(printf '%s' "$$ans" |
        sed -n 's/.*"webUi":{"port":\([0-9]*\),"movedFrom":\([0-9]*\),"lanAddress":"\([^"]*\)".*/\1 \2 \3/p')
    if [ -z "$$web" ]; then
        echo "firc: could not ask the daemon where the WebUI is; firc.conf sets port $$(conf_web_port)" >&2
        return 0
    fi
    report_dns
    set -- $$web
    port=$$1 moved=$$2 addr=$${3:-}
    if [ "$$port" = 0 ]; then
        echo "firc: the WebUI is not listening (turned off, or every port tried is in use);" >&2
        echo "firc: set app.httpWeb.host.port in $$CONF and run /opt/etc/init.d/S99firc restart" >&2
        return 0
    fi
    [ "$$moved" = 0 ] || echo "firc: port $$moved is in use; the WebUI moved to $$port"
    case "$$addr" in
        "") addr="<router address>" ;;
        *:*) addr="[$$addr]" ;;
    esac
    echo "firc: WebUI at http://$$addr:$$port"
}

# Used by the differential tests; opkg never passes them.
if [ "$${1:-}" = "--migrate-only" ]; then
    migrate_groups "$${2:-$$GROUPS}"
    exit $$?
fi
if [ "$${1:-}" = "--report-web" ]; then
    report_web
    exit $$?
fi

migrate_groups "$$GROUPS" || {
    echo "firc: could not rewrite $$GROUPS; the original is at $$GROUPS.pre-0027.bak" >&2
}

# A failed start must fail the configure step so opkg leaves the package unpacked.
/opt/etc/init.d/S99firc restart || {
    echo "firc: the daemon did not start; the line above says why, and the system log has it under fircd" >&2
    exit 1
}

report_web
exit 0
endef

# Mirrors files/entware/_ipk/control/prerm with every `$` doubled.
define Package/firc/prerm
#!/bin/sh

# A daemon left running from an unlinked binary keeps its rules until reboot, so refuse if it will not stop.
ACTION="$${1:-}"
if [ -x /opt/etc/init.d/S99firc ]; then
  /opt/etc/init.d/S99firc stop || {
    if [ "$$ACTION" = upgrade ]; then
      MSG="firc: the running daemon would not stop; the upgrade is refused while it still routes. Reboot, then upgrade."
    else
      MSG="firc: the daemon would not stop; its files are not removed while it still routes. Reboot, then remove (or opkg remove --force-remove)."
    fi
    echo "$$MSG" >&2
    logger -t firc "$$MSG" 2>/dev/null
    exit 1
  }
fi

# A stop keeps the pool's barrier and reject routes for the next daemon; purge them only on "remove".
case "$$ACTION" in
  remove)
    try_purge() {
      # fircd's status goes out on fd 3: a pipeline reports logger's status, not the purge's.
      rc=`{ { /opt/bin/fircd --purge; echo $$? >&3; } | logger -t fircd 2>/dev/null; } 3>&1`
      return $${rc:-1}
    }
    if [ -x /opt/bin/fircd ]; then
      try_purge || try_purge ||
        logger -t firc "purge did not finish; some rules or routes may be left until the next reboot"
    fi
    rm -rf /tmp/firc-init
    rm -rf /opt/var/lib/firc/tunnels
    rm -rf /opt/var/lib/firc/update
    rm -f /opt/var/lib/firc/hwid
    rm -f /opt/var/lib/firc/rci-token
    rm -f /opt/var/lib/firc/opkgtun
    rmdir /opt/var/lib/firc 2>/dev/null
    ;;
  upgrade) ;;
  *) logger -t firc "prerm: action '$$ACTION' unknown; the pool's routes and barrier are left for the next daemon" 2>/dev/null ;;
esac

exit 0
endef

define Build/Compile
	$(MAKE) -C $(PKG_BUILD_DIR)/src/backend-c \
		BUILD=entware-$(FIRC_TARGET) \
		FIRC_VERSION="$(PKG_VERSION)" \
		FIRC_TARGET="$(FIRC_TARGET)" FIRC_REVISION="$(PKG_RELEASE)" \
		CROSS_COMPILE="$(TARGET_CROSS)" \
		PLATFORM=entware \
		ENTWARE_KN=1 \
		WITH_DEPS=1 \
		CFLAGS_EXTRA="$(TARGET_CFLAGS) -isystem $(STAGING_DIR)/opt/include" \
		LDLIBS="-lpthread -Wl,-Bstatic -latomic -Wl,-Bdynamic" \
		LDFLAGS_EXTRA="$(TARGET_LDFLAGS) \
			-Wl,--dynamic-linker=/opt/lib/$(FIRC_DYNAMIC_LINKER) \
			-Wl,-rpath,/opt/lib \
			-L$(STAGING_DIR)/opt/lib \
			-Wl,-rpath-link,$(STAGING_DIR)/opt/lib"
	$(MAKE) -C $(PKG_BUILD_DIR)/src/tunvless CC="$(TARGET_CC)" AR="$(TARGET_CROSS)ar" \
		CFLAGS="$(TARGET_CFLAGS)" VERSION="$(PKG_VERSION)" \
		LDFLAGS="$(TARGET_LDFLAGS) -Wl,--dynamic-linker=/opt/lib/$(FIRC_DYNAMIC_LINKER) -Wl,-rpath,/opt/lib" \
		all
endef

define Package/firc/install
	$(INSTALL_DIR) $(1)/opt/bin
	$(INSTALL_BIN) \
		$(PKG_BUILD_DIR)/src/backend-c/build/entware-$(FIRC_TARGET)/fircd \
		$(1)/opt/bin/fircd
	$(INSTALL_BIN) $(PKG_BUILD_DIR)/src/tunvless/out/tunvless $(1)/opt/bin/tunvless


	$(INSTALL_DIR) $(1)/opt/etc/firc
	$(INSTALL_CONF) $(PKG_BUILD_DIR)/files/common/etc/firc/firc.conf \
		$(1)/opt/etc/firc/firc.conf
	$(INSTALL_CONF) $(PKG_BUILD_DIR)/files/common/etc/firc/groups.yaml \
		$(1)/opt/etc/firc/groups.yaml

	$(INSTALL_DIR) $(1)/opt/etc/init.d
	$(INSTALL_BIN) $(PKG_BUILD_DIR)/files/entware_kn/etc/init.d/S99firc \
		$(1)/opt/etc/init.d/S99firc

	$(INSTALL_DIR) $(1)/opt/etc/ndm/netfilter.d
	$(INSTALL_BIN) \
		$(PKG_BUILD_DIR)/files/entware_kn/etc/ndm/netfilter.d/100-firc \
		$(1)/opt/etc/ndm/netfilter.d/100-firc
	$(INSTALL_DIR) $(1)/opt/etc/ndm/iflayerchanged.d
	$(INSTALL_BIN) \
		$(PKG_BUILD_DIR)/files/entware_kn/etc/ndm/netfilter.d/100-firc \
		$(1)/opt/etc/ndm/iflayerchanged.d/100-firc
endef

$(eval $(call BuildPackage,firc))
