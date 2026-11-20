#ifndef FIRC_IFACENAME_H
#define FIRC_IFACENAME_H

#include <stdbool.h>
#include <stddef.h>

/* True if s is a valid kernel interface name. */
bool firc_is_interface_name(const char *s);

/* Copy of s safe for a log line: non-printable bytes become '?', truncated to cap-1; always terminates. */
void firc_interface_name_for_log(const char *s, char *out, size_t cap);

#endif
