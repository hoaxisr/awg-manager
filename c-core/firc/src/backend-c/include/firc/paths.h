#ifndef FIRC_PATHS_H
#define FIRC_PATHS_H

#include <stdlib.h>

#if defined(FIRC_PLATFORM_ENTWARE)

#ifndef FIRC_APP_SHARE_DIR
#define FIRC_APP_SHARE_DIR "/opt/usr/share/firc"
#endif
#ifndef FIRC_APP_CONF_DIR
#define FIRC_APP_CONF_DIR "/opt/etc/firc"
#endif
/* Run dir (tmpfs): pool file, instance lock, API socket; the netfilter.d hook and S99firc repeat the socket path. */
#ifndef FIRC_APP_RUN_DIR
#define FIRC_APP_RUN_DIR "/tmp/firc"
#endif
#ifndef FIRC_SOCK_PATH
#define FIRC_SOCK_PATH FIRC_APP_RUN_DIR "/firc.sock"
#endif
#ifndef FIRC_PASSWD_FILE
#define FIRC_PASSWD_FILE "/opt/etc/passwd"
#endif
#ifndef FIRC_SHADOW_FILE
#define FIRC_SHADOW_FILE "/opt/etc/shadow"
#endif
/* Run by POST /api/v1/system/restart; its boot loop watches the daemon only in the first five minutes of uptime. */
#ifndef FIRC_INIT_SCRIPT
#define FIRC_INIT_SCRIPT "/opt/etc/init.d/S99firc"
#endif
#ifndef FIRC_TUNVLESS_BIN
#define FIRC_TUNVLESS_BIN "/opt/bin/tunvless"
#endif
#ifndef FIRC_TUNNELS_CACHE_DIR
#define FIRC_TUNNELS_CACHE_DIR "/opt/var/lib/firc/tunnels"
#endif
#ifndef FIRC_UPDATE_DIR
#define FIRC_UPDATE_DIR "/opt/var/lib/firc/update"
#endif
#ifndef FIRC_HWID_PATH
#define FIRC_HWID_PATH "/opt/var/lib/firc/hwid"
#endif
#ifndef FIRC_RCI_TOKEN_PATH
#define FIRC_RCI_TOKEN_PATH "/opt/var/lib/firc/rci-token"
#endif
#ifndef FIRC_OPKGTUN_INTENT_PATH
#define FIRC_OPKGTUN_INTENT_PATH "/opt/var/lib/firc/opkgtun"
#endif
#ifndef FIRC_NDMC_BIN
#define FIRC_NDMC_BIN "/bin/ndmc"
#endif
#ifndef FIRC_SH_BIN
#define FIRC_SH_BIN "/opt/bin/sh"
#endif
#ifndef FIRC_OPKG_BIN
#define FIRC_OPKG_BIN "/opt/bin/opkg"
#endif

#else

#ifndef FIRC_APP_SHARE_DIR
#define FIRC_APP_SHARE_DIR "/usr/share/firc"
#endif
#ifndef FIRC_APP_CONF_DIR
#define FIRC_APP_CONF_DIR "/etc/firc"
#endif
#ifndef FIRC_APP_RUN_DIR
#define FIRC_APP_RUN_DIR "/run/firc"
#endif
#ifndef FIRC_SOCK_PATH
#define FIRC_SOCK_PATH FIRC_APP_RUN_DIR "/firc.sock"
#endif
#ifndef FIRC_PASSWD_FILE
#define FIRC_PASSWD_FILE "/etc/passwd"
#endif
#ifndef FIRC_SHADOW_FILE
#define FIRC_SHADOW_FILE "/etc/shadow"
#endif
#ifndef FIRC_INIT_SCRIPT
#define FIRC_INIT_SCRIPT "/opt/etc/init.d/S99firc"
#endif
#ifndef FIRC_TUNVLESS_BIN
#define FIRC_TUNVLESS_BIN "tunvless"
#endif
#ifndef FIRC_TUNNELS_CACHE_DIR
#define FIRC_TUNNELS_CACHE_DIR "/var/lib/firc/tunnels"
#endif
#ifndef FIRC_UPDATE_DIR
#define FIRC_UPDATE_DIR "/var/lib/firc/update"
#endif
#ifndef FIRC_HWID_PATH
#define FIRC_HWID_PATH "/var/lib/firc/hwid"
#endif
#ifndef FIRC_RCI_TOKEN_PATH
#define FIRC_RCI_TOKEN_PATH "/var/lib/firc/rci-token"
#endif
#ifndef FIRC_OPKGTUN_INTENT_PATH
#define FIRC_OPKGTUN_INTENT_PATH "/var/lib/firc/opkgtun"
#endif
#ifndef FIRC_NDMC_BIN
#define FIRC_NDMC_BIN "ndmc"
#endif
#ifndef FIRC_SH_BIN
#define FIRC_SH_BIN "/bin/sh"
#endif
#ifndef FIRC_OPKG_BIN
#define FIRC_OPKG_BIN "/opt/bin/opkg"
#endif

#endif

#ifndef FIRC_CONFIG_PATH
#define FIRC_CONFIG_PATH FIRC_APP_CONF_DIR "/firc.conf"
#endif
#ifndef FIRC_TUNNELS_PATH
#define FIRC_TUNNELS_PATH FIRC_APP_CONF_DIR "/tunnels.yaml"
#endif
#ifndef FIRC_FIELDS_STATE_PATH
#define FIRC_FIELDS_STATE_PATH FIRC_APP_RUN_DIR "/fields.state"
#endif
#ifndef FIRC_FAKEIP_V6_PREFIX_FILE
#define FIRC_FAKEIP_V6_PREFIX_FILE FIRC_APP_CONF_DIR "/v6-pool-prefix"
#endif

/* tunnels.yaml: FIRC_TUNNELS_PATH from the environment when set, for tests and the contract run. */
static inline const char *firc_tunnels_path(void)
{
    const char *p = getenv("FIRC_TUNNELS_PATH");
    return p != NULL && p[0] != 0 ? p : FIRC_TUNNELS_PATH;
}

/* The stored router id: FIRC_HWID_PATH from the environment when set, for tests and the contract run. */
static inline const char *firc_hwid_path(void)
{
    const char *p = getenv("FIRC_HWID_PATH");
    return p != NULL && p[0] != 0 ? p : FIRC_HWID_PATH;
}

#endif
