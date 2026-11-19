#include "firc/dnswire.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define MAX_POINTER_JUMPS 32

typedef struct reader {
    const uint8_t *buf;
    size_t len;
    size_t pos;
} reader_t;

static bool rd_u16(reader_t *r, uint16_t *v)
{
    if (r->pos + 2 > r->len) {
        return false;
    }
    *v = (uint16_t)((uint16_t)r->buf[r->pos] << 8 |
                    (uint16_t)r->buf[r->pos + 1]);
    r->pos += 2;
    return true;
}

static bool rd_u32(reader_t *r, uint32_t *v)
{
    if (r->pos + 4 > r->len) {
        return false;
    }
    *v = (uint32_t)r->buf[r->pos] << 24 | (uint32_t)r->buf[r->pos + 1] << 16 |
         (uint32_t)r->buf[r->pos + 2] << 8 | (uint32_t)r->buf[r->pos + 3];
    r->pos += 4;
    return true;
}

static bool rd_name(reader_t *r, uint8_t *out, size_t *out_len)
{
    size_t pos = r->pos;
    size_t written = 0;
    int jumps = 0;
    size_t after_first_pointer = 0; /* 0 = none yet */

    for (;;) {
        if (pos >= r->len) {
            return false;
        }
        uint8_t len8 = r->buf[pos];
        if ((len8 & 0xC0) == 0xC0) {
            if (pos + 2 > r->len) {
                return false;
            }
            if (++jumps > MAX_POINTER_JUMPS) {
                return false;
            }
            size_t target = ((size_t)(len8 & 0x3F) << 8) | r->buf[pos + 1];
            if (after_first_pointer == 0) {
                after_first_pointer = pos + 2;
            }
            if (target >= r->len) {
                return false;
            }
            pos = target;
            continue;
        }
        if ((len8 & 0xC0) != 0) {
            return false;
        }
        if (len8 == 0) {
            if (written + 1 > FIRC_DNS_MAX_NAME) {
                return false;
            }
            out[written++] = 0;
            *out_len = written;
            r->pos = after_first_pointer != 0 ? after_first_pointer : pos + 1;
            return true;
        }
        if (pos + 1 + len8 > r->len) {
            return false;
        }
        if (written + 1 + len8 + 1 > FIRC_DNS_MAX_NAME + 1) {
            return false;
        }
        out[written++] = len8;
        memcpy(out + written, r->buf + pos + 1, len8);
        written += len8;
        pos += 1 + (size_t)len8;
    }
}

static bool type_has_compressible_rdata(uint16_t t)
{
    switch (t) {
    case FIRC_DNS_TYPE_NS:
    case FIRC_DNS_TYPE_CNAME:
    case FIRC_DNS_TYPE_SOA:
    case 7:  /* MB */
    case 8:  /* MG */
    case 9:  /* MR */
    case FIRC_DNS_TYPE_PTR:
    case 14: /* MINFO */
    case FIRC_DNS_TYPE_MX:
    case 17: /* RP */
    case 18: /* AFSDB */
    case 21: /* RT */
    case FIRC_DNS_TYPE_SRV:
    case 36: /* KX */
    case 39: /* DNAME */
        return true;
    default:
        return false;
    }
}

static bool canonicalize_rdata(reader_t *r, size_t rd_end, uint16_t rtype,
                               uint8_t **out, size_t *out_len)
{
    uint8_t tmp[2 * (FIRC_DNS_MAX_NAME + 1) + 20];
    size_t w = 0;

    size_t fixed = 0;
    switch (rtype) {
    case FIRC_DNS_TYPE_MX:
    case 18: /* AFSDB */
    case 21: /* RT */
    case 36: /* KX */
        fixed = 2;
        break;
    case FIRC_DNS_TYPE_SRV:
        fixed = 6;
        break;
    default:
        fixed = 0;
        break;
    }
    if (r->pos + fixed > rd_end) {
        return false;
    }
    memcpy(tmp + w, r->buf + r->pos, fixed);
    w += fixed;
    r->pos += fixed;

    int names = 1;
    if (rtype == FIRC_DNS_TYPE_SOA || rtype == 14 /*MINFO*/ ||
        rtype == 17 /*RP*/) {
        names = 2;
    }
    for (int i = 0; i < names; i++) {
        uint8_t name[FIRC_DNS_MAX_NAME + 1];
        size_t name_len = 0;
        if (!rd_name(r, name, &name_len) || r->pos > rd_end) {
            return false;
        }
        memcpy(tmp + w, name, name_len);
        w += name_len;
    }

    if (rtype == FIRC_DNS_TYPE_SOA) {
        if (r->pos + 20 > rd_end) {
            return false;
        }
        memcpy(tmp + w, r->buf + r->pos, 20);
        w += 20;
        r->pos += 20;
    }

    if (r->pos != rd_end) {
        return false;
    }

    *out = malloc(w > 0 ? w : 1);
    if (*out == NULL) {
        return false;
    }
    memcpy(*out, tmp, w);
    *out_len = w;
    return true;
}

static bool parse_rr(reader_t *r, firc_dns_rr_t *rr)
{
    memset(rr, 0, sizeof(*rr));
    if (!rd_name(r, rr->name, &rr->name_len)) {
        return false;
    }
    uint16_t rdlength;
    if (!rd_u16(r, &rr->rtype) || !rd_u16(r, &rr->rclass) ||
        !rd_u32(r, &rr->ttl) || !rd_u16(r, &rdlength)) {
        return false;
    }
    if (r->pos + rdlength > r->len) {
        return false;
    }
    size_t rd_end = r->pos + rdlength;

    if (type_has_compressible_rdata(rr->rtype)) {
        if (!canonicalize_rdata(r, rd_end, rr->rtype, &rr->rdata,
                                &rr->rdata_len)) {
            return false;
        }
    } else {
        rr->rdata = malloc(rdlength > 0 ? rdlength : 1);
        if (rr->rdata == NULL) {
            return false;
        }
        memcpy(rr->rdata, r->buf + r->pos, rdlength);
        rr->rdata_len = rdlength;
        r->pos = rd_end;
    }
    return true;
}

static bool parse_rr_section(reader_t *r, size_t count, firc_dns_rr_t **out)
{
    if (count == 0) {
        *out = NULL;
        return true;
    }
    firc_dns_rr_t *rrs = calloc(count, sizeof(firc_dns_rr_t));
    if (rrs == NULL) {
        return false;
    }
    for (size_t i = 0; i < count; i++) {
        if (!parse_rr(r, &rrs[i])) {
            for (size_t j = 0; j <= i; j++) {
                free(rrs[j].rdata);
            }
            free(rrs);
            return false;
        }
    }
    *out = rrs;
    return true;
}

firc_err_t firc_dns_msg_parse(const uint8_t *buf, size_t len, firc_dns_msg_t **out)
{
    if (len < FIRC_DNS_HEADER_LEN || len > FIRC_DNS_MAX_MSG) {
        return FIRC_ERR_PROTO;
    }
    reader_t r = {buf, len, 0};

    firc_dns_msg_t *msg = calloc(1, sizeof(*msg));
    if (msg == NULL) {
        return FIRC_ERR_NOMEM;
    }

    uint16_t qd, an, ns, ar;
    if (!rd_u16(&r, &msg->id) || !rd_u16(&r, &msg->flags) ||
        !rd_u16(&r, &qd) || !rd_u16(&r, &an) || !rd_u16(&r, &ns) ||
        !rd_u16(&r, &ar)) {
        free(msg);
        return FIRC_ERR_PROTO;
    }

    /* A header-only message (some servers send one on REFUSED) parses with empty sections. */
    if (r.pos == len) {
        *out = msg;
        return FIRC_OK;
    }

    firc_err_t err = FIRC_ERR_PROTO;
    if (qd > 0) {
        msg->questions = calloc(qd, sizeof(firc_dns_question_t));
        if (msg->questions == NULL) {
            err = FIRC_ERR_NOMEM;
            goto fail;
        }
        for (size_t i = 0; i < qd; i++) {
            firc_dns_question_t *q = &msg->questions[i];
            if (!rd_name(&r, q->name, &q->name_len) ||
                !rd_u16(&r, &q->qtype) || !rd_u16(&r, &q->qclass)) {
                goto fail;
            }
            msg->n_questions = i + 1;
        }
    }

    if (!parse_rr_section(&r, an, &msg->answers)) {
        goto fail;
    }
    msg->n_answers = an;
    if (!parse_rr_section(&r, ns, &msg->authority)) {
        goto fail;
    }
    msg->n_authority = ns;
    if (!parse_rr_section(&r, ar, &msg->additional)) {
        goto fail;
    }
    msg->n_additional = ar;

    /* Trailing bytes after the last section are tolerated. */
    *out = msg;
    return FIRC_OK;

fail:
    firc_dns_msg_free(msg);
    return err;
}

void firc_dns_msg_free(firc_dns_msg_t *msg)
{
    if (msg == NULL) {
        return;
    }
    free(msg->questions);
    for (size_t i = 0; i < msg->n_answers; i++) {
        free(msg->answers[i].rdata);
    }
    free(msg->answers);
    for (size_t i = 0; i < msg->n_authority; i++) {
        free(msg->authority[i].rdata);
    }
    free(msg->authority);
    for (size_t i = 0; i < msg->n_additional; i++) {
        free(msg->additional[i].rdata);
    }
    free(msg->additional);
    free(msg);
}

typedef struct writer {
    uint8_t *buf;
    size_t len;
    size_t cap;
} writer_t;

static bool wr_bytes(writer_t *w, const uint8_t *data, size_t n)
{
    if (w->len + n > w->cap) {
        size_t cap = w->cap == 0 ? 512 : w->cap;
        while (w->len + n > cap) {
            cap *= 2;
        }
        uint8_t *grown = realloc(w->buf, cap);
        if (grown == NULL) {
            return false;
        }
        w->buf = grown;
        w->cap = cap;
    }
    memcpy(w->buf + w->len, data, n);
    w->len += n;
    return true;
}

static bool wr_u16(writer_t *w, uint16_t v)
{
    uint8_t b[2] = {(uint8_t)(v >> 8), (uint8_t)v};
    return wr_bytes(w, b, 2);
}

static bool wr_u32(writer_t *w, uint32_t v)
{
    uint8_t b[4] = {(uint8_t)(v >> 24), (uint8_t)(v >> 16),
                    (uint8_t)(v >> 8), (uint8_t)v};
    return wr_bytes(w, b, 4);
}

/* Owner names only: rdata is never compressed (RFC 3597). */
#define NAMES_MAX 128

typedef struct name_table {
    const uint8_t *name[NAMES_MAX];
    size_t len[NAMES_MAX];
    size_t off[NAMES_MAX];
    size_t n;
} name_table_t;

static bool suffix_offset(const name_table_t *t, size_t e, const uint8_t *suffix, size_t slen,
                          size_t *out)
{
    size_t j = 0;
    while (j < t->len[e]) {
        size_t rest = t->len[e] - j;
        if (rest == slen && memcmp(t->name[e] + j, suffix, slen) == 0) {
            *out = t->off[e] + j;
            return true;
        }
        if (rest < slen || t->name[e][j] == 0) { return false; }
        j += 1u + t->name[e][j];
    }
    return false;
}

static bool wr_name(writer_t *w, name_table_t *t, const uint8_t *name, size_t len)
{
    size_t start = w->len;
    size_t pos = 0;
    while (pos < len && name[pos] != 0) {
        for (size_t e = 0; e < t->n; e++) {
            size_t off;
            if (suffix_offset(t, e, name + pos, len - pos, &off) && off < 0x4000) {
                if (!wr_bytes(w, name, pos) || !wr_u16(w, (uint16_t)(0xC000 | off))) { return false; }
                goto remember;
            }
        }
        pos += 1u + name[pos];
    }
    if (!wr_bytes(w, name, len)) { return false; }
remember:
    if (t->n < NAMES_MAX && start < 0x4000) {
        t->name[t->n] = name;
        t->len[t->n] = len;
        t->off[t->n] = start;
        t->n++;
    }
    return true;
}

static bool wr_rr(writer_t *w, name_table_t *t, const firc_dns_rr_t *rr)
{
    if (!wr_name(w, t, rr->name, rr->name_len) || !wr_u16(w, rr->rtype) ||
        !wr_u16(w, rr->rclass) || !wr_u32(w, rr->ttl)) {
        return false;
    }
    if (rr->rdata_len > UINT16_MAX) {
        return false;
    }
    if (!wr_u16(w, (uint16_t)rr->rdata_len)) {
        return false;
    }
    if (rr->rdata_len == 0) { return true; } /* rdata may be NULL */
    return wr_bytes(w, rr->rdata, rr->rdata_len);
}

firc_err_t firc_dns_msg_pack(const firc_dns_msg_t *msg, uint8_t **out,
                         size_t *out_len)
{
    writer_t w = {NULL, 0, 0};
    name_table_t names = {{0}, {0}, {0}, 0};
    bool ok = wr_u16(&w, msg->id) && wr_u16(&w, msg->flags) &&
              wr_u16(&w, (uint16_t)msg->n_questions) &&
              wr_u16(&w, (uint16_t)msg->n_answers) &&
              wr_u16(&w, (uint16_t)msg->n_authority) &&
              wr_u16(&w, (uint16_t)msg->n_additional);
    for (size_t i = 0; ok && i < msg->n_questions; i++) {
        const firc_dns_question_t *q = &msg->questions[i];
        ok = wr_name(&w, &names, q->name, q->name_len) && wr_u16(&w, q->qtype) &&
             wr_u16(&w, q->qclass);
    }
    for (size_t i = 0; ok && i < msg->n_answers; i++) {
        ok = wr_rr(&w, &names, &msg->answers[i]);
    }
    for (size_t i = 0; ok && i < msg->n_authority; i++) {
        ok = wr_rr(&w, &names, &msg->authority[i]);
    }
    for (size_t i = 0; ok && i < msg->n_additional; i++) {
        ok = wr_rr(&w, &names, &msg->additional[i]);
    }
    if (!ok || w.len > FIRC_DNS_MAX_MSG) {
        free(w.buf);
        return !ok ? FIRC_ERR_NOMEM : FIRC_ERR_LIMIT;
    }
    *out = w.buf;
    *out_len = w.len;
    return FIRC_OK;
}

bool firc_dns_msg_queried_name(const firc_dns_msg_t *msg, char *out, size_t out_len) {
    if (msg == NULL || out == NULL || out_len == 0 || msg->n_questions == 0) { return false; }

    char buf[FIRC_DNS_MAX_NAME * 4 + 2];
    size_t n = 0;
    if (firc_dns_name_to_string(msg->questions[0].name, msg->questions[0].name_len, buf,
                                sizeof(buf), &n) != FIRC_OK) {
        return false;
    }

    if (n > 0 && buf[n - 1] == '.') { buf[--n] = '\0'; }
    if (n == 0) { return false; }

    for (size_t i = 0; i < n; i++) {
        /* An escaped dot is still a dot to the namespace matcher: refuse the name. */
        if (buf[i] == '\\') { return false; }
        /* Fold ASCII only: the matchers and the operator's rules compare bytes. */
        if (buf[i] >= 'A' && buf[i] <= 'Z') { buf[i] = (char)(buf[i] - 'A' + 'a'); }
    }

    /* Refuse rather than truncate: a prefix of a name is another domain. */
    if (n + 1 > out_len) { return false; }
    memcpy(out, buf, n + 1);
    return true;
}

bool firc_dns_msg_clamp_ttl(firc_dns_msg_t *msg, uint32_t max_ttl) {
    bool lowered = false;
    for (size_t i = 0; i < msg->n_answers; i++) {
        if (msg->answers[i].ttl > max_ttl) {
            msg->answers[i].ttl = max_ttl;
            lowered = true;
        }
    }
    return lowered;
}

bool firc_dns_msg_strip_aaaa(firc_dns_msg_t *msg)
{
    size_t kept = 0;
    for (size_t i = 0; i < msg->n_answers; i++) {
        if (msg->answers[i].rtype == FIRC_DNS_TYPE_AAAA) {
            free(msg->answers[i].rdata);
            continue;
        }
        if (kept != i) {
            msg->answers[kept] = msg->answers[i];
        }
        kept++;
    }
    bool removed = kept != msg->n_answers;
    msg->n_answers = kept;
    return removed;
}

static void drop_rrs(firc_dns_rr_t *rrs, size_t *n, bool keep_opt)
{
    size_t kept = 0;
    for (size_t i = 0; i < *n; i++) {
        if (keep_opt && rrs[i].rtype == FIRC_DNS_TYPE_OPT) {
            if (kept != i) { rrs[kept] = rrs[i]; }
            kept++;
            continue;
        }
        free(rrs[i].rdata);
    }
    *n = kept;
}

void firc_dns_msg_truncate(firc_dns_msg_t *msg)
{
    msg->flags |= FIRC_DNS_FLAG_TC;
    drop_rrs(msg->answers, &msg->n_answers, false);
    drop_rrs(msg->authority, &msg->n_authority, false);
    drop_rrs(msg->additional, &msg->n_additional, true);
}

uint16_t firc_dns_msg_udp_payload_size(const firc_dns_msg_t *msg)
{
    uint16_t size = FIRC_DNS_UDP_MIN_SIZE;
    for (size_t i = 0; i < msg->n_additional; i++) {
        if (msg->additional[i].rtype != FIRC_DNS_TYPE_OPT) { continue; }
        /* The OPT class field is the UDP payload size; under 512 counts as 512. */
        if (msg->additional[i].rclass > size) { size = msg->additional[i].rclass; }
        break;
    }
    return size;
}

static int hex_nibble(uint8_t c)
{
    if (c >= '0' && c <= '9') { return c - '0'; }
    if (c >= 'a' && c <= 'f') { return c - 'a' + 10; }
    if (c >= 'A' && c <= 'F') { return c - 'A' + 10; }
    return -1;
}

static bool label_is(const uint8_t *label, const char *want)
{
    size_t n = strlen(want);
    if (label[0] != n) { return false; }
    for (size_t i = 0; i < n; i++) {
        uint8_t c = label[1 + i];
        if (c >= 'A' && c <= 'Z') { c = (uint8_t)(c + 32); }
        if (c != (uint8_t)want[i]) { return false; }
    }
    return true;
}

static bool octet_label(const uint8_t *label, uint8_t *out)
{
    size_t n = label[0];
    if (n == 0 || n > 3 || (n > 1 && label[1] == '0')) { return false; }
    unsigned v = 0;
    for (size_t i = 0; i < n; i++) {
        if (label[1 + i] < '0' || label[1 + i] > '9') { return false; }
        v = v * 10 + (unsigned)(label[1 + i] - '0');
    }
    if (v > 255) { return false; }
    *out = (uint8_t)v;
    return true;
}

static bool reverse_addr(const uint8_t *name, size_t name_len, firc_ip_t *out)
{
    const uint8_t *labels[36];
    size_t n = 0, pos = 0;
    while (pos < name_len && name[pos] != 0) {
        if (n == 36 || pos + 1 + name[pos] >= name_len) { return false; }
        labels[n++] = name + pos;
        pos += 1u + name[pos];
    }
    if (n == 6 && label_is(labels[4], "in-addr") && label_is(labels[5], "arpa")) {
        firc_ip_t ip = {{0}, 4};
        for (size_t i = 0; i < 4; i++) {
            if (!octet_label(labels[i], &ip.b[3 - i])) { return false; }
        }
        *out = ip;
        return true;
    }
    if (n == 34 && label_is(labels[32], "ip6") && label_is(labels[33], "arpa")) {
        firc_ip_t ip = {{0}, 16};
        for (size_t i = 0; i < 32; i++) {
            int v = labels[i][0] == 1 ? hex_nibble(labels[i][1]) : -1;
            if (v < 0) { return false; }
            ip.b[15 - i / 2] = (uint8_t)(ip.b[15 - i / 2] | (i % 2 == 0 ? v : v << 4));
        }
        *out = ip;
        return true;
    }
    return false;
}

bool firc_dns_ptr_query_addr(const firc_dns_msg_t *query, firc_ip_t *out)
{
    if (query == NULL || out == NULL || query->n_questions != 1) { return false; }
    if ((query->flags & FIRC_DNS_FLAG_QR) != 0 || ((query->flags >> 11) & 0x0fu) != 0) { return false; }
    const firc_dns_question_t *q = &query->questions[0];
    if (q->qtype != FIRC_DNS_TYPE_PTR || q->qclass != FIRC_DNS_CLASS_IN) { return false; }
    return reverse_addr(q->name, q->name_len, out);
}

bool firc_dns_name_from_text(const char *text, uint8_t *out, size_t *out_len)
{
    size_t len = 0;
    const char *p = text;
    for (;;) {
        const char *dot = strchr(p, '.');
        size_t n = dot != NULL ? (size_t)(dot - p) : strlen(p);
        if (n == 0 || n > 63 || len + 1 + n + 1 > FIRC_DNS_MAX_NAME) { return false; }
        out[len++] = (uint8_t)n;
        memcpy(out + len, p, n);
        len += n;
        if (dot == NULL) { break; }
        p = dot + 1;
    }
    out[len++] = 0;
    *out_len = len;
    return true;
}

firc_err_t firc_dns_make_ptr_response(const firc_dns_msg_t *query, const char *target, uint32_t ttl,
                                      uint8_t **out, size_t *out_len)
{
    if (query == NULL || out == NULL || out_len == NULL || query->n_questions != 1) {
        return FIRC_ERR_INVAL;
    }
    uint8_t rdata[FIRC_DNS_MAX_NAME + 1];
    size_t rdata_len = 0;
    if (target != NULL && !firc_dns_name_from_text(target, rdata, &rdata_len)) { return FIRC_ERR_INVAL; }
    bool edns = false;
    for (size_t i = 0; i < query->n_additional; i++) {
        if (query->additional[i].rtype == FIRC_DNS_TYPE_OPT) { edns = true; }
    }
    const firc_dns_question_t *q = &query->questions[0];
    uint16_t flags = (uint16_t)(FIRC_DNS_FLAG_QR | FIRC_DNS_FLAG_RA | (query->flags & FIRC_DNS_FLAG_RD) |
                                (target == NULL ? FIRC_DNS_RCODE_NXDOMAIN : 0));
    writer_t w = {NULL, 0, 0};
    bool ok = wr_u16(&w, query->id) && wr_u16(&w, flags) && wr_u16(&w, 1) &&
              wr_u16(&w, target != NULL ? 1 : 0) && wr_u16(&w, 0) && wr_u16(&w, edns ? 1 : 0) &&
              wr_bytes(&w, q->name, q->name_len) && wr_u16(&w, q->qtype) && wr_u16(&w, q->qclass);
    if (ok && target != NULL) {
        ok = wr_u16(&w, 0xC000 | FIRC_DNS_HEADER_LEN) && wr_u16(&w, FIRC_DNS_TYPE_PTR) &&
             wr_u16(&w, FIRC_DNS_CLASS_IN) && wr_u32(&w, ttl) && wr_u16(&w, (uint16_t)rdata_len) &&
             wr_bytes(&w, rdata, rdata_len);
    }
    if (ok && edns) {
        const uint8_t opt[11] = {0, 0, FIRC_DNS_TYPE_OPT, (uint8_t)(FIRC_DNS_EDNS_SIZE >> 8),
                                 (uint8_t)FIRC_DNS_EDNS_SIZE, 0, 0, 0, 0, 0, 0};
        ok = wr_bytes(&w, opt, sizeof(opt));
    }
    if (!ok) {
        free(w.buf);
        return FIRC_ERR_NOMEM;
    }
    *out = w.buf;
    *out_len = w.len;
    return FIRC_OK;
}

uint8_t firc_dns_block_rcode(firc_block_answer_t answer)
{
    switch (answer) {
    case FIRC_BLOCK_NXDOMAIN: return FIRC_DNS_RCODE_NXDOMAIN;
    case FIRC_BLOCK_REFUSED: return 5;
    case FIRC_BLOCK_NULL: break;
    }
    return 0;
}

bool firc_dns_blockable(const firc_dns_msg_t *msg)
{
    return msg != NULL && msg->n_questions == 1 && msg->questions != NULL && ((msg->flags >> 11) & 0x0fu) == 0 &&
           msg->questions[0].qclass == FIRC_DNS_CLASS_IN;
}

static bool has_opt(const firc_dns_msg_t *msg)
{
    for (size_t i = 0; i < msg->n_additional; i++) {
        if (msg->additional[i].rtype == FIRC_DNS_TYPE_OPT) { return true; }
    }
    return false;
}

static void free_section(firc_dns_rr_t **rrs, size_t *n)
{
    for (size_t i = 0; i < *n; i++) { free((*rrs)[i].rdata); }
    free(*rrs);
    *rrs = NULL;
    *n = 0;
}

static firc_err_t block_msg(firc_dns_msg_t *msg, firc_block_answer_t answer, uint32_t ttl, bool edns)
{
    if (!firc_dns_blockable(msg)) { return FIRC_ERR_INVAL; }
    const firc_dns_question_t *q = &msg->questions[0];
    size_t addr_len = 0;
    if (answer == FIRC_BLOCK_NULL && q->qtype == FIRC_DNS_TYPE_A) { addr_len = 4; }
    if (answer == FIRC_BLOCK_NULL && q->qtype == FIRC_DNS_TYPE_AAAA) { addr_len = 16; }
    firc_dns_rr_t *an = NULL, *ad = NULL;
    if (addr_len > 0) {
        an = calloc(1, sizeof(*an));
        uint8_t *rd = calloc(1, addr_len);
        if (an == NULL || rd == NULL) {
            free(an);
            free(rd);
            return FIRC_ERR_NOMEM;
        }
        memcpy(an->name, q->name, q->name_len);
        an->name_len = q->name_len;
        an->rtype = q->qtype;
        an->rclass = FIRC_DNS_CLASS_IN;
        an->ttl = ttl;
        an->rdata = rd;
        an->rdata_len = addr_len;
    }
    if (edns) {
        ad = calloc(1, sizeof(*ad));
        if (ad == NULL) {
            if (an != NULL) { free(an->rdata); }
            free(an);
            return FIRC_ERR_NOMEM;
        }
        ad->name_len = 1;
        ad->rtype = FIRC_DNS_TYPE_OPT;
        ad->rclass = FIRC_DNS_EDNS_SIZE;
    }
    free_section(&msg->answers, &msg->n_answers);
    free_section(&msg->authority, &msg->n_authority);
    free_section(&msg->additional, &msg->n_additional);
    msg->flags = (uint16_t)(FIRC_DNS_FLAG_QR | FIRC_DNS_FLAG_RA | (msg->flags & FIRC_DNS_FLAG_RD) |
                            firc_dns_block_rcode(answer));
    msg->answers = an;
    msg->n_answers = an != NULL ? 1 : 0;
    msg->additional = ad;
    msg->n_additional = ad != NULL ? 1 : 0;
    return FIRC_OK;
}

firc_err_t firc_dns_msg_block(firc_dns_msg_t *msg, firc_block_answer_t answer, uint32_t ttl)
{
    if (msg == NULL) { return FIRC_ERR_INVAL; }
    return block_msg(msg, answer, ttl, has_opt(msg));
}

firc_err_t firc_dns_make_block_response(const firc_dns_msg_t *query, firc_block_answer_t answer, uint32_t ttl,
                                        uint8_t **out, size_t *out_len)
{
    if (query == NULL || out == NULL || out_len == NULL || query->n_questions != 1) { return FIRC_ERR_INVAL; }
    firc_dns_msg_t m;
    memset(&m, 0, sizeof(m));
    m.id = query->id;
    m.flags = query->flags;
    m.questions = query->questions;
    m.n_questions = 1;
    firc_err_t err = block_msg(&m, answer, ttl, has_opt(query));
    if (err == FIRC_OK) { err = firc_dns_msg_pack(&m, out, out_len); }
    free_section(&m.answers, &m.n_answers);
    free_section(&m.additional, &m.n_additional);
    return err;
}

firc_err_t firc_dns_name_to_string(const uint8_t *name, size_t name_len,
                               char *buf, size_t buf_len, size_t *out_written)
{
    size_t w = 0;
    size_t pos = 0;
    bool any_label = false;

#define PUTC(c)                          \
    do {                                 \
        if (w + 1 >= buf_len) {          \
            return FIRC_ERR_LIMIT;         \
        }                                \
        buf[w++] = (c);                  \
    } while (0)

    while (pos < name_len) {
        uint8_t len8 = name[pos++];
        if (len8 == 0) {
            break;
        }
        if (pos + len8 > name_len) {
            return FIRC_ERR_PROTO;
        }
        any_label = true;
        for (size_t i = 0; i < len8; i++) {
            uint8_t b = name[pos + i];
            if (b == '.' || b == '(' || b == ')' || b == ';' || b == ' ' ||
                b == '@' || b == '"' || b == '\\') {
                PUTC('\\');
                PUTC((char)b);
            } else if (b < 32 || b >= 127) {
                char esc[5];
                snprintf(esc, sizeof(esc), "\\%03u", b);
                for (int k = 0; k < 4; k++) {
                    PUTC(esc[k]);
                }
            } else {
                PUTC((char)b);
            }
        }
        pos += len8;
        PUTC('.');
    }
    if (!any_label) {
        PUTC('.');
    }
    buf[w] = '\0';
    if (out_written != NULL) { *out_written = w; }
    return FIRC_OK;
#undef PUTC
}
