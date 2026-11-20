#include "firc/jwt.h"

#include <cjson/cJSON.h>
#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "firc/hash.h"

/* must stay byte-exact: alg then typ, no whitespace */
static const char JWT_HEADER_JSON[] = "{\"alg\":\"HS256\",\"typ\":\"JWT\"}";

static char *build_claims_json(const firc_jwt_claims_t *claims) {
    cJSON *obj = cJSON_CreateObject();
    if (!obj) { return NULL; }
    bool ok = cJSON_AddStringToObject(obj, "sub", claims->sub) != NULL &&
             cJSON_AddStringToObject(obj, "iss", claims->iss) != NULL &&
             cJSON_AddNumberToObject(obj, "iat", (double)claims->iat) != NULL &&
             cJSON_AddNumberToObject(obj, "exp", (double)claims->exp) != NULL;
    char *out = ok ? cJSON_PrintUnformatted(obj) : NULL;
    cJSON_Delete(obj);
    return out;
}

static firc_err_t hmac_sign(const char *payload, size_t payload_len, const uint8_t *secret,
                         size_t secret_len, char *sig_b64_out) {
    uint8_t mac[FIRC_SHA256_DIGEST_LEN];
    firc_hmac_sha256(secret, secret_len, (const uint8_t *)payload, payload_len, mac);
    firc_base64url_encode(mac, sizeof(mac), sig_b64_out);
    return FIRC_OK;
}

firc_err_t firc_jwt_sign(const firc_jwt_claims_t *claims, const uint8_t *secret, size_t secret_len,
                    char *out, size_t out_len) {
    char *claims_json = build_claims_json(claims);
    if (!claims_json) { return FIRC_ERR_NOMEM; }

    char header_b64[64];
    if (firc_base64url_encoded_len(sizeof(JWT_HEADER_JSON) - 1) >= sizeof(header_b64)) {
        free(claims_json);
        return FIRC_ERR_LIMIT;
    }
    firc_base64url_encode((const uint8_t *)JWT_HEADER_JSON, sizeof(JWT_HEADER_JSON) - 1, header_b64);

    size_t claims_len = strlen(claims_json);
    size_t claims_b64_len = firc_base64url_encoded_len(claims_len);
    char *claims_b64 = malloc(claims_b64_len + 1);
    if (!claims_b64) {
        free(claims_json);
        return FIRC_ERR_NOMEM;
    }
    firc_base64url_encode((const uint8_t *)claims_json, claims_len, claims_b64);
    free(claims_json);

    size_t header_len = strlen(header_b64);
    size_t payload_len = header_len + 1 + claims_b64_len;
    char *payload = malloc(payload_len + 1);
    if (!payload) {
        free(claims_b64);
        return FIRC_ERR_NOMEM;
    }
    memcpy(payload, header_b64, header_len);
    payload[header_len] = '.';
    memcpy(payload + header_len + 1, claims_b64, claims_b64_len);
    payload[payload_len] = '\0';
    free(claims_b64);

    char sig_b64[64];
    hmac_sign(payload, payload_len, secret, secret_len, sig_b64);

    size_t needed = payload_len + 1 + strlen(sig_b64) + 1;
    if (out_len < needed) {
        free(payload);
        return FIRC_ERR_LIMIT;
    }
    snprintf(out, out_len, "%s.%s", payload, sig_b64);
    free(payload);
    return FIRC_OK;
}

static firc_err_t split_token(const char *token, const char **p1, size_t *l1, const char **p2,
                            size_t *l2, const char **p3, size_t *l3) {
    const char *dot1 = strchr(token, '.');
    if (!dot1) { return FIRC_ERR_PROTO; }
    const char *dot2 = strchr(dot1 + 1, '.');
    if (!dot2) { return FIRC_ERR_PROTO; }
    if (strchr(dot2 + 1, '.') != NULL) { return FIRC_ERR_PROTO; }

    *p1 = token;
    *l1 = (size_t)(dot1 - token);
    *p2 = dot1 + 1;
    *l2 = (size_t)(dot2 - *p2);
    *p3 = dot2 + 1;
    *l3 = strlen(*p3);
    return FIRC_OK;
}

static firc_err_t decode_claims(const char *claims_b64, size_t claims_b64_len,
                              firc_jwt_claims_t *out) {
    size_t decoded_cap = ((claims_b64_len + 3) / 4) * 3 + 1;
    uint8_t *decoded = malloc(decoded_cap);
    if (!decoded) { return FIRC_ERR_NOMEM; }
    size_t decoded_len = 0;
    if (firc_base64url_decode(claims_b64, claims_b64_len, decoded, decoded_cap, &decoded_len) != 0) {
        free(decoded);
        return FIRC_ERR_PROTO;
    }

    cJSON *obj = cJSON_ParseWithLength((const char *)decoded, decoded_len);
    free(decoded);
    if (!obj) { return FIRC_ERR_PROTO; }

    memset(out, 0, sizeof(*out));
    cJSON *sub = cJSON_GetObjectItemCaseSensitive(obj, "sub");
    cJSON *iss = cJSON_GetObjectItemCaseSensitive(obj, "iss");
    cJSON *iat = cJSON_GetObjectItemCaseSensitive(obj, "iat");
    cJSON *exp = cJSON_GetObjectItemCaseSensitive(obj, "exp");
    if (cJSON_IsString(sub)) { snprintf(out->sub, sizeof(out->sub), "%s", sub->valuestring); }
    if (cJSON_IsString(iss)) { snprintf(out->iss, sizeof(out->iss), "%s", iss->valuestring); }
    if (cJSON_IsNumber(iat)) { out->iat = (int64_t)iat->valuedouble; }
    if (cJSON_IsNumber(exp)) { out->exp = (int64_t)exp->valuedouble; }
    cJSON_Delete(obj);
    return FIRC_OK;
}

firc_err_t firc_jwt_parse_unverified(const char *token, firc_jwt_claims_t *out) {
    const char *p1, *p2, *p3;
    size_t l1, l2, l3;
    firc_err_t err = split_token(token, &p1, &l1, &p2, &l2, &p3, &l3);
    if (err != FIRC_OK) { return err; }
    (void)p1;
    (void)l1;
    (void)p3;
    (void)l3;
    return decode_claims(p2, l2, out);
}

firc_err_t firc_jwt_parse_and_verify(const char *token, const uint8_t *secret, size_t secret_len,
                                firc_jwt_claims_t *out) {
    const char *p1, *p2, *p3;
    size_t l1, l2, l3;
    firc_err_t err = split_token(token, &p1, &l1, &p2, &l2, &p3, &l3);
    if (err != FIRC_OK) { return err; }

    size_t payload_len = l1 + 1 + l2;
    uint8_t mac[FIRC_SHA256_DIGEST_LEN];
    firc_hmac_sha256(secret, secret_len, (const uint8_t *)p1, payload_len, mac);
    char expected_sig[64];
    firc_base64url_encode(mac, sizeof(mac), expected_sig);

    if (l3 != strlen(expected_sig) || !firc_hash_equal((const uint8_t *)expected_sig,
                                                     (const uint8_t *)p3, l3)) {
        return FIRC_ERR_INVAL;
    }
    return decode_claims(p2, l2, out);
}
