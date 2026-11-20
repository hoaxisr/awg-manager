#ifndef FIRC_SUBHDR_H
#define FIRC_SUBHDR_H

#include <stdbool.h>
#include <stddef.h>

#define FIRC_SUBHDR_OWN_MAX 16
#define FIRC_SUBHDR_NAME_MAX 64
#define FIRC_SUBHDR_VALUE_MAX 512
#define FIRC_SUBHDR_SET_MAX (FIRC_SUBHDR_OWN_MAX + 5)

typedef struct {
    char *name;
    char *value;
} firc_subhdr_pair_t;

typedef struct {
    char name[FIRC_SUBHDR_NAME_MAX + 1];
    char value[FIRC_SUBHDR_VALUE_MAX + 1];
} firc_subhdr_t;

typedef struct {
    firc_subhdr_t h[FIRC_SUBHDR_SET_MAX];
    size_t n;
} firc_subhdr_set_t;

/* True for an HTTP token of 1..64 bytes. */
bool firc_subhdr_name_ok(const char *name);

/* NULL when name: value may be written by hand (an empty value included), else why not; never quotes the value. */
const char *firc_subhdr_check(const char *name, const char *value);

/* User-Agent, X-HWID, X-Device-OS, X-Ver-OS, X-Device-Model in that order; a NULL, empty or unsendable value is
   left out. */
void firc_subhdr_defaults(const char *hwid, const char *release, const char *model, firc_subhdr_set_t *out);

/* defaults with own on top by name, case-insensitive: a value replaces in place, an empty one leaves the header out,
   the other own headers follow in order. out must not be defaults. */
void firc_subhdr_merge(const firc_subhdr_set_t *defaults, const firc_subhdr_pair_t *own, size_t n_own,
                       firc_subhdr_set_t *out);

/* NULL-terminated "Name: value" lines, plus "User-Agent:" when s has none; NULL on no memory. */
char **firc_subhdr_lines(const firc_subhdr_set_t *s);

/* A copy of a NULL-terminated line list; NULL on no memory. */
char **firc_subhdr_lines_copy(char *const *lines);

/* NULL-safe. */
void firc_subhdr_lines_free(char **lines);

#endif
