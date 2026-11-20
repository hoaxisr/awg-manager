#ifndef FIRC_MATCH_H
#define FIRC_MATCH_H

#include <stdbool.h>

#include "firc/err.h"
#include "firc/models.h"
#include "firc/subnet.h"

typedef struct firc_rule_matcher firc_rule_matcher_t;

/* Compiles one rule; invalid regex yields a matcher that never matches (firc_rule_matcher_ok says so). */
firc_rule_matcher_t *firc_rule_matcher_new(const char *type, const char *rule);
void firc_rule_matcher_free(firc_rule_matcher_t *m);
bool firc_rule_matcher_ok(const firc_rule_matcher_t *m); /* regex compiled? */
bool firc_rule_matcher_match(firc_rule_matcher_t *m, const char *domain);


/* Compared by POINTER, not text: the reason firc_rule_is_usable reports when an allocation failed. */
extern const char firc_rule_why_nomem[];

/* True if a rule of type/pattern can ever match; false with *why (a static string) when it cannot. */
bool firc_rule_is_usable(const char *type, const char *rule, const char **why);

/* Name-rule half of firc_rule_is_usable; allow_glob permits '*'/'?'; why is optional. */
bool firc_rule_name_pattern_is_usable(const char *rule, bool allow_glob, const char **why);

/* Subnet/subnet6 parsers; a bare address is a host prefix, /0 stays /0. */
bool firc_rule_parse_subnet4(const char *rule, firc_ipv4_subnet_t *out);
bool firc_rule_parse_subnet6(const char *rule, firc_ipv6_subnet_t *out);

/* proto NULL/tcp/udp; ports N or N-M comma list, <=FIRC_PORTS_MAX_SLOTS slots; out is iptables form. */
bool firc_rule_parse_proto(const char *proto, uint8_t *out);
bool firc_rule_parse_ports(const char *ports, char *out, size_t cap);

/* firc_rule_is_usable plus the proto/ports check (subnet/subnet6 only; ports need a proto). */
bool firc_rule_spec_is_usable(const char *type, const char *rule, const char *proto,
                              const char *ports, const char **why);

/* Folds a name pattern in place (ASCII lower, no surrounding blanks, no trailing dot); not for regexes. */
size_t firc_rule_fold_name(char *p);

bool firc_wildcard_match(const char *pattern, const char *s);

typedef struct firc_matcher firc_matcher_t;

/* Copies every name it is given. */
firc_matcher_t *firc_matcher_new(void);
void firc_matcher_free(firc_matcher_t *m);

/* Adds an enabled rule to the index. Skips disabled rules at the caller. */
firc_err_t firc_matcher_add(firc_matcher_t *m, const char *type, const char *rule);

/* Keeps a pointer to domain/namespace text instead of copying; text must outlive the matcher unchanged. */
firc_err_t firc_matcher_add_borrowed(firc_matcher_t *m, const char *type, const char *rule);

bool firc_matcher_match(firc_matcher_t *m, const char *domain);

#endif /* FIRC_MATCH_H */
