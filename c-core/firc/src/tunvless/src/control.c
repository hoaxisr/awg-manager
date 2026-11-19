#include "control.h"
#include <dirent.h>
#include <inttypes.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

static int g_on;
static pthread_mutex_t g_mu = PTHREAD_MUTEX_INITIALIZER;

void ctl_init(int enabled) { g_on = enabled; }

void ctl_close_inherited_fds(void) {
    enum { BATCH = 256 };
    for (;;) {
        DIR *d = opendir("/proc/self/fd");
        if (!d) break;
        int self = dirfd(d);
        int fds[BATCH];
        size_t n = 0;
        struct dirent *e;
        while (n < BATCH && (e = readdir(d)) != NULL) {
            if (e->d_name[0] < '0' || e->d_name[0] > '9') continue;
            int fd = atoi(e->d_name);
            if (fd > 2 && fd != self) fds[n++] = fd;
        }
        closedir(d);
        for (size_t i = 0; i < n; i++) close(fds[i]);
        if (n < BATCH) return;
    }
    long max = sysconf(_SC_OPEN_MAX);
    if (max < 0 || max > 65536) max = 65536;
    for (int fd = 3; fd < max; fd++) close(fd);
}

size_t ctl_json_str(char *out, size_t cap, const char *s) {
    size_t o = 0;
    if (cap < 3) return 0;
    out[o++] = '"';
    for (; *s; s++) {
        unsigned char c = (unsigned char)*s;
        char tmp[7];
        size_t k;
        if (c == '"' || c == '\\') { tmp[0] = '\\'; tmp[1] = (char)c; k = 2; }
        else if (c < 0x20) { snprintf(tmp, sizeof tmp, "\\u%04x", c); k = 6; }
        else { tmp[0] = (char)c; k = 1; }
        if (o + k + 2 > cap) return 0;
        memcpy(out + o, tmp, k);
        o += k;
    }
    out[o++] = '"';
    out[o] = 0;
    return o;
}

static void emit(const char *line, size_t n) {
    if (!g_on) return;
    pthread_mutex_lock(&g_mu);
    while (n > 0) {
        ssize_t w = write(1, line, n);
        if (w <= 0) break;
        line += w;
        n -= (size_t)w;
    }
    pthread_mutex_unlock(&g_mu);
}

static size_t ctl_json_str_trunc(char *out, size_t cap, const char *s) {
    char tmp[256];
    size_t len = strlen(s);
    if (len >= sizeof tmp) len = sizeof tmp - 1;
    memcpy(tmp, s, len);
    for (;;) {
        tmp[len] = 0;
        size_t k = ctl_json_str(out, cap, tmp);
        if (k || len == 0) {
            if (!k && cap >= 3) { memcpy(out, "\"\"", 3); return 2; }
            return k;
        }
        len--;
    }
}

static void emit1(const char *type, const char *key, const char *val) {
    char v[300], l[1024];
    ctl_json_str_trunc(v, sizeof v, val);
    int n = snprintf(l, sizeof l, "{\"type\":\"%s\",\"%s\":%s}\n", type, key, v);
    if (n > 0 && (size_t)n < sizeof l) emit(l, (size_t)n);
}

void ctl_emit_ready(const char *dev, int tcp_port) {
    char v[300], l[1024];
    ctl_json_str_trunc(v, sizeof v, dev);
    int n = snprintf(l, sizeof l, "{\"type\":\"ready\",\"dev\":%s,\"tcpPort\":%d}\n", v, tcp_port);
    if (n > 0 && (size_t)n < sizeof l) emit(l, (size_t)n);
}

static void emit_count(const char *type, int count) {
    char l[64];
    int n = snprintf(l, sizeof l, "{\"type\":\"%s\",\"count\":%d}\n", type, count);
    emit(l, (size_t)n);
}

void ctl_emit_map(int count) { emit_count("map", count); }

void ctl_emit_close(int count) { emit_count("close", count); }

void ctl_emit_miss(const char *fake) { emit1("miss", "fake", fake); }

void ctl_emit_bytes(uint32_t rx, uint32_t tx) {
    char l[96];
    int n = snprintf(l, sizeof l, "{\"type\":\"bytes\",\"rx\":%" PRIu32 ",\"tx\":%" PRIu32 "}\n", rx, tx);
    if (n > 0 && (size_t)n < sizeof l) emit(l, (size_t)n);
}

void ctl_emit_node_up(const char *node, int index) {
    char a[300], l[1024];
    ctl_json_str_trunc(a, sizeof a, node);
    int n = snprintf(l, sizeof l, "{\"type\":\"node_up\",\"node\":%s,\"index\":%d}\n", a, index);
    if (n > 0 && (size_t)n < sizeof l) emit(l, (size_t)n);
}

void ctl_emit_node_down(const char *node, int index, const char *why) {
    char a[300], b[300], l[1024];
    ctl_json_str_trunc(a, sizeof a, node);
    ctl_json_str_trunc(b, sizeof b, why);
    int n = snprintf(l, sizeof l, "{\"type\":\"node_down\",\"node\":%s,\"index\":%d,\"why\":%s}\n",
                     a, index, b);
    if (n > 0 && (size_t)n < sizeof l) emit(l, (size_t)n);
}

void ctl_emit_active(const char *const *names, const int *index, const int *v6, size_t cnt) {
    char l[1024], v[300], ix[192];
    if (cnt > 8) cnt = 8;
    size_t xo = (size_t)snprintf(ix, sizeof ix, "],\"index\":[");
    for (size_t i = 0; i < cnt; i++)
        xo += (size_t)snprintf(ix + xo, sizeof ix - xo, "%s%d", i ? "," : "", index[i]);
    xo += (size_t)snprintf(ix + xo, sizeof ix - xo, "],\"v6\":[");
    for (size_t i = 0; i < cnt; i++)
        xo += (size_t)snprintf(ix + xo, sizeof ix - xo, "%s%s", i ? "," : "", v6[i] ? "true" : "false");
    xo += (size_t)snprintf(ix + xo, sizeof ix - xo, "]}\n");
    size_t o = (size_t)snprintf(l, sizeof l, "{\"type\":\"active\",\"nodes\":[");
    size_t room = sizeof l - o - xo - 1;
    size_t each = cnt ? room / cnt - 1 : 0;
    if (each > sizeof v) each = sizeof v;
    for (size_t i = 0; i < cnt; i++) {
        size_t k = ctl_json_str_trunc(v, each, names[i]);
        if (i) l[o++] = ',';
        memcpy(l + o, v, k);
        o += k;
    }
    memcpy(l + o, ix, xo);
    emit(l, o + xo);
}

void ctl_emit_nodes(int count) { emit_count("nodes", count); }

void ctl_emit_inbound(unsigned port, const char *state, const char *why) {
    char a[300], b[300], l[1024];
    ctl_json_str_trunc(a, sizeof a, state);
    ctl_json_str_trunc(b, sizeof b, why);
    int n = snprintf(l, sizeof l, "{\"type\":\"inbound\",\"port\":%u,\"state\":%s,\"why\":%s}\n", port, a, b);
    if (n > 0 && (size_t)n < sizeof l) emit(l, (size_t)n);
}

void ctl_emit_ibytes(unsigned port, uint32_t rx, uint32_t tx) {
    char l[128];
    int n = snprintf(l, sizeof l, "{\"type\":\"ibytes\",\"port\":%u,\"rx\":%" PRIu32 ",\"tx\":%" PRIu32 "}\n",
                     port, rx, tx);
    if (n > 0 && (size_t)n < sizeof l) emit(l, (size_t)n);
}

void ctl_emit_inbounds(long count) {
    char l[64];
    int n = snprintf(l, sizeof l, "{\"type\":\"inbounds\",\"count\":%ld}\n", count);
    emit(l, (size_t)n);
}

void ctl_emit_pins_full(void) {
    static const char l[] = "{\"type\":\"pins_full\"}\n";
    emit(l, sizeof l - 1);
}

void ctl_emit_no_node(int retry_s) {
    char l[64];
    int n = snprintf(l, sizeof l, "{\"type\":\"no_node\",\"retry\":%d}\n", retry_s);
    emit(l, (size_t)n);
}
