#include "hy2loss.h"

static void window_drop(struct hy2loss *s) { s->sent = s->lost = s->ms = 0; }

static int judged_lossy(struct hy2loss *s, uint64_t sent, uint64_t lost) {
    if (!s->run) s->run_sent = s->run_lost = 0;
    s->run_sent += sent;
    s->run_lost += lost;
    if (++s->run < HY2LOSS_RUN) return HY2LOSS_NONE;
    s->run = 0;
    return HY2LOSS_LOSSY;
}

int hy2loss_step(struct hy2loss *s, uint64_t sent, uint64_t lost, int hopped, uint64_t span_ms) {
    if (hopped) {
        window_drop(s);
        s->run = 0;
        return HY2LOSS_NONE;
    }
    if (span_ms > HY2LOSS_MAX_MS) {
        window_drop(s);
        return HY2LOSS_NONE;
    }
    s->sent += sent;
    s->lost += lost;
    s->ms += span_ms;
    if (s->sent < HY2LOSS_MIN_PKTS) {
        if (s->ms > HY2LOSS_MAX_MS) window_drop(s);
        return HY2LOSS_NONE;
    }
    uint64_t w_sent = s->sent, w_lost = s->lost;
    window_drop(s);
    if (w_lost * 100 >= 5 * w_sent) return judged_lossy(s, w_sent, w_lost);
    if (w_lost * 100 <= 2 * w_sent) s->run = 0;
    return HY2LOSS_NONE;
}
