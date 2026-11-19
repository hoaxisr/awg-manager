#include "firc/auth.h"

#include <ctype.h>
#include <errno.h>
#include <fcntl.h>
#include <limits.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <time.h>
#include <unistd.h>

#include <cjson/cJSON.h>

#include "firc/atomic_write.h"
#include "firc/crypt.h"
#include "firc/hash.h"
#include "firc/jwt.h"
#include "firc/log.h"
#include "firc/logrep.h"
#include "firc/paths.h"
#include "firc/rand.h"

#define FIRC_AUTH_SECRET_LEN 32
#define FIRC_AUTH_JWT_ISSUER "firc"
#define FIRC_AUTH_JWT_YEARS 20

static bool g_secret_loaded = false;
static uint8_t g_secret[FIRC_AUTH_SECRET_LEN];
static size_t g_secret_len = 0;
static firc_err_t g_secret_err = FIRC_OK;

static void hex_encode(const uint8_t *data, size_t len, char *out) {
    static const char digits[] = "0123456789abcdef";
    for (size_t i = 0; i < len; i++) {
        out[i * 2] = digits[data[i] >> 4];
        out[i * 2 + 1] = digits[data[i] & 0xf];
    }
    out[len * 2] = '\0';
}

/* Howard Hinnant's civil-calendar arithmetic; rolls Feb 29 to Mar 1 in a non-leap target year */
static void civil_from_days(int64_t z, int64_t *y, unsigned *m, unsigned *d) {
    z += 719468;
    int64_t era = (z >= 0 ? z : z - 146096) / 146097;
    unsigned doe = (unsigned)(z - era * 146097);
    unsigned yoe = (doe - doe / 1460 + doe / 36524 - doe / 146096) / 365;
    int64_t y_ = (int64_t)yoe + era * 400;
    unsigned doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    unsigned mp = (5 * doy + 2) / 153;
    unsigned d_ = doy - (153 * mp + 2) / 5 + 1;
    unsigned m_ = mp + (mp < 10 ? 3 : (unsigned)-9);
    y_ += (m_ <= 2);
    *y = y_;
    *m = m_;
    *d = d_;
}

static int64_t days_from_civil(int64_t y, unsigned m, unsigned d) {
    y -= (m <= 2);
    int64_t era = (y >= 0 ? y : y - 399) / 400;
    unsigned yoe = (unsigned)(y - era * 400);
    unsigned doy = (153 * (m + (m > 2 ? (unsigned)-3 : 9)) + 2) / 5 + d - 1;
    unsigned doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    return era * 146097 + (int64_t)doe - 719468;
}

int64_t firc_auth_add_years_utc(int64_t unix_ts, int years) {
    int64_t days = unix_ts >= 0 ? unix_ts / 86400 : (unix_ts - 86399) / 86400;
    int64_t secs_of_day = unix_ts - days * 86400;
    int64_t y;
    unsigned m, d;
    civil_from_days(days, &y, &m, &d);
    y += years;
    int64_t new_days = days_from_civil(y, m, d);
    return new_days * 86400 + secs_of_day;
}

/* reachable by an unauthenticated caller on the DNS loop, so the hash file is cached and logins throttled below */
#define AUTH_CACHE_FILES 2
#define AUTH_CACHE_ENTRIES 64
#define AUTH_CACHE_LOGIN 64
#define AUTH_CACHE_HASH 128

typedef struct {
    char login[AUTH_CACHE_LOGIN];
    char hash[AUTH_CACHE_HASH];
    bool too_long; /* the line named this account and its hash did not fit */
} auth_entry_t;

typedef struct {
    char path[512];
    dev_t dev;
    ino_t ino;
    off_t size;
    struct timespec mtime;
    auth_entry_t e[AUTH_CACHE_ENTRIES];
    size_t n;
    bool valid;
    bool complete; /* every line of the file is in here */
} auth_file_cache_t;

static auth_file_cache_t g_file_cache[AUTH_CACHE_FILES];

/* NULL (no caching) when the path is too long to key on */
static auth_file_cache_t *cache_slot(const char *path) {
    if (strlen(path) >= sizeof(g_file_cache[0].path)) { return NULL; }
    for (size_t i = 0; i < AUTH_CACHE_FILES; i++) {
        if (g_file_cache[i].valid && strcmp(g_file_cache[i].path, path) == 0) {
            return &g_file_cache[i];
        }
    }
    for (size_t i = 0; i < AUTH_CACHE_FILES; i++) {
        if (!g_file_cache[i].valid) { return &g_file_cache[i]; }
    }
    return &g_file_cache[0];
}

static bool cache_fresh(const auth_file_cache_t *c, const char *path, const struct stat *st) {
    return c->valid && c->complete && strcmp(c->path, path) == 0 && c->dev == st->st_dev &&
           c->ino == st->st_ino && c->size == st->st_size &&
           c->mtime.tv_sec == st->st_mtim.tv_sec && c->mtime.tv_nsec == st->st_mtim.tv_nsec;
}

/* "*" means no password login (FIRC_ERR_STATE); must not be read as "x" (FIRC_ERR_NOENT) or it falls through */
static firc_err_t entry_answer(const auth_entry_t *e, char *out, size_t out_len) {
    if (e->too_long) { return FIRC_ERR_LIMIT; }
    if (e->hash[0] == '*' && e->hash[1] == '\0') { return FIRC_ERR_STATE; }
    if (e->hash[0] == '\0' || (e->hash[0] == 'x' && e->hash[1] == '\0')) { return FIRC_ERR_NOENT; }
    if (strlen(e->hash) >= out_len) { return FIRC_ERR_LIMIT; }
    snprintf(out, out_len, "%s", e->hash);
    return FIRC_OK;
}

void firc_auth_forget_cached_hash(void) {
    for (size_t i = 0; i < AUTH_CACHE_FILES; i++) { g_file_cache[i].valid = false; }
}


/* throttles the work, not a caller identity (there is none to key on); one success clears it */
#define FIRC_AUTH_THROTTLE_AFTER 3
#define FIRC_AUTH_THROTTLE_MS 1000
#define FIRC_AUTH_SAY_EVERY_MS 60000u

static unsigned g_login_failures;
static int64_t g_login_next_ms;
static firc_lograte_t g_said_unreadable, g_said_no_hash, g_said_odd_hash;

static int64_t mono_ms(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (int64_t)ts.tv_sec * 1000 + ts.tv_nsec / 1000000;
}

void firc_auth_reset_throttle_for_test(void) {
    g_login_failures = 0;
    g_login_next_ms = 0;
    g_said_unreadable = (firc_lograte_t){0};
    g_said_no_hash = (firc_lograte_t){0};
    g_said_odd_hash = (firc_lograte_t){0};
}

static void login_failed(void) {
    if (g_login_failures < UINT_MAX) { g_login_failures++; }
    g_login_next_ms = mono_ms() + FIRC_AUTH_THROTTLE_MS;
}

/* FIRC_ERR_NOENT covers both no-such-file and no-hash-for-this-account: either way, look elsewhere */
static firc_err_t hash_from_file(const char *path, const char *login, char *out, size_t out_len) {
    FILE *f = fopen(path, "re");
    if (!f) { return errno == ENOENT ? FIRC_ERR_NOENT : firc_err_from_errno(errno); }

    struct stat st;
    bool have_st = fstat(fileno(f), &st) == 0;
    auth_file_cache_t *c = have_st ? cache_slot(path) : NULL;
    if (c != NULL && cache_fresh(c, path, &st)) {
        fclose(f);
        for (size_t i = 0; i < c->n; i++) {
            if (strcmp(c->e[i].login, login) == 0) { return entry_answer(&c->e[i], out, out_len); }
        }
        return FIRC_ERR_NOENT;
    }

    if (c != NULL) {
        c->valid = false;
        c->n = 0;
        c->complete = true;
        snprintf(c->path, sizeof(c->path), "%s", path);
        c->dev = st.st_dev;
        c->ino = st.st_ino;
        c->size = st.st_size;
        c->mtime = st.st_mtim;
    }

    char *line = NULL;
    size_t cap = 0;
    firc_err_t result = FIRC_ERR_NOENT;
    bool answered = false;
    ssize_t n;
    while ((n = getline(&line, &cap, f)) >= 0) {
        while (n > 0 && (line[n - 1] == '\n' || line[n - 1] == '\r')) { line[--n] = '\0'; }
        if (n == 0 || line[0] == '#') { continue; }
        char *colon1 = memchr(line, ':', (size_t)n);
        if (!colon1) { continue; }
        size_t name_len = (size_t)(colon1 - line);

        char *hash_start = colon1 + 1;
        char *colon2 = strchr(hash_start, ':');
        size_t hash_len = colon2 ? (size_t)(colon2 - hash_start) : strlen(hash_start);
        while (hash_len > 0 && isspace((unsigned char)hash_start[hash_len - 1])) { hash_len--; }
        while (hash_len > 0 && isspace((unsigned char)*hash_start)) {
            hash_start++;
            hash_len--;
        }

        auth_entry_t entry;
        memset(&entry, 0, sizeof(entry));
        entry.too_long = hash_len >= sizeof(entry.hash);
        if (!entry.too_long) { memcpy(entry.hash, hash_start, hash_len); }

        if (!answered && name_len == strlen(login) && strncmp(line, login, name_len) == 0) {
            answered = true;
            result = entry_answer(&entry, out, out_len);
        }

        /* an oversized login marks the file not-cached rather than silently dropping it (a lockout) */
        if (c != NULL) {
            if (name_len >= sizeof(entry.login) || c->n >= AUTH_CACHE_ENTRIES) {
                c->complete = false;
            } else {
                memcpy(entry.login, line, name_len);
                c->e[c->n++] = entry;
            }
        }
    }
    free(line);
    fclose(f);
    if (c != NULL) { c->valid = true; }
    return result;
}

/* shadow then passwd, per account rather than per file: a shadow omitting this account must still fall back */
firc_err_t firc_auth_load_password_hash_from(const char *shadow_path, const char *passwd_path,
                                        const char *login, char *out, size_t out_len) {
    firc_err_t err = hash_from_file(shadow_path, login, out, out_len);
    if (err == FIRC_OK || err == FIRC_ERR_STATE) { return err; } /* STATE ("no password login") is an answer too */
    if (err != FIRC_ERR_NOENT) {
        firc_lograte_say(&g_said_unreadable, FIRC_AUTH_SAY_EVERY_MS, FIRC_LOG_WARN, NULL,
                         "%s could not be read (%s): falling back to %s", shadow_path, firc_err_str(err), passwd_path);
    }
    firc_err_t fallback = hash_from_file(passwd_path, login, out, out_len);
    if (fallback == FIRC_OK) { return FIRC_OK; }
    return err == FIRC_ERR_NOENT ? fallback : err; /* shadow's own error outranks a plain "no such account" */
}

firc_err_t firc_auth_load_password_hash(const char *login, char *out, size_t out_len) {
    return firc_auth_load_password_hash_from(FIRC_SHADOW_FILE, FIRC_PASSWD_FILE, login, out, out_len);
}

static firc_err_t load_or_create_secret(const char *state_dir) {
    char path[512];
    if (snprintf(path, sizeof(path), "%s/auth_secret", state_dir) >= (int)sizeof(path)) {
        return FIRC_ERR_INVAL;
    }

    FILE *f = fopen(path, "rbe");
    if (f) {
        char buf[256];
        size_t n = fread(buf, 1, sizeof(buf) - 1, f);
        fclose(f);
        buf[n] = '\0';
        size_t start = 0;
        size_t end = n;
        while (start < end && isspace((unsigned char)buf[start])) { start++; }
        while (end > start && isspace((unsigned char)buf[end - 1])) { end--; }
        if (end == start) { return FIRC_ERR_INVAL; }

        size_t decoded_len = 0;
        if (firc_base64_decode(buf + start, end - start, g_secret, sizeof(g_secret), &decoded_len) != 0 ||
            decoded_len != sizeof(g_secret)) {
            return FIRC_ERR_INVAL;
        }
        g_secret_len = decoded_len;
        return FIRC_OK;
    }
    if (errno != ENOENT) { return firc_err_from_errno(errno); }

    uint8_t secret[FIRC_AUTH_SECRET_LEN];
    if (firc_random_bytes(secret, sizeof(secret)) != FIRC_OK) { return FIRC_ERR_SYS; }

    char encoded[64];
    firc_base64_encode(secret, sizeof(secret), encoded);

    /* 0755: firc's shared config dir, must agree with main.c's own create; chmod undoes the inherited umask */
    if (mkdir(state_dir, 0755) == 0) {
        (void)chmod(state_dir, 0755);
    } else if (errno != EEXIST) {
        return firc_err_from_errno(errno);
    }

    /* temp file + fsync + rename: a power cut mid-write must not leave a zero-length (never regenerated) secret */
    char tmp[576];
    if (snprintf(tmp, sizeof(tmp), "%s.tmp.XXXXXX", path) >= (int)sizeof(tmp)) {
        return FIRC_ERR_INVAL;
    }
    int fd = firc_mkstemp_cloexec(tmp);
    if (fd < 0) { return firc_err_from_errno(errno); }
    if (fchmod(fd, 0600) != 0) {
        close(fd);
        unlink(tmp);
        return firc_err_from_errno(errno);
    }
    size_t encoded_len = strlen(encoded);
    ssize_t written = write(fd, encoded, encoded_len);
    if (written < 0 || (size_t)written != encoded_len || fsync(fd) != 0) {
        close(fd);
        unlink(tmp);
        return FIRC_ERR_IO;
    }
    if (close(fd) != 0 || rename(tmp, path) != 0) {
        unlink(tmp);
        return firc_err_from_errno(errno);
    }
    int dfd = open(state_dir, O_RDONLY | O_DIRECTORY | O_CLOEXEC);
    if (dfd >= 0) {
        (void)fsync(dfd);
        close(dfd);
    }

    memcpy(g_secret, secret, sizeof(secret));
    g_secret_len = sizeof(secret);
    return FIRC_OK;
}

void firc_auth_forget_secret_for_test(void) {
    g_secret_loaded = false;
    g_secret_err = FIRC_OK;
    g_secret_len = 0;
}

firc_err_t firc_auth_load_secret(const char *state_dir, uint8_t *out, size_t *out_len) {
    if (!g_secret_loaded) {
        g_secret_err = load_or_create_secret(state_dir);
        g_secret_loaded = true;
        if (g_secret_err != FIRC_OK) {
            /* without this line the only evidence is a login form that silently refuses forever */
            FIRC_ERROR("the WebUI's signing secret in %s could not be loaded or created (%s): "
                       "every login will be refused until it is fixed or removed",
                       state_dir, firc_err_str(g_secret_err));
        }
    }
    if (g_secret_err != FIRC_OK) { return g_secret_err; }
    memcpy(out, g_secret, g_secret_len);
    *out_len = g_secret_len;
    return FIRC_OK;
}

static firc_err_t derive_signing_key(const char *state_dir, const char *password_hash,
                                   char *out_hex, size_t out_hex_len) {
    uint8_t secret[FIRC_AUTH_SECRET_LEN];
    size_t secret_len;
    firc_err_t err = firc_auth_load_secret(state_dir, secret, &secret_len);
    if (err != FIRC_OK) { return err; }

    uint8_t mac[FIRC_SHA256_DIGEST_LEN];
    firc_hmac_sha256(secret, secret_len, (const uint8_t *)password_hash, strlen(password_hash), mac);
    if (out_hex_len < sizeof(mac) * 2 + 1) { return FIRC_ERR_LIMIT; }
    hex_encode(mac, sizeof(mac), out_hex);
    return FIRC_OK;
}

/* a login name is logged verbatim elsewhere; this escapes it so it cannot forge a second log line or evict real ones */
static void quote_login(const char *login, char *out, size_t cap) {
    size_t at = 0;
    for (const char *p = login != NULL ? login : ""; *p != '\0' && at + 1 < cap; p++) {
        unsigned char c = (unsigned char)*p;
        out[at++] = (char)((c < 0x20 || c == 0x7f || c == '"' || c == '\\') ? '?' : c);
    }
    out[at] = '\0';
}

firc_err_t firc_auth_authenticate_from(const char *shadow_path, const char *passwd_path,
                                  const char *state_dir, const char *login, const char *password,
                                  char *token_out, size_t token_out_len) {
    if (!login || login[0] == '\0' || !password || password[0] == '\0') { return FIRC_ERR_INVAL; }

    /* refused before any work: a caller must not run SHA-512-crypt at its own rate on the DNS loop thread */
    if (g_login_failures >= FIRC_AUTH_THROTTLE_AFTER && mono_ms() < g_login_next_ms) {
        return FIRC_ERR_AGAIN;
    }

    char password_hash[128];
    firc_err_t err =
        firc_auth_load_password_hash_from(shadow_path, passwd_path, login, password_hash,
                                sizeof(password_hash));
    if (err != FIRC_OK) {
        char safe[64]; /* the HTTP layer flattens this to "Invalid credentials"; log the real reason */
        quote_login(login, safe, sizeof(safe));
        firc_lograte_say(&g_said_no_hash, FIRC_AUTH_SAY_EVERY_MS, FIRC_LOG_WARN, NULL,
                         "login refused for \"%s\": no usable password hash in %s or %s (%s)", safe, shadow_path,
                         passwd_path, firc_err_str(err));
        login_failed();
        return err;
    }

    char computed[128];
    err = firc_crypt_password(password, password_hash, computed, sizeof(computed));
    if (err != FIRC_OK) {
        char safe[64];
        quote_login(login, safe, sizeof(safe));
        firc_lograte_say(&g_said_odd_hash, FIRC_AUTH_SAY_EVERY_MS, FIRC_LOG_WARN, NULL,
                         "login refused for \"%s\": this build cannot compute the hash in %s -- "
                         "firc knows $1$, $5$ and $6$, and that one starts \"%.3s\"",
                         safe, shadow_path, password_hash);
        login_failed();
        return FIRC_ERR_INVAL;
    }
    if (strcmp(computed, password_hash) != 0) {
        login_failed();
        return FIRC_ERR_INVAL;
    }
    g_login_failures = 0;

    char signing_key[FIRC_SHA256_DIGEST_LEN * 2 + 1];
    err = derive_signing_key(state_dir, password_hash, signing_key, sizeof(signing_key));
    if (err != FIRC_OK) { return err; }

    int64_t now = (int64_t)time(NULL);
    firc_jwt_claims_t claims = {0};
    snprintf(claims.sub, sizeof(claims.sub), "%s", login);
    snprintf(claims.iss, sizeof(claims.iss), "%s", FIRC_AUTH_JWT_ISSUER);
    claims.iat = now;
    claims.exp = firc_auth_add_years_utc(now, FIRC_AUTH_JWT_YEARS);

    return firc_jwt_sign(&claims, (const uint8_t *)signing_key, strlen(signing_key), token_out,
                       token_out_len);
}

firc_err_t firc_auth_authenticate(const char *state_dir, const char *login, const char *password,
                              char *token_out, size_t token_out_len) {
    return firc_auth_authenticate_from(FIRC_SHADOW_FILE, FIRC_PASSWD_FILE, state_dir, login, password,
                             token_out, token_out_len);
}

firc_err_t firc_auth_verify_token_from(const char *shadow_path, const char *passwd_path,
                                  const char *state_dir, const char *token) {
    firc_jwt_claims_t unverified;
    if (firc_jwt_parse_unverified(token, &unverified) != FIRC_OK) { return FIRC_ERR_INVAL; }
    if (unverified.sub[0] == '\0') { return FIRC_ERR_INVAL; }

    char password_hash[128];
    if (firc_auth_load_password_hash_from(shadow_path, passwd_path, unverified.sub, password_hash,
                                sizeof(password_hash)) != FIRC_OK) {
        return FIRC_ERR_INVAL;
    }

    char signing_key[FIRC_SHA256_DIGEST_LEN * 2 + 1];
    if (derive_signing_key(state_dir, password_hash, signing_key, sizeof(signing_key)) != FIRC_OK) {
        return FIRC_ERR_INVAL;
    }

    firc_jwt_claims_t verified;
    if (firc_jwt_parse_and_verify(token, (const uint8_t *)signing_key, strlen(signing_key),
                                &verified) != FIRC_OK) {
        return FIRC_ERR_INVAL;
    }
    if (strcmp(verified.sub, unverified.sub) != 0) { return FIRC_ERR_INVAL; }
    if (strcmp(verified.iss, FIRC_AUTH_JWT_ISSUER) != 0) { return FIRC_ERR_INVAL; }
    if (verified.exp <= (int64_t)time(NULL)) { return FIRC_ERR_INVAL; }
    return FIRC_OK;
}

firc_err_t firc_auth_verify_token(const char *state_dir, const char *token) {
    return firc_auth_verify_token_from(FIRC_SHADOW_FILE, FIRC_PASSWD_FILE, state_dir, token);
}

bool firc_auth_middleware(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    firc_auth_ctx_t *ctx = ud;
    const char *path = firc_http_req_path(req);
    if (strncmp(path, "/api/", 5) != 0) { return true; }
    if (strcmp(path, "/api/v1/auth") == 0) { return true; }

    const char *token = firc_http_req_header(req, "X-Firc-Token");
    if (!token) {
        const char *authz = firc_http_req_header(req, "Authorization");
        if (!authz) {
            firc_http_res_write_error(res, 401, "Unauthorized");
            return false;
        }
        while (*authz == ' ' || *authz == '\t') { authz++; }
        static const char prefix[] = "Bearer ";
        if (strncmp(authz, prefix, sizeof(prefix) - 1) != 0) {
            firc_http_res_write_error(res, 401, "Unauthorized");
            return false;
        }
        token = authz + sizeof(prefix) - 1;
    }
    if (*token == '\0') {
        firc_http_res_write_error(res, 401, "Unauthorized");
        return false;
    }

    if (firc_auth_verify_token_from(ctx->shadow_path != NULL ? ctx->shadow_path : FIRC_SHADOW_FILE,
                                    ctx->passwd_path != NULL ? ctx->passwd_path : FIRC_PASSWD_FILE,
                                    ctx->state_dir(ctx->ud), token) != FIRC_OK) {
        firc_http_res_write_error(res, 401, "Unauthorized");
        return false;
    }
    return true;
}

void firc_auth_status_handler(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    (void)req;
    (void)ud;
    cJSON *obj = cJSON_CreateObject();
    cJSON_AddBoolToObject(obj, "enabled", true);
    firc_http_res_write_json(res, 200, obj);
}

void firc_auth_login_handler(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    firc_auth_ctx_t *ctx = ud;
    size_t body_len;
    const uint8_t *body = firc_http_req_body(req, &body_len);
    cJSON *json = body_len > 0 ? cJSON_ParseWithLength((const char *)body, body_len) : NULL;
    if (!json) {
        firc_http_res_write_error(res, 400, "failed to parse request");
        return;
    }

    cJSON *login_j = cJSON_GetObjectItemCaseSensitive(json, "login");
    cJSON *password_j = cJSON_GetObjectItemCaseSensitive(json, "password");
    const char *login = cJSON_IsString(login_j) ? login_j->valuestring : "";
    const char *password = cJSON_IsString(password_j) ? password_j->valuestring : "";
    if (login[0] == '\0' || password[0] == '\0') {
        cJSON_Delete(json);
        firc_http_res_write_error(res, 400, "missing credentials");
        return;
    }

    char token[FIRC_JWT_MAX_TOKEN];
    firc_err_t err =
        firc_auth_authenticate_from(ctx->shadow_path != NULL ? ctx->shadow_path : FIRC_SHADOW_FILE,
                                    ctx->passwd_path != NULL ? ctx->passwd_path : FIRC_PASSWD_FILE,
                                    ctx->state_dir(ctx->ud), login, password, token, sizeof(token));
    cJSON_Delete(json);
    if (err == FIRC_ERR_AGAIN) {
        /* its own status: credentials were not judged at all; a 403 would make a client retry forever */
        firc_http_res_write_error(res, 429, "too many failed logins: wait a moment and try again");
        return;
    }
    if (err != FIRC_OK) {
        firc_http_res_write_error(res, 403, "Invalid credentials");
        return;
    }

    cJSON *out = cJSON_CreateObject();
    cJSON_AddStringToObject(out, "token", token);
    firc_http_res_write_json(res, 200, out);
}
