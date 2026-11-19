#include "firc/tapreport.h"

#include <stdarg.h>
#include <stdlib.h>
#include <string.h>

#include "firc/taprules.h" /* FIRC_TAP_LIMIT_PER_SEC */

#define DEFAULT_MAX_FLOWS 256u

typedef struct flow {
    struct flow *next;
    firc_ip_t client;
    char *name;
    int64_t last_event; /* when its last event was put */
    uint32_t folded;    /* flows seen since then */
} flow_t;

typedef struct {
    int64_t sec;       /* the second being counted */
    uint32_t in_sec;   /* packets read in it so far */
    uint64_t at_limit; /* seconds that reached FIRC_TAP_LIMIT_PER_SEC */
} per_sec_t;

struct firc_tap_report {
    flow_t *flows;
    size_t n_flows;
    size_t max_flows;
    bool table_filled;
    bool out_of_memory;
    uint64_t packets;
    uint64_t no_name;
    uint64_t lost;
    uint64_t repeats;
    uint64_t printed;
    uint64_t strangers;
    uint64_t unparsed;
    uint64_t rules_gone;
    bool iface_gone;      /* as of the last time anyone asked */
    bool iface_ever_gone; /* at least once during this capture */
    per_sec_t sock[2];    /* 0: new-connection socket, 1: ClientHello socket */
    bool cold_recall;
};

firc_tap_report_t *firc_tap_report_new(size_t max_flows) {
    firc_tap_report_t *r = calloc(1, sizeof(*r));
    if (r == NULL) { return NULL; }
    r->max_flows = max_flows ? max_flows : DEFAULT_MAX_FLOWS;
    return r;
}

void firc_tap_report_free(firc_tap_report_t *r) {
    if (r == NULL) { return; }
    flow_t *f = r->flows;
    while (f != NULL) {
        flow_t *next = f->next;
        free(f->name);
        free(f);
        f = next;
    }
    free(r);
}

void firc_tap_report_saw_packet(firc_tap_report_t *r) {
    if (r != NULL) { r->packets++; }
}

void firc_tap_report_saw_no_name(firc_tap_report_t *r) {
    if (r != NULL) { r->no_name++; }
}

void firc_tap_report_saw_loss(firc_tap_report_t *r) {
    if (r != NULL) { r->lost++; }
}

void firc_tap_report_saw_iface_gone(firc_tap_report_t *r) {
    if (r != NULL) {
        r->iface_gone = true;
        r->iface_ever_gone = true;
    }
}

void firc_tap_report_saw_iface_there(firc_tap_report_t *r) {
    if (r != NULL) { r->iface_gone = false; }
}

void firc_tap_report_saw_rules_gone(firc_tap_report_t *r) {
    if (r != NULL) { r->rules_gone++; }
}

void firc_tap_report_saw_unparsed(firc_tap_report_t *r) {
    if (r != NULL) { r->unparsed++; }
}

void firc_tap_report_saw_stranger(firc_tap_report_t *r) {
    if (r != NULL) { r->strangers++; }
}

static bool same_client(const firc_ip_t *a, const firc_ip_t *b) {
    return a->len == b->len && memcmp(a->b, b->b, a->len) == 0;
}

bool firc_tap_report_admit(firc_tap_report_t *r, const firc_ip_t *client, const char *name,
                           int64_t now, uint32_t *repeats_out) {
    if (repeats_out != NULL) { *repeats_out = 0; }
    /* No report, or a pair that cannot be keyed: the bypass goes out either way. */
    if (r == NULL || client == NULL || name == NULL) { return true; }

    for (flow_t *f = r->flows; f != NULL; f = f->next) {
        if (!same_client(&f->client, client) || strcmp(f->name, name) != 0) { continue; }
        if (now - f->last_event < FIRC_TAP_DEDUP_SECS) {
            if (f->folded < UINT32_MAX) { f->folded++; }
            r->repeats++;
            return false;
        }
        if (repeats_out != NULL) { *repeats_out = f->folded; }
        f->folded = 0;
        f->last_event = now;
        r->printed++;
        return true;
    }

    /* Past the bound, or out of memory: an event anyway, just not remembered. */
    if (r->n_flows >= r->max_flows) {
        r->table_filled = true;
        r->printed++;
        return true;
    }
    flow_t *f = calloc(1, sizeof(*f));
    char *name_copy = strdup(name);
    if (f == NULL || name_copy == NULL) {
        free(f);
        free(name_copy);
        r->out_of_memory = true;
        r->printed++;
        return true;
    }
    f->client = *client;
    f->name = name_copy;
    f->last_event = now;
    f->next = r->flows;
    r->flows = f;
    r->n_flows++;
    r->printed++;
    return true;
}

void firc_tap_report_saw_at(firc_tap_report_t *r, int which, int64_t sec) {
    if (r == NULL || which < 0 || which > 1) { return; }
    per_sec_t *p = &r->sock[which];
    if (p->in_sec == 0 || p->sec != sec) {
        p->sec = sec;
        p->in_sec = 0;
    }
    if (++p->in_sec == (uint32_t)FIRC_TAP_LIMIT_PER_SEC) { p->at_limit++; }
}

void firc_tap_report_saw_cold_recall(firc_tap_report_t *r) {
    if (r != NULL) { r->cold_recall = true; }
}

typedef void (*sentence_fn)(const char *sentence, void *ud);

__attribute__((format(printf, 3, 4)))
static void say(sentence_fn fn, void *ud, const char *fmt, ...) {
    char buf[FIRC_TAP_SENTENCE_MAX + 1];
    va_list ap;
    va_start(ap, fmt);
    int n = vsnprintf(buf, sizeof(buf), fmt, ap);
    va_end(ap);
    if (n > 0) { fn(buf, ud); }
}

uint64_t firc_tap_report_printed(const firc_tap_report_t *r) { return r == NULL ? 0 : r->printed; }

static const char *s_(uint64_t n) { return n == 1 ? "" : "s"; }

void firc_tap_report_sentences(const firc_tap_report_t *r, bool final, sentence_fn fn,
                               void *ud) {
    if (r == NULL || fn == NULL) { return; }
    static const char *const sock_name[2] = {"new-connection", "ClientHello"};
    bool at_limit = r->sock[0].at_limit > 0 || r->sock[1].at_limit > 0;

    if (r->repeats > 0) {
        say(fn, ud, "%llu packet%s read, %llu bypass%s reported, %llu repeat%s not reported on "
                    "its own.",
            (unsigned long long)r->packets, s_(r->packets), (unsigned long long)r->printed,
            r->printed == 1 ? "" : "es", (unsigned long long)r->repeats, s_(r->repeats));
    } else {
        say(fn, ud, "%llu packet%s read, %llu bypass%s reported.", (unsigned long long)r->packets,
            s_(r->packets), (unsigned long long)r->printed, r->printed == 1 ? "" : "es");
    }

    if (final && r->packets == 0) {
        say(fn, ud, "No packet reached the capture at all: nothing matched its rules.");
    } else if (final && r->printed == 0 && r->unparsed == 0 && r->rules_gone == 0 &&
               r->lost == 0 && r->no_name == 0 && !r->iface_ever_gone && !r->iface_gone &&
               !r->cold_recall && !r->table_filled && !r->out_of_memory && !at_limit) {
        say(fn, ud, "No bypass: every flow the capture saw was one firc had nothing to say "
                    "about.");
    }
    if (r->iface_gone) {
        /* Said first: it means every number below is beside the point. */
        say(fn, ud, "An interface this capture was watching does not exist: iptables takes a "
                    "name no interface has, so its rules matched nothing and it watched "
                    "nothing there, whatever the numbers say.");
    } else if (r->iface_ever_gone) {
        say(fn, ud, "An interface this capture was watching went away during it and came back: "
                    "while it was missing its rules matched nothing there, so part of this "
                    "capture was not watched.");
    }
    if (r->rules_gone > 0) {
        say(fn, ud, "%llu netfilter pass%s failed while this capture was open, so its rules "
                    "were not in the kernel until a later one landed: those seconds were not "
                    "watched.",
            (unsigned long long)r->rules_gone, r->rules_gone == 1 ? "" : "es");
    }
    for (int w = 0; w < 2; w++) {
        if (r->sock[w].at_limit == 0) { continue; }
        say(fn, ud, "A rule's limit of %d/s may have been reached in at least %llu second%s on "
                    "the %s capture: some flows may not have been seen.",
            FIRC_TAP_LIMIT_PER_SEC, (unsigned long long)r->sock[w].at_limit,
            s_(r->sock[w].at_limit), sock_name[w]);
    }
    if (r->cold_recall) {
        say(fn, ud, "firc had recorded no real address when the capture started; bypasses were "
                    "recognised by SNI only until clients asked.");
    }
    if (r->unparsed > 0) {
        say(fn, ud, "%llu packet%s could not be read as IPv4 or IPv6 at all, which the capture "
                    "rules should not have delivered: the rules and this reader disagree.",
            (unsigned long long)r->unparsed, s_(r->unparsed));
    }
    if (r->no_name > 0) {
        say(fn, ud, "%llu packet%s carried no server name this reader could take: a hello "
                    "spanning segments reads the same as a client that sent none.",
            (unsigned long long)r->no_name, s_(r->no_name));
    }
    if (r->lost > 0) {
        say(fn, ud, "%llu time%s packets were lost before the capture read them: this capture "
                    "is not complete.",
            (unsigned long long)r->lost, s_(r->lost));
    }
    if (r->table_filled) {
        say(fn, ud, "The dedup table filled at %zu pairs: past that, a bypass is reported every "
                    "time rather than once a minute.",
            r->max_flows);
    }
    if (r->out_of_memory) {
        say(fn, ud, "There was no memory to remember some pairs by: those are reported every "
                    "time rather than once a minute.");
    }
    if (r->strangers > 0) {
        say(fn, ud, "%llu datagram%s came from another process on this machine and %s ignored: "
                    "the capture reads what the kernel sends it and nothing else.",
            (unsigned long long)r->strangers, s_(r->strangers), r->strangers == 1 ? "was" : "were");
    }
}
