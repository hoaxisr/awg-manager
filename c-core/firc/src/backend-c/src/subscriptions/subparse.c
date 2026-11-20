#define PCRE2_CODE_UNIT_WIDTH 8

#include "firc/subparse.h"

#include "subparse_internal.h"

#include "firc/match.h"

#include <ctype.h>
#include <stdio.h>
#include <pcre2.h>
#include <stdlib.h>
#include <string.h>

static int parse_dec(const char *s, size_t len)
{
    if (len == 0) {
        return -1;
    }
    int n = 0;
    for (size_t i = 0; i < len; i++) {
        if (s[i] < '0' || s[i] > '9') {
            return -1;
        }
        int d = s[i] - '0';
        if (n > (999999999 - d) / 10) {
            return -1;
        }
        n = n * 10 + d;
    }
    return n;
}

/* ^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})(?:\/(\d{1,2}))?$ with octets 0..255 and prefix 0..32 */
static bool is_valid_subnet(const char *p)
{
    const char *s = p;
    for (int octet = 0; octet < 4; octet++) {
        const char *start = s;
        while (*s >= '0' && *s <= '9') {
            s++;
        }
        size_t dlen = (size_t)(s - start);
        if (dlen < 1 || dlen > 3) {
            return false;
        }
        int v = parse_dec(start, dlen);
        if (v < 0 || v > 255) {
            return false;
        }
        if (octet < 3) {
            if (*s != '.') {
                return false;
            }
            s++;
        }
    }
    if (*s == '/') {
        s++;
        const char *start = s;
        while (*s >= '0' && *s <= '9') {
            s++;
        }
        size_t dlen = (size_t)(s - start);
        if (dlen < 1 || dlen > 2) {
            return false;
        }
        int v = parse_dec(start, dlen);
        if (v < 0 || v > 32) {
            return false;
        }
    }
    return *s == '\0';
}

static bool is_valid_ipv6_chars(const char *ip)
{
    if (strchr(ip, ':') == NULL) {
        return false;
    }
    for (const char *p = ip; *p != '\0'; p++) {
        char c = *p;
        if ((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') ||
            (c >= 'A' && c <= 'F') || c == ':') {
            continue;
        }
        return false;
    }
    return true;
}

static bool is_valid_subnet6(const char *p)
{
    const char *slash = strchr(p, '/');
    if (slash == NULL) {
        return is_valid_ipv6_chars(p);
    }
    if (strchr(slash + 1, '/') != NULL) {
        return false;
    }
    int prefix = parse_dec(slash + 1, strlen(slash + 1));
    if (prefix < 0 || prefix > 128) {
        return false;
    }
    char head[256];
    size_t head_len = (size_t)(slash - p);
    if (head_len >= sizeof(head)) {
        return false;
    }
    memcpy(head, p, head_len);
    head[head_len] = '\0';
    return is_valid_ipv6_chars(head);
}

static bool is_valid_regex_checked(const char *p, bool *oom)
{
    int errcode = 0;
    PCRE2_SIZE erroff = 0;
    pcre2_code *code =
        pcre2_compile((PCRE2_SPTR)p, PCRE2_ZERO_TERMINATED,
                      PCRE2_UTF | PCRE2_UCP, &errcode, &erroff, NULL);
    if (code == NULL) {
        /* compile failure and OOM share the same NULL; NOMEMORY/HEAPLIMIT are the machine's fault */
        if (errcode == PCRE2_ERROR_NOMEMORY || errcode == PCRE2_ERROR_HEAPLIMIT) { *oom = true; }
        return false;
    }
    pcre2_code_free(code);
    return true;
}

/* tests the actual property (matches strings no rule may); a syntactic proxy is always beatable */
static bool matches_things_no_rule_may_match(const char *p, bool *oom)
{
    /* one control per label depth 1-6: a catch-all varies on depth, and sampling breadth always loses */
    static const char *const controls[] = {
        "",
        "a",
        "qx7vnb2mklda",
        "qx7vnb2mkl.example",
        "qx7vnb2mkl.example.invalid",
        "qx7vnb2mkl.example.invalid.test",
        "qx7vnb2mkl.example.invalid.test.local",
        "qx7vnb2mkl.example.invalid.test.local.onion",
        "totally-unrelated.example",
    };
    firc_rule_matcher_t *m = firc_rule_matcher_new(FIRC_RULE_REGEX, p);
    if (m == NULL) {
        /* not "matches everything" -- "could not tell"; read as a refusal it drops a good rule and miscounts it */
        *oom = true;
        return true;
    }
    bool bad = false;
    for (size_t i = 0; i < sizeof(controls) / sizeof(controls[0]) && !bad; i++) {
        if (firc_rule_matcher_match(m, controls[i])) { bad = true; }
    }
    firc_rule_matcher_free(m);
    return bad;
}

/* anchors are a decent signal of "written as a regex" but not a safety property; that's the function above's job */
static bool written_as_a_regex(const char *p)
{
    size_t n = strlen(p);
    bool head = p[0] == '^' || (n >= 2 && p[0] == '\\' && p[1] == 'A');
    bool tail = (n >= 1 && p[n - 1] == '$') ||
                (n >= 2 && p[n - 2] == '\\' && (p[n - 1] == 'z' || p[n - 1] == 'Z'));
    return n >= 2 && head && tail;
}

/* crude substring test for "(*": a PCRE2-aware scanner missed (?#...) / \Q...\E and let a verb through */
static bool has_a_control_verb(const char *p)
{
    return strstr(p, "(*") != NULL;
}

static char *anchored_copy(const char *p)
{
    size_t n = strlen(p) + 9; /* \A(?: + ... + )\z + NUL */
    char *out = malloc(n);
    if (out != NULL) {
        snprintf(out, n, "\\A(?:%s)\\z", p);
    }
    return out;
}

const char *firc_sub_detect_type(const char *pattern)
{
    bool ignored = false;
    return firc_sub_detect_type_checked(pattern, &ignored);
}

const char *firc_sub_detect_type_checked(const char *pattern, bool *oom)
{
    *oom = false;
    /* callers already trim; re-trimming here is harmless */
    if (is_valid_subnet6(pattern)) {
        return FIRC_RULE_SUBNET6;
    }
    if (is_valid_subnet(pattern)) {
        return FIRC_RULE_SUBNET;
    }
    /* the daemon's own name grammar, not a separate one -- a looser one let valid names fall into an unanchored regex */
    if (firc_rule_name_pattern_is_usable(pattern, false, NULL)) {
        /* namespace and domain share a validator; namespace is checked first, so a plain domain detects as namespace */
        return FIRC_RULE_NAMESPACE;
    }
    /* checked before regex: `a*.example.com` compiles as a regex but is plainly meant as a wildcard */
    if (firc_rule_name_pattern_is_usable(pattern, true, NULL)) {
        return FIRC_RULE_WILDCARD;
    }
    if (written_as_a_regex(pattern) && !has_a_control_verb(pattern) &&
        is_valid_regex_checked(pattern, oom) && !matches_things_no_rule_may_match(pattern, oom)) {
        return FIRC_RULE_REGEX;
    }
    /* refused rather than guessed; the caller drops it (an earlier version stored it anyway with no type) */
    return "";
}

/* one open-addressing set of indices into firc_sub_rules_t, replacing three hash tables that copied every key */
static uint32_t fnv1a_n(const char *s, size_t n)
{
    uint32_t h = 2166136261u;
    for (size_t i = 0; i < n; i++) {
        h = (h ^ (uint8_t)s[i]) * 16777619u;
    }
    return h;
}

#define IDX_EMPTY UINT32_MAX

typedef struct idx_slot {
    uint32_t hash;
    uint32_t idx;   /* into the caller's firc_sub_rules_t; IDX_EMPTY when free */
    uint32_t count; /* the compare's multiset; unused by the others */
} idx_slot_t;

typedef struct idx_set {
    idx_slot_t *slots;
    size_t cap; /* power of two, or 0 */
    size_t len;
} idx_set_t;

/* a lookup key; fields the caller doesn't key on are left zero and ignored by eq; NULL and "" are one spec */
typedef struct rule_key {
    const char *text;
    size_t len;
    firc_id_t id;
    uint8_t type;
    bool enable;
    const char *proto;
    const char *ports;
} rule_key_t;

/* *oom is set when the comparison itself couldn't be made, never reported as "not equal" */
typedef bool (*idx_eq_fn)(const firc_sub_rules_t *rs, uint32_t idx, const rule_key_t *key, bool *oom);

static void idx_set_free(idx_set_t *t)
{
    free(t->slots);
    t->slots = NULL;
    t->cap = 0;
    t->len = 0;
}

static firc_err_t idx_set_grow(idx_set_t *t)
{
    size_t cap = t->cap == 0 ? 64 : t->cap * 2;
    idx_slot_t *fresh = malloc(cap * sizeof(*fresh));
    if (fresh == NULL) { return FIRC_ERR_NOMEM; }
    for (size_t i = 0; i < cap; i++) { fresh[i].idx = IDX_EMPTY; }
    for (size_t i = 0; i < t->cap; i++) {
        if (t->slots[i].idx == IDX_EMPTY) { continue; }
        size_t at = t->slots[i].hash & (cap - 1);
        while (fresh[at].idx != IDX_EMPTY) { at = (at + 1) & (cap - 1); }
        fresh[at] = t->slots[i];
    }
    free(t->slots);
    t->slots = fresh;
    t->cap = cap;
    return FIRC_OK;
}

/* sized for n entries up front, so a pass over a known-length list never rehashes */
static firc_err_t idx_set_reserve(idx_set_t *t, size_t n)
{
    while (t->cap * 3 < n * 4 + 4) {
        firc_err_t err = idx_set_grow(t);
        if (err != FIRC_OK) { return err; }
    }
    return FIRC_OK;
}

static idx_slot_t *idx_set_find(const idx_set_t *t, uint32_t h, const firc_sub_rules_t *rs,
                                idx_eq_fn eq, const rule_key_t *key, bool *oom)
{
    if (t->cap == 0) { return NULL; }
    size_t at = h & (t->cap - 1);
    while (t->slots[at].idx != IDX_EMPTY) {
        if (t->slots[at].hash == h && eq(rs, t->slots[at].idx, key, oom)) { return &t->slots[at]; }
        if (oom != NULL && *oom) { return NULL; }
        at = (at + 1) & (t->cap - 1);
    }
    return NULL;
}

/* Inserts without looking for a duplicate: the caller has just looked. */
static firc_err_t idx_set_insert(idx_set_t *t, uint32_t h, uint32_t idx, idx_slot_t **out_slot)
{
    if (t->len * 4 >= t->cap * 3) {
        firc_err_t err = idx_set_grow(t);
        if (err != FIRC_OK) { return err; }
    }
    size_t at = h & (t->cap - 1);
    while (t->slots[at].idx != IDX_EMPTY) { at = (at + 1) & (t->cap - 1); }
    t->slots[at].hash = h;
    t->slots[at].idx = idx;
    t->slots[at].count = 0;
    t->len++;
    if (out_slot != NULL) { *out_slot = &t->slots[at]; }
    return FIRC_OK;
}

static uint32_t type_salt(uint8_t type)
{
    return (uint32_t)type * 0x9E3779B9u;
}

/* NULL and "" are one spec; not folded into any hash, only the eq functions compare it */
static bool spec_eq(const char *a, const char *b) { return strcmp(a ? a : "", b ? b : "") == 0; }

/* The dedup's key: the type, the text, and the spec. */
static bool eq_type_text(const firc_sub_rules_t *rs, uint32_t idx, const rule_key_t *key, bool *oom)
{
    (void)oom;
    const char *t = firc_sub_rules_text(rs, idx);
    return rs->v[idx].type == key->type && strncmp(t, key->text, key->len) == 0 && t[key->len] == '\0' &&
           spec_eq(firc_sub_rules_proto(rs, idx), key->proto) &&
           spec_eq(firc_sub_rules_ports(rs, idx), key->ports);
}

/* the refresh's key: list_type + text + spec, so a text repeating under two list types keeps its own entry */
static bool eq_text(const firc_sub_rules_t *rs, uint32_t idx, const rule_key_t *key, bool *oom)
{
    (void)oom;
    const char *t = firc_sub_rules_text(rs, idx);
    return rs->v[idx].list_type == key->type && strncmp(t, key->text, key->len) == 0 &&
           t[key->len] == '\0' && spec_eq(firc_sub_rules_proto(rs, idx), key->proto) &&
           spec_eq(firc_sub_rules_ports(rs, idx), key->ports);
}

/* ASCII whitespace only; unicode spaces aren't expected in subscription lists */
char *firc_sub_trim(char *s)
{
    while (*s != '\0' && isspace((unsigned char)*s)) {
        s++;
    }
    size_t len = strlen(s);
    while (len > 0 && isspace((unsigned char)s[len - 1])) {
        s[--len] = '\0';
    }
    return s;
}

static firc_err_t next_unique_id(firc_id_set_t *used, firc_id_pool_t *pool, firc_id_t *out)
{
    for (;;) {
        firc_id_t candidate;
        firc_err_t err = firc_id_pool_next(pool, &candidate);
        if (err != FIRC_OK) { return err; }
        if (!firc_id_set_contains(used, candidate)) {
            *out = candidate;
            return firc_id_set_add(used, candidate);
        }
    }
}

/* everything one piece of a list needs to become a rule; shared with the sing-box parser */
struct firc_sub_sink {
    firc_sub_rules_t *out;
    idx_set_t *seen;
    firc_id_set_t *used;
    firc_id_pool_t *ids;
};

firc_err_t firc_sub_sink_take(firc_sub_sink_t *s, char *piece, const char *type, const char *proto,
                              const char *ports, bool *took)
{
    *took = false;
    /* only a guessed line (type==NULL) gets trim-and-skip-# treatment; a typed empty value is a drop, not a no-op */
    char *line = piece;
    if (type == NULL) {
        line = firc_sub_trim(piece);
        if (*line == '\0' || line[0] == '#') {
            *took = true; /* nothing to take and nothing wrong: not a drop */
            return FIRC_OK;
        }
    } else if (*line == '\0') {
        return FIRC_OK; /* *took stays false: a drop */
    }
    /* three outcomes: rule, not-a-rule, or couldn't-tell; reading OOM as "not a rule" drops a good rule and miscounts it */
    char *wrapped = NULL;
    const char *t = type; /* the given type, or the guess below; never NULL after this block */
    if (t == NULL) {
        bool oom = false;
        t = firc_sub_detect_type_checked(line, &oom);
        if (oom) { return FIRC_ERR_NOMEM; }
        if (strcmp(t, FIRC_RULE_REGEX) == 0) {
            if ((wrapped = anchored_copy(line)) == NULL) { return FIRC_ERR_NOMEM; }
        } else {
            /* folded so the matcher can read stored text directly; a subnet is folded too since overrides key on it */
            firc_rule_fold_name(line);
        }
    } else if (strcmp(t, FIRC_RULE_REGEX) != 0) {
        /* a type the list named is taken, not guessed -- the guesser calls every plain name a namespace */
        firc_rule_fold_name(line);
    }
    const char *text = wrapped != NULL ? wrapped : line;
    /* typed isn't usable by itself (looser checks here); re-checked on the WRAPPED text, since the wrap isn't total */
    if (t[0] != '\0') {
        const char *why = NULL;
        if (!firc_rule_spec_is_usable(t, text, proto, ports, &why)) {
            if (why == firc_rule_why_nomem) {
                free(wrapped);
                return FIRC_ERR_NOMEM;
            }
            t = "";
        }
    }
    if (t[0] == '\0') {
        free(wrapped);
        return FIRC_OK; /* took nothing; the caller decides if that is a drop */
    }
    rule_key_t key = {.text = text, .len = strlen(text), .type = firc_rule_type_index(t),
                      .proto = proto, .ports = ports};
    uint32_t h = fnv1a_n(text, key.len) ^ type_salt(key.type);
    firc_err_t err = FIRC_OK;
    if (idx_set_find(s->seen, h, s->out, eq_type_text, &key, NULL) != NULL) {
        *took = true;
    } else {
        firc_id_t id;
        err = next_unique_id(s->used, s->ids, &id);
        if (err == FIRC_OK) { err = firc_sub_rules_push_spec(s->out, text, t, true, id, proto, ports); }
        if (err == FIRC_OK) { err = idx_set_insert(s->seen, h, (uint32_t)(s->out->n - 1), NULL); }
        if (err == FIRC_OK) { *took = true; }
    }
    free(wrapped);
    return err;
}

firc_err_t firc_sub_parse_rules(const char *list, firc_sub_rules_t *out)
{
    return firc_sub_parse_rules_counted(list, out, NULL);
}

firc_err_t firc_sub_parse_rules_counted(const char *list, firc_sub_rules_t *out, size_t *out_dropped)
{
    return firc_sub_parse_rules_progress(list, out, out_dropped, NULL, NULL);
}

/* cuts copy on '\n'/'\r', and a non-whole-rule line further on ','; mutates copy in place (strtok_r), caller owns it */
static firc_err_t parse_lines(char *copy, firc_sub_sink_t *sink, size_t *dropped,
                              firc_sub_parse_progress_fn progress, void *ud)
{
    size_t lines = 0;
    firc_err_t err = FIRC_OK;

    /* lines first, commas second: a derived regex can carry its own comma ({1,3}), so only a non-whole line is cut */
    char *lsave = NULL;
    for (char *raw = strtok_r(copy, "\n\r", &lsave); raw != NULL;
         raw = strtok_r(NULL, "\n\r", &lsave)) {
        bool took = false;
        bool typed = false;
        if ((err = firc_sub_typed_line(sink, raw, &typed, &took)) != FIRC_OK) { return err; }
        if (!typed && (err = firc_sub_sink_take(sink, raw, NULL, NULL, NULL, &took)) != FIRC_OK) { return err; }
        if (!took) {
            if (typed || strchr(raw, ',') == NULL) {
                /* dropped and counted per LINE, not per comma-token (a 3-line list could otherwise report 4 drops) */
                (*dropped)++;
            } else {
                bool any = false;
                char *csave = NULL;
                for (char *piece = strtok_r(raw, ",", &csave); piece != NULL;
                     piece = strtok_r(NULL, ",", &csave)) {
                    bool one = false;
                    if ((err = firc_sub_sink_take(sink, piece, NULL, NULL, NULL, &one)) != FIRC_OK) {
                        return err;
                    }
                    if (one) { any = true; }
                }
                if (!any) { (*dropped)++; }
            }
        }
        lines++;
        if (progress != NULL && (lines % FIRC_SUB_PARSE_PROGRESS_LINES) == 0 && !progress(ud, lines)) {
            return FIRC_ERR_CANCELED;
        }
    }
    return FIRC_OK;
}

firc_err_t firc_sub_parse_rules_stats(const char *list, size_t len, firc_sub_rules_t *out,
                                      firc_sub_parse_stats_t *st, firc_sub_parse_progress_fn progress, void *ud)
{
    memset(st, 0, sizeof(*st));
    bool srs = firc_sub_body_is_srs(list, len);
    const char *p = list;
    if ((unsigned char)p[0] == 0xEF && (unsigned char)p[1] == 0xBB && (unsigned char)p[2] == 0xBF) { p += 3; }
    while (*p != '\0' && isspace((unsigned char)*p)) { p++; }
    /* no list line starts with '<': refused as an HTML page rather than silently parsed as empty */
    if (*p == '<') {
        st->why = "got an HTML page, not a list; for GitHub use the raw file URL";
        return FIRC_ERR_INVAL;
    }

    firc_sub_rules_t rs;
    firc_sub_rules_init(&rs);
    idx_set_t seen = {NULL, 0, 0};
    firc_id_set_t used = {NULL, 0, 0};
    firc_id_pool_t ids = {0};
    firc_sub_sink_t sink = {&rs, &seen, &used, &ids};

    firc_err_t err;
    if (srs) {
        err = firc_sub_parse_srs(list, len, &sink, st, progress, ud);
    } else if (*p == '{') {
        err = firc_sub_parse_singbox(p, &sink, st, progress, ud);
    } else {
        char *copy = strdup(p);
        err = copy != NULL ? parse_lines(copy, &sink, &st->dropped, progress, ud) : FIRC_ERR_NOMEM;
        free(copy);
    }

    idx_set_free(&seen);
    firc_id_set_free(&used);
    if (err != FIRC_OK) {
        firc_sub_rules_free(&rs);
        return err;
    }
    /* The arena is final from here: the matcher will borrow from it. */
    firc_sub_rules_shrink(&rs);
    firc_sub_rules_move(out, &rs);
    return FIRC_OK;
}

firc_err_t firc_sub_parse_rules_progress(const char *list, firc_sub_rules_t *out, size_t *out_dropped,
                                         firc_sub_parse_progress_fn progress, void *ud)
{
    firc_sub_parse_stats_t st;
    firc_err_t err = firc_sub_parse_rules_stats(list, strlen(list), out, &st, progress, ud);
    if (out_dropped != NULL) { *out_dropped = err == FIRC_OK ? st.dropped : 0; }
    return err;
}

/* exact text tried first, then one wrap peeled off; whichever was stored FIRST wins (keeps enable deterministic) */
static uint32_t stored_find(const idx_set_t *by_text, const firc_sub_rules_t *existing,
                            const char *derived, uint8_t list_type, const char *proto,
                            const char *ports)
{
    size_t n = strlen(derived);
    rule_key_t k = {.text = derived, .len = n, .type = list_type, .proto = proto, .ports = ports};
    const idx_slot_t *exact = idx_set_find(by_text, fnv1a_n(derived, n), existing, eq_text, &k, NULL);
    const idx_slot_t *peeled = NULL;
    if (n >= 9 && strncmp(derived, "\\A(?:", 5) == 0 && strcmp(derived + n - 3, ")\\z") == 0) {
        rule_key_t pk = {.text = derived + 5, .len = n - 8, .type = list_type, .proto = proto,
                         .ports = ports};
        peeled = idx_set_find(by_text, fnv1a_n(derived + 5, n - 8), existing, eq_text, &pk, NULL);
    }
    if (exact != NULL && peeled != NULL) {
        return exact->idx <= peeled->idx ? exact->idx : peeled->idx;
    }
    if (exact != NULL) { return exact->idx; }
    return peeled != NULL ? peeled->idx : IDX_EMPTY;
}

firc_err_t firc_sub_refresh_rules(const char *list, size_t len, const firc_sub_rules_t *existing,
                                  firc_sub_rules_t *out)
{
    firc_sub_rules_t rs;
    firc_sub_rules_init(&rs);
    firc_sub_parse_stats_t st;
    firc_err_t err = firc_sub_parse_rules_stats(list, len, &rs, &st, NULL, NULL);
    if (err == FIRC_OK) {
        err = firc_sub_rules_inherit(&rs, existing);
    }
    if (err != FIRC_OK) {
        firc_sub_rules_free(&rs);
        return err;
    }
    firc_sub_rules_move(out, &rs);
    return FIRC_OK;
}

firc_err_t firc_sub_rules_inherit(firc_sub_rules_t *rs, const firc_sub_rules_t *existing)
{
    if (rs->n == 0) {
        return FIRC_OK;
    }

    /* indexed by text (the hash), compared by list_type+text+spec (eq_text); first occurrence wins */
    idx_set_t by_text = {NULL, 0, 0};
    firc_id_set_t used = {NULL, 0, 0};
    firc_id_pool_t ids = {0};
    firc_err_t err = FIRC_OK;
    if ((err = idx_set_reserve(&by_text, existing->n)) != FIRC_OK) { goto done; }
    for (size_t i = 0; i < existing->n; i++) {
        const char *t = firc_sub_rules_text(existing, i);
        if (t[0] == '\0') { continue; }
        rule_key_t k = {.text = t, .len = strlen(t), .type = existing->v[i].list_type,
                        .proto = firc_sub_rules_proto(existing, i), .ports = firc_sub_rules_ports(existing, i)};
        uint32_t h = fnv1a_n(t, k.len);
        if (idx_set_find(&by_text, h, existing, eq_text, &k, NULL) != NULL) { continue; }
        if ((err = idx_set_insert(&by_text, h, (uint32_t)i, NULL)) != FIRC_OK) { goto done; }
    }

    for (size_t i = 0; i < rs->n; i++) {
        uint32_t cur = stored_find(&by_text, existing, firc_sub_rules_text(rs, i), rs->v[i].list_type,
                                   firc_sub_rules_proto(rs, i), firc_sub_rules_ports(rs, i));
        if (cur != IDX_EMPTY) {
            rs->v[i].id = existing->v[cur].id;
            rs->v[i].enable = existing->v[cur].enable;
            /* a stored type is kept UNLESS it's a stale old-guesser regex, or one the daemon can no longer use */
            uint8_t cur_type = existing->v[cur].type;
            uint8_t regex = firc_rule_type_index(FIRC_RULE_REGEX);
            bool stale_guess = cur_type == regex && rs->v[i].type != regex;
            bool unusable = cur_type != 0 &&
                            !firc_rule_spec_is_usable(firc_rule_type_name(cur_type), firc_sub_rules_text(rs, i),
                                                      firc_sub_rules_proto(rs, i), firc_sub_rules_ports(rs, i),
                                                      NULL);
            if (!stale_guess && !unusable && cur_type != 0) {
                rs->v[i].type = cur_type;
            }
        }
        if (firc_id_is_zero(rs->v[i].id) || firc_id_set_contains(&used, rs->v[i].id)) {
            if ((err = next_unique_id(&used, &ids, &rs->v[i].id)) != FIRC_OK) { break; }
            continue;
        }
        if ((err = firc_id_set_add(&used, rs->v[i].id)) != FIRC_OK) {
            break;
        }
    }

done:
    firc_id_set_free(&used);
    idx_set_free(&by_text);
    return err;
}

/* id and enable are IN the key, not held beside it -- holding them separately let a duplicate report false "changed" */
static firc_err_t same_rules_key(const firc_sub_rules_t *rs, size_t i, rule_key_t *key, uint32_t *hash)
{
    key->text = firc_sub_rules_text(rs, i);
    key->len = strlen(key->text);
    key->id = rs->v[i].id;
    key->enable = rs->v[i].enable;
    key->type = rs->v[i].type;
    key->proto = firc_sub_rules_proto(rs, i);
    key->ports = firc_sub_rules_ports(rs, i);
    if (key->type == 0) {
        bool oom = false;
        key->type = firc_rule_type_index(firc_sub_detect_type_checked(key->text, &oom));
        if (oom) { return FIRC_ERR_NOMEM; }
    }
    uint32_t h = fnv1a_n(key->text, key->len) ^ type_salt(key->type);
    h = (h ^ fnv1a_n((const char *)key->id.b, sizeof(key->id.b))) * 16777619u;
    *hash = h ^ (key->enable ? 0x5bd1e995u : 0u);
    return FIRC_OK;
}

/* proto/ports are compared here (spec_eq) but not folded into same_rules_key's hash, unlike id/enable */
static bool eq_same(const firc_sub_rules_t *rs, uint32_t idx, const rule_key_t *key, bool *oom)
{
    rule_key_t mine;
    uint32_t h;
    if (same_rules_key(rs, idx, &mine, &h) != FIRC_OK) {
        *oom = true;
        return false;
    }
    return mine.type == key->type && mine.enable == key->enable && firc_id_equal(mine.id, key->id) &&
           mine.len == key->len && memcmp(mine.text, key->text, key->len) == 0 &&
           spec_eq(mine.proto, key->proto) && spec_eq(mine.ports, key->ports);
}

firc_err_t firc_sub_same_rules_checked(const firc_sub_rules_t *left, const firc_sub_rules_t *right,
                                       bool *same)
{
    if (same == NULL) { return FIRC_ERR_INVAL; }
    *same = false;
    if (left->n != right->n) {
        return FIRC_OK;
    }
    if (left->n == 0) {
        *same = true;
        return FIRC_OK;
    }

    idx_set_t states = {NULL, 0, 0};
    firc_err_t err = idx_set_reserve(&states, left->n);
    bool result = true;

    for (size_t i = 0; err == FIRC_OK && i < left->n; i++) {
        rule_key_t key;
        uint32_t h;
        if ((err = same_rules_key(left, i, &key, &h)) != FIRC_OK) { break; }
        bool oom = false;
        idx_slot_t *st = idx_set_find(&states, h, left, eq_same, &key, &oom);
        if (oom) {
            err = FIRC_ERR_NOMEM;
            break;
        }
        if (st == NULL && (err = idx_set_insert(&states, h, (uint32_t)i, &st)) != FIRC_OK) { break; }
        st->count++;
    }
    for (size_t i = 0; err == FIRC_OK && result && i < right->n; i++) {
        rule_key_t key;
        uint32_t h;
        if ((err = same_rules_key(right, i, &key, &h)) != FIRC_OK) { break; }
        bool oom = false;
        idx_slot_t *st = idx_set_find(&states, h, left, eq_same, &key, &oom);
        if (oom) {
            err = FIRC_ERR_NOMEM;
            break;
        }
        if (st == NULL || st->count == 0) {
            result = false;
            break;
        }
        st->count--;
    }
    if (err == FIRC_OK && result) {
        for (size_t i = 0; i < states.cap && result; i++) {
            if (states.slots[i].idx != IDX_EMPTY && states.slots[i].count != 0) { result = false; }
        }
    }

    idx_set_free(&states);
    if (err != FIRC_OK) { return err; }
    *same = result;
    return FIRC_OK;
}

bool firc_sub_is_due(bool enable, const firc_group_list_t *l, int64_t now_unix)
{
    if (!enable || l == NULL || l->url == NULL || l->url[0] == '\0') {
        return false;
    }
    /* a first fetch is not an update: interval:0 only stops the timer, never the initial fetch */
    if (l->last_check == 0) {
        return true;
    }
    if (!l->has_body_hash && l->failed_fetches > 0) {
        unsigned shift = l->failed_fetches > 7 ? 6u : (unsigned)l->failed_fetches - 1u;
        uint64_t delay = 60u << shift;
        if (delay > 3600u) {
            delay = 3600u;
        }
        if ((uint64_t)now_unix >= (uint64_t)l->last_check + delay) {
            return true;
        }
    }
    if (l->interval == 0) {
        return false;
    }
    /* asks last_check, not "has it any rules": a list that parses to nothing would otherwise be due again every minute */
    if ((uint64_t)now_unix < (uint64_t)l->last_check + (uint64_t)l->interval) {
        return false;
    }
    return true;
}
