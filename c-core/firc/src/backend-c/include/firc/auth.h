#ifndef FIRC_AUTH_H
#define FIRC_AUTH_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/err.h"
#include "firc/httpd.h"

/* crypt(3) hash for login, shadow falling back to passwd; out needs >= 128 bytes */
firc_err_t firc_auth_load_password_hash(const char *login, char *out, size_t out_len);

void firc_auth_forget_cached_hash(void); /* test seam: drops the cached hash file */
void firc_auth_reset_throttle_for_test(void); /* test seam: clears the login throttle */
void firc_auth_forget_secret_for_test(void); /* test seam: forces the signing secret to reload */

/* shadow_path/passwd_path are injectable for tests; production always passes the real paths */
firc_err_t firc_auth_load_password_hash_from(const char *shadow_path, const char *passwd_path,
                                         const char *login, char *out, size_t out_len);
firc_err_t firc_auth_authenticate_from(const char *shadow_path, const char *passwd_path,
                                   const char *state_dir, const char *login,
                                   const char *password, char *token_out, size_t token_out_len);
firc_err_t firc_auth_verify_token_from(const char *shadow_path, const char *passwd_path,
                                   const char *state_dir, const char *token);

/* adds years calendar-wise (UTC); Feb 29 rolls to Mar 1 in a non-leap target year */
int64_t firc_auth_add_years_utc(int64_t unix_ts, int years);

/* loads or creates+persists the HMAC signing secret under state_dir; out needs >= 32 bytes */
firc_err_t firc_auth_load_secret(const char *state_dir, uint8_t *out, size_t *out_len);

/* login+password -> signed JWT (20-year expiry); token_out needs >= FIRC_JWT_MAX_TOKEN */
firc_err_t firc_auth_authenticate(const char *state_dir, const char *login, const char *password,
                              char *token_out, size_t token_out_len);

/* verifies signature, issuer and expiry against the user's current password hash; FIRC_ERR_INVAL on any failure */
firc_err_t firc_auth_verify_token(const char *state_dir, const char *token);

typedef const char *(*firc_auth_state_dir_fn)(void *ud);

typedef struct firc_auth_ctx {
    firc_auth_state_dir_fn state_dir;
    void *ud;
    const char *shadow_path; /* NULL is FIRC_SHADOW_FILE; a test names a fixture */
    const char *passwd_path; /* NULL is FIRC_PASSWD_FILE; a test names a fixture */
} firc_auth_ctx_t;

/* TCP listener only; gates every /api/ path but /api/v1/auth behind a bearer JWT. ud is a firc_auth_ctx_t *. */
bool firc_auth_middleware(firc_http_req_t *req, firc_http_res_t *res, void *ud);

/* GET /api/v1/auth -> {"enabled": true}, always; ud must be a firc_auth_ctx_t *. */
void firc_auth_status_handler(firc_http_req_t *req, firc_http_res_t *res, void *ud);
/* POST /api/v1/auth -> {"token": "..."}; 400 bad body, 403 bad credentials, 429 throttled */
void firc_auth_login_handler(firc_http_req_t *req, firc_http_res_t *res, void *ud);

#endif /* FIRC_AUTH_H */
