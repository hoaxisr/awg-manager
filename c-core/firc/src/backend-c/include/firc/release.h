#ifndef FIRC_RELEASE_H
#define FIRC_RELEASE_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/err.h"

#define FIRC_RELEASE_JSON_MAX ((size_t)256 * 1024)
#define FIRC_RELEASE_NOTES_MAX 16384
#define FIRC_RELEASE_ASSET_MAX ((uint64_t)16 * 1024 * 1024)
#define FIRC_UPDATE_API_URL "https://api.github.com/repos/hoaxisr/firc/releases/latest"
#define FIRC_UPDATE_DOWNLOAD_PREFIX "https://github.com/hoaxisr/firc/releases/download/"

typedef struct firc_version {
    uint32_t major, minor, patch;
    bool dev;
    uint32_t revision;
} firc_version_t;

/* "X.Y.Z" or "X.Y.Z~git<one or more chars>", each part 1-9 digits; false for anything else ("unattached"). */
bool firc_version_parse_installed(const char *s, uint32_t revision, firc_version_t *out);

/* "[v]X.Y.Z[-revN]", N >= 1, revision 1 when absent; never dev. */
bool firc_version_parse_tag(const char *tag, firc_version_t *out);

/* triple, then a dev build below the release of its triple, then revision; <0, 0, >0. */
int firc_version_cmp(const firc_version_t *a, const firc_version_t *b);

/* "X.Y.Z" */
void firc_version_text(const firc_version_t *v, char *buf, size_t cap);

/* firc_X.Y.Z-N_entware_<target>.ipk; the length snprintf would write, 0 on an encoding error */
size_t firc_release_asset_name(char *buf, size_t cap, const firc_version_t *v, const char *target);

typedef struct firc_release {
    char tag[64];
    bool known;
    firc_version_t version;
    char version_text[32];
    char published_at[32];
    char *notes;
    struct {
        char name[160];
        uint64_t size;
        uint8_t sha256[32];
        char url[512];
    } asset;
    bool installable;
    const char *reason;
} firc_release_t;

typedef struct firc_release_want {
    const char *target;
    const char *download_prefix;
} firc_release_want_t;

/* FIRC_ERR_PROTO: not a JSON object or no string tag_name; FIRC_ERR_NOMEM. Otherwise FIRC_OK with reason set
 * (static, NULL when installable). notes is malloc'd, never NULL after FIRC_OK, <= 16 KiB on a UTF-8 boundary. */
firc_err_t firc_release_parse(const char *json, size_t len, const firc_release_want_t *want, firc_release_t *out);

/* frees notes; safe on a release any parse left, successful or not */
void firc_release_clear(firc_release_t *r);

/* https, host exactly api.github.com, default port, no userinfo */
bool firc_update_api_url_ok(const char *url);

/* https, host github.com or under .githubusercontent.com, default port, no userinfo */
bool firc_update_download_url_ok(const char *url);

#endif
