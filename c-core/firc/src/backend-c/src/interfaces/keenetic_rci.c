#include "firc/keenetic_rci.h"

#include <stdatomic.h>
#include <stdbool.h>
#include <time.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>

#include <cjson/cJSON.h>
#include <curl/curl.h>

#include "firc/log.h"
#include "firc/rci_token.h"
#include "firc/utf8.h"

static bool is_space(char c) {
    return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f';
}

/* Trims ASCII whitespace only, so a U+00A0 stays in the label. */
static void copy_trimmed(char *dst, size_t dst_sz, const char *src) {
    dst[0] = '\0';
    if (!src) { return; }

    while (*src && is_space(*src)) { src++; }
    size_t len = strlen(src);
    while (len > 0 && is_space(src[len - 1])) { len--; }
    /* Описания NDMS — UTF-8 (кириллица, флаги): резать только по границе символа. */
    if (len >= dst_sz) { len = (size_t)firc_utf8_clamp(src, (int)dst_sz - 1); }
    memcpy(dst, src, len);
    dst[len] = '\0';
}

static void copy_trimmed_member(char *dst, size_t dst_sz, const cJSON *obj, const char *key) {
    dst[0] = '\0';
    const cJSON *m = cJSON_GetObjectItemCaseSensitive(obj, key);
    if (cJSON_IsString(m)) { copy_trimmed(dst, dst_sz, m->valuestring); }
}

static bool role_has_inet(const cJSON *entry) {
    const cJSON *role = cJSON_GetObjectItemCaseSensitive(entry, "role");
    const cJSON *r = NULL;
    if (!cJSON_IsArray(role)) { return false; }
    cJSON_ArrayForEach(r, role) {
        if (cJSON_IsString(r) && strcmp(r->valuestring, "inet") == 0) { return true; }
    }
    return false;
}

static bool traits_has(const cJSON *traits, const char *want) {
    const cJSON *tr = NULL;
    if (!cJSON_IsArray(traits)) { return false; }
    cJSON_ArrayForEach(tr, traits) {
        if (cJSON_IsString(tr) && strcasecmp(tr->valuestring, want) == 0) { return true; }
    }
    return false;
}

static bool is_vpn_tunnel_entry(const cJSON *entry) {
    const cJSON *type = cJSON_GetObjectItemCaseSensitive(entry, "type");
    if (cJSON_IsString(type)) {
        const char *t = type->valuestring;
        if (strcasecmp(t, "Wireguard") == 0 || strcasecmp(t, "OpkgTun") == 0 ||
            strcasecmp(t, "Proxy") == 0 || strcasecmp(t, "OpenVPN") == 0 ||
            strcasecmp(t, "SSTP") == 0 || strcasecmp(t, "IPsec") == 0 ||
            strcasecmp(t, "IKE") == 0 || strcasecmp(t, "VirtualIP") == 0 ||
            strcasecmp(t, "ZeroTier") == 0) {
            return true;
        }
    }
    const cJSON *traits = cJSON_GetObjectItemCaseSensitive(entry, "traits");
    return traits_has(traits, "Wireguard") || traits_has(traits, "OpkgTun") ||
           traits_has(traits, "Proxy") || traits_has(traits, "OpenVPN") ||
           traits_has(traits, "Sstp") || traits_has(traits, "Ipsec") ||
           traits_has(traits, "EncryptedPppTunnel") || traits_has(traits, "ZeroTier");
}

static bool is_excluded_wan_entry(const cJSON *entry) {
    if (is_vpn_tunnel_entry(entry)) { return true; }
    const cJSON *sec = cJSON_GetObjectItemCaseSensitive(entry, "security-level");
    if (cJSON_IsString(sec) && strcasecmp(sec->valuestring, "public") != 0) {
        return true;
    }
    const cJSON *type = cJSON_GetObjectItemCaseSensitive(entry, "type");
    if (cJSON_IsString(type) &&
        (strcasecmp(type->valuestring, "Bridge") == 0 || strcasecmp(type->valuestring, "Port") == 0 ||
         strcasecmp(type->valuestring, "AccessPoint") == 0 ||
         strcasecmp(type->valuestring, "WifiMaster") == 0)) {
        return true;
    }
    const cJSON *traits = cJSON_GetObjectItemCaseSensitive(entry, "traits");
    return traits_has(traits, "Bridge") || traits_has(traits, "AccessPoint") ||
           traits_has(traits, "WifiMaster");
}

static bool global_wan(const cJSON *entry) {
    if (is_excluded_wan_entry(entry)) { return false; }

    if (!cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(entry, "global"))) {
        return false;
    }
    const cJSON *traits = cJSON_GetObjectItemCaseSensitive(entry, "traits");
    if (traits_has(traits, "Ethernet") || traits_has(traits, "Pppoe") ||
        traits_has(traits, "Mobile") || traits_has(traits, "UsbModem") ||
        traits_has(traits, "UsbLte") || traits_has(traits, "UsbQmi") ||
        traits_has(traits, "Dsl") || traits_has(traits, "CdcEthernet")) {
        return true;
    }
    const cJSON *type = cJSON_GetObjectItemCaseSensitive(entry, "type");
    if (cJSON_IsString(type)) {
        const char *t = type->valuestring;
        if (strcasecmp(t, "GigabitEthernet") == 0 || strcasecmp(t, "FastEthernet") == 0 ||
            strcasecmp(t, "Ethernet") == 0 || strcasecmp(t, "PPPoE") == 0 ||
            strcasecmp(t, "UsbModem") == 0 || strcasecmp(t, "UsbLte") == 0 ||
            strcasecmp(t, "UsbQmi") == 0 || strcasecmp(t, "CdcEthernet") == 0 ||
            strcasecmp(t, "UsbDsl") == 0 || strcasecmp(t, "IPoE") == 0 ||
            strcasecmp(t, "WifiStation") == 0 || strcasecmp(t, "MwsMobile") == 0) {
            return true;
        }
    }
    return false;
}

static void mark_inet_for(const cJSON *root, const cJSON *entry, firc_kn_iface_meta_t *arr, size_t n) {
    const cJSON *role = cJSON_GetObjectItemCaseSensitive(entry, "role");
    const cJSON *r = NULL;
    if (!cJSON_IsArray(role)) { return; }
    cJSON_ArrayForEach(r, role) {
        const cJSON *what = cJSON_GetObjectItemCaseSensitive(r, "role");
        const cJSON *target = cJSON_GetObjectItemCaseSensitive(r, "for");
        if (!cJSON_IsObject(r) || !cJSON_IsString(what) || strcmp(what->valuestring, "inet") != 0 ||
            !cJSON_IsString(target)) {
            continue;
        }
        for (size_t i = 0; i < n; i++) {
            if (strcmp(arr[i].id, target->valuestring) == 0) {
                const cJSON *target_entry = cJSON_GetObjectItemCaseSensitive(root, arr[i].id);
                if (target_entry != NULL && !is_excluded_wan_entry(target_entry)) { arr[i].inet = true; }
            }
        }
    }
}

firc_err_t firc_kn_parse_interface_list(const char *json, firc_kn_iface_meta_t **out, size_t *out_n) {
    *out = NULL;
    *out_n = 0;
    if (!json) { return FIRC_ERR_INVAL; }

    cJSON *root = cJSON_Parse(json);
    if (!root) { return FIRC_ERR_PROTO; }
    if (!cJSON_IsObject(root)) {
        cJSON_Delete(root);
        return FIRC_ERR_PROTO;
    }

    size_t cap = 0;
    size_t n = 0;
    firc_kn_iface_meta_t *arr = NULL;

    const cJSON *entry = NULL;
    cJSON_ArrayForEach(entry, root) {
        if (!cJSON_IsObject(entry) || !entry->string) { continue; }

        if (n == cap) {
            size_t new_cap = cap ? cap * 2 : 8;
            firc_kn_iface_meta_t *na = realloc(arr, new_cap * sizeof(*na));
            if (!na) {
                free(arr);
                cJSON_Delete(root);
                return FIRC_ERR_NOMEM;
            }
            arr = na;
            cap = new_cap;
        }

        memset(&arr[n], 0, sizeof(arr[n]));
        snprintf(arr[n].id, sizeof(arr[n].id), "%s", entry->string);
        copy_trimmed_member(arr[n].description, sizeof(arr[n].description), entry, "description");
        copy_trimmed_member(arr[n].interface_name, sizeof(arr[n].interface_name), entry,
                            "interface-name");
        arr[n].inet = !is_excluded_wan_entry(entry) && (role_has_inet(entry) || global_wan(entry));
        n++;
    }

    cJSON_ArrayForEach(entry, root) {
        if (cJSON_IsObject(entry)) { mark_inet_for(root, entry, arr, n); }
    }

    cJSON_Delete(root);
    *out = arr;
    *out_n = n;
    return FIRC_OK;
}

char *firc_kn_build_system_name_request(const firc_kn_iface_meta_t *metas, size_t n) {
    cJSON *arr = cJSON_CreateArray();
    if (!arr) { return NULL; }

    for (size_t i = 0; i < n; i++) {
        cJSON *iface = cJSON_CreateObject();
        cJSON *show = cJSON_CreateObject();
        cJSON *item = cJSON_CreateObject();
        if (!iface || !show || !item) {
            cJSON_Delete(iface);
            cJSON_Delete(show);
            cJSON_Delete(item);
            cJSON_Delete(arr);
            return NULL;
        }

        if (!cJSON_AddStringToObject(iface, "name", metas[i].id) ||
            !cJSON_AddStringToObject(iface, "details", "yes") ||
            !cJSON_AddStringToObject(iface, "system-name", "yes")) {
            cJSON_Delete(iface);
            cJSON_Delete(show);
            cJSON_Delete(item);
            cJSON_Delete(arr);
            return NULL;
        }
        cJSON_AddItemToObject(show, "interface", iface);
        cJSON_AddItemToObject(item, "show", show);
        cJSON_AddItemToArray(arr, item);
    }

    char *body = cJSON_PrintUnformatted(arr);
    cJSON_Delete(arr);
    return body;
}

firc_err_t firc_kn_parse_system_names(const char *json, firc_kn_iface_meta_t *metas, size_t n) {
    if (!json) { return FIRC_ERR_INVAL; }

    cJSON *root = cJSON_Parse(json);
    if (!root) { return FIRC_ERR_PROTO; }
    if (!cJSON_IsArray(root)) {
        cJSON_Delete(root);
        return FIRC_ERR_PROTO;
    }

    size_t i = 0;
    const cJSON *item = NULL;
    cJSON_ArrayForEach(item, root) {
        if (i >= n) { break; }

        const cJSON *show = cJSON_GetObjectItemCaseSensitive(item, "show");
        const cJSON *iface = cJSON_GetObjectItemCaseSensitive(show, "interface");
        copy_trimmed_member(metas[i].system_name, sizeof(metas[i].system_name), iface,
                            "system-name");
        i++;
    }

    cJSON_Delete(root);
    return FIRC_OK;
}

firc_err_t firc_kn_build_aliases(const firc_kn_iface_meta_t *metas, size_t n, firc_kn_aliases_t *out) {
    out->items = NULL;
    out->n = 0;
    if (n == 0) { return FIRC_OK; }

    firc_kn_alias_t *arr = calloc(n, sizeof(*arr));
    if (!arr) { return FIRC_ERR_NOMEM; }

    size_t count = 0;
    for (size_t i = 0; i < n; i++) {
        char system_name[sizeof(metas[i].system_name)];
        copy_trimmed(system_name, sizeof(system_name), metas[i].system_name);
        if (system_name[0] == '\0') { continue; }

        char description[sizeof(arr[count].description)];
        copy_trimmed(description, sizeof(description), metas[i].description);
        if (strcmp(description, system_name) == 0) { description[0] = '\0'; }
        char ndms_name[sizeof(arr[count].ndms_name)];
        copy_trimmed(ndms_name, sizeof(ndms_name), metas[i].interface_name);
        if (strcmp(ndms_name, system_name) == 0) { ndms_name[0] = '\0'; }

        snprintf(arr[count].system_name, sizeof(arr[count].system_name), "%.15s", system_name);
        snprintf(arr[count].description, sizeof(arr[count].description), "%.*s",
                 firc_utf8_clamp(description, 63), description);
        snprintf(arr[count].ndms_name, sizeof(arr[count].ndms_name), "%.*s", firc_utf8_clamp(ndms_name, 63),
                 ndms_name);
        arr[count].inet = metas[i].inet;
        count++;
    }

    if (count == 0) {
        free(arr);
        return FIRC_OK;
    }
    out->items = arr;
    out->n = count;
    return FIRC_OK;
}

const char *firc_kn_aliases_lookup(const firc_kn_aliases_t *aliases, const char *system_name) {
    if (!aliases || !aliases->items || !system_name) { return NULL; }
    for (size_t i = 0; i < aliases->n; i++) {
        if (aliases->items[i].description[0] != '\0' &&
            strcmp(aliases->items[i].system_name, system_name) == 0) {
            return aliases->items[i].description;
        }
    }
    return NULL;
}

bool firc_kn_aliases_known(const firc_kn_aliases_t *aliases, const char *system_name) {
    if (!aliases || !aliases->items || !system_name) { return false; }
    for (size_t i = 0; i < aliases->n; i++) {
        if (strcmp(aliases->items[i].system_name, system_name) == 0) { return true; }
    }
    return false;
}

const char *firc_kn_aliases_ndms_name(const firc_kn_aliases_t *aliases, const char *system_name) {
    if (!aliases || !aliases->items || !system_name) { return NULL; }
    for (size_t i = 0; i < aliases->n; i++) {
        if (aliases->items[i].ndms_name[0] != '\0' &&
            strcmp(aliases->items[i].system_name, system_name) == 0) {
            return aliases->items[i].ndms_name;
        }
    }
    return NULL;
}

bool firc_kn_aliases_inet(const firc_kn_aliases_t *aliases, const char *system_name) {
    if (!aliases || !aliases->items || !system_name) { return false; }
    for (size_t i = 0; i < aliases->n; i++) {
        if (aliases->items[i].inet && strcmp(aliases->items[i].system_name, system_name) == 0) {
            return true;
        }
    }
    return false;
}

void firc_kn_aliases_free(firc_kn_aliases_t *aliases) {
    if (!aliases) { return; }
    free(aliases->items);
    aliases->items = NULL;
    aliases->n = 0;
}

typedef struct rci_buf {
    char *data;
    size_t len;
    size_t cap;
    bool truncated;
} rci_buf_t;

static size_t write_cb(char *ptr, size_t size, size_t nmemb, void *ud) {
    rci_buf_t *buf = ud;
    size_t n = size * nmemb;
    if (buf->len + n > FIRC_KN_RCI_MAX_BODY_BYTES) {
        buf->truncated = true;
        return 0;
    }
    if (buf->len + n + 1 > buf->cap) {
        size_t new_cap = buf->cap ? buf->cap * 2 : 4096;
        while (new_cap < buf->len + n + 1) { new_cap *= 2; }
        char *na = realloc(buf->data, new_cap);
        if (!na) { return 0; }
        buf->data = na;
        buf->cap = new_cap;
    }
    memcpy(buf->data + buf->len, ptr, n);
    buf->len += n;
    buf->data[buf->len] = '\0';
    return n;
}

/* True at most once an hour across all threads. */
static bool refusal_is_due(void) {
    static _Atomic long last = 0;
    struct timespec now;
    if (clock_gettime(CLOCK_MONOTONIC, &now) != 0) { return true; }
    long prev = atomic_load(&last);
    if (prev != 0 && now.tv_sec - prev < 3600) { return false; }
    /* A lost race costs one duplicate line, not worth a lock. */
    return atomic_compare_exchange_strong(&last, &prev, now.tv_sec == 0 ? 1 : now.tv_sec);
}

#define DETAIL_MAX 128

static size_t header_cb(char *ptr, size_t size, size_t nmemb, void *ud) {
    char *detail = ud;
    size_t n = size * nmemb;
    static const char name[] = "X-Detail:";
    if (n > sizeof name - 1 && strncasecmp(ptr, name, sizeof name - 1) == 0) {
        size_t v = sizeof name - 1;
        while (v < n && (ptr[v] == ' ' || ptr[v] == '\t')) { v++; }
        size_t e = n;
        while (e > v && (ptr[e - 1] == '\r' || ptr[e - 1] == '\n')) { e--; }
        snprintf(detail, DETAIL_MAX, "%.*s", (int)(e - v), ptr + v);
    }
    return n;
}

typedef struct {
    const atomic_bool *stop;
    const atomic_bool *cut;
} flags_t;

static int stop_cb(void *ud, curl_off_t dltotal, curl_off_t dlnow, curl_off_t ultotal, curl_off_t ulnow) {
    (void)dltotal;
    (void)dlnow;
    (void)ultotal;
    (void)ulnow;
    const flags_t *f = ud;
    return (f->stop != NULL && atomic_load(f->stop)) || (f->cut != NULL && atomic_load(f->cut)) ? 1 : 0;
}

static firc_err_t rci_once(const char *url, const char *post_body, long timeout_s, const atomic_bool *stop,
                           const atomic_bool *cut, const char *tok, long *status, char *detail, rci_buf_t *buf) {
    *status = 0;
    detail[0] = '\0';
    CURL *curl = curl_easy_init();
    if (!curl) { return FIRC_ERR_NOMEM; }

    struct curl_slist *headers = NULL;
    char tok_header[FIRC_KN_TOKEN_MAX + 32];
    bool ok = true;
    if (tok[0] != '\0') {
        snprintf(tok_header, sizeof tok_header, FIRC_KN_TOKEN_HEADER ": %s", tok);
        struct curl_slist *h = curl_slist_append(headers, tok_header);
        ok = h != NULL;
        if (ok) { headers = h; }
    }
    if (ok && post_body) {
        struct curl_slist *h = curl_slist_append(headers, "Content-Type: application/json");
        ok = h != NULL;
        if (ok) { headers = h; }
    }
    if (!ok) {
        curl_slist_free_all(headers);
        curl_easy_cleanup(curl);
        return FIRC_ERR_NOMEM;
    }

    curl_easy_setopt(curl, CURLOPT_URL, url);
    curl_easy_setopt(curl, CURLOPT_FOLLOWLOCATION, 0L);
    curl_easy_setopt(curl, CURLOPT_TIMEOUT, timeout_s);
    curl_easy_setopt(curl, CURLOPT_WRITEFUNCTION, write_cb);
    curl_easy_setopt(curl, CURLOPT_WRITEDATA, buf);
    curl_easy_setopt(curl, CURLOPT_HEADERFUNCTION, header_cb);
    curl_easy_setopt(curl, CURLOPT_HEADERDATA, detail);
    curl_easy_setopt(curl, CURLOPT_NOSIGNAL, 1L);
    if (headers) { curl_easy_setopt(curl, CURLOPT_HTTPHEADER, headers); }
    flags_t flags = {stop, cut};
    if (stop != NULL || cut != NULL) {
        curl_easy_setopt(curl, CURLOPT_NOPROGRESS, 0L);
        curl_easy_setopt(curl, CURLOPT_XFERINFOFUNCTION, stop_cb);
        curl_easy_setopt(curl, CURLOPT_XFERINFODATA, (void *)&flags);
    }
    if (post_body) {
        curl_easy_setopt(curl, CURLOPT_POSTFIELDS, post_body);
        curl_easy_setopt(curl, CURLOPT_POSTFIELDSIZE, (long)strlen(post_body));
    }

    CURLcode rc = curl_easy_perform(curl);
    if (rc == CURLE_OK) { curl_easy_getinfo(curl, CURLINFO_RESPONSE_CODE, status); }

    curl_slist_free_all(headers);
    curl_easy_cleanup(curl);

    if (rc == CURLE_ABORTED_BY_CALLBACK) { return FIRC_ERR_CANCELED; }
    if (rc != CURLE_OK) {
        if (buf->truncated) {
            FIRC_DEBUG("keenetic rci: %s response exceeded %d bytes", url,
                     FIRC_KN_RCI_MAX_BODY_BYTES);
            return FIRC_ERR_LIMIT;
        }
        FIRC_DEBUG("keenetic rci: %s failed: %s (curl %d)", url, curl_easy_strerror(rc), (int)rc);
        return rc == CURLE_OPERATION_TIMEDOUT ? FIRC_ERR_TIMEOUT : FIRC_ERR_UPSTREAM;
    }
    return FIRC_OK;
}

static firc_err_t rci_call(const char *url, const char *post_body, long timeout_s, const atomic_bool *stop,
                           const atomic_bool *cut, char **out_body) {
    *out_body = NULL;

    char tok[FIRC_KN_TOKEN_MAX];
    if (!firc_kn_token_current(tok, stop)) { return FIRC_ERR_CANCELED; }
    rci_buf_t first = {0};
    rci_buf_t again = {0};
    rci_buf_t *buf = &first;
    long status = 0;
    char detail[DETAIL_MAX];
    firc_err_t err = rci_once(url, post_body, timeout_s, stop, cut, tok, &status, detail, &first);
    if (err == FIRC_OK && firc_kn_token_refusal(status, detail)) {
        char fresh[FIRC_KN_TOKEN_MAX];
        if (!firc_kn_token_refused(tok, fresh, stop)) {
            free(first.data);
            return FIRC_ERR_CANCELED;
        }
        if (strcmp(fresh, tok) != 0) {
            err = rci_once(url, post_body, timeout_s, stop, cut, fresh, &status, detail, &again);
            free(first.data);
            first.data = NULL;
            buf = &again;
        }
    }
    if (err != FIRC_OK) {
        free(buf->data);
        return err;
    }
    if (status == 401 || status == 403) {
        /* Logged, or device scoping fails open in silence. */
        free(buf->data);
        if (refusal_is_due()) {
            FIRC_WARN("keenetic rci refused %s (HTTP %ld, %s): firc has no token this firmware accepts, "
                      "and until it has one every policy: selector matches no device",
                      url, status, detail[0] != '\0' ? detail : "no detail");
        }
        return FIRC_ERR_UPSTREAM;
    }
    if (status != 200) {
        free(buf->data);
        FIRC_DEBUG("keenetic rci: %s returned status %ld", url, status);
        return FIRC_ERR_UPSTREAM;
    }
    if (!buf->data) {
        return FIRC_ERR_PROTO;
    }

    *out_body = buf->data;
    return FIRC_OK;
}

static firc_err_t rci_url(const char *base_url, const char *path, char *url, size_t cap) {
    if (!base_url || !path) { return FIRC_ERR_INVAL; }
    size_t base_len = strlen(base_url);
    while (base_len > 0 && base_url[base_len - 1] == '/') { base_len--; }
    if (snprintf(url, cap, "%.*s%s", (int)base_len, base_url, path) >= (int)cap) {
        return FIRC_ERR_INVAL;
    }
    return FIRC_OK;
}

firc_err_t firc_kn_rci_get(const char *base_url, const char *path, char **out_body) {
    return firc_kn_rci_get_timeout(base_url, path, FIRC_KN_RCI_TIMEOUT_SECONDS, NULL, out_body);
}

firc_err_t firc_kn_rci_get_timeout(const char *base_url, const char *path, long timeout_s, const atomic_bool *stop,
                                   char **out_body) {
    *out_body = NULL;
    char url[512];
    firc_err_t err = rci_url(base_url, path, url, sizeof url);
    return err != FIRC_OK ? err : rci_call(url, NULL, timeout_s, stop, NULL, out_body);
}

firc_err_t firc_kn_rci_post(const char *base_url, const char *path, const char *body, char **out_body) {
    return firc_kn_rci_post_timeout(base_url, path, body, FIRC_KN_RCI_TIMEOUT_SECONDS, NULL, out_body);
}

firc_err_t firc_kn_rci_post_timeout(const char *base_url, const char *path, const char *body, long timeout_s,
                                    const atomic_bool *stop, char **out_body) {
    return firc_kn_rci_post_cut(base_url, path, body, timeout_s, stop, NULL, out_body);
}

firc_err_t firc_kn_rci_post_cut(const char *base_url, const char *path, const char *body, long timeout_s,
                                const atomic_bool *stop, const atomic_bool *cut, char **out_body) {
    *out_body = NULL;
    char url[512];
    firc_err_t err = rci_url(base_url, path, url, sizeof url);
    if (err == FIRC_OK && !body) { err = FIRC_ERR_INVAL; }
    return err != FIRC_OK ? err : rci_call(url, body, timeout_s, stop, cut, out_body);
}

/* n == 0 leaves *metas NULL and makes no batch call. */
static firc_err_t fetch_metas(const char *base_url, firc_kn_iface_meta_t **metas_out, size_t *n_out) {
    *metas_out = NULL;
    *n_out = 0;
    char *list_body = NULL;
    firc_err_t err = firc_kn_rci_get(base_url, "/rci/show/interface", &list_body);
    if (err != FIRC_OK) { return err; }

    firc_kn_iface_meta_t *metas = NULL;
    size_t n = 0;
    err = firc_kn_parse_interface_list(list_body, &metas, &n);
    free(list_body);
    if (err != FIRC_OK) { return err; }

    if (n == 0) {
        free(metas);
        return FIRC_OK;
    }

    char *req_body = firc_kn_build_system_name_request(metas, n);
    if (!req_body) {
        free(metas);
        return FIRC_ERR_NOMEM;
    }

    char *batch_body = NULL;
    err = firc_kn_rci_post(base_url, "/rci/", req_body, &batch_body);
    free(req_body);
    if (err != FIRC_OK) {
        free(metas);
        return err;
    }

    err = firc_kn_parse_system_names(batch_body, metas, n);
    free(batch_body);
    if (err != FIRC_OK) {
        free(metas);
        return err;
    }

    *metas_out = metas;
    *n_out = n;
    return FIRC_OK;
}

firc_err_t firc_kn_get_iface_aliases_from(const char *base_url, firc_kn_aliases_t *out) {
    out->items = NULL;
    out->n = 0;
    firc_kn_iface_meta_t *metas = NULL;
    size_t n = 0;
    firc_err_t err = fetch_metas(base_url, &metas, &n);
    if (err != FIRC_OK || n == 0) { return err; }
    err = firc_kn_build_aliases(metas, n, out);
    free(metas);
    return err;
}

firc_err_t firc_kn_build_id_map(const firc_kn_iface_meta_t *metas, size_t n, firc_kn_id_map_t *out) {
    out->items = NULL;
    out->n = 0;
    if (n == 0) { return FIRC_OK; }
    firc_kn_id_entry_t *items = calloc(n, sizeof(*items));
    if (items == NULL) { return FIRC_ERR_NOMEM; }
    size_t k = 0;
    for (size_t i = 0; i < n; i++) {
        if (metas[i].system_name[0] == '\0' || metas[i].id[0] == '\0') { continue; }
        snprintf(items[k].id, sizeof(items[k].id), "%s", metas[i].id);
        snprintf(items[k].system_name, sizeof(items[k].system_name), "%s", metas[i].system_name);
        k++;
    }
    if (k == 0) {
        free(items);
        return FIRC_OK;
    }
    out->items = items;
    out->n = k;
    return FIRC_OK;
}

firc_err_t firc_kn_get_id_map_from(const char *base_url, firc_kn_id_map_t *out) {
    out->items = NULL;
    out->n = 0;
    firc_kn_iface_meta_t *metas = NULL;
    size_t n = 0;
    firc_err_t err = fetch_metas(base_url, &metas, &n);
    if (err != FIRC_OK || n == 0) { return err; }
    err = firc_kn_build_id_map(metas, n, out);
    free(metas);
    return err;
}

const char *firc_kn_id_map_lookup(const firc_kn_id_map_t *m, const char *rci_id) {
    if (m == NULL || rci_id == NULL) { return NULL; }
    for (size_t i = 0; i < m->n; i++) {
        if (strcmp(m->items[i].id, rci_id) == 0) { return m->items[i].system_name; }
    }
    return NULL;
}

void firc_kn_id_map_free(firc_kn_id_map_t *m) {
    if (m == NULL) { return; }
    free(m->items);
    m->items = NULL;
    m->n = 0;
}

firc_err_t firc_kn_get_iface_aliases(firc_kn_aliases_t *out) {
#ifdef FIRC_ENTWARE_KN
    return firc_kn_get_iface_aliases_from(FIRC_KN_RCI_BASE_URL, out);
#else
    out->items = NULL;
    out->n = 0;
    return FIRC_OK;
#endif
}
