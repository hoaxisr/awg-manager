#ifndef FIRC_RCI_TOKEN_H
#define FIRC_RCI_TOKEN_H

#include <stdatomic.h>
#include <stdbool.h>
#include <stddef.h>

#include "firc/err.h"

#define FIRC_KN_TOKEN_HEADER "X-NDMA-TKN"

#define FIRC_KN_TOKEN_USER_DATA "firc"

#define FIRC_KN_TOKEN_MAX 128

#define FIRC_KN_TOKEN_REISSUE_SECONDS 60

#define FIRC_KN_NDMC_TIMEOUT_MS 5000

/* True for a release of 5.02 or later ("5.02.A.11.0-1"); false for older, empty or unreadable. */
bool firc_kn_token_release_ok(const char *release);

/* Reads ndmc's `authentication token generate` output; false unless a value of 32+ alphanumerics is found. */
bool firc_kn_token_parse_generated(const char *out, char *id, size_t id_cap, char *tok, size_t tok_cap);

/* Ids of `show authentication token` blocks whose user-data is user_data; returns how many, at most cap. */
size_t firc_kn_token_ids(const char *out, const char *user_data, char ids[][16], size_t cap);

/* Sets the token file and the ndmc binary and forgets the token in memory; on only when the file exists. */
void firc_kn_token_setup(const char *path, const char *ndmc);

/* Turns the token on for a release firc_kn_token_release_ok accepts; any other leaves it as it is. */
void firc_kn_token_set_release(const char *release);

/* The token to send now (read from the file once, else issued at most once a minute); empty when off or none.
   False, with out empty, when stop (NULL for none) ended the wait or the issue; an ended issue counts as failed. */
bool firc_kn_token_current(char out[FIRC_KN_TOKEN_MAX], const atomic_bool *stop);

/* After the router refused stale as a token: turns the token on, then gives one issued since, or a fresh one at
   most once a minute, else empty. False when stop ended it, as for firc_kn_token_current. */
bool firc_kn_token_refused(const char *stale, char out[FIRC_KN_TOKEN_MAX], const atomic_bool *stop);

/* True when a 401/403's X-Detail names a refused or missing token. */
bool firc_kn_token_refusal(long status, const char *detail);

/* Deletes every firc token on the router, then the file; asks no ndmc when the file is absent and release is
   known to be older than 5.02. A failure keeps the file. */
firc_err_t firc_kn_token_revoke(const char *path, const char *ndmc, const char *release);

#endif
