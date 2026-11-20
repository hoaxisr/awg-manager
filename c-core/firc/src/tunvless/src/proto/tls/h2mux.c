#include <stdlib.h>
#include <string.h>

#include "h2mux.h"
#include "tscratch.h"

#define ST_LIVE      0x01
#define ST_LOCAL_END 0x02
#define ST_STATUS    0x04

#define NO_SLOT  UINT16_MAX
#define WIN_MAX  0x7FFFFFFF
#define ST_FIRST 16
#define CAP_PRE  100

#define E_FLOW_CONTROL 3
#define E_NO_ERROR     0
#define E_CANCEL       8

#define FRAME_MAX 16777215

static struct h2mux_scratch *scratch(void) {
    return ts_get(TS_H2MUX, sizeof(struct h2mux_scratch));
}

static int die(struct h2m *m, int rc) {
    if (!m->dead) m->dead = rc;
    return m->dead;
}

static int emit(struct h2m *m, unsigned char type, unsigned char flags, uint32_t sid,
                const unsigned char *body, size_t n) {
    struct h2mux_scratch *s = scratch();
    if (!s) return die(m, H2_EIO);
    h2w_frame_hdr(s->frame, (uint32_t)n, type, flags, sid);
    if (n) memcpy(s->frame + 9, body, n);
    int rc = m->cb.out(m->cb.ctx, s->frame, 9 + n, H2M_CTL | H2M_END);
    return rc ? die(m, rc) : 0;
}

static int emit_u32(struct h2m *m, unsigned char type, uint32_t sid, uint32_t v) {
    unsigned char b[4];
    put32(b, v);
    return emit(m, type, 0, sid, b, 4);
}

static int emit_data(struct h2m *m, unsigned char flags, uint32_t sid, const unsigned char *d, size_t n) {
    unsigned char h[9];
    h2w_frame_hdr(h, (uint32_t)n, FR_DATA, flags, sid);
    int rc = m->cb.out(m->cb.ctx, h, 9, n ? 0u : H2M_END);
    if (!rc && n) rc = m->cb.out(m->cb.ctx, d, n, H2M_PAY | H2M_END);
    return rc ? die(m, rc) : 0;
}

static int live(const struct h2m *m, uint16_t slot) {
    return slot < m->st_cap && (m->st[slot].state & ST_LIVE);
}

static uint16_t slot_of(const struct h2m *m, uint32_t sid) {
    if (!sid) return NO_SLOT;
    for (uint16_t i = 0; i < m->st_cap; i++)
        if ((m->st[i].state & ST_LIVE) && m->st[i].sid == sid) return i;
    return NO_SLOT;
}

uint32_t h2m_in_add(struct h2m_in *f, uint32_t n) {
    int64_t u = (int64_t)f->unsent + n;
    if (u + f->avail > WIN_MAX) u = (int64_t)WIN_MAX - f->avail;
    f->unsent = (int32_t)u;
    if (f->unsent < H2M_MIN_REFRESH && f->unsent < f->avail) return 0;
    f->avail += f->unsent;
    f->unsent = 0;
    return (uint32_t)u;
}

static void in_keep(struct h2m_in *f, uint32_t n) {
    int64_t u = (int64_t)f->unsent + n;
    f->unsent = u + f->avail > WIN_MAX ? WIN_MAX - f->avail : (int32_t)u;
}

static void credit_out(struct h2m *m, uint32_t c, uint32_t sid, uint32_t s) {
    unsigned char b[26];
    size_t n = 0;
    if (c) {
        h2w_frame_hdr(b, 4, FR_WINDOW_UPDATE, 0, 0);
        put32(b + 9, c);
        n = 13;
    }
    if (s) {
        h2w_frame_hdr(b + n, 4, FR_WINDOW_UPDATE, 0, sid);
        put32(b + n + 9, s);
        n += 13;
    }
    if (!n) return;
    int rc = m->cb.out(m->cb.ctx, b, n, H2M_CTL | H2M_END);
    if (rc) die(m, rc);
}

static void free_slot(struct h2m *m, uint16_t slot) {
    m->st[slot].state = 0;
    m->open_n--;
    if (m->f_slot != slot) return;
    m->f_slot = NO_SLOT;
    if (m->f_type == FR_DATA && !m->paid) {
        m->paid = 1;
        m->f_drop += m->f_pad + (m->f_left > m->pad_left ? m->f_left - m->pad_left : 0);
        m->f_pad = 0;
    }
}

static void end_stream(struct h2m *m, uint16_t slot, int code) {
    struct h2m_in in = m->st[slot].in;
    free_slot(m, slot);
    m->cb.end(m->cb.ctx, slot, code, &in);
}

static int flow_reset(struct h2m *m, uint16_t slot) {
    int rc = emit_u32(m, FR_RST_STREAM, m->st[slot].sid, E_FLOW_CONTROL);
    end_stream(m, slot, H2_EFLOW);
    return rc;
}

static int remote_end(struct h2m *m, uint16_t slot) {
    int rc = 0;
    if (!(m->st[slot].state & ST_LOCAL_END))
        rc = emit_u32(m, FR_RST_STREAM, m->st[slot].sid, E_NO_ERROR);
    end_stream(m, slot, 0);
    return rc;
}

static int grow(struct h2m *m) {
    if (m->st_cap >= H2M_STREAMS) return -1;
    uint16_t cap = (uint16_t)(m->st_cap * 2);
    struct h2m_stream *st = realloc(m->st, cap * sizeof(*st));
    if (!st) return -1;
    memset(st + m->st_cap, 0, (size_t)(cap - m->st_cap) * sizeof(*st));
    m->st = st;
    m->st_cap = cap;
    return 0;
}

int h2m_start(struct h2m *m, const struct h2m_cb *cb, uint64_t seed) {
    memset(m, 0, sizeof(*m));
    struct h2mux_scratch *s = scratch();
    if (!s) return H2_EIO;
    m->cb = *cb;
    m->rng = seed;
    m->peer_max_frame = DEFAULT_MAX_FRAME;
    hpe_init(&m->enc);
    m->st = calloc(ST_FIRST, sizeof(*m->st));
    if (!m->st) return H2_EIO;
    m->st_cap = ST_FIRST;
    m->next_sid = 1;
    m->send_win_conn = 65535;
    m->peer_init_win = 65535;
    m->in_conn.avail = 65535 + H2M_CONN_INC;
    m->f_slot = NO_SLOT;
    size_t n = h2w_go_preface(s->frame, sizeof s->frame);
    int rc = m->cb.out(m->cb.ctx, s->frame, n, H2M_END);
    return rc ? die(m, rc) : 0;
}

void h2m_free(struct h2m *m) {
    free(m->st);
    m->st = NULL;
    m->st_cap = 0;
    m->open_n = 0;
}

uint32_t h2m_cap(const struct h2m *m) {
    if (!m->peer_settings_seen) return CAP_PRE;
    if (!m->peer_max_set || m->peer_max_streams > H2M_STREAMS) return H2M_STREAMS;
    return m->peer_max_streams;
}

uint32_t h2m_header_budget(const struct h2m *m, uint32_t server_max) {
    if (!m->peer_settings_seen) return server_max && server_max < 8192 ? server_max : 8192;
    return m->peer_hdr_set ? m->peer_hdr_list : 16384;
}

unsigned h2m_open_n(const struct h2m *m) { return m->open_n; }

static int headers_out(struct h2m *m, const unsigned char *block, size_t bn, int end_stream) {
    size_t at = 0;
    unsigned char type = FR_HEADERS, es = end_stream ? FLAG_END_STREAM : 0;
    do {
        size_t k = bn - at < m->peer_max_frame ? bn - at : m->peer_max_frame;
        int last = at + k == bn;
        unsigned char h[9];
        h2w_frame_hdr(h, (uint32_t)k, type, (unsigned char)(es | (last ? FLAG_END_HEADERS : 0)), m->next_sid);
        int rc = m->cb.out(m->cb.ctx, h, 9, 0);
        if (!rc) rc = m->cb.out(m->cb.ctx, block + at, k, last ? H2M_END : 0u);
        if (rc) return die(m, rc);
        at += k;
        type = FR_CONTINUATION;
        es = 0;
    } while (at < bn);
    return 0;
}

int h2m_open(struct h2m *m, const struct h2_req *r, uint16_t *slot) {
    if (m->dead) return m->dead;
    if (m->goaway || m->next_sid > WIN_MAX) return H2_ERESET;
    if (m->open_n >= h2m_cap(m)) return H2_EFULL;
    uint16_t s = 0;
    while (s < m->st_cap && (m->st[s].state & ST_LIVE)) s++;
    if (s == m->st_cap && grow(m)) return H2_EFULL;
    struct h2mux_scratch *x = scratch();
    if (!x) return H2_EIO;
    uint64_t rng = m->rng;
    int rc = h2w_go_fields(r, &rng, &x->list);
    if (rc) return rc;
    if (hpe_bound(x->list.f, x->list.n) > H2M_BLOCK_MAX) return H2_ETOOBIG;
    size_t bn = hpe_block(&m->enc, x->list.f, x->list.n, x->block, sizeof x->block);
    if (!bn || bn > sizeof x->block) return H2_ETOOBIG;
    m->rng = rng;
    rc = headers_out(m, x->block, bn, r->end_stream);
    if (rc) return rc;
    struct h2m_stream *st = &m->st[s];
    st->sid = m->next_sid;
    st->send_win = m->peer_init_win;
    st->in = (struct h2m_in){ m->next_sid, H2M_STREAM_WIN, 0, 0 };
    st->state = (uint8_t)(ST_LIVE | (r->end_stream ? ST_LOCAL_END : 0));
    m->next_sid += 2;
    m->open_n++;
    *slot = s;
    return 0;
}

long h2m_room(const struct h2m *m, uint16_t slot) {
    if (!live(m, slot) || (m->st[slot].state & ST_LOCAL_END)) return 0;
    int32_t w = m->st[slot].send_win < m->send_win_conn ? m->st[slot].send_win : m->send_win_conn;
    return w > 0 ? w : 0;
}

long h2m_conn_room(const struct h2m *m) {
    return m->send_win_conn > 0 ? m->send_win_conn : 0;
}

int h2m_send_begin(struct h2m *m, uint16_t slot, size_t n, int end) {
    if (m->dead) return m->dead;
    if (!live(m, slot)) return H2_ERESET;
    struct h2m_stream *s = &m->st[slot];
    if (s->state & ST_LOCAL_END || m->up_left) return H2_EPROTO;
    if (n > INT32_MAX) return H2_ETOOBIG;
    if (n && ((int32_t)n > s->send_win || (int32_t)n > m->send_win_conn)) return H2_EWINDOW;
    s->send_win -= (int32_t)n;
    m->send_win_conn -= (int32_t)n;
    if (end) s->state |= ST_LOCAL_END;
    m->up_sid = s->sid;
    m->up_left = (uint32_t)n;
    m->up_frame = 0;
    m->up_end = end ? 1 : 0;
    return !n && end ? emit_data(m, FLAG_END_STREAM, s->sid, NULL, 0) : 0;
}

int h2m_send_put(struct h2m *m, const unsigned char *d, size_t k) {
    if (m->dead) return m->dead;
    if (k > m->up_left) return H2_EPROTO;
    uint32_t fmax = m->peer_max_frame < H2M_SEND_SCRATCH ? m->peer_max_frame : H2M_SEND_SCRATCH;
    while (k) {
        if (!m->up_frame) {
            unsigned char h[9];
            m->up_frame = m->up_left < fmax ? m->up_left : fmax;
            int es = m->up_end && m->up_frame == m->up_left;
            h2w_frame_hdr(h, m->up_frame, FR_DATA, es ? FLAG_END_STREAM : 0, m->up_sid);
            int rc = m->cb.out(m->cb.ctx, h, 9, 0);
            if (rc) return die(m, rc);
        }
        uint32_t c = k < m->up_frame ? (uint32_t)k : m->up_frame;
        m->up_frame -= c;
        m->up_left -= c;
        int rc = m->cb.out(m->cb.ctx, d, c, H2M_PAY | (m->up_frame ? 0u : H2M_END));
        if (rc) return die(m, rc);
        d += c;
        k -= c;
    }
    return 0;
}

int h2m_send(struct h2m *m, uint16_t slot, const unsigned char *d, size_t n, int end) {
    int rc = h2m_send_begin(m, slot, n, end);
    return rc || !n ? rc : h2m_send_put(m, d, n);
}

int h2m_reset(struct h2m *m, uint16_t slot, uint32_t code) {
    if (m->dead) return m->dead;
    if (!live(m, slot)) return H2_ERESET;
    uint32_t sid = m->st[slot].sid;
    int ping = 0;
    if (code == E_CANCEL && !m->goaway && m->read_before_sid <= sid && !m->rst_ping_block)
        ping = !m->resets_pending++;
    unsigned char b[30];
    h2w_frame_hdr(b, 4, FR_RST_STREAM, 0, sid);
    put32(b + 9, code);
    int rc;
    if (ping) {
        uint64_t v = h2w_rng(&m->rng);
        h2w_frame_hdr(b + 13, 8, FR_PING, 0, 0);
        put32(b + 22, (uint32_t)(v >> 32));
        put32(b + 26, (uint32_t)v);
        rc = m->cb.out(m->cb.ctx, b, 13, H2M_CTL);
        if (!rc) rc = m->cb.out(m->cb.ctx, b + 13, 17, H2M_CTL | H2M_END);
    } else {
        rc = m->cb.out(m->cb.ctx, b, 13, H2M_CTL | H2M_END);
    }
    free_slot(m, slot);
    return rc ? die(m, rc) : 0;
}

static struct h2m_in *inflow(struct h2m *m, uint16_t slot, struct h2m_in *ended) {
    if (live(m, slot)) return &m->st[slot].in;
    return slot == NO_SLOT ? ended : NULL;
}

void h2m_consumed(struct h2m *m, uint16_t slot, struct h2m_in *ended, uint32_t n, int give) {
    if (m->dead || !n) return;
    struct h2m_in *f = inflow(m, slot, ended);
    uint32_t k = n;
    if (f && !give) {
        f->wh += (int32_t)n;
        k = 0;
    } else if (f) {
        k += (uint32_t)f->wh;
        f->wh = 0;
    }
    if (m->hold) {
        in_keep(&m->in_conn, n);
        if (f && k) in_keep(f, k);
        return;
    }
    uint32_t c = h2m_in_add(&m->in_conn, n), s = f && k ? h2m_in_add(f, k) : 0;
    credit_out(m, c, f ? f->sid : 0, s);
}

void h2m_conn_refund(struct h2m *m, uint32_t n) {
    if (m->dead || !n) return;
    if (m->hold) {
        in_keep(&m->in_conn, n);
        return;
    }
    credit_out(m, h2m_in_add(&m->in_conn, n), 0, 0);
}

uint32_t h2m_stream_flush(struct h2m *m, uint16_t slot, struct h2m_in *ended) {
    struct h2m_in *f = inflow(m, slot, ended);
    if (!f) return 0;
    if (m->dead) return (uint32_t)f->unsent;
    in_keep(f, (uint32_t)f->wh);
    f->wh = 0;
    if (m->hold) return (uint32_t)f->unsent;
    credit_out(m, 0, f->sid, h2m_in_add(f, 0));
    return (uint32_t)f->unsent;
}

int h2m_ping(struct h2m *m, const unsigned char op[8]) {
    if (m->dead) return m->dead;
    int rc = emit(m, FR_PING, 0, 0, op, 8);
    if (rc) return rc;
    memcpy(m->ping_op, op, 8);
    m->ping_out = 1;
    return 0;
}

static int setting(struct h2m *m, const unsigned char *e) {
    unsigned id = ((unsigned)e[0] << 8) | e[1];
    uint32_t v = get32(e + 2);
    if (id == 0x01) {
        hpe_set_max(&m->enc, v);
    } else if (id == 0x05) {
        if (v < DEFAULT_MAX_FRAME || v > FRAME_MAX) return H2_EPROTO;
        m->peer_max_frame = v;
    } else if (id == 0x03) {
        m->peer_max_streams = v;
        m->peer_max_set = 1;
    } else if (id == 0x06) {
        m->peer_hdr_list = v;
        m->peer_hdr_set = 1;
    } else if (id == 0x04) {
        if (v > WIN_MAX) return H2_EFLOW;
        int64_t delta = (int64_t)v - m->peer_init_win;
        for (uint16_t i = 0; i < m->st_cap; i++) {
            if (!(m->st[i].state & ST_LIVE)) continue;
            int64_t w = (int64_t)m->st[i].send_win + delta;
            if (w > WIN_MAX) return H2_EFLOW;
            m->st[i].send_win = (int32_t)w;
        }
        if (delta > 0) m->grew = 1;
        m->peer_init_win = (int32_t)v;
    }
    return 0;
}

static int settings_end(struct h2m *m) {
    if (m->f_flags & FLAG_ACK) return 0;
    m->peer_settings_seen = 1;
    int rc = emit(m, FR_SETTINGS, FLAG_ACK, 0, NULL, 0);
    if (rc || !m->grew) return rc;
    m->grew = 0;
    for (uint16_t i = 0; i < m->st_cap; i++)
        if ((m->st[i].state & (ST_LIVE | ST_LOCAL_END)) == ST_LIVE) m->cb.room(m->cb.ctx, i);
    return 0;
}

static int window_update(struct h2m *m) {
    if (m->ctl_n < 4) return 0;
    uint32_t inc = get32(m->ctl) & 0x7FFFFFFFu;
    if (!inc) return 0;
    if (m->f_sid == 0) {
        if ((int64_t)m->send_win_conn + inc > WIN_MAX) return H2_EFLOW;
        m->send_win_conn += (int32_t)inc;
        m->cb.room(m->cb.ctx, NO_SLOT);
        return 0;
    }
    uint16_t s = slot_of(m, m->f_sid);
    if (s == NO_SLOT) return 0;
    if ((int64_t)m->st[s].send_win + inc > WIN_MAX) return flow_reset(m, s);
    m->st[s].send_win += (int32_t)inc;
    if (!(m->st[s].state & ST_LOCAL_END)) m->cb.room(m->cb.ctx, s);
    return 0;
}

static void goaway(struct h2m *m) {
    if (m->ctl_n < 8) return;
    uint32_t last = get32(m->ctl) & 0x7FFFFFFFu;
    uint32_t code = get32(m->ctl + 4);
    m->goaway = 1;
    m->goaway_last = last;
    m->cb.goaway(m->cb.ctx, last, code);
    for (uint16_t i = 0; i < m->st_cap; i++)
        if ((m->st[i].state & ST_LIVE) && m->st[i].sid > last) end_stream(m, i, H2_ERESET);
}

static int frame_begin(struct h2m *m) {
    m->f_len = m->f_left = h2w_frame_len(m->hdr);
    m->f_type = m->hdr[3];
    m->f_flags = m->hdr[4];
    m->f_sid = get32(m->hdr + 5) & 0x7FFFFFFFu;
    m->f_slot = NO_SLOT;
    m->ctl_n = 0;
    if (m->f_len > H2M_FRAME_IN) return H2_EPROTO;
    if (m->f_type == FR_DATA || m->f_type == FR_HEADERS) m->rst_ping_block = 0;
    if (m->f_type == FR_DATA || m->f_type == FR_HEADERS || m->f_type == FR_RST_STREAM ||
        (m->f_type == FR_WINDOW_UPDATE && m->f_sid))
        m->read_before_sid = m->next_sid;
    m->pad_wait = m->pad_left = m->skip_left = 0;
    m->peek = m->paid = 0;
    m->f_drop = m->f_pad = 0;
    if (m->cont && (m->f_type != FR_CONTINUATION || m->f_sid != m->cont_sid)) return H2_EPROTO;
    if (!m->cont && m->f_type == FR_CONTINUATION) return H2_EPROTO;
    switch (m->f_type) {
        case FR_DATA:
            m->pad_wait = (m->f_flags & FLAG_PADDED) != 0;
            if (m->pad_wait > m->f_len) return H2_EPROTO;
            if ((int64_t)m->f_len > m->in_conn.avail) return H2_EFLOW;
            m->in_conn.avail -= (int32_t)m->f_len;
            m->f_slot = slot_of(m, m->f_sid);
            if (m->f_slot != NO_SLOT && (int64_t)m->f_len > m->st[m->f_slot].in.avail) {
                uint16_t s = m->f_slot;
                m->f_slot = NO_SLOT;
                int rc = flow_reset(m, s);
                if (rc) return rc;
            }
            if (m->f_slot == NO_SLOT) {
                m->paid = 1;
                m->f_drop = m->f_len;
            } else {
                m->st[m->f_slot].in.avail -= (int32_t)m->f_len;
            }
            return 0;
        case FR_HEADERS:
            m->pad_wait = (m->f_flags & FLAG_PADDED) != 0;
            m->skip_left = (m->f_flags & FLAG_PRIORITY) ? 5 : 0;
            if (m->pad_wait + m->skip_left > m->f_len) return H2_EPROTO;
            m->f_slot = slot_of(m, m->f_sid);
            m->peek = m->f_slot != NO_SLOT && !(m->st[m->f_slot].state & ST_STATUS);
            if (!(m->f_flags & FLAG_END_HEADERS)) {
                m->cont = 1;
                m->cont_sid = m->f_sid;
                m->cont_end = (m->f_flags & FLAG_END_STREAM) != 0;
            }
            return 0;
        case FR_CONTINUATION:
            m->f_slot = slot_of(m, m->f_sid);
            return 0;
    }
    return 0;
}

static void refund(struct h2m *m, uint16_t s) {
    if (!m->f_len) return;
    uint32_t r = m->f_drop + m->f_pad, c = 0, k = 0;
    struct h2m_in *f = s != NO_SLOT ? &m->st[s].in : NULL;
    if (m->hold) {
        in_keep(&m->in_conn, r);
        if (f) in_keep(f, m->f_pad);
        return;
    }
    c = h2m_in_add(&m->in_conn, r);
    if (f) k = h2m_in_add(f, m->f_pad);
    credit_out(m, c, f ? f->sid : 0, k);
}

static int frame_end(struct h2m *m) {
    uint16_t s = m->f_slot;
    switch (m->f_type) {
        case FR_DATA:
            refund(m, s);
            if (m->dead) return m->dead;
            if ((m->f_flags & FLAG_END_STREAM) && s != NO_SLOT) return remote_end(m, s);
            return 0;
        case FR_HEADERS:
            if (m->peek && s != NO_SLOT) {
                m->st[s].state |= ST_STATUS;
                int st = h2w_status(m->ctl, m->ctl_n);
                m->cb.status(m->cb.ctx, s, st ? st : -1);
                s = m->f_slot;
            }
            if ((m->f_flags & (FLAG_END_HEADERS | FLAG_END_STREAM)) == (FLAG_END_HEADERS | FLAG_END_STREAM) &&
                s != NO_SLOT)
                return remote_end(m, s);
            return 0;
        case FR_CONTINUATION:
            if (!(m->f_flags & FLAG_END_HEADERS)) return 0;
            m->cont = 0;
            if (m->cont_end && s != NO_SLOT) return remote_end(m, s);
            return 0;
        case FR_SETTINGS:
            return settings_end(m);
        case FR_PING:
            if (m->f_len != 8) return 0;
            if (!(m->f_flags & FLAG_ACK)) return emit(m, FR_PING, FLAG_ACK, 0, m->ctl, 8);
            if (m->resets_pending) {
                m->resets_pending = 0;
                m->rst_ping_block = 1;
            }
            if (m->ping_out && memcmp(m->ping_op, m->ctl, 8) == 0) {
                m->ping_out = 0;
                m->cb.ping_ack(m->cb.ctx);
            }
            return 0;
        case FR_WINDOW_UPDATE:
            return window_update(m);
        case FR_RST_STREAM:
            s = slot_of(m, m->f_sid);
            if (m->ctl_n >= 4 && s != NO_SLOT) {
                uint32_t code = get32(m->ctl);
                end_stream(m, s, code ? (int)(code & 0x7FFFFFFFu) : H2_ERESET);
            }
            return 0;
        case FR_GOAWAY:
            goaway(m);
            return 0;
    }
    return 0;
}

static int content(struct h2m *m, const unsigned char *d, size_t n) {
    if (!n) return 0;
    if (m->f_type == FR_DATA) {
        if (m->f_slot != NO_SLOT) m->cb.data(m->cb.ctx, m->f_slot, d, n);
        return 0;
    }
    if (m->f_type == FR_HEADERS) {
        if (!m->peek) return 0;
    } else if (m->f_type == FR_SETTINGS) {
        if (m->f_flags & FLAG_ACK) return 0;
        for (size_t i = 0; i < n; i++) {
            m->ctl[m->ctl_n++] = d[i];
            if (m->ctl_n < 6) continue;
            m->ctl_n = 0;
            int rc = setting(m, m->ctl);
            if (rc) return rc;
        }
        return 0;
    } else if (m->f_type == FR_CONTINUATION) {
        return 0;
    }
    size_t room = sizeof(m->ctl) - m->ctl_n;
    size_t cp = n < room ? n : room;
    memcpy(m->ctl + m->ctl_n, d, cp);
    m->ctl_n = (uint8_t)(m->ctl_n + cp);
    return 0;
}

static int body(struct h2m *m, const unsigned char *d, size_t n) {
    size_t i = 0;
    while (i < n) {
        if (m->pad_wait) {
            m->pad_wait = 0;
            if ((uint32_t)d[i] + m->skip_left > m->f_left - 1) return H2_EPROTO;
            m->pad_left = d[i];
            i++;
            m->f_left--;
            if (m->f_type == FR_DATA && !m->paid) m->f_pad = (uint32_t)m->pad_left + 1;
            continue;
        }
        if (m->skip_left) {
            size_t k = m->skip_left < n - i ? m->skip_left : n - i;
            m->skip_left = (uint8_t)(m->skip_left - k);
            m->f_left -= (uint32_t)k;
            i += k;
            continue;
        }
        size_t k = n - i;
        size_t want = m->f_left > m->pad_left ? m->f_left - m->pad_left : 0;
        size_t real = k < want ? k : want;
        m->f_left -= (uint32_t)k;
        int rc = content(m, d + i, real);
        if (rc) return rc;
        i += k;
    }
    return 0;
}

int h2m_feed(struct h2m *m, const unsigned char *d, size_t n) {
    if (m->dead) return m->dead;
    size_t p = 0;
    while (p < n) {
        int rc;
        if (m->hdr_n < 9) {
            size_t k = 9 - m->hdr_n;
            if (k > n - p) k = n - p;
            memcpy(m->hdr + m->hdr_n, d + p, k);
            m->hdr_n = (uint8_t)(m->hdr_n + k);
            p += k;
            if (m->hdr_n < 9) break;
            rc = frame_begin(m);
        } else {
            size_t k = m->f_left < n - p ? m->f_left : n - p;
            rc = body(m, d + p, k);
            p += k;
        }
        if (!rc && m->hdr_n == 9 && m->f_left == 0) {
            rc = frame_end(m);
            m->hdr_n = 0;
            m->f_slot = NO_SLOT;
        }
        if (rc) return die(m, rc);
        if (m->dead) return m->dead;
    }
    return 0;
}
void h2m_hold(struct h2m *m, int on) {
    if (on) {
        m->hold = 1;
        return;
    }
    if (!m->hold) return;
    m->hold = 0;
    if (m->dead) return;
    uint32_t c = h2m_in_add(&m->in_conn, 0);
    for (uint16_t i = 0; i < m->st_cap && !m->dead; i++) {
        if (!live(m, i)) continue;
        uint32_t k = h2m_in_add(&m->st[i].in, 0);
        if (!k) continue;
        credit_out(m, c, m->st[i].sid, k);
        c = 0;
    }
    if (!m->dead) credit_out(m, c, 0, 0);
}
