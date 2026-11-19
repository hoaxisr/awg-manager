/* Byte-faithful wildcard port; do not "fix" it -- parity with the differential corpus is the contract. */
#include "firc/match.h"

#include <string.h>

bool firc_wildcard_match(const char *pattern, const char *s)
{
    size_t pattern_len = strlen(pattern);
    size_t s_len = strlen(s);

    if (pattern_len == 0) {
        return s_len == 0;
    }
    if (strcmp(pattern, "*") == 0 || strcmp(pattern, s) == 0) {
        return true;
    }

    char last_eroteme_cluster = 0;
    size_t pattern_index = 0;
    size_t s_index = 0;
    size_t last_star = 0;
    size_t last_eroteme = 0;
    /* -1 sentinels replaced by has_* flags (sizes are unsigned here) */
    bool has_star = false;
    size_t star = 0;
    bool has_eroteme = false;
    size_t eroteme = 0;

    for (;;) {
        if (s_index >= s_len) {
            goto check_pattern;
        }
        if (pattern_index >= pattern_len) {
            if (has_star) {
                pattern_index = star + 1;
                last_star++;
                s_index = last_star;
                continue;
            }
            return false;
        }

        switch (pattern[pattern_index]) {
        case '.':
            /* matches any single byte */
            break;
        case '?':
            has_eroteme = true;
            eroteme = pattern_index;
            last_eroteme = s_index;
            last_eroteme_cluster = s[s_index];
            break;
        case '*':
            has_star = true;
            star = pattern_index;
            last_star = s_index;
            pattern_index++;
            continue;
        default:
            if (pattern[pattern_index] != s[s_index]) {
                if (has_eroteme) {
                    pattern_index = eroteme + 1;
                    s_index = last_eroteme;
                    has_eroteme = false;
                    continue;
                }
                if (has_star) {
                    pattern_index = star + 1;
                    last_star++;
                    s_index = last_star;
                    continue;
                }
                return false;
            }
            if (has_eroteme && last_eroteme_cluster != s[s_index]) {
                has_eroteme = false;
            }
            break;
        }

        pattern_index++;
        s_index++;
    }

check_pattern:
    while (pattern_index < pattern_len) {
        if (pattern[pattern_index] == '*') {
            pattern_index++;
        } else if (pattern[pattern_index] == '?') {
            if (s_index >= s_len && s_index > 0) {
                s_index--;
            }
            pattern_index++;
        } else {
            break;
        }
    }
    return pattern_index == pattern_len;
}
