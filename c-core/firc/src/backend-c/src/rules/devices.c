#include "firc/devices.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

typedef enum { ENTRY_ADDR, ENTRY_POLICY, ENTRY_MAC } entry_kind_t;

typedef struct entry {
    entry_kind_t kind;
    char *policy;   /* owned, ENTRY_POLICY */
    firc_ip_t addr; /* ENTRY_ADDR */
    uint8_t prefix;
    firc_mac_t mac; /* ENTRY_MAC, never all zero */
} entry_t;

struct firc_devsel {
    entry_t *allow;
    size_t n_allow;
    entry_t *deny;
    size_t n_deny;
};

static const char WHY_MAC[] = "is not a MAC address (mac:aa:bb:cc:dd:ee:ff)";
static const char WHY_POLICY[] = "names no policy";
static const char WHY_MAPPED[] = "is a v4-mapped prefix shorter than /96, which covers no client";
static const char WHY_OTHER[] = "is neither an address, a prefix, policy:<name> nor mac:<address>";
static const char WHY_NOMEM[] = "could not be checked (out of memory)";

static void entries_free(entry_t *e, size_t n) {
    for (size_t i = 0; i < n; i++) { free(e[i].policy); }
    free(e);
}

static int hex_digit(char c) {
    if (c >= '0' && c <= '9') { return c - '0'; }
    if (c >= 'a' && c <= 'f') { return c - 'a' + 10; }
    if (c >= 'A' && c <= 'F') { return c - 'A' + 10; }
    return -1;
}

bool firc_mac_parse(const char *text, firc_mac_t *out) {
    if (text == NULL || strlen(text) != 17) { return false; }
    char sep = text[2];
    if (sep != ':' && sep != '-') { return false; }
    firc_mac_t m;
    bool zero = true;
    for (size_t i = 0; i < 6; i++) {
        const char *p = text + i * 3;
        if (i < 5 && p[2] != sep) { return false; }
        int hi = hex_digit(p[0]), lo = hex_digit(p[1]);
        if (hi < 0 || lo < 0) { return false; }
        m.b[i] = (uint8_t)(hi << 4 | lo);
        if (m.b[i] != 0) { zero = false; }
    }
    if (zero) { return false; }
    *out = m;
    return true;
}

void firc_mac_format(const firc_mac_t *mac, char out[18]) {
    snprintf(out, 18, "%02x:%02x:%02x:%02x:%02x:%02x", mac->b[0], mac->b[1], mac->b[2], mac->b[3], mac->b[4],
             mac->b[5]);
}

/* FIRC_OK, FIRC_ERR_NOMEM, or FIRC_ERR_INVAL with *why set. */
static firc_err_t parse_entry(const char *text, entry_t *out, const char **why) {
    memset(out, 0, sizeof(*out));
    *why = WHY_OTHER;
    if (text == NULL) { return FIRC_ERR_INVAL; }
    size_t mlen = strlen(FIRC_DEVSEL_MAC_PREFIX);
    if (strncmp(text, FIRC_DEVSEL_MAC_PREFIX, mlen) == 0) {
        if (!firc_mac_parse(text + mlen, &out->mac)) {
            *why = WHY_MAC;
            return FIRC_ERR_INVAL;
        }
        out->kind = ENTRY_MAC;
        return FIRC_OK;
    }
    size_t plen = strlen(FIRC_DEVSEL_POLICY_PREFIX);
    if (strncmp(text, FIRC_DEVSEL_POLICY_PREFIX, plen) == 0) {
        const char *name = text + plen;
        size_t nlen = strlen(name);
        if (nlen == 0 || name[0] == ' ' || name[nlen - 1] == ' ') {
            *why = WHY_POLICY;
            return FIRC_ERR_INVAL;
        }
        out->kind = ENTRY_POLICY;
        out->policy = strdup(name);
        return out->policy != NULL ? FIRC_OK : FIRC_ERR_NOMEM;
    }
    out->kind = ENTRY_ADDR;
    bool ok;
    if (strchr(text, '/') != NULL) {
        ok = firc_ip_parse_cidr(text, &out->addr, &out->prefix);
    } else {
        /* A bare address is a host prefix. */
        char with_len[64];
        if (strlen(text) + 5 > sizeof(with_len)) { return FIRC_ERR_INVAL; }
        snprintf(with_len, sizeof(with_len), "%s/%s", text, strchr(text, ':') != NULL ? "128" : "32");
        ok = firc_ip_parse_cidr(with_len, &out->addr, &out->prefix);
    }
    if (!ok) { return FIRC_ERR_INVAL; }
    /* ::ffff:a.b.c.d is normalized to a.b.c.d: clients reach the selector unmapped. */
    static const uint8_t mapped[12] = {0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff};
    if (out->addr.len == 16 && memcmp(out->addr.b, mapped, 12) == 0) {
        /* a mapped prefix < /96 covers no client the proxy can ever report */
        if (out->prefix < 96) {
            *why = WHY_MAPPED;
            return FIRC_ERR_INVAL;
        }
        memmove(out->addr.b, out->addr.b + 12, 4);
        memset(out->addr.b + 4, 0, 12);
        out->addr.len = 4;
        out->prefix = (uint8_t)(out->prefix - 96);
    }
    return FIRC_OK;
}

const char *firc_devsel_entry_why(const char *entry) {
    entry_t e;
    const char *why = NULL;
    firc_err_t err = parse_entry(entry, &e, &why);
    free(e.policy);
    /* NULL means accepted; an entry that could not even be checked (OOM) is refused, not accepted. */
    if (err == FIRC_OK) { return NULL; }
    return err == FIRC_ERR_INVAL ? why : WHY_NOMEM;
}

void firc_devsel_entry_canon(char *entry) {
    size_t mlen = strlen(FIRC_DEVSEL_MAC_PREFIX);
    firc_mac_t m;
    if (entry == NULL || strncmp(entry, FIRC_DEVSEL_MAC_PREFIX, mlen) != 0 || !firc_mac_parse(entry + mlen, &m)) {
        return;
    }
    /* 17 chars + NUL, matching what firc_mac_parse validated. */
    firc_mac_format(&m, entry + mlen);
}

firc_err_t firc_devsel_entry_parse(const char *text, firc_devsel_entry_t *out) {
    entry_t e;
    const char *why = NULL;
    firc_err_t err = parse_entry(text, &e, &why);
    if (err != FIRC_OK) {
        free(e.policy);
        return err;
    }
    memset(out, 0, sizeof(*out));
    switch (e.kind) {
    case ENTRY_POLICY:
        out->kind = FIRC_DEVSEL_POLICY;
        break;
    case ENTRY_MAC:
        out->kind = FIRC_DEVSEL_MAC;
        break;
    case ENTRY_ADDR:
        out->kind = FIRC_DEVSEL_ADDR;
        break;
    }
    out->addr = e.addr;
    out->prefix = e.prefix;
    out->mac = e.mac;
    out->policy = e.kind == ENTRY_POLICY ? text + strlen(FIRC_DEVSEL_POLICY_PREFIX) : NULL;
    free(e.policy);
    return FIRC_OK;
}

static firc_err_t compile_list(const char *const *texts, size_t n, entry_t **out, size_t *out_n) {
    *out = NULL;
    *out_n = 0;
    if (n == 0) { return FIRC_OK; }
    entry_t *e = calloc(n, sizeof(*e));
    if (e == NULL) { return FIRC_ERR_NOMEM; }
    for (size_t i = 0; i < n; i++) {
        const char *why = NULL;
        firc_err_t err = parse_entry(texts[i], &e[i], &why);
        if (err != FIRC_OK) {
            entries_free(e, i + 1);
            return err;
        }
    }
    *out = e;
    *out_n = n;
    return FIRC_OK;
}

firc_err_t firc_devsel_compile(const char *const *allow, size_t n_allow, const char *const *deny,
                               size_t n_deny, firc_devsel_t **out) {
    *out = NULL;
    firc_devsel_t *s = calloc(1, sizeof(*s));
    if (s == NULL) { return FIRC_ERR_NOMEM; }
    firc_err_t err = compile_list(allow, n_allow, &s->allow, &s->n_allow);
    if (err == FIRC_OK) { err = compile_list(deny, n_deny, &s->deny, &s->n_deny); }
    if (err != FIRC_OK) {
        firc_devsel_free(s);
        return err;
    }
    *out = s;
    return FIRC_OK;
}

void firc_devsel_free(firc_devsel_t *s) {
    if (s == NULL) { return; }
    entries_free(s->allow, s->n_allow);
    entries_free(s->deny, s->n_deny);
    free(s);
}

bool firc_devsel_prefix_covers(const firc_ip_t *net, uint8_t prefix, const firc_ip_t *addr) {
    if (net->len != addr->len) { return false; }
    size_t full = prefix / 8;
    if (memcmp(net->b, addr->b, full) != 0) { return false; }
    unsigned rest = prefix % 8;
    if (rest == 0) { return true; }
    uint8_t mask = (uint8_t)(0xffu << (8 - rest));
    return (net->b[full] & mask) == (addr->b[full] & mask);
}

/* The client plus every other address of it the firmware knows. */
typedef struct {
    firc_ip_t addrs[FIRC_DEVSEL_MAX_DEVICE_ADDRS];
    size_t n;
    firc_mac_t mac; /* all zero: the table knows no MAC for it */
} device_t;

static void device_of(const firc_ip_t *client, firc_devsel_device_fn device, void *ud, device_t *out) {
    out->addrs[0] = *client;
    out->n = 1;
    memset(&out->mac, 0, sizeof(out->mac));
    if (device == NULL) { return; }
    out->n += device(client, out->addrs + 1, FIRC_DEVSEL_MAX_DEVICE_ADDRS - 1, &out->mac, ud);
}

static bool list_matches(const entry_t *e, size_t n, const firc_ip_t *client, const device_t *dev,
                         firc_devsel_policy_fn policy, void *ud) {
    for (size_t i = 0; i < n; i++) {
        switch (e[i].kind) {
        case ENTRY_POLICY:
            if (policy != NULL && policy(e[i].policy, client, ud)) { return true; }
            break;
        case ENTRY_MAC:
            /* never all zero (firc_mac_parse), so an unknown host never matches */
            if (memcmp(e[i].mac.b, dev->mac.b, sizeof(dev->mac.b)) == 0) { return true; }
            break;
        case ENTRY_ADDR:
            for (size_t k = 0; k < dev->n; k++) {
                if (firc_devsel_prefix_covers(&e[i].addr, e[i].prefix, &dev->addrs[k])) { return true; }
            }
            break;
        }
    }
    return false;
}

bool firc_devsel_allows(const firc_devsel_t *s, const firc_ip_t *client, firc_devsel_policy_fn policy,
                        firc_devsel_device_fn device, void *ud) {
    if (s == NULL || client == NULL || client->len == 0) { return true; }
    if (s->n_allow == 0 && s->n_deny == 0) { return true; } /* every device: no table lookup */
    device_t dev;
    device_of(client, device, ud, &dev);
    if (list_matches(s->deny, s->n_deny, client, &dev, policy, ud)) { return false; }
    if (s->n_allow == 0) { return true; }
    return list_matches(s->allow, s->n_allow, client, &dev, policy, ud);
}

bool firc_devsel_is_empty(const firc_devsel_t *s) {
    return s == NULL || (s->n_allow == 0 && s->n_deny == 0);
}

bool firc_devsel_names_a_policy_or_mac(const firc_devsel_t *s) {
    if (s == NULL) { return false; }
    for (size_t i = 0; i < s->n_allow; i++) { if (s->allow[i].kind != ENTRY_ADDR) { return true; } }
    for (size_t i = 0; i < s->n_deny; i++) { if (s->deny[i].kind != ENTRY_ADDR) { return true; } }
    return false;
}
