#include "firc/regdomain.h"

#include <string.h>

static const char *const generic_sld[] = {
    "ac", "biz", "co", "com", "edu", "gen", "go", "gob", "gov", "info", "ltd",
    "me", "mil", "ne", "net", "nic", "nom", "or", "org", "plc", "sch",
};

static const char *const geographic[] = {
    "msk.ru", "spb.ru", "nov.ru", "msk.su", "spb.su", "kiev.ua", "kharkov.ua",
};

static const char *const shared[] = {
    "appspot.com", "azurewebsites.net", "blogspot.com", "cloudfront.net", "firebaseapp.com",
    "fly.dev", "github.io", "githubusercontent.com", "gitlab.io", "herokuapp.com", "netlify.app",
    "onrender.com", "pages.dev", "r2.dev", "vercel.app", "web.app", "workers.dev",
    "amazonaws.com", "cloudapp.net", "azureedge.net", "trafficmanager.net", "repl.co",
    "myshopify.com", "wixsite.com", "tumblr.com", "wordpress.com", "livejournal.com",
    "narod.ru", "ucoz.ru", "tilda.ws",
    "googleusercontent.com", "akamaized.net", "akamaihd.net", "edgekey.net", "edgesuite.net",
    "fastly.net", "b-cdn.net", "cloudflarestorage.com",
};

static const char *const shared3[] = {
    "core.windows.net",
};

#define COUNT(a) (sizeof(a) / sizeof((a)[0]))

static bool in(const char *s, size_t n, const char *const *set, size_t count) {
    for (size_t i = 0; i < count; i++) {
        if (strlen(set[i]) == n && memcmp(set[i], s, n) == 0) { return true; }
    }
    return false;
}

static bool is_letter(char c) { return c >= 'a' && c <= 'z'; }

static bool two_label_suffix(const char *s, size_t n) {
    if (in(s, n, geographic, COUNT(geographic)) || in(s, n, shared, COUNT(shared))) { return true; }
    const char *dot = memchr(s, '.', n);
    if (dot == NULL) { return false; }
    size_t sld = (size_t)(dot - s);
    size_t tld = n - sld - 1;
    if (tld != 2 || !is_letter(dot[1]) || !is_letter(dot[2])) { return false; }
    return in(s, sld, generic_sld, sizeof(generic_sld) / sizeof(generic_sld[0]));
}

static bool address_literal(const char *s, size_t n) {
    if (memchr(s, ':', n) != NULL) { return true; }
    for (size_t i = 0; i < n; i++) {
        if (s[i] != '.' && (s[i] < '0' || s[i] > '9')) { return false; }
    }
    return true;
}

bool firc_autolist_exclude_ok(const char *name) {
    if (name == NULL) { return false; }
    size_t n = strlen(name);
    if (n == 0 || n > 253 || name[0] == '.' || name[n - 1] == '.') { return false; }
    size_t label = 0;
    for (size_t i = 0; i < n; i++) {
        char c = name[i];
        if (c == '.') {
            if (label == 0) { return false; }
            label = 0;
            continue;
        }
        if (!is_letter(c) && !(c >= '0' && c <= '9') && c != '-' && c != '_') { return false; }
        if (++label > 63) { return false; }
    }
    return true;
}

bool firc_autolist_excluded(const char *host, char *const *list, size_t n) {
    if (host == NULL || list == NULL) { return false; }
    size_t hl = strlen(host);
    for (size_t i = 0; i < n; i++) {
        const char *e = list[i];
        if (e == NULL) { continue; }
        size_t el = strlen(e);
        if (el == 0 || el > hl) { continue; }
        if (memcmp(host + (hl - el), e, el) != 0) { continue; }
        if (el == hl || host[hl - el - 1] == '.') { return true; }
    }
    return false;
}

bool firc_regdomain_shared(const char *domain) {
    if (domain == NULL) { return false; }
    const char *dot = strchr(domain, '.');
    if (dot == NULL) { return false; }
    size_t n = strlen(dot + 1);
    return in(dot + 1, n, shared, COUNT(shared)) || in(dot + 1, n, shared3, COUNT(shared3));
}

bool firc_regdomain(const char *host, char *out, size_t cap) {
    if (host == NULL || out == NULL || cap == 0) { return false; }
    size_t n = strlen(host);
    if (n > 0 && host[n - 1] == '.') { n--; }
    if (n == 0 || host[0] == '.' || address_literal(host, n)) { return false; }

    long starts[4] = {-1, -1, -1, -1};
    size_t found = 0;
    size_t end = n;
    while (found < 4) {
        size_t i = end;
        while (i > 0 && host[i - 1] != '.') { i--; }
        if (i == end) { return false; }
        starts[found++] = (long)i;
        if (i == 0) { break; }
        end = i - 1;
    }
    if (found < 2) { return false; }

    size_t take;
    if (found >= 3 && in(host + starts[2], n - (size_t)starts[2], shared3, COUNT(shared3))) {
        if (found < 4) { return false; }
        take = n - (size_t)starts[3];
    } else if (two_label_suffix(host + starts[1], n - (size_t)starts[1])) {
        if (found < 3) { return false; }
        take = n - (size_t)starts[2];
    } else {
        take = n - (size_t)starts[1];
    }
    if (take + 1 > cap) { return false; }
    memcpy(out, host + (n - take), take);
    out[take] = '\0';
    return true;
}
