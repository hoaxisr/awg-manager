#include "firc/ptr.h"

#include <arpa/inet.h>
#include <errno.h>
#include <netinet/in.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/epoll.h>
#include <sys/socket.h>
#include <unistd.h>

#include "firc/dnswire.h"
#include "firc/rand.h"

#define SLOTS 1024
#define KEEP_MS (24LL * 60 * 60 * 1000)
#define RETRY_MS (5LL * 60 * 1000)
#define QUERY_MAX (FIRC_DNS_HEADER_LEN + FIRC_DNS_MAX_NAME + 4)

typedef struct slot {
    firc_ptr_t *owner;
    bool used;
    firc_ip_t ip;
    firc_ptr_state_t state;
    int64_t at_ms;
    int64_t keep_ms;
    uint64_t seq;
    uint16_t id;
    uint8_t qname[FIRC_DNS_MAX_NAME + 1];
    size_t qname_len;
    int timer;
    char name[FIRC_DNS_MAX_NAME + 1];
} slot_t;

struct firc_ptr {
    firc_loop_t *loop;
    uint32_t timeout_ms;
    int fd;
    int family;
    uint64_t seq;
    slot_t slots[SLOTS];
};

firc_ptr_t *firc_ptr_new(firc_loop_t *loop, uint32_t timeout_ms)
{
    firc_ptr_t *p = calloc(1, sizeof(*p));
    if (p == NULL) { return NULL; }
    p->loop = loop;
    p->timeout_ms = timeout_ms;
    p->fd = -1;
    for (size_t i = 0; i < SLOTS; i++) {
        p->slots[i].owner = p;
        p->slots[i].timer = -1;
    }
    return p;
}

static void stop_timer(slot_t *s)
{
    if (s->timer >= 0) {
        (void)firc_loop_del_timer(s->owner->loop, s->timer);
        s->timer = -1;
    }
}

static void close_socket(firc_ptr_t *p)
{
    if (p->fd < 0) { return; }
    (void)firc_loop_del_fd(p->loop, p->fd);
    close(p->fd);
    p->fd = -1;
}

void firc_ptr_free(firc_ptr_t *p)
{
    if (p == NULL) { return; }
    for (size_t i = 0; i < SLOTS; i++) { stop_timer(&p->slots[i]); }
    close_socket(p);
    free(p);
}

bool firc_ptr_name_for(const firc_ip_t *ip, char *out, size_t cap)
{
    static const char hex[] = "0123456789abcdef";
    char buf[80];
    size_t w = 0;
    if (ip->len == 4) {
        int n = snprintf(buf, sizeof(buf), "%u.%u.%u.%u.in-addr.arpa", ip->b[3], ip->b[2], ip->b[1], ip->b[0]);
        if (n < 0) { return false; }
        w = (size_t)n;
    } else if (ip->len == 16) {
        for (int i = 15; i >= 0; i--) {
            buf[w++] = hex[ip->b[i] & 0x0fu];
            buf[w++] = '.';
            buf[w++] = hex[ip->b[i] >> 4u];
            buf[w++] = '.';
        }
        memcpy(buf + w, "ip6.arpa", sizeof("ip6.arpa"));
        w += sizeof("ip6.arpa") - 1;
    } else {
        return false;
    }
    if (w + 1 > cap) { return false; }
    memcpy(out, buf, w + 1);
    return true;
}

static bool expired(const slot_t *s, int64_t now_ms)
{
    return s->state != FIRC_PTR_PENDING && now_ms - s->at_ms >= s->keep_ms;
}

static slot_t *find(const firc_ptr_t *p, const firc_ip_t *ip)
{
    for (size_t i = 0; i < SLOTS; i++) {
        const slot_t *s = &p->slots[i];
        if (s->used && s->ip.len == ip->len && memcmp(s->ip.b, ip->b, ip->len) == 0) { return (slot_t *)s; }
    }
    return NULL;
}

static slot_t *victim(firc_ptr_t *p)
{
    slot_t *oldest = &p->slots[0];
    for (size_t i = 0; i < SLOTS; i++) {
        slot_t *s = &p->slots[i];
        if (!s->used) { return s; }
        if (s->seq < oldest->seq) { oldest = s; }
    }
    return oldest;
}

static firc_ptr_state_t answer(const slot_t *s, char *out, size_t cap)
{
    size_t n = s->state == FIRC_PTR_NAME ? strlen(s->name) : 0;
    if (n + 1 > cap) {
        if (cap > 0) { out[0] = '\0'; }
        return s->state == FIRC_PTR_NAME ? FIRC_PTR_NONE : s->state;
    }
    memcpy(out, s->name, n);
    out[n] = '\0';
    return s->state;
}

firc_ptr_state_t firc_ptr_peek(const firc_ptr_t *p, const firc_ip_t *ip, int64_t now_ms, char *out, size_t cap)
{
    const slot_t *s = find(p, ip);
    if (s == NULL || expired(s, now_ms)) {
        if (cap > 0) { out[0] = '\0'; }
        return FIRC_PTR_PENDING;
    }
    return answer(s, out, cap);
}

static void settle(slot_t *s, firc_ptr_state_t state, int64_t keep_ms)
{
    stop_timer(s);
    s->state = state;
    s->keep_ms = keep_ms;
}

static bool same_name(const uint8_t *a, size_t a_len, const uint8_t *b, size_t b_len)
{
    if (a_len != b_len) { return false; }
    for (size_t i = 0; i < a_len; i++) {
        uint8_t x = a[i] >= 'A' && a[i] <= 'Z' ? (uint8_t)(a[i] + 32) : a[i];
        uint8_t y = b[i] >= 'A' && b[i] <= 'Z' ? (uint8_t)(b[i] + 32) : b[i];
        if (x != y) { return false; }
    }
    return true;
}

static slot_t *pending_by_id(firc_ptr_t *p, uint16_t id)
{
    for (size_t i = 0; i < SLOTS; i++) {
        slot_t *s = &p->slots[i];
        if (s->used && s->state == FIRC_PTR_PENDING && s->id == id) { return s; }
    }
    return NULL;
}

static bool target_of(const firc_dns_msg_t *msg, char *out, size_t cap)
{
    for (size_t i = 0; i < msg->n_answers; i++) {
        const firc_dns_rr_t *rr = &msg->answers[i];
        if (rr->rtype != FIRC_DNS_TYPE_PTR || rr->rclass != FIRC_DNS_CLASS_IN) { continue; }
        char buf[4 * FIRC_DNS_MAX_NAME + 2];
        size_t n = 0;
        if (firc_dns_name_to_string(rr->rdata, rr->rdata_len, buf, sizeof(buf), &n) != FIRC_OK) { return false; }
        if (n > 0 && buf[n - 1] == '.') { buf[--n] = '\0'; }
        if (n == 0 || n + 1 > cap) { return false; }
        for (size_t k = 0; k < n; k++) {
            if (buf[k] >= 'A' && buf[k] <= 'Z') { buf[k] = (char)(buf[k] - 'A' + 'a'); }
        }
        memcpy(out, buf, n + 1);
        return true;
    }
    return false;
}

static void take_reply(firc_ptr_t *p, const uint8_t *buf, size_t len)
{
    firc_dns_msg_t *msg = NULL;
    if (firc_dns_msg_parse(buf, len, &msg) != FIRC_OK) {
        slot_t *s = len >= 2 ? pending_by_id(p, (uint16_t)(((unsigned)buf[0] << 8u) | (unsigned)buf[1])) : NULL;
        if (s != NULL) { settle(s, FIRC_PTR_NONE, RETRY_MS); }
        return;
    }
    slot_t *s = pending_by_id(p, msg->id);
    if (s == NULL || ((unsigned)msg->flags & (unsigned)FIRC_DNS_FLAG_QR) == 0u || msg->n_questions != 1 ||
        msg->questions[0].qtype != FIRC_DNS_TYPE_PTR || msg->questions[0].qclass != FIRC_DNS_CLASS_IN ||
        !same_name(msg->questions[0].name, msg->questions[0].name_len, s->qname, s->qname_len)) {
        firc_dns_msg_free(msg);
        return;
    }
    unsigned rcode = (unsigned)msg->flags & 0x000fu;
    if (rcode != 0u && rcode != FIRC_DNS_RCODE_NXDOMAIN) {
        settle(s, FIRC_PTR_NONE, RETRY_MS);
    } else {
        settle(s, rcode == 0u && target_of(msg, s->name, sizeof(s->name)) ? FIRC_PTR_NAME : FIRC_PTR_NONE, KEEP_MS);
    }
    firc_dns_msg_free(msg);
}

static void on_readable(firc_loop_t *loop, int fd, uint32_t events, void *ud)
{
    (void)loop;
    (void)events;
    uint8_t buf[4096];
    for (;;) {
        ssize_t n = recv(fd, buf, sizeof(buf), MSG_DONTWAIT);
        if (n < 0 && errno == EINTR) { continue; }
        if (n < 0) { return; }
        take_reply(ud, buf, (size_t)n);
    }
}

static void on_timeout(firc_loop_t *loop, void *ud)
{
    (void)loop;
    slot_t *s = ud;
    s->timer = -1;
    if (s->state == FIRC_PTR_PENDING) {
        s->state = FIRC_PTR_NONE;
        s->keep_ms = RETRY_MS;
    }
}

static bool open_socket(firc_ptr_t *p, int family)
{
    if (p->fd >= 0 && p->family == family) { return true; }
    close_socket(p);
    int fd = socket(family, SOCK_DGRAM | SOCK_NONBLOCK | SOCK_CLOEXEC, 0);
    if (fd < 0) { return false; }
    if (firc_loop_add_fd(p->loop, fd, EPOLLIN, on_readable, p) != FIRC_OK) {
        close(fd);
        return false;
    }
    p->fd = fd;
    p->family = family;
    return true;
}

static bool send_query(firc_ptr_t *p, slot_t *s, const char *upstream, uint16_t port, bool *sent)
{
    struct sockaddr_storage ss;
    memset(&ss, 0, sizeof(ss));
    socklen_t ss_len;
    struct sockaddr_in *sin = (struct sockaddr_in *)&ss;
    struct sockaddr_in6 *sin6 = (struct sockaddr_in6 *)&ss;
    if (inet_pton(AF_INET, upstream, &sin->sin_addr) == 1) {
        sin->sin_family = AF_INET;
        sin->sin_port = htons(port);
        ss_len = sizeof(*sin);
    } else if (inet_pton(AF_INET6, upstream, &sin6->sin6_addr) == 1) {
        sin6->sin6_family = AF_INET6;
        sin6->sin6_port = htons(port);
        ss_len = sizeof(*sin6);
    } else {
        return false;
    }

    char text[80];
    uint8_t id[2];
    if (!firc_ptr_name_for(&s->ip, text, sizeof(text)) || !firc_dns_name_from_text(text, s->qname, &s->qname_len) ||
        firc_random_bytes(id, sizeof(id)) != FIRC_OK) {
        return false;
    }
    s->id = (uint16_t)(((unsigned)id[0] << 8u) | (unsigned)id[1]);

    uint8_t q[QUERY_MAX];
    memset(q, 0, FIRC_DNS_HEADER_LEN);
    q[0] = id[0];
    q[1] = id[1];
    q[2] = (uint8_t)((unsigned)FIRC_DNS_FLAG_RD >> 8u);
    q[5] = 1;
    size_t len = FIRC_DNS_HEADER_LEN;
    memcpy(q + len, s->qname, s->qname_len);
    len += s->qname_len;
    q[len++] = 0;
    q[len++] = FIRC_DNS_TYPE_PTR;
    q[len++] = 0;
    q[len++] = FIRC_DNS_CLASS_IN;

    if (!open_socket(p, ss.ss_family)) { return false; }
    if (sendto(p->fd, q, len, 0, (const struct sockaddr *)&ss, ss_len) != (ssize_t)len) { return false; }
    *sent = true;
    return firc_loop_add_timer(p->loop, p->timeout_ms, 0, on_timeout, s, &s->timer) == FIRC_OK;
}

firc_ptr_state_t firc_ptr_get(firc_ptr_t *p, const firc_ip_t *ip, const char *upstream, uint16_t port,
                              int64_t now_ms, char *out, size_t cap, bool *sent)
{
    bool went = false;
    if (sent != NULL) { *sent = false; }
    slot_t *s = find(p, ip);
    if (s != NULL && !expired(s, now_ms)) { return answer(s, out, cap); }
    if (s == NULL) { s = victim(p); }
    stop_timer(s);
    s->used = true;
    s->ip = *ip;
    s->seq = ++p->seq;
    s->at_ms = now_ms;
    s->name[0] = '\0';
    if (send_query(p, s, upstream, port, &went)) {
        s->state = FIRC_PTR_PENDING;
    } else {
        settle(s, FIRC_PTR_NONE, RETRY_MS);
    }
    if (sent != NULL) { *sent = went; }
    return answer(s, out, cap);
}
