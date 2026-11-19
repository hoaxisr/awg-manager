#ifndef FIRC_JWT_H
#define FIRC_JWT_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/err.h"

#define FIRC_JWT_MAX_SUB 256
#define FIRC_JWT_MAX_ISS 64
#define FIRC_JWT_MAX_TOKEN 1024

typedef struct firc_jwt_claims {
    char sub[FIRC_JWT_MAX_SUB];
    char iss[FIRC_JWT_MAX_ISS];
    int64_t iat;
    int64_t exp;
} firc_jwt_claims_t;

/* signs claims with HMAC-SHA256(secret); out needs >= FIRC_JWT_MAX_TOKEN bytes */
firc_err_t firc_jwt_sign(const firc_jwt_claims_t *claims, const uint8_t *secret, size_t secret_len,
                    char *out, size_t out_len);

/* parses claims without verifying the signature; FIRC_ERR_PROTO on malformed token/claims */
firc_err_t firc_jwt_parse_unverified(const char *token, firc_jwt_claims_t *out);

/* full parse + signature verification; FIRC_ERR_PROTO on malformed token, FIRC_ERR_INVAL on bad signature */
firc_err_t firc_jwt_parse_and_verify(const char *token, const uint8_t *secret, size_t secret_len,
                                firc_jwt_claims_t *out);

#endif /* FIRC_JWT_H */
