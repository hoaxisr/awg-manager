#ifndef FIRC_SETTINGS_H
#define FIRC_SETTINGS_H

#include <stdbool.h>
#include <stddef.h>

#include "firc/err.h"
#include "firc/models.h"

typedef enum {
    FIRC_SETTING_LIVE,    /* applied to the running daemon when saved */
    FIRC_SETTING_RESTART, /* saved at once, used from the next start */
} firc_setting_class_t;

typedef enum {
    FIRC_SK_BOOL,   /* bool */
    FIRC_SK_ADDR,   /* char *: an IPv4 or IPv6 literal, brackets allowed */
    FIRC_SK_PORT,   /* uint16_t */
    FIRC_SK_STRING, /* char * */
    FIRC_SK_U64,    /* uint64_t */
    FIRC_SK_U32,    /* uint32_t */
    FIRC_SK_CHUNK,  /* uint8_t, a prefix length 0..128 */
    FIRC_SK_HEX32,  /* uint32_t; the API writes it as "0x..." */
    FIRC_SK_MS,     /* firc_duration_t; the API counts whole milliseconds */
    FIRC_SK_SEC,    /* firc_duration_t; ... whole seconds */
    FIRC_SK_LIST,   /* char ** with its size_t count at off_n */
} firc_setting_kind_t;

typedef enum {
    FIRC_APPLY_NONE = 0,
    FIRC_APPLY_UPSTREAM,    /* firc_dnsproxy_set_upstream, address and port together */
    FIRC_APPLY_PROXY_FLAGS, /* firc_dnsproxy_set_disable_drop_aaaa */
    FIRC_APPLY_UNMATCHED_TTL, /* firc_dns_pipeline_set_unmatched_ttl */
    FIRC_APPLY_CHECK,         /* the interface check's url, interval and timeout */
    FIRC_APPLY_DNS_STATS,     /* the app's DNS stats switch */
    FIRC_APPLY_AUTOLIST,
} firc_setting_apply_t;

typedef struct firc_setting {
    const char *path; /* "app.dnsProxy.upstream.address" */
    firc_setting_kind_t kind;
    firc_setting_class_t cls;
    size_t off;   /* offsetof(firc_app_config_t, ...) */
    size_t off_n; /* FIRC_SK_LIST only: where the count is */
    firc_setting_apply_t apply; /* FIRC_APPLY_NONE for update.check and every restart row */
} firc_setting_t;

#define FIRC_SETTINGS_COUNT 39
extern const firc_setting_t firc_settings[FIRC_SETTINGS_COUNT];

/* An http or https URL with a name or IPv4 host, no blanks, 512 characters at most; what the check's fetch accepts,
 * without libcurl. An IPv6 literal is refused: the check route is IPv4 only. */
bool firc_check_url_ok(const char *url);

const firc_setting_t *firc_setting_find(const char *path); /* NULL if path names no setting */

/* whether a and b hold the same value for s; a NULL string equals "" */
bool firc_setting_equal(const firc_setting_t *s, const firc_app_config_t *a,
                        const firc_app_config_t *b);

/* dst's value for s becomes a copy of src's; FIRC_ERR_NOMEM leaves dst unchanged */
firc_err_t firc_setting_copy(const firc_setting_t *s, firc_app_config_t *dst,
                             const firc_app_config_t *src);

/* deep copy of every field; on FIRC_ERR_NOMEM dst holds a partial copy, clear it with firc_app_config_clear */
firc_err_t firc_app_config_copy(firc_app_config_t *dst, const firc_app_config_t *src);

/* rules a whole app block must pass; FIRC_ERR_INVAL sets *field and *why (static strings) */
firc_err_t firc_app_config_check(const firc_app_config_t *c, const char **field,
                                 const char **why);

#endif /* FIRC_SETTINGS_H */
