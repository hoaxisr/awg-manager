#ifndef FIRC_SUBINFO_H
#define FIRC_SUBINFO_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

typedef struct {
    bool have;
    uint64_t upload, download;
    uint64_t total;
    int64_t expire;
} firc_subinfo_t;

#define FIRC_SUBINFO_HEADER "subscription-userinfo"
#define FIRC_SUBINFO_LINE_MAX 160

/* Parses a header value; keys in any case and order, unknown ones skipped. False, out cleared, when it names none
   of the four or a number is not one. */
bool firc_subinfo_parse(const char *value, firc_subinfo_t *out);

/* The "#subscription-userinfo: ..." line among the comment lines that open body; false when there is none. */
bool firc_subinfo_from_body(const char *body, size_t len, firc_subinfo_t *out);

/* Writes info back as a header value, "upload=N; download=N; total=N; expire=N". */
void firc_subinfo_format(const firc_subinfo_t *info, char *buf, size_t cap);

#endif
