#include "firc/subhdr.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>

#include "firc/utf8.h"
#include "firc/version.h"

static const char *const REFUSED[] = {"Host", "Content-Length", "Transfer-Encoding", "Connection"};

static bool tchar(unsigned char c)
{
    return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
           (c != '\0' && strchr("!#$%&'*+-.^_`|~", c) != NULL);
}

bool firc_subhdr_name_ok(const char *name)
{
    size_t n = name != NULL ? strlen(name) : 0;
    if (n == 0 || n > FIRC_SUBHDR_NAME_MAX) {
        return false;
    }
    for (size_t i = 0; i < n; i++) {
        if (!tchar((unsigned char)name[i])) {
            return false;
        }
    }
    return true;
}

const char *firc_subhdr_check(const char *name, const char *value)
{
    if (!firc_subhdr_name_ok(name)) {
        return "a header name is 1..64 letters, digits or !#$%&'*+-.^_`|~";
    }
    for (size_t i = 0; i < sizeof REFUSED / sizeof REFUSED[0]; i++) {
        if (strcasecmp(name, REFUSED[i]) == 0) {
            return "set by the fetch itself, not by hand";
        }
    }
    if (value == NULL || strlen(value) > FIRC_SUBHDR_VALUE_MAX || !firc_utf8_valid(value) ||
        firc_text_has_control(value)) {
        return "a header value is up to 512 bytes without control characters";
    }
    return NULL;
}

static void add(firc_subhdr_set_t *s, const char *name, const char *value)
{
    if (s->n >= FIRC_SUBHDR_SET_MAX) {
        return;
    }
    snprintf(s->h[s->n].name, sizeof s->h[s->n].name, "%s", name);
    snprintf(s->h[s->n].value, sizeof s->h[s->n].value, "%s", value);
    s->n++;
}

static void put_default(firc_subhdr_set_t *s, const char *name, const char *value)
{
    if (value != NULL && value[0] != '\0' && firc_subhdr_check(name, value) == NULL) {
        add(s, name, value);
    }
}

void firc_subhdr_defaults(const char *hwid, const char *release, const char *model, firc_subhdr_set_t *out)
{
    out->n = 0;
    put_default(out, "User-Agent", "firc/" FIRC_VERSION);
    put_default(out, "X-HWID", hwid);
    put_default(out, "X-Device-OS", "KeeneticOS");
    put_default(out, "X-Ver-OS", release);
    put_default(out, "X-Device-Model", model);
}

static const firc_subhdr_pair_t *own_named(const firc_subhdr_pair_t *own, size_t n, const char *name)
{
    for (size_t i = 0; i < n; i++) {
        if (own[i].name != NULL && strcasecmp(own[i].name, name) == 0) {
            return &own[i];
        }
    }
    return NULL;
}

static bool in_set(const firc_subhdr_set_t *s, const char *name)
{
    for (size_t i = 0; i < s->n; i++) {
        if (strcasecmp(s->h[i].name, name) == 0) {
            return true;
        }
    }
    return false;
}

void firc_subhdr_merge(const firc_subhdr_set_t *defaults, const firc_subhdr_pair_t *own, size_t n_own,
                       firc_subhdr_set_t *out)
{
    out->n = 0;
    for (size_t i = 0; i < defaults->n; i++) {
        const firc_subhdr_pair_t *o = own_named(own, n_own, defaults->h[i].name);
        if (o == NULL) {
            add(out, defaults->h[i].name, defaults->h[i].value);
        } else if (o->value != NULL && o->value[0] != '\0') {
            add(out, o->name, o->value);
        }
    }
    for (size_t i = 0; i < n_own; i++) {
        if (own[i].name == NULL || own[i].value == NULL || own[i].value[0] == '\0' || in_set(defaults, own[i].name) ||
            in_set(out, own[i].name)) {
            continue;
        }
        add(out, own[i].name, own[i].value);
    }
}

void firc_subhdr_lines_free(char **lines)
{
    for (size_t i = 0; lines != NULL && lines[i] != NULL; i++) {
        free(lines[i]);
    }
    free(lines);
}

char **firc_subhdr_lines(const firc_subhdr_set_t *s)
{
    char **v = calloc(s->n + 2, sizeof *v);
    if (v == NULL) {
        return NULL;
    }
    bool agent = false;
    for (size_t i = 0; i < s->n; i++) {
        size_t len = strlen(s->h[i].name) + strlen(s->h[i].value) + 3;
        v[i] = malloc(len);
        if (v[i] == NULL) {
            firc_subhdr_lines_free(v);
            return NULL;
        }
        snprintf(v[i], len, "%s: %s", s->h[i].name, s->h[i].value);
        agent = agent || strcasecmp(s->h[i].name, "User-Agent") == 0;
    }
    if (!agent && (v[s->n] = strdup("User-Agent:")) == NULL) {
        firc_subhdr_lines_free(v);
        return NULL;
    }
    return v;
}

char **firc_subhdr_lines_copy(char *const *lines)
{
    size_t n = 0;
    while (lines != NULL && lines[n] != NULL) {
        n++;
    }
    char **v = calloc(n + 1, sizeof *v);
    for (size_t i = 0; v != NULL && i < n; i++) {
        if ((v[i] = strdup(lines[i])) == NULL) {
            firc_subhdr_lines_free(v);
            return NULL;
        }
    }
    return v;
}
