#include <stdbool.h>
#include <stdio.h>
#include <string.h>
#include <strings.h>

#include "firc/models.h"
#include "subparse_internal.h"

typedef struct typed_name {
    const char *name;
    const char *type;
} typed_name_t;

static const typed_name_t k_typed[] = {
    {"DOMAIN", FIRC_RULE_DOMAIN},         {"HOST", FIRC_RULE_DOMAIN},
    {"DOMAIN-SUFFIX", FIRC_RULE_NAMESPACE}, {"HOST-SUFFIX", FIRC_RULE_NAMESPACE},
    {"DOMAIN-KEYWORD", FIRC_RULE_WILDCARD}, {"HOST-KEYWORD", FIRC_RULE_WILDCARD},
    {"DOMAIN-REGEX", FIRC_RULE_REGEX},    {"IP-CIDR", FIRC_RULE_SUBNET},
    {"IP-CIDR6", FIRC_RULE_SUBNET},       {"IP6-CIDR", FIRC_RULE_SUBNET},
};

static const char *const k_unmapped[] = {
    "DOMAIN-WILDCARD", "GEOSITE", "GEOIP", "SRC-GEOIP", "IP-ASN", "SRC-IP-ASN", "IP-SUFFIX",
    "SRC-IP-CIDR", "SRC-IP-SUFFIX", "SRC-PORT", "DST-PORT", "IN-PORT", "IN-TYPE", "IN-USER",
    "IN-NAME", "REMATCH-NAME", "PROCESS-NAME", "PROCESS-PATH", "PROCESS-NAME-REGEX",
    "PROCESS-PATH-REGEX", "PROCESS-NAME-WILDCARD", "PROCESS-PATH-WILDCARD", "NETWORK", "UID",
    "DSCP", "RULE-SET", "SUB-RULE", "AND", "OR", "NOT", "MATCH",
};

bool firc_sub_keyword_wildcard(const char *kw, char *buf, size_t cap) {
    if (strpbrk(kw, "*?") != NULL) { return false; }
    int n = snprintf(buf, cap, "*%s*", kw);
    return n >= 0 && (size_t)n < cap;
}

static bool name_is(const char *s, size_t len, const char *name) {
    return strlen(name) == len && strncasecmp(s, name, len) == 0;
}

static const char *typed_type(const char *s, size_t len, bool *known) {
    *known = true;
    for (size_t i = 0; i < sizeof(k_typed) / sizeof(k_typed[0]); i++) {
        if (name_is(s, len, k_typed[i].name)) { return k_typed[i].type; }
    }
    for (size_t i = 0; i < sizeof(k_unmapped) / sizeof(k_unmapped[0]); i++) {
        if (name_is(s, len, k_unmapped[i])) { return NULL; }
    }
    *known = false;
    return NULL;
}

static bool tail_ok(char *tail, bool ip) {
    bool label = false;
    while (tail != NULL) {
        char *next = strchr(tail, ',');
        if (next != NULL) { *next++ = '\0'; }
        char *f = firc_sub_trim(tail);
        if (*f == '\0' || strcasecmp(f, "src") == 0) { return false; }
        if (strcasecmp(f, "no-resolve") == 0) {
            if (!ip || next != NULL) { return false; }
        } else if (label) {
            return false;
        } else {
            label = true;
        }
        tail = next;
    }
    return true;
}

firc_err_t firc_sub_typed_line(firc_sub_sink_t *s, char *raw, bool *typed, bool *took) {
    *typed = false;
    *took = false;
    char *line = raw;
    while (*line == ' ' || *line == '\t') { line++; }
    char *comma = strchr(line, ',');
    if (comma == NULL) {
        if (line[0] == '+' && line[1] == '.') {
            line += 2;
        } else if (line[0] == '.') {
            line += 1;
        } else {
            return FIRC_OK;
        }
        *typed = true;
        return firc_sub_sink_take(s, firc_sub_trim(line), FIRC_RULE_NAMESPACE, NULL, NULL, took);
    }
    size_t len = (size_t)(comma - line);
    while (len > 0 && (line[len - 1] == ' ' || line[len - 1] == '\t')) { len--; }
    bool known = false;
    const char *type = typed_type(line, len, &known);
    if (!known) { return FIRC_OK; }
    *typed = true;
    if (type == NULL) { return FIRC_OK; }
    char *value = comma + 1;
    bool ip = strcmp(type, FIRC_RULE_SUBNET) == 0;
    if (strcmp(type, FIRC_RULE_REGEX) != 0) {
        char *tail = strchr(value, ',');
        if (tail != NULL) { *tail++ = '\0'; }
        if (!tail_ok(tail, ip)) { return FIRC_OK; }
    }
    value = firc_sub_trim(value);
    if (*value == '\0') { return FIRC_OK; }
    if (ip && strchr(value, ':') != NULL) { type = FIRC_RULE_SUBNET6; }
    if (strcmp(type, FIRC_RULE_NAMESPACE) == 0 && value[0] == '.') { value++; }
    char buf[300];
    if (strcmp(type, FIRC_RULE_WILDCARD) == 0) {
        if (!firc_sub_keyword_wildcard(value, buf, sizeof(buf))) { return FIRC_OK; }
        value = buf;
    }
    return firc_sub_sink_take(s, value, type, NULL, NULL, took);
}
