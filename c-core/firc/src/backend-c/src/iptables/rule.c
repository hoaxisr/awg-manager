#include "firc/iptables.h"

#include <stdlib.h>
#include <string.h>

static void free_parts(char **parts, size_t n) {
    if (!parts) { return; }
    for (size_t i = 0; i < n; i++) { free(parts[i]); }
    free(parts);
}

firc_ipt_rule_t *firc_ipt_rule_new(const char *const *args, size_t n_args) {
    firc_ipt_rule_t *r = calloc(1, sizeof(*r));
    if (!r) { return NULL; }

    if (n_args == 0) {
        r->parts = NULL;
        r->n_parts = 0;
        return r;
    }

    r->parts = calloc(n_args, sizeof(char *));
    if (!r->parts) {
        free(r);
        return NULL;
    }
    for (size_t i = 0; i < n_args; i++) {
        r->parts[i] = strdup(args[i] ? args[i] : "");
        if (!r->parts[i]) {
            free_parts(r->parts, i);
            free(r);
            return NULL;
        }
        r->n_parts++;
    }
    return r;
}

firc_ipt_rule_t *firc_ipt_rule_clone(const firc_ipt_rule_t *r) {
    if (!r) { return NULL; }
    return firc_ipt_rule_new((const char *const *)r->parts, r->n_parts);
}

void firc_ipt_rule_free(firc_ipt_rule_t *r) {
    if (!r) { return; }
    free_parts(r->parts, r->n_parts);
    free(r);
}

bool firc_ipt_rule_equal(const firc_ipt_rule_t *a, const firc_ipt_rule_t *b) {
    if (a == b) { return true; }
    if (!a || !b) { return false; }
    if (a->n_parts != b->n_parts) { return false; }
    for (size_t i = 0; i < a->n_parts; i++) {
        if (strcmp(a->parts[i], b->parts[i]) != 0) { return false; }
    }
    return true;
}

char *firc_ipt_rule_string(const firc_ipt_rule_t *r) {
    if (!r || r->n_parts == 0) { return strdup(""); }

    size_t total = 0;
    for (size_t i = 0; i < r->n_parts; i++) {
        total += strlen(r->parts[i]);
        if (i > 0) { total += 1; }
    }

    char *out = malloc(total + 1);
    if (!out) { return NULL; }

    size_t off = 0;
    for (size_t i = 0; i < r->n_parts; i++) {
        if (i > 0) { out[off++] = ' '; }
        size_t len = strlen(r->parts[i]);
        memcpy(out + off, r->parts[i], len);
        off += len;
    }
    out[off] = '\0';
    return out;
}
