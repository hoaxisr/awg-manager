#ifndef FIRC_SUBPARSE_H
#define FIRC_SUBPARSE_H

#include <stdbool.h>
#include <stdint.h>

#include "firc/err.h"
#include "firc/models.h"

/* Detected type for a trimmed pattern; one of the FIRC_RULE_* constants or "" (never NULL). */
const char *firc_sub_detect_type(const char *pattern);

/* Like firc_sub_detect_type, but *oom (set false first) distinguishes "nothing fits" from an allocation failure. */
const char *firc_sub_detect_type_checked(const char *pattern, bool *oom);

/* Parses a raw list; every rule gets a random unique id and enable=true. */
firc_err_t firc_sub_parse_rules(const char *list, firc_sub_rules_t *out);

/* Like firc_sub_parse_rules, plus how many lines made nothing (out_dropped may be NULL). */
firc_err_t firc_sub_parse_rules_counted(const char *list, firc_sub_rules_t *out, size_t *out_dropped);

/* Called every FIRC_SUB_PARSE_PROGRESS_LINES lines; false aborts with FIRC_ERR_CANCELED. NULL: no reporting. */
typedef bool (*firc_sub_parse_progress_fn)(void *ud, size_t lines);
#define FIRC_SUB_PARSE_PROGRESS_LINES 20000

/* Like firc_sub_parse_rules_counted, with a progress callback. */
firc_err_t firc_sub_parse_rules_progress(const char *list, firc_sub_rules_t *out, size_t *out_dropped,
                                         firc_sub_parse_progress_fn progress, void *ud);

/* What a parse says besides its rules; why is set (to a static string) only on FIRC_ERR_INVAL. */
typedef struct firc_sub_parse_stats {
    size_t dropped;       /* lines, or sing-box objects and values, nothing was made of */
    size_t unconstrained; /* names taken without their object's network/port */
    const char *why;
} firc_sub_parse_stats_t;

/* Like firc_sub_parse_rules_progress over len bytes, plus sing-box stats; a sing-box rule-set, source or
   compiled, is read as one. */
firc_err_t firc_sub_parse_rules_stats(const char *list, size_t len, firc_sub_rules_t *out,
                                      firc_sub_parse_stats_t *st, firc_sub_parse_progress_fn progress, void *ud);

/* True when body is a compiled sing-box rule-set (.srs): binary, so a NUL in it is no reason to refuse it. */
bool firc_sub_body_is_srs(const char *body, size_t len);

/* Parses list, then carries id/enable/type from existing for rules with identical text. */
firc_err_t firc_sub_refresh_rules(const char *list, size_t len, const firc_sub_rules_t *existing,
                                  firc_sub_rules_t *out);

/* Like firc_sub_refresh_rules split out for pre-parsed rs; rs is mutated even on error, caller still owns it. */
firc_err_t firc_sub_rules_inherit(firc_sub_rules_t *rs, const firc_sub_rules_t *existing);

/* Order-insensitive multiset compare incl. id/enable; FIRC_ERR_NOMEM means "could not tell", not "changed". */
firc_err_t firc_sub_same_rules_checked(const firc_sub_rules_t *left, const firc_sub_rules_t *right,
                                       bool *same);

/* enable is the owner's; the rest is read from the list. */
bool firc_sub_is_due(bool enable, const firc_group_list_t *l, int64_t now_unix);

#endif /* FIRC_SUBPARSE_H */
