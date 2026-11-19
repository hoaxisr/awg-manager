#include "firc/release.h"

#include <math.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include <cjson/cJSON.h>
#include <curl/curl.h>

#include "firc/json.h"
#include "firc/utf8.h"

static const char *part(const char *s, uint32_t *out) {
    uint32_t v = 0;
    int n = 0;
    while (s[n] >= '0' && s[n] <= '9') {
        if (n == 9) { return NULL; }
        v = v * 10u + (uint32_t)(s[n] - '0');
        n++;
    }
    if (n == 0) { return NULL; }
    *out = v;
    return s + n;
}

static const char *triple(const char *s, firc_version_t *v) {
    s = part(s, &v->major);
    if (s == NULL || *s != '.') { return NULL; }
    s = part(s + 1, &v->minor);
    if (s == NULL || *s != '.') { return NULL; }
    return part(s + 1, &v->patch);
}

bool firc_version_parse_installed(const char *s, uint32_t revision, firc_version_t *out) {
    if (s == NULL) { return false; }
    firc_version_t v = {0};
    const char *rest = triple(s, &v);
    if (rest == NULL) { return false; }
    if (*rest != '\0') {
        if (strncmp(rest, "~git", 4) != 0 || rest[4] == '\0') { return false; }
        v.dev = true;
    }
    v.revision = revision;
    *out = v;
    return true;
}

bool firc_version_parse_tag(const char *tag, firc_version_t *out) {
    if (tag == NULL) { return false; }
    firc_version_t v = {0};
    const char *rest = triple(tag[0] == 'v' ? tag + 1 : tag, &v);
    if (rest == NULL) { return false; }
    v.revision = 1;
    if (*rest != '\0') {
        if (strncmp(rest, "-rev", 4) != 0) { return false; }
        rest = part(rest + 4, &v.revision);
        if (rest == NULL || *rest != '\0' || v.revision == 0) { return false; }
    }
    *out = v;
    return true;
}

static int cmp_u32(uint32_t a, uint32_t b) { return (a > b) - (a < b); }

int firc_version_cmp(const firc_version_t *a, const firc_version_t *b) {
    int c = cmp_u32(a->major, b->major);
    if (c == 0) { c = cmp_u32(a->minor, b->minor); }
    if (c == 0) { c = cmp_u32(a->patch, b->patch); }
    if (c == 0) { c = (int)b->dev - (int)a->dev; }
    if (c == 0) { c = cmp_u32(a->revision, b->revision); }
    return c;
}

void firc_version_text(const firc_version_t *v, char *buf, size_t cap) {
    snprintf(buf, cap, "%u.%u.%u", (unsigned)v->major, (unsigned)v->minor, (unsigned)v->patch);
}

size_t firc_release_asset_name(char *buf, size_t cap, const firc_version_t *v, const char *target) {
    int n = snprintf(buf, cap, "firc_%u.%u.%u-%u_entware_%s.ipk", (unsigned)v->major, (unsigned)v->minor,
                     (unsigned)v->patch, (unsigned)v->revision, target);
    return n < 0 ? 0 : (size_t)n;
}

static void copy_clamped(char *dst, size_t cap, const char *src) {
    int n = firc_utf8_clamp(src, (int)cap - 1);
    memcpy(dst, src, (size_t)n);
    dst[n] = '\0';
}

static const char *string_of(const cJSON *obj, const char *key) {
    const cJSON *item = cJSON_GetObjectItemCaseSensitive(obj, key);
    return cJSON_IsString(item) ? item->valuestring : NULL;
}

static int hex_val(char c) {
    if (c >= '0' && c <= '9') { return c - '0'; }
    if (c >= 'a' && c <= 'f') { return c - 'a' + 10; }
    if (c >= 'A' && c <= 'F') { return c - 'A' + 10; }
    return -1;
}

static bool sha256_digest(const char *s, uint8_t out[32]) {
    if (s == NULL || strncmp(s, "sha256:", 7) != 0 || strlen(s + 7) != 64) { return false; }
    s += 7;
    for (size_t i = 0; i < 32; i++) {
        int hi = hex_val(s[2 * i]);
        int lo = hex_val(s[2 * i + 1]);
        if (hi < 0 || lo < 0) { return false; }
        out[i] = (uint8_t)((hi << 4) | lo);
    }
    return true;
}

static bool asset_size(const cJSON *item, uint64_t *out) {
    if (!cJSON_IsNumber(item)) { return false; }
    double d = item->valuedouble;
    if (!(d >= 1.0) || d > (double)FIRC_RELEASE_ASSET_MAX || floor(d) != d) { return false; }
    *out = (uint64_t)d;
    return true;
}

static const char *judge_asset(const cJSON *root, const firc_release_want_t *want, firc_release_t *out) {
    if (want == NULL || want->target == NULL || want->target[0] == '\0') { return "no-target"; }
    size_t n = firc_release_asset_name(out->asset.name, sizeof(out->asset.name), &out->version, want->target);
    if (n == 0 || n >= sizeof(out->asset.name)) {
        out->asset.name[0] = '\0';
        return "no-asset";
    }
    const cJSON *found = NULL;
    int count = 0;
    const cJSON *assets = cJSON_GetObjectItemCaseSensitive(root, "assets");
    if (cJSON_IsArray(assets)) {
        const cJSON *a = NULL;
        cJSON_ArrayForEach(a, assets) {
            const char *name = string_of(a, "name");
            if (name != NULL && strcmp(name, out->asset.name) == 0) {
                found = a;
                count++;
            }
        }
    }
    if (count == 0) { return "no-asset"; }
    if (count > 1) { return "many-assets"; }
    const char *state = string_of(found, "state");
    if (state == NULL || strcmp(state, "uploaded") != 0) { return "not-uploaded"; }
    if (!sha256_digest(string_of(found, "digest"), out->asset.sha256)) { return "no-digest"; }
    if (!asset_size(cJSON_GetObjectItemCaseSensitive(found, "size"), &out->asset.size)) { return "bad-size"; }
    const char *url = string_of(found, "browser_download_url");
    char expected[sizeof(out->asset.url)];
    int en = snprintf(expected, sizeof(expected), "%s%s/%s", want->download_prefix ? want->download_prefix : "",
                      out->tag, out->asset.name);
    if (want->download_prefix == NULL || url == NULL || en < 0 || (size_t)en >= sizeof(expected) ||
        strcmp(url, expected) != 0) {
        return "bad-url";
    }
    memcpy(out->asset.url, expected, (size_t)en + 1);
    return NULL;
}

firc_err_t firc_release_parse(const char *json, size_t len, const firc_release_want_t *want, firc_release_t *out) {
    memset(out, 0, sizeof(*out));
    cJSON *root = cJSON_ParseWithLength(json, len);
    const char *tag = cJSON_IsObject(root) ? string_of(root, "tag_name") : NULL;
    if (tag == NULL) {
        cJSON_Delete(root);
        return FIRC_ERR_PROTO;
    }
    const char *body = string_of(root, "body");
    if (body == NULL) { body = ""; }
    int nn = firc_utf8_clamp(body, FIRC_RELEASE_NOTES_MAX);
    out->notes = malloc((size_t)nn + 1);
    if (out->notes == NULL) {
        cJSON_Delete(root);
        return FIRC_ERR_NOMEM;
    }
    memcpy(out->notes, body, (size_t)nn);
    out->notes[nn] = '\0';
    copy_clamped(out->tag, sizeof(out->tag), tag);
    const char *published = string_of(root, "published_at");
    copy_clamped(out->published_at, sizeof(out->published_at), published ? published : "");
    out->known = firc_version_parse_tag(tag, &out->version);
    if (out->known) { firc_version_text(&out->version, out->version_text, sizeof(out->version_text)); }
    if (!out->known) {
        out->reason = "unknown-version";
    } else if (cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(root, "draft")) ||
               cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(root, "prerelease"))) {
        out->reason = "prerelease";
    } else {
        out->reason = judge_asset(root, want, out);
    }
    out->installable = out->reason == NULL;
    cJSON_Delete(root);
    return FIRC_OK;
}

void firc_release_clear(firc_release_t *r) {
    free(r->notes);
    r->notes = NULL;
}

static bool url_parts_ok(const char *url, bool (*host_ok)(const char *host)) {
    if (url == NULL) { return false; }
    CURLU *u = curl_url();
    if (u == NULL) { return false; }
    char *scheme = NULL, *host = NULL, *port = NULL, *user = NULL, *password = NULL;
    bool ok = curl_url_set(u, CURLUPART_URL, url, 0) == CURLUE_OK &&
              curl_url_get(u, CURLUPART_SCHEME, &scheme, 0) == CURLUE_OK && strcmp(scheme, "https") == 0 &&
              curl_url_get(u, CURLUPART_HOST, &host, 0) == CURLUE_OK && host_ok(host) &&
              curl_url_get(u, CURLUPART_PORT, &port, CURLU_DEFAULT_PORT) == CURLUE_OK && strcmp(port, "443") == 0 &&
              curl_url_get(u, CURLUPART_USER, &user, 0) == CURLUE_NO_USER &&
              curl_url_get(u, CURLUPART_PASSWORD, &password, 0) == CURLUE_NO_PASSWORD;
    curl_free(scheme);
    curl_free(host);
    curl_free(port);
    curl_free(user);
    curl_free(password);
    curl_url_cleanup(u);
    return ok;
}

static bool api_host(const char *host) { return strcmp(host, "api.github.com") == 0; }

static bool download_host(const char *host) {
    static const char suffix[] = ".githubusercontent.com";
    size_t n = strlen(host), s = sizeof(suffix) - 1;
    return strcmp(host, "github.com") == 0 || (n > s && strcmp(host + n - s, suffix) == 0);
}

bool firc_update_api_url_ok(const char *url) { return url_parts_ok(url, api_host); }

bool firc_update_download_url_ok(const char *url) { return url_parts_ok(url, download_host); }
