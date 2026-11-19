#ifndef FIRC_UTF8_H
#define FIRC_UTF8_H

#include <stdbool.h>

/* Whether s is well-formed UTF-8: no stray, overlong, surrogate or past-U+10FFFF sequence. */
bool firc_utf8_valid(const char *s);

/* Whether s holds a C0 control, DEL or a UTF-8 C1 control (U+0080..U+009F). */
bool firc_text_has_control(const char *s);

/* Longest prefix of s that is <= max_bytes and ends on a UTF-8 boundary. */
int firc_utf8_clamp(const char *s, int max_bytes);

#endif
