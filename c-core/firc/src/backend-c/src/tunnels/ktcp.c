#include "firc/ktcp.h"

#include <arpa/inet.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

firc_err_t firc_ktcp_image_add(firc_ktcp_image_t *im, uint8_t dev, const uint8_t fake[4], const firc_ip_t *real)
{
    if (im->n == im->cap) {
        size_t cap = im->cap ? im->cap * 2 : 64;
        firc_ktcp_pair_t *v = realloc(im->v, cap * sizeof *v);
        if (v == NULL) { return FIRC_ERR_NOMEM; }
        im->v = v;
        im->cap = cap;
    }
    firc_ktcp_pair_t *p = &im->v[im->n++];
    p->dev = dev;
    memcpy(p->fake, fake, 4);
    memset(p->real, 0, sizeof(p->real));
    memcpy(p->real, real->b, real->len);
    p->real_len = real->len;
    return FIRC_OK;
}

static int key_cmp(const firc_ktcp_pair_t *a, const firc_ktcp_pair_t *b)
{
    if (a->dev != b->dev) { return a->dev < b->dev ? -1 : 1; }
    return memcmp(a->fake, b->fake, 4);
}

static void swap(firc_ktcp_pair_t *v, size_t i, size_t j)
{
    firc_ktcp_pair_t t = v[i];
    v[i] = v[j];
    v[j] = t;
}

static void swap_range(firc_ktcp_pair_t *v, size_t a, size_t b, size_t n)
{
    for (size_t i = 0; i < n; i++) { swap(v, a + i, b + i); }
}

static void rotate(firc_ktcp_pair_t *v, size_t a, size_t m, size_t b)
{
    size_t i = m - a, j = b - m;
    while (i != j) {
        if (i > j) {
            swap_range(v, m - i, m, j);
            i -= j;
        } else {
            swap_range(v, m - i, m + j - i, i);
            j -= i;
        }
    }
    swap_range(v, m - i, m, i);
}

static void insertion_sort(firc_ktcp_pair_t *v, size_t a, size_t b)
{
    for (size_t i = a + 1; i < b; i++) {
        for (size_t j = i; j > a && key_cmp(&v[j], &v[j - 1]) < 0; j--) { swap(v, j, j - 1); }
    }
}

static void sym_merge(firc_ktcp_pair_t *v, size_t a, size_t m, size_t b)
{
    if (m - a == 1) {
        size_t i = m, j = b;
        while (i < j) {
            size_t h = i + (j - i) / 2;
            if (key_cmp(&v[h], &v[a]) < 0) {
                i = h + 1;
            } else {
                j = h;
            }
        }
        for (size_t k = a; k + 1 < i; k++) { swap(v, k, k + 1); }
        return;
    }
    if (b - m == 1) {
        size_t i = a, j = m;
        while (i < j) {
            size_t h = i + (j - i) / 2;
            if (key_cmp(&v[m], &v[h]) >= 0) {
                i = h + 1;
            } else {
                j = h;
            }
        }
        for (size_t k = m; k > i; k--) { swap(v, k, k - 1); }
        return;
    }
    size_t mid = a + (b - a) / 2, n = mid + m, start, r;
    if (m > mid) {
        start = n - b;
        r = mid;
    } else {
        start = a;
        r = m;
    }
    size_t p = n - 1;
    while (start < r) {
        size_t c = start + (r - start) / 2;
        if (key_cmp(&v[p - c], &v[c]) >= 0) {
            start = c + 1;
        } else {
            r = c;
        }
    }
    size_t end = n - start;
    if (start < m && m < end) { rotate(v, start, m, end); }
    if (a < start && start < mid) { sym_merge(v, a, start, mid); }
    if (mid < end && end < b) { sym_merge(v, mid, end, b); }
}

static void stable_sort(firc_ktcp_pair_t *v, size_t n)
{
    size_t block = 20, a = 0, b = block;
    for (; b <= n; a = b, b += block) { insertion_sort(v, a, b); }
    insertion_sort(v, a, n);
    for (; block < n; block *= 2) {
        for (a = 0, b = 2 * block; b <= n; a = b, b += 2 * block) { sym_merge(v, a, a + block, b); }
        if (a + block < n) { sym_merge(v, a, a + block, n); }
    }
}

void firc_ktcp_image_sort(firc_ktcp_image_t *im)
{
    if (im->n < 2) { return; }
    stable_sort(im->v, im->n);
    size_t w = 0;
    for (size_t i = 0; i < im->n; i++) {
        if (i + 1 < im->n && key_cmp(&im->v[i], &im->v[i + 1]) == 0) { continue; }
        im->v[w++] = im->v[i];
    }
    im->n = w;
}

void firc_ktcp_image_free(firc_ktcp_image_t *im)
{
    free(im->v);
    memset(im, 0, sizeof(*im));
}

static firc_ip_t real_of_pair(const firc_ktcp_pair_t *p)
{
    firc_ip_t r = {{0}, p->real_len};
    memcpy(r.b, p->real, sizeof(r.b));
    return r;
}

static void emit(void (*fn)(uint8_t dev, const uint8_t fake[4], const firc_ip_t *real, void *ud),
                 const firc_ktcp_pair_t *p, void *ud)
{
    firc_ip_t r = real_of_pair(p);
    fn(p->dev, p->fake, &r, ud);
}

void firc_ktcp_image_diff(const firc_ktcp_image_t *prev, const firc_ktcp_image_t *next,
                          void (*fn)(uint8_t dev, const uint8_t fake[4], const firc_ip_t *real, void *ud), void *ud)
{
    size_t i = 0, j = 0;
    while (i < prev->n && j < next->n) {
        const firc_ktcp_pair_t *p = &prev->v[i], *q = &next->v[j];
        int c = key_cmp(p, q);
        if (c < 0) {
            fn(p->dev, p->fake, NULL, ud);
            i++;
        } else if (c > 0) {
            emit(fn, q, ud);
            j++;
        } else {
            if (p->real_len != q->real_len || memcmp(p->real, q->real, sizeof(p->real)) != 0) { emit(fn, q, ud); }
            i++;
            j++;
        }
    }
    for (; i < prev->n; i++) { fn(prev->v[i].dev, prev->v[i].fake, NULL, ud); }
    for (; j < next->n; j++) { emit(fn, &next->v[j], ud); }
}

static size_t room_for(size_t n, size_t min)
{
    size_t c = min;
    while (c < n) { c *= 2; }
    return c;
}

static firc_err_t append(firc_ktcp_cmds_t *c, const char *s, size_t n)
{
    size_t want = c->len + n + 1;
    if (c->text == NULL || room_for(c->len + 1, 256) < want) {
        char *t = realloc(c->text, room_for(want, 256));
        if (t == NULL) { return FIRC_ERR_NOMEM; }
        c->text = t;
    }
    memcpy(c->text + c->len, s, n);
    c->len += n;
    c->text[c->len] = '\0';
    return FIRC_OK;
}

static firc_err_t reserve_cmd(firc_ktcp_cmds_t *c)
{
    if (c->counts != NULL && room_for(c->n_cmds, 8) >= c->n_cmds + 1) { return FIRC_OK; }
    size_t *v = realloc(c->counts, room_for(c->n_cmds + 1, 8) * sizeof *v);
    if (v == NULL) { return FIRC_ERR_NOMEM; }
    c->counts = v;
    return FIRC_OK;
}

static firc_err_t open_cmd(firc_ktcp_cmds_t *c, const char *word)
{
    char head[16];
    int n = snprintf(head, sizeof(head), "%s\n\n", word);
    firc_err_t err = reserve_cmd(c);
    if (err == FIRC_OK) { err = append(c, head, (size_t)n); }
    if (err == FIRC_OK) { c->counts[c->n_cmds++] = 0; }
    return err;
}

#define MAP_LINE_MAX (16 + INET6_ADDRSTRLEN)

static firc_err_t add_line(firc_ktcp_cmds_t *c, const char *word, bool open, const char *line)
{
    bool fresh = open || c->n_cmds == 0 || c->counts[c->n_cmds - 1] == FIRC_KTCP_CMD_LINES;
    char buf[MAP_LINE_MAX + 8];
    int n = fresh ? snprintf(buf, sizeof(buf), "%s\n%s\n\n", word, line) : snprintf(buf, sizeof(buf), "%s\n\n", line);
    firc_err_t err = fresh ? reserve_cmd(c) : FIRC_OK;
    if (err != FIRC_OK) { return err; }
    if (!fresh) { c->len--; }
    err = append(c, buf, (size_t)n);
    if (err != FIRC_OK) {
        if (!fresh) { c->len++; }
        return err;
    }
    if (fresh) {
        c->counts[c->n_cmds++] = 1;
    } else {
        c->counts[c->n_cmds - 1]++;
    }
    return FIRC_OK;
}

static void map_line(char *buf, size_t cap, const uint8_t fake[4], const firc_ip_t *real)
{
    int n = snprintf(buf, cap, "%u.%u.%u.%u ", fake[0], fake[1], fake[2], fake[3]);
    char *at = buf + n;
    size_t left = cap - (size_t)n;
    if (real == NULL) {
        snprintf(at, left, "-");
    } else if (real->len == 16) {
        inet_ntop(AF_INET6, real->b, at, (socklen_t)left);
    } else {
        snprintf(at, left, "%u.%u.%u.%u", real->b[0], real->b[1], real->b[2], real->b[3]);
    }
}

static void restore(firc_ktcp_cmds_t *c, size_t len, size_t n_cmds)
{
    c->len = len;
    c->n_cmds = n_cmds;
    if (c->text) { c->text[len] = '\0'; }
}

firc_err_t firc_ktcp_map_cmds(const firc_ktcp_image_t *im, uint8_t dev, bool all, firc_ktcp_cmds_t *out)
{
    size_t len = out->len, n_cmds = out->n_cmds;
    firc_err_t err = all ? open_cmd(out, "mapall") : FIRC_OK;
    bool open = !all;
    for (size_t i = 0; err == FIRC_OK && i < im->n; i++) {
        const firc_ktcp_pair_t *p = &im->v[i];
        if (p->dev != dev) { continue; }
        char buf[MAP_LINE_MAX];
        firc_ip_t r = real_of_pair(p);
        map_line(buf, sizeof(buf), p->fake, &r);
        err = add_line(out, "map", open, buf);
        open = false;
    }
    if (err != FIRC_OK) { restore(out, len, n_cmds); }
    return err;
}

firc_err_t firc_ktcp_line(firc_ktcp_cmds_t *out, const uint8_t fake[4], const firc_ip_t *real)
{
    char buf[MAP_LINE_MAX];
    map_line(buf, sizeof(buf), fake, real);
    return add_line(out, "map", false, buf);
}

firc_err_t firc_ktcp_close_cmd(const firc_nf_prefix_t *p, size_t n, firc_ktcp_cmds_t *out)
{
    size_t len = out->len, n_cmds = out->n_cmds;
    firc_err_t err = FIRC_OK;
    bool open = true;
    for (size_t i = 0; err == FIRC_OK && i < n; i++) {
        if (p[i].family != 4) { continue; }
        char buf[32];
        snprintf(buf, sizeof(buf), "%u.%u.%u.%u/%u", p[i].addr[0], p[i].addr[1], p[i].addr[2], p[i].addr[3], p[i].cidr);
        err = add_line(out, "close", open, buf);
        open = false;
    }
    if (err != FIRC_OK) { restore(out, len, n_cmds); }
    return err;
}

void firc_ktcp_cmds_free(firc_ktcp_cmds_t *c)
{
    free(c->text);
    free(c->counts);
    memset(c, 0, sizeof(*c));
}
