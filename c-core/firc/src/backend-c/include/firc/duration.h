#ifndef FIRC_DURATION_H
#define FIRC_DURATION_H

#include <stddef.h>
#include <stdint.h>

#include "firc/err.h"

typedef int64_t firc_duration_t; /* nanoseconds */

#define FIRC_DURATION_MS  INT64_C(1000000)
#define FIRC_DURATION_SEC INT64_C(1000000000)

/* Parses "300ms"/"1.5h"/"2h45m"/"-5s"/"1h0m0s" (units ns/us/µs/ms/s/m/h); FIRC_ERR_INVAL if invalid. */
firc_err_t firc_duration_parse(const char *s, firc_duration_t *out);

/* Formats like "5s"/"1h0m0s"; buf must hold at least 32 bytes + sign. */
void firc_duration_format(firc_duration_t d, char *buf, size_t buf_len);

#endif /* FIRC_DURATION_H */
