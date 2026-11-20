#include "firc/tapwindow.h"

#include <string.h>
#include <time.h>

#include "firc/clienthello.h"
#include "firc/events.h"
#include "firc/id.h"
#include "firc/tappacket.h"
#include "firc/utf8.h"

typedef struct {
    firc_tap_drain_t *w;
    int64_t now;
    size_t events;
} drain_turn_t;

static void put_bypass(drain_turn_t *t, const firc_tap_find_t *f, const firc_ip_t *client,
                       const firc_ip_t *dst, uint16_t port, uint8_t proto) {
    firc_tap_drain_t *w = t->w;
    uint32_t repeats = 0;
    if (!firc_tap_report_admit(w->report, client, f->name, t->now, &repeats)) { return; }

    firc_event_t ev;
    memset(&ev, 0, sizeof(ev));
    ev.kind = FIRC_EVENT_BYPASS;
    ev.at = t->now;
    firc_event_bypass_t *b = &ev.u.bypass;
    b->client = *client;
    b->dst = *dst;
    b->dst_port = port;
    b->proto = proto;
    b->how = f->how;
    snprintf(b->name, sizeof(b->name), "%s", f->name);
    firc_id_format(f->group->id, b->group_id);
    const char *gn = f->group->name != NULL ? f->group->name : "";
    snprintf(b->group_name, sizeof(b->group_name), "%.*s",
             firc_utf8_clamp(gn, (int)sizeof(b->group_name) - 1), gn);
    /* What firc last told this address about this name; the address may not be the one that asked. */
    firc_recall_answer_t a;
    const unsigned family = dst->len == 16 ? FIRC_RECALL_V6 : FIRC_RECALL_V4;
    if (firc_recall_last_answer(w->ctx.recall, client, f->name, family, t->now, &a)) {
        b->last_decision = a.decision;
        b->last_at = a.at;
        b->last_fake = a.fake;
    } else {
        b->last_decision = FIRC_BYPASS_NOT_ASKED;
    }
    b->repeats = repeats;
    firc_event_put(&ev);
    t->events++;
}

static void drain_new(drain_turn_t *t, const uint8_t *pkt, size_t len) {
    firc_tap_drain_t *w = t->w;
    firc_tap_head_t h;
    if (!firc_tap_head_parse(pkt, len, &h)) {
        firc_tap_report_saw_unparsed(w->report);
        return;
    }
    firc_tap_find_t f;
    if (firc_tap_judge_addr(&w->ctx, &h.src, &h.dst, &f)) {
        put_bypass(t, &f, &h.src, &h.dst, h.dst_port, h.proto);
    }
}

/* Reports by SNI only when the address path misses: that path's own bypass is the new-connection socket's to report. */
static void drain_hello(drain_turn_t *t, const uint8_t *pkt, size_t len) {
    firc_tap_drain_t *w = t->w;
    firc_tap_packet_t p;
    if (!firc_tap_packet_parse(pkt, len, &p)) {
        firc_tap_report_saw_unparsed(w->report);
        return;
    }
    /* Before reading anything out of the hello: a device nobody covers is not counted either. */
    if (!firc_dns_pipeline_covers(w->ctx.pipeline, &p.src)) { return; }
    /* Handshake type byte, not just record type: an encrypted Finished is also a 0x16 record. */
    if (p.payload_len <= 5 || p.payload[0] != 0x16 || p.payload[5] != 0x01) { return; }
    firc_tap_find_t f;
    if (firc_tap_judge_addr(&w->ctx, &p.src, &p.dst, &f)) { return; }
    char sni[256];
    if (!firc_tls_client_hello_sni(p.payload, p.payload_len, sni, sizeof(sni))) {
        firc_tap_report_saw_no_name(w->report);
        return;
    }
    if (firc_tap_judge_sni(&w->ctx, &p.src, &p.dst, sni, &f)) {
        put_bypass(t, &f, &p.src, &p.dst, p.dst_port, 6);
    }
}

static void drain_one(const uint8_t *pkt, size_t len, void *ud) {
    drain_turn_t *t = ud;
    firc_tap_report_saw_packet(t->w->report);
    firc_tap_report_saw_at(t->w->report, t->w->which, t->now);
    if (t->w->which == 1) {
        drain_hello(t, pkt, len);
    } else {
        drain_new(t, pkt, len);
    }
}

firc_err_t firc_tap_drain(firc_tap_drain_t *w, firc_nflog_t *n, size_t *out_events) {
    if (out_events != NULL) { *out_events = 0; }
    if (w == NULL || n == NULL) { return FIRC_ERR_INVAL; }

    /* One clock reading per datagram: every packet in it arrived together. */
    drain_turn_t t = {w, w->now != NULL ? w->now() : (int64_t)time(NULL), 0};
    firc_err_t e = firc_nflog_read(n, drain_one, &t);
    if (out_events != NULL) { *out_events = t.events; }

    switch (e) {
    case FIRC_ERR_LIMIT:
        firc_tap_report_saw_loss(w->report);
        return FIRC_OK;
    case FIRC_ERR_PROTO:
        firc_tap_report_saw_stranger(w->report);
        return FIRC_OK;
    default:
        /* FIRC_OK/FIRC_ERR_AGAIN pass through; anything else is the reader broken and must end the capture. */
        return e;
    }
}
