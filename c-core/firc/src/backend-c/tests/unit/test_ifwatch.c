#include "greatest.h"

#include <linux/rtnetlink.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "fake_rtnl.h"
#include "firc/events.h"
#include "firc/ifwatch.h"
#include "firc/logrep.h"
#include "firc/mark.h"
#include "firc/models.h"
#include "firc/rtnl.h"

static int64_t g_now;
static int64_t clock_ms(void) { return g_now; }
static uint64_t clock_u(void) { return (uint64_t)g_now; }

static firc_group_t *g_groups[8];
static size_t g_n;
static size_t n_groups(void *ud) {
    (void)ud;
    return g_n;
}
static const firc_group_t *group_at(void *ud, size_t i) {
    (void)ud;
    return g_groups[i];
}

typedef struct {
    firc_id_t id;
    size_t to;
    bool blocked;
} move_t;
static move_t g_moves[16];
static size_t g_n_moves;
static firc_err_t g_move_err;
static firc_err_t record_move(void *ud, firc_id_t id, size_t to, bool blocked) {
    (void)ud;
    if (g_n_moves < 16) { g_moves[g_n_moves++] = (move_t){id, to, blocked}; }
    return g_move_err;
}

static const char *g_down;
static bool link_up(void *ud, const char *name) {
    (void)ud;
    return g_down == NULL || strcmp(name, g_down) != 0;
}
static const char *g_tunnel;
static bool g_tunnel_is_down;
static bool tunnel_known(void *ud, const char *dev) {
    (void)ud;
    return g_tunnel != NULL && strcmp(dev, g_tunnel) == 0;
}
static bool g_starting;
static int64_t g_started_ms;
static bool tunnel_down(void *ud, const char *dev) {
    (void)ud;
    (void)dev;
    return g_tunnel_is_down || g_starting;
}
static bool tunnel_starting(void *ud, const char *dev, int64_t *since_ms) {
    (void)ud;
    if (!g_starting || g_tunnel == NULL || strcmp(dev, g_tunnel) != 0) { return false; }
    *since_ms = g_started_ms;
    return true;
}

static char g_queued[64][16];
static uint32_t g_queued_gen[64];
static size_t g_n_queued;
static firc_err_t record_queue(void *ud, const char *name, uint32_t mark, uint32_t gen) {
    (void)ud;
    (void)mark;
    snprintf(g_queued[g_n_queued % 64], sizeof(g_queued[0]), "%s", name);
    g_queued_gen[g_n_queued % 64] = gen;
    g_n_queued++;
    return FIRC_OK;
}
static uint32_t last_gen(const char *name) {
    uint32_t gen = 0;
    for (size_t i = 0; i < g_n_queued && i < 64; i++) {
        if (strcmp(g_queued[i], name) == 0) { gen = g_queued_gen[i]; }
    }
    return gen;
}
static size_t times_queued(const char *name) {
    size_t c = 0;
    for (size_t i = 0; i < g_n_queued && i < 64; i++) { c += strcmp(g_queued[i], name) == 0 ? 1u : 0u; }
    return c;
}

static const char *const AB[] = {"up0", "up1"};
static const firc_id_t G1 = {{1, 1, 1, 1}};

static firc_group_t *grp(uint8_t tag, const char *const *names, size_t n) {
    firc_group_t *g = firc_group_new();
    g->id = (firc_id_t){{tag, tag, tag, tag}};
    g->name = strdup("G");
    g->enable = true;
    firc_group_set_ifaces(g, names, n);
    return g;
}

static fake_rtnl_t *g_kernel;
static firc_rtnl_t *g_rtnl;
static unsigned three_links(const char *name) {
    if (strcmp(name, "up0") == 0) { return 7; }
    if (strcmp(name, "up1") == 0) { return 8; }
    if (strcmp(name, "up2") == 0) { return 9; }
    if (strcmp(name, "opkgtun3") == 0) { return 13; }
    if (strcmp(name, "opkgtun10") == 0) { return 10; }
    if (name[0] == 'c' && name[1] >= 'a' && name[1] <= 'z' && name[2] == '\0') { return 20u + (unsigned)(name[1] - 'a'); }
    return 0;
}

static size_t check_rules(uint16_t type) {
    fake_rtnl_msg_t m[256];
    size_t n = fake_rtnl_messages(g_kernel, m, 256), c = 0;
    for (size_t i = 0; i < n; i++) {
        c += m[i].type == type && m[i].priority == FIRC_RULE_PRIORITY_TUNNEL ? 1u : 0u;
    }
    return c;
}

static firc_ifwatch_t *fresh(void) {
    g_move_err = FIRC_OK;
    g_n_moves = 0;
    g_n_queued = 0;
    g_n = 0;
    g_now = 1000;
    g_down = NULL;
    g_tunnel = NULL;
    g_tunnel_is_down = false;
    g_starting = false;
    firc_event_reset_for_test();
    firc_logrep_set_clock_for_test(clock_u);
    g_kernel = fake_rtnl_start(&g_rtnl);
    fake_rtnl_set_link_flags(g_kernel, 0x1 | 0x10);
    firc_rtnl_set_nametoindex_for_test(three_links);
    firc_ifwatch_deps_t d = {.rtnl = g_rtnl,
                             .start_table = 500,
                             .n_groups = n_groups,
                             .group_at = group_at,
                             .move = record_move,
                             .link_up = link_up,
                             .tunnel_known = tunnel_known,
                             .tunnel_down = tunnel_down,
                             .tunnel_starting = tunnel_starting,
                             .queue = record_queue,
                             .now_ms = clock_ms};
    firc_ifwatch_t *w = firc_ifwatch_new(&d);
    firc_app_config_t a;
    firc_app_config_init_defaults(&a);
    firc_ifwatch_configure(w, &a);
    firc_app_config_clear(&a);
    return w;
}

static void kernel_done(void) {
    for (size_t i = 0; i < g_n; i++) { firc_group_free(g_groups[i]); }
    g_n = 0;
    firc_rtnl_set_nametoindex_for_test(NULL);
    firc_rtnl_close(g_rtnl);
    fake_rtnl_stop(g_kernel);
    firc_logrep_set_clock_for_test(NULL);
}

static void done(firc_ifwatch_t *w) {
    firc_ifwatch_free(w);
    kernel_done();
}

static void result_as(firc_ifwatch_t *w, const char *name, bool passed, bool guarded, bool wan, uint32_t gen,
                      const char *error) {
    firc_ifcheck_result_t r;
    memset(&r, 0, sizeof(r));
    snprintf(r.iface, sizeof(r.iface), "%s", name);
    r.gen = gen;
    r.passed = passed;
    r.control_failed = guarded;
    r.control_ran = !passed && wan;
    snprintf(r.error, sizeof(r.error), "%s", passed ? "" : error);
    firc_ifwatch_results(w, &r, 1);
}

static void result(firc_ifwatch_t *w, const char *name, bool passed, bool guarded) {
    result_as(w, name, passed, guarded, true, last_gen(name), "timed out");
}

static size_t times_said(firc_log_level_t level, const char *part) {
    firc_event_t out[64];
    uint64_t next = 0, dropped = 0;
    size_t n = firc_event_read(0, out, 64, &next, &dropped), c = 0;
    for (size_t i = 0; i < n; i++) {
        if (out[i].kind == FIRC_EVENT_LOG && out[i].u.log.level == level && strstr(out[i].u.log.text, part) != NULL) {
            c++;
        }
    }
    return c;
}

static bool said(firc_log_level_t level, const char *part) { return times_said(level, part) > 0; }

static bool last_move_is(size_t to, bool blocked) {
    return g_n_moves > 0 && firc_id_equal(g_moves[g_n_moves - 1].id, G1) && g_moves[g_n_moves - 1].to == to &&
           g_moves[g_n_moves - 1].blocked == blocked;
}

static firc_ifwatch_t *both_alive(void) {
    firc_ifwatch_t *w = fresh();
    g_groups[g_n++] = grp(1, AB, 2);
    firc_ifwatch_tick(w);
    result(w, "up0", true, false);
    result(w, "up1", true, false);
    return w;
}

TEST a_list_s_interfaces_are_checked_at_once_and_a_single_one_never(void) {
    firc_ifwatch_t *w = fresh();
    static const char *const C[] = {"up2"};
    g_groups[g_n++] = grp(1, AB, 2);
    g_groups[g_n++] = grp(2, C, 1);
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)1, times_queued("up0"), "%zu");
    ASSERT_EQ_FMT((size_t)1, times_queued("up1"), "%zu");
    ASSERT_EQ_FMT((size_t)0, times_queued("up2"), "%zu");
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMTm("nothing is queued twice while in flight", (size_t)2, g_n_queued, "%zu");
    done(w);
    PASS();
}

TEST the_confirm_is_asked_3_s_later_and_the_second_failure_moves(void) {
    firc_ifwatch_t *w = both_alive();
    result(w, "up0", false, false);
    ASSERT_EQ_FMT((size_t)0, g_n_moves, "%zu");
    g_now += 2999;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)1, times_queued("up0"), "%zu");
    g_now += 1;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)2, times_queued("up0"), "%zu");
    result(w, "up0", false, false);
    ASSERT(last_move_is(1, false));
    ASSERT(said(FIRC_LOG_WARN, "group G moved from up0 to up1: timed out"));
    ASSERT(said(FIRC_LOG_WARN, "interface up0 is dead: timed out"));
    done(w);
    PASS();
}

TEST a_regular_check_waits_the_interval_and_a_shorter_one_brings_it_forward(void) {
    firc_ifwatch_t *w = both_alive();
    g_now += 29999;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)1, times_queued("up0"), "%zu");
    g_now += 1;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)2, times_queued("up0"), "%zu");
    ASSERT_EQ_FMTm("the second interface comes a second later", (size_t)1, times_queued("up1"), "%zu");
    g_now += 1000;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)2, times_queued("up1"), "%zu");
    result(w, "up0", true, false);
    firc_app_config_t a;
    firc_app_config_init_defaults(&a);
    a.check.interval = 10 * FIRC_DURATION_SEC;
    firc_ifwatch_configure(w, &a);
    firc_app_config_clear(&a);
    g_now += 9999;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)2, times_queued("up0"), "%zu");
    g_now += 1;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMTm("the shorter interval holds from the change", (size_t)3, times_queued("up0"), "%zu");
    done(w);
    PASS();
}

TEST the_group_moves_back_after_120_s_of_passes_and_not_before(void) {
    firc_ifwatch_t *w = both_alive();
    result(w, "up0", false, false);
    g_now += 3000;
    result(w, "up0", false, false);
    ASSERT(last_move_is(1, false));
    g_now += 6000;
    result(w, "up0", true, false);
    ASSERT_EQ_FMT((size_t)1, g_n_moves, "%zu");
    g_now += 119999;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)1, g_n_moves, "%zu");
    g_now += 1;
    firc_ifwatch_tick(w);
    ASSERT(last_move_is(0, false));
    ASSERT(said(FIRC_LOG_INFO, "group G moved back from up1 to up0"));
    done(w);
    PASS();
}

TEST a_move_up_the_list_off_a_dead_entry_is_a_warning(void) {
    firc_ifwatch_t *w = fresh();
    static const char *const ABC[] = {"up0", "up1", "up2"};
    g_groups[g_n++] = grp(1, ABC, 3);
    firc_ifwatch_tick(w);
    result(w, "up0", true, false);
    result(w, "up1", true, false);
    result(w, "up2", true, false);
    result(w, "up0", false, false);
    g_now += 3000;
    result(w, "up0", false, false);
    ASSERT(last_move_is(1, false));
    g_now += 6000;
    result(w, "up0", true, false);
    result(w, "up1", false, false);
    g_now += 3000;
    result(w, "up1", false, false);
    ASSERT(last_move_is(0, false));
    ASSERT(said(FIRC_LOG_WARN, "group G moved from up1 to up0: timed out"));
    ASSERT_FALSE(said(FIRC_LOG_INFO, "moved back"));
    done(w);
    PASS();
}

TEST a_link_down_moves_at_once_without_a_check(void) {
    firc_ifwatch_t *w = both_alive();
    firc_ifwatch_link(w, "up0", false);
    ASSERT(last_move_is(1, false));
    size_t queued = g_n_queued;
    firc_ifwatch_link(w, "up0", true);
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMTm("a link up asks for a check at once", queued + 1, g_n_queued, "%zu");
    done(w);
    PASS();
}

TEST a_link_up_leaves_a_pending_confirm_where_it_is(void) {
    firc_ifwatch_t *w = both_alive();
    result(w, "up0", false, false);
    g_now += 1000;
    firc_ifwatch_link(w, "up0", true);
    g_now += 1999;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)1, times_queued("up0"), "%zu");
    g_now += 1;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)2, times_queued("up0"), "%zu");
    done(w);
    PASS();
}

TEST a_chatty_link_pulls_a_check_forward_once_per_interval(void) {
    firc_ifwatch_t *w = both_alive();
    firc_ifwatch_link(w, "up0", true);
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)2, times_queued("up0"), "%zu");
    g_now += 15000;
    result(w, "up0", true, false);
    g_now += 4000;
    firc_ifwatch_link(w, "up0", true);
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMTm("a second link up inside the interval asks nothing", (size_t)2, times_queued("up0"), "%zu");
    g_now += 10999;
    firc_ifwatch_link(w, "up0", true);
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)2, times_queued("up0"), "%zu");
    g_now += 1;
    firc_ifwatch_link(w, "up0", true);
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)3, times_queued("up0"), "%zu");
    done(w);
    PASS();
}

TEST a_refresh_reads_the_link_and_finds_it_gone(void) {
    firc_ifwatch_t *w = both_alive();
    g_down = "up0";
    firc_ifwatch_refresh(w, "up1");
    ASSERT_EQ_FMT((size_t)0, g_n_moves, "%zu");
    firc_ifwatch_refresh(w, NULL);
    ASSERT(last_move_is(1, false));
    done(w);
    PASS();
}

TEST a_tunnel_entry_follows_its_status_and_is_never_queued(void) {
    firc_ifwatch_t *w = fresh();
    static const char *const T[] = {"tunvless0", "up1"};
    g_tunnel = "tunvless0";
    g_groups[g_n++] = grp(1, T, 2);
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)0, times_queued("tunvless0"), "%zu");
    firc_if_view_t v;
    ASSERT(firc_ifwatch_view(w, "tunvless0", &v));
    ASSERT_EQ(FIRC_IF_ALIVE, v.state);
    g_tunnel_is_down = true;
    firc_ifwatch_tick(w);
    ASSERT(last_move_is(1, false));
    done(w);
    PASS();
}

static firc_ifwatch_t *tunnel_first_both_alive(void) {
    firc_ifwatch_t *w = fresh();
    static const char *const T[] = {"tunvless0", "up1"};
    g_tunnel = "tunvless0";
    g_groups[g_n++] = grp(1, T, 2);
    firc_ifwatch_tick(w);
    result(w, "up1", true, false);
    return w;
}

/* Catches: an NDM event that asks no check, asks one on every event, or decides the entry's state itself. */
TEST an_ndm_event_pulls_a_check_forward_once_per_interval(void) {
    firc_ifwatch_t *w = both_alive();
    firc_ifwatch_nudge(w, "up0");
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)2, times_queued("up0"), "%zu");
    g_now += 15000;
    result(w, "up0", true, false);
    g_now += 4000;
    firc_ifwatch_nudge(w, "up0");
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMTm("a second event inside the interval asks nothing", (size_t)2, times_queued("up0"), "%zu");
    g_now += 10999;
    firc_ifwatch_nudge(w, "up0");
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)2, times_queued("up0"), "%zu");
    g_now += 1;
    firc_ifwatch_nudge(w, "up0");
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)3, times_queued("up0"), "%zu");
    ASSERT(firc_ifwatch_pick(w, g_groups[0]).active == 0);
    done(w);
    PASS();
}

/* Catches: an NDM event spending the budget a kernel link-up needs to ask its own check. */
TEST an_ndm_event_never_holds_back_a_link_up(void) {
    firc_ifwatch_t *w = both_alive();
    firc_ifwatch_nudge(w, "up0");
    firc_ifwatch_tick(w);
    g_now += 2000;
    result(w, "up0", true, false);
    g_now += 1000;
    firc_ifwatch_link(w, "up0", true);
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)3, times_queued("up0"), "%zu");
    done(w);
    PASS();
}

/* Catches: an NDM event on a tunnel entry, which is never checked, queuing a check. */
TEST an_ndm_event_on_a_tunnel_entry_asks_nothing(void) {
    firc_ifwatch_t *w = tunnel_first_both_alive();
    size_t before = times_queued("tunvless0");
    firc_ifwatch_nudge(w, "tunvless0");
    firc_ifwatch_nudge(w, "nosuch0");
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT(before, times_queued("tunvless0"), "%zu");
    done(w);
    PASS();
}


/* Catches: a removed tunnel's device kept as a tunnel entry, so with its link up it reads alive and is never checked. */
TEST a_tunnel_removed_from_the_config_is_checked_by_http(void) {
    firc_ifwatch_t *w = tunnel_first_both_alive();
    ASSERT_EQ_FMT((size_t)0, times_queued("tunvless0"), "%zu");
    g_tunnel = NULL;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)1, times_queued("tunvless0"), "%zu");
    firc_if_view_t v;
    ASSERT(firc_ifwatch_view(w, "tunvless0", &v));
    ASSERT_EQ(FIRC_IF_UNKNOWN, v.state);
    result(w, "tunvless0", true, false);
    ASSERT(firc_ifwatch_view(w, "tunvless0", &v));
    ASSERT_EQ(FIRC_IF_ALIVE, v.state);
    done(w);
    PASS();
}

/* Catches: a removed tunnel's missing device not taken as dead, so its group stays on it. */
TEST a_tunnel_removed_with_its_device_is_dead_and_the_group_moves(void) {
    firc_ifwatch_t *w = tunnel_first_both_alive();
    ASSERT_EQ_FMT((size_t)0, g_n_moves, "%zu");
    g_tunnel = NULL;
    g_down = "tunvless0";
    firc_ifwatch_tick(w);
    ASSERT(last_move_is(1, false));
    firc_if_view_t v;
    ASSERT(firc_ifwatch_view(w, "tunvless0", &v));
    ASSERT_EQ(FIRC_IF_DEAD, v.state);
    done(w);
    PASS();
}

/* Catches: an entry taken for a tunnel by its opkgtun or tunvless name, so another program's device is never checked. */
TEST a_foreign_opkgtun_entry_is_checked_by_http(void) {
    firc_ifwatch_t *w = fresh();
    static const char *const T[] = {"opkgtun10", "up1"};
    g_tunnel = "tunvless10";
    g_groups[g_n++] = grp(1, T, 2);
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)1, times_queued("opkgtun10"), "%zu");
    result(w, "opkgtun10", true, false);
    firc_if_view_t v;
    ASSERT(firc_ifwatch_view(w, "opkgtun10", &v));
    ASSERT_EQ(FIRC_IF_ALIVE, v.state);
    done(w);
    PASS();
}

/* Catches: the tunnel flag read once when the entry is added, so a device configured later is still checked by HTTP. */
TEST a_name_that_becomes_a_tunnel_follows_it(void) {
    firc_ifwatch_t *w = fresh();
    static const char *const T[] = {"opkgtun3", "up1"};
    g_groups[g_n++] = grp(1, T, 2);
    firc_ifwatch_tick(w);
    result(w, "opkgtun3", true, false);
    result(w, "up1", true, false);
    ASSERT_EQ_FMT((size_t)1, times_queued("opkgtun3"), "%zu");
    ASSERT_EQ_FMT((size_t)0, g_n_moves, "%zu");
    g_tunnel = "opkgtun3";
    g_tunnel_is_down = true;
    firc_ifwatch_tick(w);
    ASSERT(last_move_is(1, false));
    g_now += 120000;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)1, times_queued("opkgtun3"), "%zu");
    done(w);
    PASS();
}

static firc_ifwatch_t *booting_tunnel_first(void) {
    firc_ifwatch_t *w = fresh();
    static const char *const T[] = {"tunvless0", "up1"};
    g_tunnel = "tunvless0";
    g_starting = true;
    g_started_ms = g_now;
    g_down = "tunvless0";
    g_groups[g_n++] = grp(1, T, 2);
    firc_ifwatch_tick(w);
    result(w, "up1", true, false);
    return w;
}

TEST a_starting_tunnel_keeps_its_group_for_30_s_then_dies(void) {
    firc_ifwatch_t *w = booting_tunnel_first();
    ASSERT_EQ_FMT((size_t)0, g_n_moves, "%zu");
    firc_if_view_t v;
    ASSERT(firc_ifwatch_view(w, "tunvless0", &v));
    ASSERT_EQ(FIRC_IF_UNKNOWN, v.state);
    g_now += 29999;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)0, g_n_moves, "%zu");
    g_now += 1;
    firc_ifwatch_tick(w);
    ASSERT(last_move_is(1, false));
    ASSERT(firc_ifwatch_view(w, "tunvless0", &v));
    ASSERT_EQ(FIRC_IF_DEAD, v.state);
    done(w);
    PASS();
}

TEST a_tunnel_up_within_30_s_keeps_its_group(void) {
    firc_ifwatch_t *w = booting_tunnel_first();
    g_now += 10000;
    g_starting = false;
    g_down = NULL;
    firc_ifwatch_tick(w);
    firc_if_view_t v;
    ASSERT(firc_ifwatch_view(w, "tunvless0", &v));
    ASSERT_EQ(FIRC_IF_ALIVE, v.state);
    g_now += 30000;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)0, g_n_moves, "%zu");
    done(w);
    PASS();
}

TEST a_restarting_tunnel_is_unknown_again(void) {
    firc_ifwatch_t *w = tunnel_first_both_alive();
    g_starting = true;
    g_started_ms = g_now;
    g_down = "tunvless0";
    firc_ifwatch_link(w, "tunvless0", false);
    ASSERT_EQ_FMTm("the old device going away kills nothing", (size_t)0, g_n_moves, "%zu");
    firc_ifwatch_tick(w);
    firc_if_view_t v;
    ASSERT(firc_ifwatch_view(w, "tunvless0", &v));
    ASSERT_EQ(FIRC_IF_UNKNOWN, v.state);
    ASSERT_EQ_FMT((size_t)0, g_n_moves, "%zu");
    g_now += 30000;
    firc_ifwatch_tick(w);
    ASSERT(last_move_is(1, false));
    done(w);
    PASS();
}

TEST a_starting_tunnel_without_its_device_stays_unknown_then_dies(void) {
    firc_ifwatch_t *w = booting_tunnel_first();
    g_now += 5000;
    firc_ifwatch_link(w, "tunvless0", false);
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)0, g_n_moves, "%zu");
    firc_if_view_t v;
    ASSERT(firc_ifwatch_view(w, "tunvless0", &v));
    ASSERT_EQ(FIRC_IF_UNKNOWN, v.state);
    g_now += 25000;
    firc_ifwatch_link(w, "tunvless0", false);
    ASSERT(last_move_is(1, false));
    done(w);
    PASS();
}

TEST a_starting_entry_never_unblocks_and_the_error_comes_at_5_min(void) {
    firc_ifwatch_t *w = fresh();
    static const char *const T[] = {"tunvless0", "up1"};
    g_tunnel = "tunvless0";
    g_tunnel_is_down = true;
    g_down = "tunvless0";
    g_groups[g_n++] = grp(1, T, 2);
    firc_ifwatch_tick(w);
    firc_ifwatch_link(w, "up1", false);
    ASSERT(last_move_is(1, true));
    size_t moves = g_n_moves;
    for (int cycle = 0; cycle < 10; cycle++) {
        g_starting = true;
        g_started_ms = g_now;
        firc_ifwatch_tick(w);
        ASSERTm("an enable while it starts stays blocked", firc_ifwatch_pick(w, g_groups[0]).all_dead);
        g_now += 20000;
        firc_ifwatch_tick(w);
        g_starting = false;
        g_now += 10000;
        firc_ifwatch_tick(w);
    }
    ASSERT_EQ_FMTm("a crash loop never unblocks the group", moves, g_n_moves, "%zu");
    ASSERT_FALSE(said(FIRC_LOG_INFO, "group G is back on"));
    ASSERT(said(FIRC_LOG_ERROR, "group G: every interface of its list is dead"));
    done(w);
    PASS();
}

TEST a_missing_device_no_tunnel_names_is_dead_from_the_start(void) {
    firc_ifwatch_t *w = fresh();
    static const char *const T[] = {"tunvless3", "up1"};
    g_down = "tunvless3";
    g_groups[g_n++] = grp(1, T, 2);
    firc_ifwatch_tick(w);
    ASSERT(last_move_is(1, false));
    done(w);
    PASS();
}

/* Catches: a name taken for a tunnel entry by its stem, or a configured name matched by prefix rather than exactly. */
TEST only_a_configured_name_is_a_tunnel_entry(void) {
    firc_ifwatch_t *w = fresh();
    static const char *const A[] = {"tunvless0", "tunvless05"};
    static const char *const B[] = {"tunvless01", "opkgtun3"};
    g_tunnel = "tunvless0";
    g_groups[g_n++] = grp(1, A, 2);
    g_groups[g_n++] = grp(2, B, 2);
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)0, times_queued("tunvless0"), "%zu");
    ASSERT_EQ_FMT((size_t)1, times_queued("tunvless05"), "%zu");
    ASSERT_EQ_FMT((size_t)1, times_queued("tunvless01"), "%zu");
    ASSERT_EQ_FMT((size_t)1, times_queued("opkgtun3"), "%zu");
    done(w);
    PASS();
}

TEST a_tunnel_entry_dies_with_its_link(void) {
    firc_ifwatch_t *w = tunnel_first_both_alive();
    g_down = "tunvless0";
    firc_ifwatch_link(w, "tunvless0", false);
    ASSERT(last_move_is(1, false));
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMTm("a tunnel still configured does not revive a gone device", (size_t)1, g_n_moves, "%zu");
    g_now += 120000;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMTm("nor brings the group back after a hold", (size_t)1, g_n_moves, "%zu");
    done(w);
    PASS();
}

TEST a_failed_control_moves_nothing_and_says_the_check_fails(void) {
    firc_ifwatch_t *w = both_alive();
    result(w, "up0", false, true);
    g_now += 3000;
    result(w, "up0", false, true);
    result(w, "up1", false, true);
    ASSERT_EQ_FMT((size_t)0, g_n_moves, "%zu");
    ASSERT(said(FIRC_LOG_WARN, "the interface check fails, even through the WAN: timed out"));
    done(w);
    PASS();
}

TEST a_round_that_ran_no_control_does_not_blame_the_wan(void) {
    firc_ifwatch_t *w = both_alive();
    result_as(w, "up0", false, true, false, last_gen("up0"), "the socket refused the check's mark");
    ASSERT(said(FIRC_LOG_WARN, "the interface check cannot run: the socket refused the check's mark"));
    ASSERT_FALSE(said(FIRC_LOG_WARN, "even through the WAN"));
    ASSERT_EQ_FMT((size_t)0, g_n_moves, "%zu");
    done(w);
    PASS();
}

/* Catches: a round that checked some entries said to be one where the check cannot run. */
TEST a_round_with_a_pass_names_the_entries_it_could_not_check(void) {
    firc_ifwatch_t *w = fresh();
    static const char *const ABC[] = {"up0", "up1", "up2"};
    g_groups[g_n++] = grp(1, ABC, 3);
    firc_ifwatch_tick(w);
    firc_ifcheck_result_t r[3];
    memset(r, 0, sizeof(r));
    for (size_t i = 0; i < 3; i++) {
        snprintf(r[i].iface, sizeof(r[i].iface), "%s", ABC[i]);
        r[i].gen = last_gen(ABC[i]);
        r[i].control_failed = i > 0;
        snprintf(r[i].error, sizeof(r[i].error), "%s", i > 0 ? "the check's host a.test does not resolve" : "");
    }
    r[0].passed = true;
    firc_ifwatch_results(w, r, 3);
    ASSERT(said(FIRC_LOG_WARN, "cannot check up1, up2: the check's host a.test does not resolve"));
    ASSERT_FALSE(said(FIRC_LOG_WARN, "the interface check cannot run"));
    done(w);
    PASS();
}

TEST a_result_from_before_a_forget_is_dropped(void) {
    firc_ifwatch_t *w = fresh();
    g_groups[g_n++] = grp(1, AB, 2);
    firc_ifwatch_tick(w);
    uint32_t old = last_gen("up0");
    firc_group_free(g_groups[0]);
    g_n = 0;
    firc_ifwatch_tick(w);
    g_groups[g_n++] = grp(1, AB, 2);
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)2, times_queued("up0"), "%zu");
    ASSERT(last_gen("up0") != old);
    result_as(w, "up0", true, false, true, old, "");
    firc_if_view_t v;
    ASSERT(firc_ifwatch_view(w, "up0", &v));
    ASSERT_EQm("the old check says nothing of the new record", FIRC_IF_UNKNOWN, v.state);
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMTm("and leaves the new check in flight", (size_t)2, times_queued("up0"), "%zu");
    result(w, "up0", true, false);
    ASSERT(firc_ifwatch_view(w, "up0", &v));
    ASSERT_EQ(FIRC_IF_ALIVE, v.state);
    done(w);
    PASS();
}

TEST every_entry_dead_blocks_the_group_and_errors_after_5_min(void) {
    firc_ifwatch_t *w = both_alive();
    firc_ifwatch_link(w, "up0", false);
    firc_ifwatch_link(w, "up1", false);
    ASSERT(last_move_is(1, true));
    ASSERT(said(FIRC_LOG_WARN, "group G: every interface of its list is dead"));
    ASSERT_FALSE(said(FIRC_LOG_ERROR, "group G: every interface of its list is dead"));
    g_now += 300000;
    firc_ifwatch_tick(w);
    ASSERT(said(FIRC_LOG_ERROR, "group G: every interface of its list is dead"));
    firc_event_reset_for_test();
    result(w, "up0", true, false);
    ASSERT(last_move_is(0, false));
    ASSERT(said(FIRC_LOG_INFO, "group G is back on up0"));
    ASSERT_EQ_FMTm("one line for the unblock", (size_t)1, times_said(FIRC_LOG_INFO, "group G"), "%zu");
    ASSERT_EQ_FMT((size_t)0, times_said(FIRC_LOG_WARN, "group G"), "%zu");
    done(w);
    PASS();
}

TEST an_unblock_by_pick_ends_the_all_dead_run(void) {
    firc_ifwatch_t *w = both_alive();
    firc_ifwatch_link(w, "up0", false);
    firc_ifwatch_link(w, "up1", false);
    ASSERT(last_move_is(1, true));
    g_now += 200000;
    static const char *const CB[] = {"up2", "up1"};
    firc_group_set_ifaces(g_groups[0], CB, 2);
    firc_event_reset_for_test();
    firc_ifchoice_t c = firc_ifwatch_pick(w, g_groups[0]);
    ASSERT_FALSE(c.all_dead);
    ASSERT(said(FIRC_LOG_INFO, "group G is back on up2"));
    firc_ifwatch_link(w, "up2", false);
    ASSERT(last_move_is(0, true));
    ASSERTm("a new run starts at WARN", said(FIRC_LOG_WARN, "group G: every interface of its list is dead"));
    g_now += 299999;
    firc_ifwatch_tick(w);
    ASSERT_FALSEm("and waits its own 5 min", said(FIRC_LOG_ERROR, "group G: every interface of its list is dead"));
    g_now += 1;
    firc_ifwatch_tick(w);
    ASSERT(said(FIRC_LOG_ERROR, "group G: every interface of its list is dead"));
    done(w);
    PASS();
}

TEST an_edited_list_keeps_the_active_entry_by_name(void) {
    firc_ifwatch_t *w = both_alive();
    firc_ifwatch_link(w, "up0", false);
    ASSERT(last_move_is(1, false));
    static const char *const CB[] = {"up2", "up1"};
    firc_group_set_ifaces(g_groups[0], CB, 2);
    firc_ifchoice_t c = firc_ifwatch_pick(w, g_groups[0]);
    ASSERT_EQ_FMT((size_t)1, c.active, "%zu");
    ASSERT_FALSE(c.all_dead);
    done(w);
    PASS();
}

TEST pick_puts_a_new_group_on_its_first_entry_that_is_not_dead(void) {
    firc_ifwatch_t *w = fresh();
    g_down = "up0";
    firc_group_t *g = grp(1, AB, 2);
    firc_ifchoice_t c = firc_ifwatch_pick(w, g);
    ASSERT_EQ_FMT((size_t)1, c.active, "%zu");
    firc_group_free(g);
    done(w);
    PASS();
}

TEST pick_knows_a_tunnel_entry_before_any_tick(void) {
    firc_ifwatch_t *w = fresh();
    static const char *const T[] = {"tunvless3", "up1"};
    g_tunnel = "tunvless3";
    g_tunnel_is_down = true;
    firc_group_t *g = grp(1, T, 2);
    firc_ifchoice_t c = firc_ifwatch_pick(w, g);
    ASSERT_EQ_FMT((size_t)1, c.active, "%zu");
    firc_group_free(g);
    done(w);
    PASS();
}

TEST a_refused_move_is_asked_again_on_the_next_tick(void) {
    firc_ifwatch_t *w = both_alive();
    g_move_err = FIRC_ERR_IO;
    firc_ifwatch_link(w, "up0", false);
    ASSERT_EQ_FMT((size_t)1, g_n_moves, "%zu");
    ASSERT(said(FIRC_LOG_WARN, "group G: the move to up1 failed"));
    g_move_err = FIRC_OK;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)2, g_n_moves, "%zu");
    ASSERT(last_move_is(1, false));
    result(w, "up0", true, false);
    g_move_err = FIRC_ERR_IO;
    ASSERT_EQm("a refused button says so", FIRC_ERR_IO, firc_ifwatch_move_up(w, G1));
    g_move_err = FIRC_OK;
    done(w);
    PASS();
}

TEST a_move_that_keeps_failing_is_an_error_after_a_minute(void) {
    firc_ifwatch_t *w = both_alive();
    g_move_err = FIRC_ERR_IO;
    firc_ifwatch_link(w, "up0", false);
    ASSERT(said(FIRC_LOG_WARN, "group G: the move to up1 failed"));
    g_now += 59999;
    firc_ifwatch_tick(w);
    ASSERT_FALSE(said(FIRC_LOG_ERROR, "group G: the move to up1 failed"));
    g_now += 1;
    firc_ifwatch_tick(w);
    ASSERT(said(FIRC_LOG_ERROR, "group G: the move to up1 failed"));
    done(w);
    PASS();
}

TEST a_dead_interface_s_reminder_counts_every_dead_result(void) {
    firc_ifwatch_t *w = both_alive();
    result(w, "up0", false, false);
    g_now += 3000;
    result(w, "up0", false, false);
    ASSERT(said(FIRC_LOG_WARN, "interface up0 is dead: timed out"));
    for (int i = 0; i < 3; i++) {
        g_now += 30000;
        result(w, "up0", false, false);
    }
    g_now += 510000;
    result(w, "up0", false, false);
    ASSERT(said(FIRC_LOG_WARN, "interface up0 is dead: timed out (still failing, 5 times for 10 min)"));
    done(w);
    PASS();
}

TEST a_move_of_an_unrouted_group_is_kept_but_not_said(void) {
    firc_ifwatch_t *w = both_alive();
    g_move_err = FIRC_ERR_NOENT;
    firc_ifwatch_link(w, "up0", false);
    ASSERT(last_move_is(1, false));
    ASSERT_FALSE(said(FIRC_LOG_WARN, "group G moved"));
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMTm("the entry is kept for the next enable", (size_t)1, g_n_moves, "%zu");
    result(w, "up0", true, false);
    ASSERT_EQm("the button says the group is not routed", FIRC_ERR_AGAIN, firc_ifwatch_move_up(w, G1));
    ASSERT_FALSE(said(FIRC_LOG_INFO, "group G moved"));
    done(w);
    PASS();
}

TEST move_up_needs_the_first_entry_alive(void) {
    firc_ifwatch_t *w = both_alive();
    firc_ifwatch_link(w, "up0", false);
    ASSERT_EQ(FIRC_ERR_STATE, firc_ifwatch_move_up(w, G1));
    result(w, "up0", true, false);
    ASSERT(last_move_is(1, false));
    ASSERT_EQ(FIRC_OK, firc_ifwatch_move_up(w, G1));
    ASSERT(last_move_is(0, false));
    ASSERT_EQ(FIRC_ERR_NOENT, firc_ifwatch_move_up(w, (firc_id_t){{9, 9, 9, 9}}));
    done(w);
    PASS();
}

TEST a_removed_group_s_interfaces_are_forgotten(void) {
    firc_ifwatch_t *w = both_alive();
    firc_if_view_t v;
    ASSERT(firc_ifwatch_view(w, "up0", &v));
    ASSERT_EQ_FMTm("fixture: both check rules are in", (size_t)2, check_rules(RTM_NEWRULE), "%zu");
    firc_group_free(g_groups[0]);
    g_n = 0;
    firc_ifwatch_tick(w);
    ASSERT_FALSE(firc_ifwatch_view(w, "up0", &v));
    ASSERT_EQ_FMTm("a forgotten interface takes its check rule back", (size_t)2, check_rules(RTM_DELRULE), "%zu");
    done(w);
    PASS();
}

TEST free_takes_every_check_rule_back(void) {
    firc_ifwatch_t *w = both_alive();
    ASSERT_EQ_FMT((size_t)2, check_rules(RTM_NEWRULE), "%zu");
    firc_ifwatch_free(w);
    ASSERT_EQ_FMT((size_t)2, check_rules(RTM_DELRULE), "%zu");
    kernel_done();
    PASS();
}

TEST a_lost_check_is_queued_again_after_two_minutes(void) {
    firc_ifwatch_t *w = fresh();
    g_groups[g_n++] = grp(1, AB, 2);
    firc_ifwatch_tick(w);
    g_now += FIRC_IFWATCH_LOST_MS - 1;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)1, times_queued("up0"), "%zu");
    g_now += 1;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)2, times_queued("up0"), "%zu");
    done(w);
    PASS();
}

static size_t distinct_queued(void) {
    size_t c = 0;
    for (size_t i = 0; i < g_n_queued && i < 64; i++) {
        bool first = true;
        for (size_t j = 0; j < i; j++) { first = first && strcmp(g_queued[i], g_queued[j]) != 0; }
        c += first ? 1u : 0u;
    }
    return c;
}

TEST the_seventeenth_interface_is_not_checked_and_said_once(void) {
    firc_ifwatch_t *w = fresh();
    static const char *const L[5][4] = {{"ca", "cb", "cc", "cd"},
                                        {"ce", "cf", "cg", "ch"},
                                        {"ci", "cj", "ck", "cl"},
                                        {"cm", "cn", "co", "cp"},
                                        {"cq", "cr", NULL, NULL}};
    for (uint8_t i = 0; i < 5; i++) { g_groups[g_n++] = grp((uint8_t)(i + 1), L[i], i < 4 ? 4u : 2u); }
    firc_ifwatch_tick(w);
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)16, distinct_queued(), "%zu");
    ASSERT_EQ_FMT((size_t)16, check_rules(RTM_NEWRULE), "%zu");
    firc_if_view_t v;
    ASSERT(firc_ifwatch_view(w, "cp", &v));
    ASSERT_FALSE(firc_ifwatch_view(w, "cq", &v));
    ASSERT_FALSE(firc_ifwatch_view(w, "cr", &v));
    ASSERT(said(FIRC_LOG_ERROR, "interface lists name more than 16 interfaces; cq is not checked"));
    ASSERT_FALSEm("said once", said(FIRC_LOG_ERROR, "cr is not checked") || said(FIRC_LOG_WARN, "cr is not checked"));
    ASSERT_FALSE(said(FIRC_LOG_DEBUG, "is not checked"));
    firc_group_free(g_groups[--g_n]);
    static const char *const OUT[] = {"ca", "cb"};
    firc_group_set_ifaces(g_groups[0], OUT, 2);
    firc_ifwatch_tick(w);
    firc_group_set_ifaces(g_groups[0], L[0], 4);
    g_groups[g_n++] = grp(5, L[4], 2);
    firc_event_reset_for_test();
    firc_ifwatch_tick(w);
    ASSERTm("an overflow after room came back is said again",
            said(FIRC_LOG_ERROR, "interface lists name more than 16 interfaces; cq is not checked"));
    done(w);
    PASS();
}

TEST a_swapped_name_at_the_cap_takes_the_freed_slot_at_once(void) {
    firc_ifwatch_t *w = fresh();
    static const char *const L[4][4] = {
        {"ca", "cb", "cc", "cd"}, {"ce", "cf", "cg", "ch"}, {"ci", "cj", "ck", "cl"}, {"cm", "cn", "co", "cp"}};
    for (uint8_t i = 0; i < 4; i++) { g_groups[g_n++] = grp((uint8_t)(i + 1), L[i], 4); }
    firc_ifwatch_tick(w);
    static const char *const S[] = {"ca", "cb", "cc", "cq"};
    firc_group_set_ifaces(g_groups[0], S, 4);
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)1, times_queued("cq"), "%zu");
    ASSERT_FALSE(said(FIRC_LOG_ERROR, "is not checked") || said(FIRC_LOG_WARN, "is not checked"));
    done(w);
    PASS();
}

static int64_t gw_now;
static firc_group_t *gw_grp[32];
static size_t gw_n_grp;
static size_t gw_moves;
static size_t gw_last_to;
static bool gw_last_blocked;
static firc_ifcheck_group_job_t gw_jobs[32];
static size_t gw_n_jobs;
static bool gw_tun_up1;
static bool gw_tun_down1;
static bool gw_link_down0;

static int64_t gw_clock(void) { return gw_now; }
static size_t gw_n_groups_cb(void *ud) {
    (void)ud;
    return gw_n_grp;
}
static const firc_group_t *gw_group_at_cb(void *ud, size_t i) {
    (void)ud;
    return i < gw_n_grp ? gw_grp[i] : NULL;
}
static firc_err_t gw_move_cb(void *ud, firc_id_t id, size_t to, bool blocked) {
    (void)ud;
    (void)id;
    gw_moves++;
    gw_last_to = to;
    gw_last_blocked = blocked;
    return FIRC_OK;
}
static bool gw_link_up_cb(void *ud, const char *name) {
    (void)ud;
    if (gw_link_down0 && strcmp(name, "eth3") == 0) { return false; }
    return true;
}
static bool gw_tun_known_cb(void *ud, const char *dev) {
    (void)ud;
    return gw_tun_up1 && strcmp(dev, "up1") == 0;
}
static bool gw_tun_down_cb(void *ud, const char *dev) {
    (void)ud;
    return gw_tun_down1 && strcmp(dev, "up1") == 0;
}
static firc_err_t gw_queue_cb(void *ud, const char *name, uint32_t mark, uint32_t gen) {
    (void)ud;
    (void)name;
    (void)mark;
    (void)gen;
    return FIRC_OK;
}
static firc_err_t gw_queue_group_cb(void *ud, const firc_ifcheck_group_job_t *job) {
    (void)ud;
    if (gw_n_jobs < sizeof(gw_jobs) / sizeof(gw_jobs[0])) { gw_jobs[gw_n_jobs++] = *job; }
    return FIRC_OK;
}
static bool gw_resolve_for_cb(void *ud, const firc_group_t *g, const char *iface, bool tunnel,
                              char *server, size_t cap, uint16_t *port, bool *mark_dns) {
    (void)ud;
    (void)iface;
    const char *own = g != NULL && g->resolve.server != NULL && g->resolve.server[0] != '\0' ? g->resolve.server : NULL;
    snprintf(server, cap, "%s", own != NULL ? own : (tunnel ? "1.1.1.1" : "8.8.8.8"));
    *port = 53;
    *mark_dns = true;
    return true;
}

static void gw_reset(firc_group_check_mode_t mode, const char *url, uint32_t fail_thr, uint32_t rec_thr) {
    gw_now = 100000;
    gw_moves = 0;
    gw_last_to = 0;
    gw_last_blocked = false;
    gw_n_jobs = 0;
    gw_tun_up1 = false;
    gw_tun_down1 = false;
    gw_link_down0 = false;
    gw_n_grp = 1;
    gw_grp[0] = firc_group_new();
    gw_grp[0]->id = (firc_id_t){{101, 0, 0, 0}};
    firc_strset(&gw_grp[0]->name, "svc");
    static const char *const ifs[2] = {"eth3", "tunvless0"};
    (void)firc_group_set_ifaces(gw_grp[0], ifs, 2);
    gw_grp[0]->enable = true;
    gw_grp[0]->check.mode = mode;
    if (url != NULL) { firc_strset(&gw_grp[0]->check.url, url); }
    gw_grp[0]->check.interval_sec = 10;
    gw_grp[0]->check.timeout_ms = 2000;
    gw_grp[0]->check.fail_threshold = fail_thr;
    gw_grp[0]->check.recover_threshold = rec_thr;
}

static void gw_teardown(firc_ifwatch_t *w) {
    firc_ifwatch_free(w);
    for (size_t i = 0; i < gw_n_grp; i++) {
        firc_group_free(gw_grp[i]);
        gw_grp[i] = NULL;
    }
    gw_n_grp = 0;
}

static firc_ifwatch_t *gw_new(firc_rtnl_t *rtnl) {
    firc_ifwatch_deps_t d = {
        .rtnl = rtnl,
        .start_table = 500,
        .n_groups = gw_n_groups_cb,
        .group_at = gw_group_at_cb,
        .move = gw_move_cb,
        .link_up = gw_link_up_cb,
        .tunnel_known = gw_tun_known_cb,
        .tunnel_down = gw_tun_down_cb,
        .queue = gw_queue_cb,
        .queue_group = gw_queue_group_cb,
        .resolve_for = gw_resolve_for_cb,
        .now_ms = gw_clock,
    };
    return firc_ifwatch_new(&d);
}

static void gw_send_result(firc_ifwatch_t *w, uint32_t gen, bool ok0, bool guard0, bool ok1, bool guard1) {
    firc_ifcheck_group_result_t r;
    memset(&r, 0, sizeof(r));
    r.group_id = gw_grp[0]->id;
    r.gen = gen;
    r.n_cands = 2;
    snprintf(r.cands[0].iface, sizeof(r.cands[0].iface), "%s", firc_group_iface_at(gw_grp[0], 0));
    r.cands[0].passed = ok0;
    r.cands[0].guarded = guard0;
    if (!ok0) { snprintf(r.cands[0].error, sizeof(r.cands[0].error), "timeout"); }
    snprintf(r.cands[1].iface, sizeof(r.cands[1].iface), "%s", firc_group_iface_at(gw_grp[0], 1));
    r.cands[1].passed = ok1;
    r.cands[1].guarded = guard1;
    if (!ok1) { snprintf(r.cands[1].error, sizeof(r.cands[1].error), "timeout"); }
    firc_ifwatch_group_results(w, &r);
}

TEST group_check_url_failover_confirm_and_confirmed_failback(void) {
    gw_reset(FIRC_GROUP_CHECK_URL, "https://gemini.google.com/", 2, 3);
    firc_ifwatch_t *w = gw_new(NULL);
    ASSERT(w != NULL);

    firc_ifwatch_tick(w);
    ASSERT_EQ((size_t)1, gw_n_jobs);
    uint32_t gen = gw_jobs[0].gen;
    gw_send_result(w, gen, true, false, true, false);
    ASSERT_EQ((size_t)0, gw_moves);

    gw_now += 10000;
    firc_ifwatch_tick(w);
    ASSERT_EQ((size_t)2, gw_n_jobs);
    gw_send_result(w, gw_jobs[1].gen, false, false, true, false);
    ASSERT_EQm("1st failure stays on primary until confirmed", (size_t)0, gw_moves);

    gw_now += FIRC_IFCHOOSE_CONFIRM_MS - 1;
    firc_ifwatch_tick(w);
    ASSERT_EQ((size_t)2, gw_n_jobs);
    gw_now += 1;
    firc_ifwatch_tick(w);
    ASSERT_EQm("3s confirm probe runs early", (size_t)3, gw_n_jobs);
    gw_send_result(w, gw_jobs[2].gen, false, false, true, false);
    ASSERT_EQ((size_t)1, gw_moves);
    ASSERT_EQ((size_t)1, gw_last_to);
    ASSERT_FALSE(gw_last_blocked);
    ASSERT_STR_EQ("eth3", firc_group_iface_at(gw_grp[0], 0));
    ASSERT_EQ(FIRC_ERR_STATE, firc_ifwatch_move_up(w, gw_grp[0]->id));

    firc_if_view_t v;
    ASSERT(firc_ifwatch_group_view(w, gw_grp[0]->id, "eth3", &v));
    ASSERT_EQ(FIRC_IF_DEAD, v.state);
    ASSERT(firc_ifwatch_group_view(w, gw_grp[0]->id, "tunvless0", &v));
    ASSERT_EQ(FIRC_IF_ALIVE, v.state);

    /* Confirmed failback requires 3 consecutive passes on eth3. */
    gw_now += 10000;
    firc_ifwatch_tick(w);
    gw_send_result(w, gw_jobs[gw_n_jobs - 1].gen, true, false, true, false);
    ASSERT_EQm("pass 1/3 does not fail back yet", (size_t)1, gw_moves);

    gw_now += FIRC_IFCHOOSE_CONFIRM_MS;
    firc_ifwatch_tick(w);
    gw_send_result(w, gw_jobs[gw_n_jobs - 1].gen, true, false, true, false);
    ASSERT_EQm("pass 2/3 does not fail back yet", (size_t)1, gw_moves);

    gw_now += FIRC_IFCHOOSE_CONFIRM_MS;
    firc_ifwatch_tick(w);
    gw_send_result(w, gw_jobs[gw_n_jobs - 1].gen, true, false, true, false);
    ASSERT_EQm("pass 3/3 fails back to primary", (size_t)2, gw_moves);
    ASSERT_EQ((size_t)0, gw_last_to);

    gw_teardown(w);
    PASS();
}

TEST group_check_guarded_and_stale_gen_are_ignored(void) {
    gw_reset(FIRC_GROUP_CHECK_KEENDNS, NULL, 2, 2);
    firc_ifwatch_t *w = gw_new(NULL);
    ASSERT(w != NULL);

    firc_ifwatch_tick(w);
    uint32_t gen1 = gw_jobs[0].gen;
    gw_send_result(w, gen1, true, false, true, false);

    /* Both fail (guarded) -> must not mark DEAD or move. */
    for (int i = 0; i < 3; i++) {
        gw_now += 10000;
        firc_ifwatch_tick(w);
        gw_send_result(w, gw_jobs[gw_n_jobs - 1].gen, false, true, false, true);
    }
    ASSERT_EQ((size_t)0, gw_moves);

    /* Config change bumps probe_gen; in-flight result with old gen is discarded. */
    gw_now += 10000;
    firc_ifwatch_tick(w);
    uint32_t in_flight_gen = gw_jobs[gw_n_jobs - 1].gen;
    (void)firc_strset(&gw_grp[0]->check.url, "https://example.org/");
    gw_grp[0]->check.mode = FIRC_GROUP_CHECK_URL;
    firc_ifwatch_tick(w);
    uint32_t gen2 = gw_jobs[gw_n_jobs - 1].gen;
    ASSERT(gen2 != in_flight_gen);

    gw_send_result(w, in_flight_gen, false, false, true, false);
    firc_if_view_t v;
    ASSERT(firc_ifwatch_group_view(w, gw_grp[0]->id, "eth3", &v));
    ASSERT_EQ(FIRC_IF_UNKNOWN, v.state);

    gw_teardown(w);
    PASS();
}

TEST group_check_allocates_checkroute_for_tunnel_candidate(void) {
    gw_reset(FIRC_GROUP_CHECK_KEENDNS, NULL, 2, 2);
    static const char *const ifs[2] = {"up0", "up1"};
    (void)firc_group_set_ifaces(gw_grp[0], ifs, 2);
    gw_tun_up1 = true;

    fake_rtnl_t *k = fake_rtnl_start(&g_rtnl);
    ASSERT(k != NULL);
    fake_rtnl_set_link_flags(k, 0x1 | 0x10);
    firc_rtnl_set_nametoindex_for_test(three_links);
    firc_ifwatch_t *w = gw_new(g_rtnl);
    ASSERT(w != NULL);

    firc_ifwatch_tick(w);
    ASSERT_EQ((size_t)1, gw_n_jobs);
    ASSERT_FALSE(gw_jobs[0].cands[0].skip);
    ASSERT_FALSE(gw_jobs[0].cands[1].skip);
    ASSERT(gw_jobs[0].cands[0].mark != 0);
    ASSERT(gw_jobs[0].cands[1].mark != 0);
    ASSERT(gw_jobs[0].cands[0].mark != gw_jobs[0].cands[1].mark);
    ASSERT_STR_EQ("8.8.8.8", gw_jobs[0].cands[0].dns_server);
    ASSERT_STR_EQ("1.1.1.1", gw_jobs[0].cands[1].dns_server);
    ASSERT_EQ(gw_jobs[0].cands[1].mark, gw_jobs[0].cands[1].dns_mark);

    gw_teardown(w);
    firc_rtnl_set_nametoindex_for_test(NULL);
    firc_rtnl_close(g_rtnl);
    fake_rtnl_stop(k);
    g_rtnl = NULL;
    PASS();
}

static void gw_send_job_result(firc_ifwatch_t *w, const firc_ifcheck_group_job_t *job, const bool *ok,
                               const bool *guard) {
    firc_ifcheck_group_result_t r;
    memset(&r, 0, sizeof(r));
    r.group_id = job->group_id;
    r.gen = job->gen;
    r.n_cands = job->n_cands;
    for (size_t i = 0; i < job->n_cands; i++) {
        snprintf(r.cands[i].iface, sizeof(r.cands[i].iface), "%s", job->cands[i].iface);
        r.cands[i].passed = ok[i];
        r.cands[i].guarded = guard[i];
        if (!ok[i]) { snprintf(r.cands[i].error, sizeof(r.cands[i].error), "timeout"); }
    }
    firc_ifwatch_group_results(w, &r);
}

TEST group_check_deduplicates_many_identical_groups_into_single_probe(void) {
    gw_reset(FIRC_GROUP_CHECK_URL, NULL, 2, 3);
    firc_group_free(gw_grp[0]);
    gw_n_grp = 0;
    static const char *const ifs[3] = {"tunvless0", "tunvless1", "eth3"};
    for (size_t i = 0; i < 24; i++) {
        gw_grp[i] = firc_group_new();
        gw_grp[i]->id = (firc_id_t){{200u, (uint8_t)i, 0, 1}};
        char name[32];
        snprintf(name, sizeof(name), "g%zu", i);
        (void)firc_strset(&gw_grp[i]->name, name);
        (void)firc_group_set_ifaces(gw_grp[i], ifs, 3);
        gw_grp[i]->enable = true;
        gw_grp[i]->check.mode = FIRC_GROUP_CHECK_URL;
        gw_grp[i]->check.interval_sec = 0;
        gw_grp[i]->check.timeout_ms = 2000;
        gw_grp[i]->check.fail_threshold = 2;
        gw_grp[i]->check.recover_threshold = 3;
        gw_n_grp++;
    }
    firc_ifwatch_t *w = gw_new(NULL);
    ASSERT(w != NULL);

    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)1, gw_n_jobs, "%zu");
    static const bool k_all_ok[3] = {true, true, true};
    static const bool k_no_guard[3] = {false, false, false};
    gw_send_job_result(w, &gw_jobs[0], k_all_ok, k_no_guard);
    ASSERT_EQ_FMT((size_t)0, gw_moves, "%zu");
    for (size_t i = 0; i < 24; i++) {
        firc_if_view_t v;
        ASSERT(firc_ifwatch_group_view(w, gw_grp[i]->id, "tunvless0", &v));
        ASSERT_EQ(FIRC_IF_ALIVE, v.state);
    }

    /* Zero interval_sec inherits global w->interval_ms (30s), NOT 10s. */
    gw_now += 10000;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)1, gw_n_jobs, "%zu");

    gw_now += 20000;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)2, gw_n_jobs, "%zu");
    static const bool k_first_fail[3] = {false, true, true};
    gw_send_job_result(w, &gw_jobs[1], k_first_fail, k_no_guard);
    ASSERT_EQ_FMT((size_t)0, gw_moves, "%zu");

    gw_now += FIRC_IFCHOOSE_CONFIRM_MS;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)3, gw_n_jobs, "%zu");
    gw_send_job_result(w, &gw_jobs[2], k_first_fail, k_no_guard);
    ASSERT_EQ_FMTm("all 24 groups fail over together from 3 total probes", (size_t)24, gw_moves, "%zu");
    for (size_t i = 0; i < 24; i++) {
        firc_if_view_t v;
        ASSERT(firc_ifwatch_group_view(w, gw_grp[i]->id, "tunvless0", &v));
        ASSERT_EQ(FIRC_IF_DEAD, v.state);
    }

    /* Mutating the leader while its probe is in flight wakes followers so g1 takes over immediately. */
    gw_now += 30000;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)4, gw_n_jobs, "%zu");
    ASSERT(firc_id_equal(gw_jobs[3].group_id, gw_grp[0]->id));
    (void)firc_strset(&gw_grp[0]->check.url, "https://custom.example/");
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)6, gw_n_jobs, "%zu");
    const firc_ifcheck_group_job_t *replacement = NULL;
    for (size_t i = 4; i < gw_n_jobs; i++) {
        if (!firc_id_equal(gw_jobs[i].group_id, gw_grp[0]->id)) { replacement = &gw_jobs[i]; }
    }
    ASSERT(replacement != NULL);
    gw_send_job_result(w, replacement, k_all_ok, k_no_guard);
    gw_now += 1000;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)6, gw_n_jobs, "%zu");

    gw_teardown(w);
    PASS();
}

TEST group_check_zero_interval_tracks_global_configure(void) {
    gw_reset(FIRC_GROUP_CHECK_URL, "https://one.example/", 2, 2);
    gw_grp[0]->check.interval_sec = 0;
    gw_grp[1] = firc_group_new();
    gw_grp[1]->id = (firc_id_t){{102, 0, 0, 0}};
    (void)firc_strset(&gw_grp[1]->name, "svc-custom");
    static const char *const ifs[2] = {"eth3", "tunvless0"};
    (void)firc_group_set_ifaces(gw_grp[1], ifs, 2);
    gw_grp[1]->enable = true;
    gw_grp[1]->check.mode = FIRC_GROUP_CHECK_URL;
    (void)firc_strset(&gw_grp[1]->check.url, "https://two.example/");
    gw_grp[1]->check.interval_sec = 10;
    gw_grp[1]->check.timeout_ms = 2000;
    gw_grp[1]->check.fail_threshold = 2;
    gw_grp[1]->check.recover_threshold = 2;
    gw_n_grp = 2;

    firc_ifwatch_t *w = gw_new(NULL);
    ASSERT(w != NULL);
    firc_app_config_t cfg;
    memset(&cfg, 0, sizeof(cfg));
    cfg.check.interval = 60 * FIRC_DURATION_SEC;
    firc_ifwatch_configure(w, &cfg);

    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)2, gw_n_jobs, "%zu");
    static const bool ok2[2] = {true, true};
    static const bool g2[2] = {false, false};
    gw_send_job_result(w, &gw_jobs[0], ok2, g2);
    gw_send_job_result(w, &gw_jobs[1], ok2, g2);

    /* At +10s only the explicit 10s group re-probes; the 0s group waits for 60s. */
    gw_now += 10000;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)3, gw_n_jobs, "%zu");
    ASSERT(firc_id_equal(gw_jobs[2].group_id, gw_grp[1]->id));
    gw_send_job_result(w, &gw_jobs[2], ok2, g2);

    /* Shrinking global interval to 15s pulls the 0s group forward to now + 15s. */
    cfg.check.interval = 15 * FIRC_DURATION_SEC;
    firc_ifwatch_configure(w, &cfg);
    gw_now += 10000;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)4, gw_n_jobs, "%zu");
    ASSERT(firc_id_equal(gw_jobs[3].group_id, gw_grp[1]->id));
    gw_send_job_result(w, &gw_jobs[3], ok2, g2);

    gw_now += 5000;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)5, gw_n_jobs, "%zu");
    ASSERT(firc_id_equal(gw_jobs[4].group_id, gw_grp[0]->id));

    gw_teardown(w);
    PASS();
}

/* Starts an empty fixture; groups are then added with gw_add. */
static void gw_begin(void) {
    gw_reset(FIRC_GROUP_CHECK_URL, NULL, 2, 3);
    firc_group_free(gw_grp[0]);
    gw_grp[0] = NULL;
    gw_n_grp = 0;
}

static firc_group_t *gw_add(uint8_t tag, const char *const *ifs, size_t n, uint32_t fail_thr) {
    firc_group_t *g = firc_group_new();
    g->id = (firc_id_t){{210u, tag, 0, 1}};
    char name[16];
    snprintf(name, sizeof(name), "t%u", (unsigned)tag);
    (void)firc_strset(&g->name, name);
    (void)firc_group_set_ifaces(g, ifs, n);
    g->enable = true;
    g->check.mode = FIRC_GROUP_CHECK_URL;
    g->check.interval_sec = 0;
    g->check.timeout_ms = 2000;
    g->check.fail_threshold = fail_thr;
    g->check.recover_threshold = 3;
    gw_grp[gw_n_grp++] = g;
    return g;
}

/* Result for a job: every candidate passes except the ones named in `failing` (comma-free single name or NULL). */
static void gw_answer(firc_ifwatch_t *w, size_t job, const char *failing) {
    bool ok[FIRC_GROUP_IFACES_MAX];
    bool guard[FIRC_GROUP_IFACES_MAX];
    for (size_t i = 0; i < gw_jobs[job].n_cands; i++) {
        ok[i] = failing == NULL || strcmp(gw_jobs[job].cands[i].iface, failing) != 0;
        guard[i] = false;
    }
    gw_send_job_result(w, &gw_jobs[job], ok, guard);
}

static firc_if_state_t gw_state(firc_ifwatch_t *w, const firc_group_t *g, const char *iface) {
    firc_if_view_t v;
    if (!firc_ifwatch_group_view(w, g->id, iface, &v)) { return FIRC_IF_UNKNOWN; }
    return v.state;
}

/* Catches: a 2-interface rule under a 3-interface leader probing on its own, or a failure the leader confirmed
 * through a candidate the rule does not have counting against the rule. */
TEST group_check_subset_rule_shares_probe_and_keeps_its_own_guard(void) {
    gw_begin();
    static const char *const L3[] = {"tunvless0", "tunvless1", "eth3"};
    static const char *const F2[] = {"tunvless0", "eth3"};
    firc_group_t *lead = gw_add(1, L3, 3, 2);
    firc_group_t *sub = gw_add(2, F2, 2, 2);
    firc_ifwatch_t *w = gw_new(NULL);
    ASSERT(w != NULL);

    firc_ifwatch_tick(w);
    ASSERT_EQ_FMTm("one probe covers both rules", (size_t)1, gw_n_jobs, "%zu");
    ASSERT(firc_id_equal(gw_jobs[0].group_id, lead->id));
    gw_answer(w, 0, NULL);
    ASSERT_EQ(FIRC_IF_ALIVE, gw_state(w, sub, "tunvless0"));

    /* tunvless0 and eth3 fail, only tunvless1 passes: the leader has a passing peer, the rule does not. */
    static const bool ok[3] = {false, true, false};
    static const bool guard[3] = {false, false, false};
    gw_now += 30000;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)2, gw_n_jobs, "%zu");
    gw_send_job_result(w, &gw_jobs[1], ok, guard);
    gw_now += FIRC_IFCHOOSE_CONFIRM_MS;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMTm("the rule is not pulled into the leader's confirm", (size_t)3, gw_n_jobs, "%zu");
    gw_send_job_result(w, &gw_jobs[2], ok, guard);
    ASSERT_EQ(FIRC_IF_DEAD, gw_state(w, lead, "tunvless0"));
    ASSERT_EQm("every candidate of the rule failed together: guarded, not dead", FIRC_IF_ALIVE,
               gw_state(w, sub, "tunvless0"));

    gw_teardown(w);
    PASS();
}

/* Catches: rules that resolve the check host through different DNS servers sharing one probe. */
TEST group_check_rules_with_different_dns_never_share_a_probe(void) {
    gw_begin();
    static const char *const P[] = {"eth3", "tunvless0"};
    gw_add(1, P, 2, 2);
    firc_group_t *own = gw_add(2, P, 2, 2);
    (void)firc_strset(&own->resolve.server, "9.9.9.9");
    firc_ifwatch_t *w = gw_new(NULL);
    ASSERT(w != NULL);
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)2, gw_n_jobs, "%zu");
    ASSERT_STR_EQ("9.9.9.9", gw_jobs[1].cands[0].dns_server);
    gw_teardown(w);
    PASS();
}

/* Catches: followers left in flight until the 2 min loss timeout after their leader rule is deleted. */
TEST group_check_followers_take_over_at_once_when_the_leader_is_deleted(void) {
    gw_begin();
    static const char *const P[] = {"eth3", "tunvless0"};
    firc_group_t *lead = gw_add(1, P, 2, 2);
    firc_group_t *fol = gw_add(2, P, 2, 2);
    firc_ifwatch_t *w = gw_new(NULL);
    ASSERT(w != NULL);
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)1, gw_n_jobs, "%zu");
    ASSERT(firc_id_equal(gw_jobs[0].group_id, lead->id));

    firc_group_free(lead);
    gw_grp[0] = fol;
    gw_grp[1] = NULL;
    gw_n_grp = 1;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMTm("the follower probes itself on the next tick", (size_t)2, gw_n_jobs, "%zu");
    ASSERT(firc_id_equal(gw_jobs[1].group_id, fol->id));
    gw_answer(w, 0, NULL);
    ASSERT_EQm("the deleted leader's late result is dropped", FIRC_IF_UNKNOWN, gw_state(w, fol, "eth3"));
    gw_answer(w, 1, NULL);
    ASSERT_EQ(FIRC_IF_ALIVE, gw_state(w, fol, "eth3"));

    gw_teardown(w);
    PASS();
}

/* Catches: a lost shared probe re-queued once per follower instead of once. */
TEST group_check_a_lost_shared_probe_is_queued_again_once(void) {
    gw_begin();
    static const char *const P[] = {"eth3", "tunvless0"};
    for (uint8_t i = 0; i < 4; i++) { gw_add((uint8_t)(i + 1), P, 2, 2); }
    firc_ifwatch_t *w = gw_new(NULL);
    ASSERT(w != NULL);
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)1, gw_n_jobs, "%zu");
    gw_now += FIRC_IFWATCH_LOST_MS - 1;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)1, gw_n_jobs, "%zu");
    gw_now += 1;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)2, gw_n_jobs, "%zu");
    gw_answer(w, 1, NULL);
    for (size_t i = 0; i < 4; i++) { ASSERT_EQ(FIRC_IF_ALIVE, gw_state(w, gw_grp[i], "eth3")); }
    gw_teardown(w);
    PASS();
}

/* Catches: a rule covered by two leaders out of phase being measured by both, twice per interval. */
TEST group_check_two_leaders_out_of_phase_never_double_a_rule_rate(void) {
    gw_begin();
    static const char *const A[] = {"eth3", "tunvless0", "tunvless1"};
    static const char *const F[] = {"eth3", "tunvless0"};
    static const char *const B[] = {"eth3", "tunvless0", "tunvless2"};
    firc_group_t *lead_a = gw_add(1, A, 3, 2);
    firc_group_t *fol = gw_add(2, F, 2, 1);
    firc_ifwatch_t *w = gw_new(NULL);
    ASSERT(w != NULL);
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)1, gw_n_jobs, "%zu");
    gw_answer(w, 0, NULL);

    gw_now += 15000;
    gw_add(3, B, 3, 2);
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)2, gw_n_jobs, "%zu");
    gw_answer(w, 1, "eth3");
    ASSERT_EQm("half way through its interval the rule is not pulled into the other leader's probe", FIRC_IF_ALIVE,
               gw_state(w, fol, "eth3"));

    /* At 30 s A probes again (B too: its failure scheduled a confirm); the rule rides A, its own phase. */
    gw_now += 15000;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)4, gw_n_jobs, "%zu");
    ASSERT(firc_id_equal(gw_jobs[2].group_id, lead_a->id));
    gw_answer(w, 2, "eth3");
    ASSERT_EQm("its own phase still measures it", FIRC_IF_DEAD, gw_state(w, fol, "eth3"));
    gw_teardown(w);
    PASS();
}

/* Catches: a 3 s confirm brought forward by another leader's probe, killing an entry after ~1 s. */
TEST group_check_a_pending_confirm_is_never_brought_forward(void) {
    gw_begin();
    static const char *const A[] = {"eth3", "tunvless0", "tunvless1"};
    static const char *const F[] = {"eth3", "tunvless0"};
    static const char *const B[] = {"eth3", "tunvless0", "tunvless2"};
    gw_add(1, A, 3, 2);
    firc_group_t *fol = gw_add(2, F, 2, 2);
    firc_ifwatch_t *w = gw_new(NULL);
    ASSERT(w != NULL);
    firc_ifwatch_tick(w);
    gw_answer(w, 0, NULL);

    gw_now += 30000;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)2, gw_n_jobs, "%zu");
    gw_answer(w, 1, "eth3");
    ASSERT_EQ(FIRC_IF_ALIVE, gw_state(w, fol, "eth3"));

    gw_now += 1000;
    gw_add(3, B, 3, 2);
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)3, gw_n_jobs, "%zu");
    gw_answer(w, 2, "eth3");
    ASSERT_EQm("the confirm waits its 3 s", FIRC_IF_ALIVE, gw_state(w, fol, "eth3"));

    gw_now += FIRC_IFCHOOSE_CONFIRM_MS - 1000;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)4, gw_n_jobs, "%zu");
    gw_answer(w, 3, "eth3");
    ASSERT_EQ(FIRC_IF_DEAD, gw_state(w, fol, "eth3"));
    gw_teardown(w);
    PASS();
}

/* Catches: every rule added later starting its own schedule, so N rules drift into N probes per interval. */
TEST group_check_a_rule_added_later_joins_the_existing_schedule(void) {
    gw_begin();
    static const char *const P[] = {"eth3", "tunvless0"};
    gw_add(1, P, 2, 2);
    firc_ifwatch_t *w = gw_new(NULL);
    ASSERT(w != NULL);
    firc_ifwatch_tick(w);
    gw_answer(w, 0, NULL);

    gw_now += 10000;
    firc_group_t *late = gw_add(2, P, 2, 2);
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMTm("the new rule queues nothing of its own", (size_t)1, gw_n_jobs, "%zu");
    ASSERT_EQm("and has the shared state at once", FIRC_IF_ALIVE, gw_state(w, late, "eth3"));

    gw_now += 20000;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMTm("both rules share the next probe", (size_t)2, gw_n_jobs, "%zu");
    gw_now += 29000;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)2, gw_n_jobs, "%zu");
    gw_teardown(w);
    PASS();
}

/* Catches: a link that comes up while a probe is in flight waiting a whole interval for its first real check. */
TEST group_check_a_link_up_during_a_probe_rechecks_on_its_result(void) {
    gw_begin();
    static const char *const P[] = {"eth3", "tunvless0"};
    firc_group_t *g = gw_add(1, P, 2, 2);
    gw_link_down0 = true;
    firc_ifwatch_t *w = gw_new(NULL);
    ASSERT(w != NULL);
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)1, gw_n_jobs, "%zu");
    ASSERT(gw_jobs[0].cands[0].skip);

    gw_now += 500;
    gw_link_down0 = false;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)1, gw_n_jobs, "%zu");
    static const bool ok[2] = {false, true};
    static const bool guard[2] = {true, false};
    gw_send_job_result(w, &gw_jobs[0], ok, guard);
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMTm("the risen link is probed at once", (size_t)2, gw_n_jobs, "%zu");
    ASSERT_FALSE(gw_jobs[1].cands[0].skip);
    gw_answer(w, 1, NULL);
    gw_now += 1;
    firc_ifwatch_tick(w);
    ASSERT_EQ(FIRC_IF_DEAD, gw_state(w, g, "eth3"));
    gw_teardown(w);
    PASS();
}

/* Catches: a rule recreated during a partial outage taking DEAD states from a wider rule whose failures were
 * confirmed by a candidate the new rule does not have, and blocking its traffic. */
TEST group_check_a_new_subset_rule_never_inherits_a_wider_rule_s_dead(void) {
    gw_begin();
    static const char *const L3[] = {"tunvless0", "tunvless1", "eth3"};
    static const char *const F2[] = {"tunvless0", "eth3"};
    firc_group_t *lead = gw_add(1, L3, 3, 2);
    firc_ifwatch_t *w = gw_new(NULL);
    ASSERT(w != NULL);
    firc_ifwatch_tick(w);
    gw_answer(w, 0, NULL);
    static const bool ok[3] = {false, true, false};
    static const bool guard[3] = {false, false, false};
    gw_now += 30000;
    firc_ifwatch_tick(w);
    gw_send_job_result(w, &gw_jobs[1], ok, guard);
    gw_now += FIRC_IFCHOOSE_CONFIRM_MS;
    firc_ifwatch_tick(w);
    gw_send_job_result(w, &gw_jobs[2], ok, guard);
    ASSERT_EQ(FIRC_IF_DEAD, gw_state(w, lead, "tunvless0"));
    ASSERT_EQ(FIRC_IF_DEAD, gw_state(w, lead, "eth3"));

    gw_now += 5000;
    firc_group_t *fresh = gw_add(2, F2, 2, 2);
    size_t before = gw_n_jobs;
    gw_last_blocked = false;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMTm("the new rule probes for itself", before + 1, gw_n_jobs, "%zu");
    ASSERT_EQ(FIRC_IF_UNKNOWN, gw_state(w, fresh, "tunvless0"));
    ASSERT_EQ(FIRC_IF_UNKNOWN, gw_state(w, fresh, "eth3"));
    ASSERT_FALSEm("and is not blocked", gw_last_blocked);
    gw_teardown(w);
    PASS();
}

/* Catches: a 2-interface rule and its 3-interface leader drifting apart through the leader's confirm rounds and
 * probing separately forever, two probes per interval instead of one. */
TEST group_check_drifted_subset_and_leader_meet_again(void) {
    gw_begin();
    static const char *const L3[] = {"tunvless0", "tunvless1", "eth3"};
    static const char *const F2[] = {"tunvless0", "eth3"};
    gw_add(1, L3, 3, 2);
    gw_add(2, F2, 2, 2);
    firc_ifwatch_t *w = gw_new(NULL);
    ASSERT(w != NULL);
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)1, gw_n_jobs, "%zu");
    gw_answer(w, 0, NULL);

    /* tunvless0 and eth3 fail, tunvless1 passes: the leader confirms, the rule is guarded and keeps its schedule. */
    static const bool ok[3] = {false, true, false};
    static const bool guard[3] = {false, false, false};
    gw_now += 30000;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)2, gw_n_jobs, "%zu");
    gw_send_job_result(w, &gw_jobs[1], ok, guard);
    gw_now += FIRC_IFCHOOSE_CONFIRM_MS;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMTm("the leader's confirm runs alone", (size_t)3, gw_n_jobs, "%zu");
    gw_send_job_result(w, &gw_jobs[2], ok, guard);

    /* Both are due at 60 s again: one shared probe, and none of the leader's own a few seconds later. */
    gw_now += 30000 - FIRC_IFCHOOSE_CONFIRM_MS;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)4, gw_n_jobs, "%zu");
    gw_send_job_result(w, &gw_jobs[3], ok, guard);
    for (int s = 0; s < 10; s++) {
        gw_now += 1000;
        firc_ifwatch_tick(w);
    }
    ASSERT_EQ_FMTm("no second probe in the same interval", (size_t)4, gw_n_jobs, "%zu");
    gw_now += 20000;
    firc_ifwatch_tick(w);
    ASSERT_EQ_FMT((size_t)5, gw_n_jobs, "%zu");
    gw_teardown(w);
    PASS();
}

GREATEST_MAIN_DEFS();

int main(int argc, char **argv) {
    GREATEST_MAIN_BEGIN();
    RUN_TEST(a_list_s_interfaces_are_checked_at_once_and_a_single_one_never);
    RUN_TEST(the_confirm_is_asked_3_s_later_and_the_second_failure_moves);
    RUN_TEST(a_regular_check_waits_the_interval_and_a_shorter_one_brings_it_forward);
    RUN_TEST(the_group_moves_back_after_120_s_of_passes_and_not_before);
    RUN_TEST(a_move_up_the_list_off_a_dead_entry_is_a_warning);
    RUN_TEST(a_link_down_moves_at_once_without_a_check);
    RUN_TEST(a_link_up_leaves_a_pending_confirm_where_it_is);
    RUN_TEST(a_chatty_link_pulls_a_check_forward_once_per_interval);
    RUN_TEST(an_ndm_event_pulls_a_check_forward_once_per_interval);
    RUN_TEST(an_ndm_event_on_a_tunnel_entry_asks_nothing);
    RUN_TEST(an_ndm_event_never_holds_back_a_link_up);
    RUN_TEST(a_refresh_reads_the_link_and_finds_it_gone);
    RUN_TEST(a_tunnel_entry_follows_its_status_and_is_never_queued);
    RUN_TEST(a_tunnel_removed_from_the_config_is_checked_by_http);
    RUN_TEST(a_tunnel_removed_with_its_device_is_dead_and_the_group_moves);
    RUN_TEST(a_foreign_opkgtun_entry_is_checked_by_http);
    RUN_TEST(a_name_that_becomes_a_tunnel_follows_it);
    RUN_TEST(a_starting_tunnel_keeps_its_group_for_30_s_then_dies);
    RUN_TEST(a_tunnel_up_within_30_s_keeps_its_group);
    RUN_TEST(a_restarting_tunnel_is_unknown_again);
    RUN_TEST(a_starting_tunnel_without_its_device_stays_unknown_then_dies);
    RUN_TEST(a_starting_entry_never_unblocks_and_the_error_comes_at_5_min);
    RUN_TEST(a_missing_device_no_tunnel_names_is_dead_from_the_start);
    RUN_TEST(only_a_configured_name_is_a_tunnel_entry);
    RUN_TEST(a_tunnel_entry_dies_with_its_link);
    RUN_TEST(a_failed_control_moves_nothing_and_says_the_check_fails);
    RUN_TEST(a_round_that_ran_no_control_does_not_blame_the_wan);
    RUN_TEST(a_round_with_a_pass_names_the_entries_it_could_not_check);
    RUN_TEST(a_result_from_before_a_forget_is_dropped);
    RUN_TEST(every_entry_dead_blocks_the_group_and_errors_after_5_min);
    RUN_TEST(an_unblock_by_pick_ends_the_all_dead_run);
    RUN_TEST(an_edited_list_keeps_the_active_entry_by_name);
    RUN_TEST(pick_puts_a_new_group_on_its_first_entry_that_is_not_dead);
    RUN_TEST(pick_knows_a_tunnel_entry_before_any_tick);
    RUN_TEST(a_refused_move_is_asked_again_on_the_next_tick);
    RUN_TEST(a_move_that_keeps_failing_is_an_error_after_a_minute);
    RUN_TEST(a_dead_interface_s_reminder_counts_every_dead_result);
    RUN_TEST(a_move_of_an_unrouted_group_is_kept_but_not_said);
    RUN_TEST(move_up_needs_the_first_entry_alive);
    RUN_TEST(a_removed_group_s_interfaces_are_forgotten);
    RUN_TEST(free_takes_every_check_rule_back);
    RUN_TEST(a_lost_check_is_queued_again_after_two_minutes);
    RUN_TEST(the_seventeenth_interface_is_not_checked_and_said_once);
    RUN_TEST(a_swapped_name_at_the_cap_takes_the_freed_slot_at_once);
    RUN_TEST(group_check_url_failover_confirm_and_confirmed_failback);
    RUN_TEST(group_check_guarded_and_stale_gen_are_ignored);
    RUN_TEST(group_check_allocates_checkroute_for_tunnel_candidate);
    RUN_TEST(group_check_deduplicates_many_identical_groups_into_single_probe);
    RUN_TEST(group_check_zero_interval_tracks_global_configure);
    RUN_TEST(group_check_subset_rule_shares_probe_and_keeps_its_own_guard);
    RUN_TEST(group_check_rules_with_different_dns_never_share_a_probe);
    RUN_TEST(group_check_followers_take_over_at_once_when_the_leader_is_deleted);
    RUN_TEST(group_check_a_lost_shared_probe_is_queued_again_once);
    RUN_TEST(group_check_two_leaders_out_of_phase_never_double_a_rule_rate);
    RUN_TEST(group_check_a_pending_confirm_is_never_brought_forward);
    RUN_TEST(group_check_a_rule_added_later_joins_the_existing_schedule);
    RUN_TEST(group_check_a_link_up_during_a_probe_rechecks_on_its_result);
    RUN_TEST(group_check_a_new_subset_rule_never_inherits_a_wider_rule_s_dead);
    RUN_TEST(group_check_drifted_subset_and_leader_meet_again);
    GREATEST_MAIN_END();
}
