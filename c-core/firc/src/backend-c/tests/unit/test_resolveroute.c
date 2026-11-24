#include "greatest.h"
#include "test_ids.h"

#include <arpa/inet.h>
#include <netinet/in.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include "firc/dnspipeline.h"
#include "firc/fakeip.h"
#include "firc/log.h"
#include "firc/resolveroute.h"
#include "firc/rulesnap.h"

static firc_group_t *group(const char *name, const char *iface, uint8_t id_last)
{
    firc_group_t *g = firc_group_new();
    firc_strset(&g->name, name);
    firc_strset(&g->iface, iface);
    g->enable = true;
    g->id.b[3] = id_last;
    return g;
}

static firc_resolver_addr_t addr(const char *s)
{
    firc_resolver_addr_t a;
    (void)firc_resolver_addr_parse(s, &a);
    return a;
}

#define MARK1 0x40010000u

/* Catches: server, firmware and none taken in another order, `tunnel: false` routed, or a blackhole. */
TEST precedence_is_server_then_firmware_then_none(void)
{
    firc_group_t *own = group("own", "nwg0", 1);
    firc_strset(&own->resolve.server, "8.8.8.8");
    firc_group_t *fw = group("fw", "nwg0", 2);
    firc_group_t *none = group("none", "nwg1", 3);
    firc_group_t *off = group("off", "nwg0", 4);
    off->resolve.tunnel = false;
    firc_group_t *bh = group("bh", "blackhole", 5);
    firc_strset(&bh->resolve.server, "9.9.9.9");

    firc_resolver_addr_t nwg0[2] = {addr("9.9.9.9"), addr("1.0.0.1")};
    firc_resolve_input_t in[] = {
        {.group = own, .mark = MARK1, .firmware = nwg0, .n_firmware = 2},
        {.group = fw, .mark = MARK1, .firmware = nwg0, .n_firmware = 2},
        {.group = none, .mark = MARK1},
        {.group = off, .mark = MARK1, .firmware = nwg0, .n_firmware = 2},
        {.group = bh, .mark = MARK1, .blackhole = true},
    };
    firc_resolve_table_t t = {0};
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(in, 5, NULL, &t));
    ASSERT_EQ((size_t)5, t.n);

    ASSERT_EQ(FIRC_RESOLVE_SOURCE_GROUP, t.items[0].source);
    ASSERT_EQ((size_t)1, t.items[0].n_servers);
    ASSERT_EQ(8, t.items[0].servers[0].ip.b[0]);
    ASSERT(firc_resolve_route_usable(&t.items[0]));

    ASSERT_EQ(FIRC_RESOLVE_SOURCE_FIRMWARE, t.items[1].source);
    ASSERT_EQ((size_t)2, t.items[1].n_servers);
    ASSERT_EQ(9, t.items[1].servers[0].ip.b[0]);

    ASSERT_EQ(FIRC_RESOLVE_SOURCE_NONE, t.items[2].source);
    ASSERT_FALSE(firc_resolve_route_usable(&t.items[2]));

    ASSERT_EQ(FIRC_RESOLVE_SOURCE_OFF, t.items[3].source);
    ASSERT_EQ((size_t)0, t.items[3].n_servers);
    ASSERT_FALSE(firc_resolve_route_usable(&t.items[3]));

    ASSERT_EQ(FIRC_RESOLVE_SOURCE_NONE, t.items[4].source);
    ASSERT(t.items[4].blackhole);
    ASSERT_FALSE(firc_resolve_route_usable(&t.items[4]));

    ASSERT_STR_EQ("nwg0", t.items[0].iface);
    ASSERT_STR_EQ("own", t.items[0].group_name);
    ASSERT_EQ(MARK1, t.items[0].mark);

    firc_resolve_table_free(&t);
    firc_group_t *all[] = {own, fw, none, off, bh};
    for (int i = 0; i < 5; i++) { firc_group_free(all[i]); }
    PASS();
}

/* Catches: the route's group name cut by bytes, leaving half an emoji. */
TEST a_long_group_name_is_cut_on_a_character_in_the_route(void)
{
    char name[200] = "a";
    for (int i = 0; i < 20; i++) { strcat(name, "\xf0\x9f\x98\x80"); }
    firc_group_t *g = group(name, "nwg0", 1);
    firc_resolve_input_t in[] = {{.group = g, .mark = MARK1}};
    firc_resolve_table_t t = {0};
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(in, 1, NULL, &t));
    ASSERT_EQ((size_t)61, strlen(t.items[0].group_name));
    ASSERT(memcmp(name, t.items[0].group_name, 61) == 0);
    firc_resolve_table_free(&t);
    firc_group_free(g);
    PASS();
}

/* Catches: a group with mark 0 counted as usable, so its query leaves by the WAN. */
TEST a_group_with_no_mark_is_not_usable(void)
{
    firc_group_t *g = group("g", "nwg0", 1);
    firc_strset(&g->resolve.server, "9.9.9.9");
    firc_resolve_input_t in = {.group = g, .mark = 0};
    firc_resolve_table_t t = {0};
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t));
    ASSERT_EQ(FIRC_RESOLVE_SOURCE_GROUP, t.items[0].source);
    ASSERT_EQ((size_t)1, t.items[0].n_servers);
    ASSERT_FALSE(firc_resolve_route_usable(&t.items[0]));
    firc_resolve_table_free(&t);
    firc_group_free(g);
    PASS();
}

TEST v4_first_and_v6_only_with_a_v6_route(void)
{
    firc_group_t *g = group("g", "nwg0", 1);
    firc_resolver_addr_t fw[3] = {addr("2620:fe::fe"), addr("9.9.9.9"), addr("2620:fe::9")};
    firc_resolve_input_t in = {.group = g, .mark = MARK1, .firmware = fw, .n_firmware = 3, .v6_route = true};
    firc_resolve_table_t t = {0};
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t));
    ASSERT_EQ((size_t)3, t.items[0].n_servers);
    ASSERT_EQ(4, t.items[0].servers[0].ip.len);
    ASSERT_EQ(0xfe, t.items[0].servers[1].ip.b[15]);
    ASSERT_EQ(0x09, t.items[0].servers[2].ip.b[15]);
    firc_resolve_table_free(&t);

    in.v6_route = false;
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t));
    ASSERT_EQ((size_t)1, t.items[0].n_servers);
    ASSERT_EQ(4, t.items[0].servers[0].ip.len);
    firc_resolve_table_free(&t);

    firc_strset(&g->resolve.server, "[2620:fe::fe]:53");
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t));
    ASSERT_EQ(FIRC_RESOLVE_SOURCE_GROUP, t.items[0].source);
    ASSERT_EQ((size_t)0, t.items[0].n_servers);
    firc_resolve_table_free(&t);
    firc_group_free(g);
    PASS();
}

/* Catches: only the first of several own servers kept, their order lost, or the list sent to the firmware's. */
TEST a_groups_own_servers_are_all_routed_in_order(void)
{
    firc_group_t *g = group("g", "nwg0", 1);
    firc_strset(&g->resolve.server, "1.1.1.1, 9.9.9.9:5353");
    firc_resolver_addr_t fw[1] = {addr("8.8.8.8")};
    firc_resolve_input_t in = {.group = g, .mark = MARK1, .firmware = fw, .n_firmware = 1};
    firc_resolve_table_t t = {0};
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t));
    ASSERT_EQ(FIRC_RESOLVE_SOURCE_GROUP, t.items[0].source);
    ASSERT_EQ((size_t)2, t.items[0].n_servers);
    ASSERT_EQ(1, t.items[0].servers[0].ip.b[0]);
    ASSERT_EQ(53, t.items[0].servers[0].port);
    ASSERT_EQ(9, t.items[0].servers[1].ip.b[0]);
    ASSERT_EQ(5353, t.items[0].servers[1].port);
    char buf[256];
    ASSERT(firc_resolve_route_describe(&t.items[0], buf, sizeof(buf)) > 0);
    ASSERT_STR_EQ("resolves through nwg0: 1.1.1.1, 9.9.9.9:5353 (resolve.server)", buf);
    firc_resolve_table_free(&t);
    firc_group_free(g);
    PASS();
}

TEST a_doh_entry_adds_no_server_to_the_route(void)
{
    firc_group_t *g = group("g", "nwg0", 1);
    firc_strset(&g->resolve.server, "https://a.example/d, 9.9.9.9, https://b.example/d");
    firc_resolve_input_t in = {.group = g, .mark = MARK1, .v6_route = true};
    firc_resolve_table_t t = {0};
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t));
    ASSERT_EQ(FIRC_RESOLVE_SOURCE_GROUP, t.items[0].source);
    ASSERT_EQ((size_t)1, t.items[0].n_servers);
    ASSERT_EQ(9, t.items[0].servers[0].ip.b[0]);
    ASSERT_FALSE(t.items[0].servers[0].doh);
    firc_resolve_table_free(&t);
    firc_strset(&g->resolve.server, "https://a.example/d");
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t));
    ASSERT_EQ((size_t)0, t.items[0].n_servers);
    firc_resolve_table_free(&t);
    firc_group_free(g);
    PASS();
}

/* Catches: an own IPv6 server kept without an IPv6 route, or put ahead of an IPv4 one. */
TEST an_own_v6_server_needs_a_v6_route(void)
{
    firc_group_t *g = group("g", "nwg0", 1);
    firc_strset(&g->resolve.server, "[2620:fe::fe]:53, 9.9.9.9");
    firc_resolve_input_t in = {.group = g, .mark = MARK1};
    firc_resolve_table_t t = {0};
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t));
    ASSERT_EQ(FIRC_RESOLVE_SOURCE_GROUP, t.items[0].source);
    ASSERT_EQ((size_t)1, t.items[0].n_servers);
    ASSERT_EQ(4, t.items[0].servers[0].ip.len);
    ASSERT_EQ(9, t.items[0].servers[0].ip.b[0]);
    firc_resolve_table_free(&t);

    in.v6_route = true;
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t));
    ASSERT_EQ((size_t)2, t.items[0].n_servers);
    ASSERT_EQ(4, t.items[0].servers[0].ip.len);
    ASSERT_EQ(16, t.items[0].servers[1].ip.len);
    ASSERT_EQ(0xfe, t.items[0].servers[1].ip.b[15]);
    firc_resolve_table_free(&t);
    firc_group_free(g);
    PASS();
}

/* Catches: a rebuild bumping the generation with nothing changed, or keeping it on a real change. */
TEST the_generation_moves_only_on_a_real_change(void)
{
    firc_group_t *g = group("g", "nwg0", 1);
    firc_group_t *h = group("h", "nwg1", 2);
    firc_resolver_addr_t fw[1] = {addr("9.9.9.9")};
    firc_resolve_input_t in[2] = {
        {.group = g, .mark = MARK1, .firmware = fw, .n_firmware = 1},
        {.group = h, .mark = 0x40020000u, .firmware = fw, .n_firmware = 1},
    };
    firc_resolve_table_t t1 = {0}, t2 = {0}, t3 = {0}, t4 = {0}, t5 = {0};
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(in, 2, NULL, &t1));
    ASSERT(t1.items[0].gen != 0 && t1.items[1].gen != 0 && t1.items[0].gen != t1.items[1].gen);

    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(in, 2, &t1, &t2));
    ASSERT_EQ(t1.items[0].gen, t2.items[0].gen);
    ASSERT_EQ(t1.items[1].gen, t2.items[1].gen);

    in[0].mark = 0x40030000u;
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(in, 2, &t2, &t3));
    ASSERT(t3.items[0].gen != t2.items[0].gen);
    ASSERT(t3.items[0].gen > t2.last_gen);
    ASSERT_EQ(t2.items[1].gen, t3.items[1].gen);

    firc_strset(&h->iface, "nwg2");
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(in, 2, &t3, &t4));
    ASSERT(t4.items[1].gen != t3.items[1].gen);

    firc_resolver_addr_t fw2[1] = {addr("1.0.0.1")};
    in[0].firmware = fw2;
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(in, 2, &t4, &t5));
    ASSERT(t5.items[0].gen != t4.items[0].gen);

    firc_resolve_table_t *all[] = {&t1, &t2, &t3, &t4, &t5};
    for (int i = 0; i < 5; i++) { firc_resolve_table_free(all[i]); }
    firc_group_free(g);
    firc_group_free(h);
    PASS();
}

/* Catches: a block group described as a blackhole one, or either described by the other's sentence. */
TEST a_block_route_says_firc_answers_its_names(void)
{
    firc_group_t *g = group("bk", "block", 1);
    firc_resolve_input_t in = {.group = g, .iface = "block", .mark = MARK1, .blackhole = true};
    firc_resolve_table_t t = {0};
    char buf[256];
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t));
    firc_resolve_route_describe(&t.items[0], buf, sizeof(buf));
    ASSERT_STR_EQ("uses the common upstream: its names are answered by firc", buf);
    firc_resolve_table_free(&t);
    in.iface = "blackhole";
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t));
    firc_resolve_route_describe(&t.items[0], buf, sizeof(buf));
    ASSERT_STR_EQ("uses the common upstream: a blackhole group has no tunnel", buf);
    firc_resolve_table_free(&t);
    firc_group_free(g);
    PASS();
}

/* Catches: the log line missing the interface, servers or source, or "not through the tunnel". */
TEST each_route_is_described_for_the_log(void)
{
    firc_group_t *g = group("nl", "nwg0", 1);
    firc_resolver_addr_t fw[2] = {addr("9.9.9.9"), addr("1.0.0.1")};
    firc_resolve_input_t in = {.group = g, .mark = MARK1, .firmware = fw, .n_firmware = 2};
    firc_resolve_table_t t = {0};
    char buf[256];

    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t));
    ASSERT(firc_resolve_route_describe(&t.items[0], buf, sizeof(buf)) > 0);
    ASSERT_STR_EQ("resolves through nwg0: 9.9.9.9, 1.0.0.1 (the firmware's for nwg0)", buf);
    firc_resolve_table_free(&t);

    firc_strset(&g->resolve.server, "[2620:fe::fe]:853");
    in.v6_route = true;
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t));
    firc_resolve_route_describe(&t.items[0], buf, sizeof(buf));
    ASSERT_STR_EQ("resolves through nwg0: [2620:fe::fe]:853 (resolve.server)", buf);
    firc_resolve_table_free(&t);

    in.firmware = NULL;
    in.n_firmware = 0;
    free(g->resolve.server);
    g->resolve.server = NULL;
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t));
    firc_resolve_route_describe(&t.items[0], buf, sizeof(buf));
    ASSERT_STR_EQ("uses the common upstream: the firmware binds no resolver to nwg0", buf);
    firc_resolve_table_free(&t);

    g->resolve.tunnel = false;
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t));
    firc_resolve_route_describe(&t.items[0], buf, sizeof(buf));
    ASSERT_STR_EQ("uses the common upstream: resolving through the tunnel is off", buf);
    firc_resolve_table_free(&t);

    g->resolve.tunnel = true;
    firc_strset(&g->resolve.server, "9.9.9.9");
    in.mark = 0;
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t));
    firc_resolve_route_describe(&t.items[0], buf, sizeof(buf));
    ASSERT_STR_EQ("uses the common upstream until nwg0 is routed (9.9.9.9, resolve.server)", buf);
    firc_resolve_table_free(&t);

    free(g->resolve.server);
    g->resolve.server = NULL;
    firc_resolver_addr_t fw_v6[1] = {addr("2620:fe::9")};
    in.mark = MARK1;
    in.firmware = fw_v6;
    in.n_firmware = 1;
    in.v6_route = false;
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t));
    firc_resolve_route_describe(&t.items[0], buf, sizeof(buf));
    ASSERT_STR_EQ("uses the common upstream: its resolvers for nwg0 are IPv6 and nwg0 has no IPv6 route", buf);
    firc_resolve_table_free(&t);

    firc_strset(&g->resolve.server, "2620:fe::9");
    in.firmware = NULL;
    in.n_firmware = 0;
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t));
    firc_resolve_route_describe(&t.items[0], buf, sizeof(buf));
    ASSERT_STR_EQ("uses the common upstream: resolve.server names only IPv6 resolvers and nwg0 has no IPv6 route",
                  buf);
    firc_resolve_table_free(&t);

    firc_strset(&g->resolve.server, "2620:fe::9, [2620:fe::fe]:5353");
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t));
    firc_resolve_route_describe(&t.items[0], buf, sizeof(buf));
    ASSERT_STR_EQ("uses the common upstream: resolve.server names only IPv6 resolvers and nwg0 has no IPv6 route",
                  buf);
    firc_resolve_table_free(&t);
    firc_group_free(g);
    PASS();
}

static size_t publish_logged(firc_resolve_router_t *r, firc_resolve_table_t *t, char *out, size_t cap)
{
    int fds[2];
    if (pipe(fds) != 0) { return 0; }
    firc_log_set_output_floor(FIRC_LOG_INFO);
    firc_log_set_fd(fds[1]);
    firc_resolve_router_publish(r, t);
    firc_log_set_fd(STDOUT_FILENO);
    close(fds[1]);
    ssize_t n = read(fds[0], out, cap - 1);
    close(fds[0]);
    if (n < 0) { n = 0; }
    out[n] = '\0';
    return (size_t)n;
}

/* Catches: an unchanged group logged on every publish, a change unsaid, or a copy freed by a publish. */
TEST a_publish_logs_what_changed_and_hands_out_copies(void)
{
    firc_resolve_router_t *r = firc_resolve_router_new(NULL);
    ASSERT(r != NULL);
    firc_group_t *g = group("nl", "nwg0", 1);
    firc_group_t *h = group("de", "nwg1", 2);
    firc_resolver_addr_t fw[1] = {addr("9.9.9.9")};
    firc_resolve_input_t in[2] = {
        {.group = g, .mark = MARK1, .firmware = fw, .n_firmware = 1},
        {.group = h, .mark = 0x40020000u},
    };
    char log[2048];
    firc_resolve_table_t t = {0};
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(in, 2, firc_resolve_router_table(r), &t));
    publish_logged(r, &t, log, sizeof(log));
    ASSERT_EQ(NULL, t.items);
    ASSERTm(log, strstr(log, "group \"nl\" resolves through nwg0: 9.9.9.9") != NULL);
    ASSERTm(log, strstr(log, "group \"de\" uses the common upstream") != NULL);

    firc_resolve_route_t copy;
    ASSERT(firc_resolve_router_route(r, g->id, &copy));

    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(in, 2, firc_resolve_router_table(r), &t));
    publish_logged(r, &t, log, sizeof(log));
    ASSERT_STR_EQm("nothing changed, nothing said", "", log);

    firc_resolver_addr_t fw2[1] = {addr("1.0.0.1")};
    in[0].firmware = fw2;
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(in, 2, firc_resolve_router_table(r), &t));
    publish_logged(r, &t, log, sizeof(log));
    ASSERTm(log, strstr(log, "group \"nl\" resolves through nwg0: 1.0.0.1") != NULL);
    ASSERTm(log, strstr(log, "\"de\"") == NULL);

    ASSERT_EQ(9, copy.servers[0].ip.b[0]);
    ASSERT_STR_EQ("nwg0", copy.iface);

    firc_id_t unknown = {0};
    ASSERT_FALSE(firc_resolve_router_route(r, unknown, &copy));
    firc_resolve_router_free(r);
    firc_group_free(g);
    firc_group_free(h);
    PASS();
}

static firc_dns_msg_t *query(const char *name, uint16_t qtype)
{
    uint8_t b[300] = {0};
    b[1] = 0x42;
    b[2] = 0x01;
    b[5] = 1;
    size_t p = 12;
    for (const char *s = name; *s != '\0';) {
        const char *dot = strchr(s, '.');
        size_t l = dot != NULL ? (size_t)(dot - s) : strlen(s);
        b[p++] = (uint8_t)l;
        memcpy(b + p, s, l);
        p += l;
        s += l + (dot != NULL ? 1 : 0);
    }
    b[p++] = 0;
    b[p++] = (uint8_t)(qtype >> 8);
    b[p++] = (uint8_t)qtype;
    b[p++] = 0;
    b[p++] = 1;
    firc_dns_msg_t *m = NULL;
    return firc_dns_msg_parse(b, p, &m) == FIRC_OK ? m : NULL;
}

static void add_namespace_rule(firc_group_t *g, const char *domain)
{
    firc_rule_t *r = firc_rule_new();
    r->id = test_random_id();
    firc_strset(&r->type, "namespace");
    firc_strset(&r->rule, domain);
    r->enable = true;
    firc_group_add_rule(g, r);
}

typedef struct {
    firc_config_t cfg;
    firc_dns_pipeline_t *pl;
    firc_resolve_router_t *router;
} decide_fx_t;

/* Five groups: one routed, and one for each way a query must not be routed. */
static bool decide_fx_up(decide_fx_t *fx)
{
    memset(fx, 0, sizeof(*fx));
    if (firc_config_init_defaults(&fx->cfg) != FIRC_OK) { return false; }
    const char *names[] = {"nl", "off", "none", "unmarked", "bh"};
    const char *doms[] = {"example.com", "off.test", "none.test", "unmarked.test", "bh.test"};
    const char *ifs[] = {"nwg0", "nwg0", "nwg1", "nwg0", "blackhole"};
    for (int i = 0; i < 5; i++) {
        firc_group_t *g = group(names[i], ifs[i], (uint8_t)(i + 1));
        add_namespace_rule(g, doms[i]);
        firc_config_add_group(&fx->cfg, g);
    }
    firc_group_t **gs = fx->cfg.groups;
    gs[0]->devices.allow = calloc(1, sizeof(char *));
    gs[0]->devices.allow[0] = strdup("192.168.1.0/24");
    gs[0]->devices.n_allow = 1;
    gs[1]->resolve.tunnel = false;
    firc_strset(&gs[3]->resolve.server, "8.8.8.8");
    firc_strset(&gs[4]->resolve.server, "8.8.8.8");

    fx->pl = firc_dns_pipeline_create();
    fx->router = firc_resolve_router_new(fx->pl);
    if (fx->pl == NULL || fx->router == NULL) { return false; }
    firc_dns_pipeline_set_snapshot(fx->pl, firc_ruleset_snapshot_build(&fx->cfg));

    static firc_resolver_addr_t fw[1];
    fw[0] = addr("9.9.9.9");
    firc_resolve_input_t in[5] = {
        {.group = gs[0], .mark = MARK1, .firmware = fw, .n_firmware = 1},
        {.group = gs[1], .mark = MARK1, .firmware = fw, .n_firmware = 1},
        {.group = gs[2], .mark = MARK1},
        {.group = gs[3], .mark = 0},
        {.group = gs[4], .mark = MARK1, .blackhole = true},
    };
    firc_resolve_table_t t;
    if (firc_resolve_table_build(in, 5, NULL, &t) != FIRC_OK) { return false; }
    firc_resolve_router_publish(fx->router, &t);
    return true;
}

static void decide_fx_down(decide_fx_t *fx)
{
    firc_resolve_router_free(fx->router);
    firc_dns_pipeline_destroy(fx->pl);
    firc_config_clear(&fx->cfg);
}

static bool decide(decide_fx_t *fx, const char *name, uint16_t qtype, const char *client,
                   firc_dnsproxy_route_t *out)
{
    firc_ip_t c = {.len = 4};
    const firc_ip_t *cp = NULL;
    if (client != NULL) {
        inet_pton(AF_INET, client, c.b);
        cp = &c;
    }
    firc_dns_msg_t *q = query(name, qtype);
    bool got = firc_resolve_router_decide(fx->router, q, cp, out);
    firc_dns_msg_free(q);
    return got;
}

/* Catches: the mark, server, port or group name not copied, or the route read from a replaced table. */
TEST the_owner_of_a_covered_query_is_routed_by_value(void)
{
    decide_fx_t fx;
    ASSERT(decide_fx_up(&fx));
    firc_dnsproxy_route_t r;
    ASSERT(decide(&fx, "a.example.com", 1 , "192.168.1.5", &r));
    ASSERT_EQ(MARK1, r.mark);
    ASSERT_EQ((size_t)1, r.n_servers);
    const struct sockaddr_in *s4 = (const struct sockaddr_in *)&r.servers[0];
    ASSERT_EQ(AF_INET, s4->sin_family);
    ASSERT_EQ(htons(53), s4->sin_port);
    ASSERT_EQ(htonl(0x09090909u), s4->sin_addr.s_addr);
    ASSERT_EQ((socklen_t)sizeof(struct sockaddr_in), r.server_lens[0]);
    ASSERT(firc_id_equal(fx.cfg.groups[0]->id, r.group_id));
    ASSERT_STR_EQ("nl", r.group_name);
    ASSERT(r.gen != 0);
    ASSERT_FALSE(r.blocked);

    firc_resolve_table_t empty = {0};
    firc_resolve_router_publish(fx.router, &empty);
    ASSERT_EQ(htonl(0x09090909u), s4->sin_addr.s_addr);
    ASSERT_FALSE(decide(&fx, "a.example.com", 1, "192.168.1.5", &r));
    decide_fx_down(&fx);
    PASS();
}

/* Catches: a block owner dropped for want of a route, its answer not carried, or its route row used. */
TEST a_block_owner_is_claimed_without_a_route(void)
{
    firc_config_t cfg;
    ASSERT_EQ(FIRC_OK, firc_config_init_defaults(&cfg));
    firc_group_t *ads = group("ads", "block", 1);
    ads->block = FIRC_BLOCK_NXDOMAIN;
    add_namespace_rule(ads, "ads.test");
    firc_config_add_group(&cfg, ads);
    firc_dns_pipeline_t *pl = firc_dns_pipeline_create();
    firc_resolve_router_t *router = firc_resolve_router_new(pl);
    ASSERT(pl != NULL && router != NULL);
    firc_dns_pipeline_set_snapshot(pl, firc_ruleset_snapshot_build(&cfg));

    firc_dns_msg_t *q = query("x.ads.test", 1);
    ASSERT(q != NULL);
    firc_ip_t c = {{192, 168, 1, 5}, 4};
    firc_dnsproxy_route_t r;
    memset(&r, 0x5a, sizeof(r));
    ASSERT(firc_resolve_router_decide(router, q, &c, &r));
    ASSERT(r.blocked);
    ASSERT_EQ(FIRC_BLOCK_NXDOMAIN, r.answer);

    firc_resolver_addr_t fw[1] = {addr("9.9.9.9")};
    firc_resolve_input_t in[1] = {{.group = ads, .mark = MARK1, .firmware = fw, .n_firmware = 1}};
    firc_resolve_table_t t;
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(in, 1, NULL, &t));
    firc_resolve_router_publish(router, &t);
    memset(&r, 0x5a, sizeof(r));
    ASSERT(firc_resolve_router_decide(router, q, &c, &r));
    ASSERT(r.blocked);
    ASSERT(firc_id_equal(ads->id, r.owner));
    ASSERT_EQ(FIRC_BLOCK_NXDOMAIN, r.answer);
    ASSERT_EQ(0u, r.mark);
    ASSERT_EQ((size_t)0, r.n_servers);

    firc_dns_msg_free(q);
    firc_resolve_router_free(router);
    firc_dns_pipeline_destroy(pl);
    firc_config_clear(&cfg);
    PASS();
}

/* Catches: the owner of a declined, routed or blocked query left unset, or another group's id reported. */
TEST the_owner_is_reported_when_the_route_is_declined(void)
{
    decide_fx_t fx;
    ASSERT(decide_fx_up(&fx));
    firc_dnsproxy_route_t r;
    memset(&r, 0x5a, sizeof(r));
    ASSERT_FALSE(decide(&fx, "x.bh.test", 1, "192.168.1.5", &r));
    ASSERT(firc_id_equal(fx.cfg.groups[4]->id, r.owner));
    memset(&r, 0x5a, sizeof(r));
    ASSERT_FALSE(decide(&fx, "x.off.test", 1, "192.168.1.5", &r));
    ASSERT(firc_id_equal(fx.cfg.groups[1]->id, r.owner));
    memset(&r, 0x5a, sizeof(r));
    ASSERT(decide(&fx, "a.example.com", 1, "192.168.1.5", &r));
    ASSERT(firc_id_equal(fx.cfg.groups[0]->id, r.owner));
    decide_fx_down(&fx);
    PASS();
}

/* Catches: the router stricter than the selector for an unknown client. */
TEST a_query_with_no_client_address_is_routed_like_the_pipeline_covers_it(void)
{
    decide_fx_t fx;
    ASSERT(decide_fx_up(&fx));
    firc_dnsproxy_route_t r;
    ASSERT(decide(&fx, "a.example.com", 1, NULL, &r));
    ASSERT_EQ(MARK1, r.mark);
    decide_fx_down(&fx);
    PASS();
}

/* Catches: the routing decision filtering on qtype. */
TEST every_query_type_is_routed(void)
{
    decide_fx_t fx;
    ASSERT(decide_fx_up(&fx));
    firc_dnsproxy_route_t r;
    const uint16_t types[] = {1, 28, 16 , 15 , 65 , 33 };
    for (size_t i = 0; i < sizeof(types) / sizeof(types[0]); i++) {
        ASSERTm("qtype", decide(&fx, "a.example.com", types[i], "192.168.1.5", &r));
    }
    decide_fx_down(&fx);
    PASS();
}

/* Catches: an unowned, uncovered, off, resolverless, unmarked or blackhole query tunnelled. */
TEST what_is_not_routed_goes_to_the_common_upstream(void)
{
    decide_fx_t fx;
    ASSERT(decide_fx_up(&fx));
    firc_dnsproxy_route_t r;
    ASSERT_FALSEm("no owner", decide(&fx, "a.example.org", 1, "192.168.1.5", &r));
    ASSERT_FALSEm("not covered", decide(&fx, "a.example.com", 1, "10.0.0.5", &r));
    ASSERT_FALSEm("switch off", decide(&fx, "x.off.test", 1, "192.168.1.5", &r));
    ASSERT_FALSEm("no resolver", decide(&fx, "x.none.test", 1, "192.168.1.5", &r));
    ASSERT_FALSEm("mark 0", decide(&fx, "x.unmarked.test", 1, "192.168.1.5", &r));
    ASSERT_FALSEm("blackhole", decide(&fx, "x.bh.test", 1, "192.168.1.5", &r));
    firc_resolve_router_t *bare = firc_resolve_router_new(NULL);
    firc_dns_msg_t *q = query("a.example.com", 1);
    firc_ip_t c = {.b = {192, 168, 1, 5}, .len = 4};
    ASSERT_FALSEm("no pipeline", firc_resolve_router_decide(bare, q, &c, &r));
    firc_dns_msg_free(q);
    firc_resolve_router_free(bare);
    decide_fx_down(&fx);
    PASS();
}

/* Catches: the router's owner fixed by the first match, or a device falling through when its group cannot resolve. */
TEST a_lower_groups_device_is_resolved_by_its_group_and_never_through_another(void)
{
    decide_fx_t fx;
    memset(&fx, 0, sizeof(fx));
    ASSERT_EQ(FIRC_OK, firc_config_init_defaults(&fx.cfg));
    firc_group_t *a = group("a", "nwg0", 1);
    add_namespace_rule(a, "shared.test");
    a->devices.allow = calloc(1, sizeof(char *));
    a->devices.allow[0] = strdup("192.168.1.5");
    a->devices.n_allow = 1;
    firc_config_add_group(&fx.cfg, a);
    firc_group_t *b = group("b", "nwg1", 2);
    add_namespace_rule(b, "shared.test");
    firc_config_add_group(&fx.cfg, b);
    fx.pl = firc_dns_pipeline_create();
    fx.router = firc_resolve_router_new(fx.pl);
    ASSERT(fx.pl != NULL && fx.router != NULL);
    firc_dns_pipeline_set_snapshot(fx.pl, firc_ruleset_snapshot_build(&fx.cfg));
    static firc_resolver_addr_t fw[1];
    fw[0] = addr("9.9.9.9");
    firc_resolve_input_t in[2] = {
        {.group = a, .mark = 0, .firmware = fw, .n_firmware = 1},
        {.group = b, .mark = MARK1, .firmware = fw, .n_firmware = 1},
    };
    firc_resolve_table_t t;
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(in, 2, NULL, &t));
    firc_resolve_router_publish(fx.router, &t);
    firc_dnsproxy_route_t r;
    ASSERT_FALSEm("A's device stays with A, which cannot resolve", decide(&fx, "x.shared.test", 1, "192.168.1.5", &r));
    ASSERTm("B's device goes to B's resolver", decide(&fx, "x.shared.test", 1, "192.168.1.9", &r));
    ASSERT(firc_id_equal(b->id, r.group_id));
    decide_fx_down(&fx);
    PASS();
}

/* Catches: an unchanged group reported stale, a changed one missed, or rewired taken for removed. */
TEST the_stale_groups_are_the_removed_and_the_rewired(void)
{
    firc_group_t *g = group("g", "nwg0", 1), *h = group("h", "nwg1", 2), *k = group("k", "nwg2", 3);
    firc_resolver_addr_t fw[1] = {addr("9.9.9.9")};
    firc_resolve_input_t in[3] = {
        {.group = g, .mark = MARK1, .firmware = fw, .n_firmware = 1},
        {.group = h, .mark = 0x40020000u, .firmware = fw, .n_firmware = 1},
        {.group = k, .mark = 0x40030000u, .firmware = fw, .n_firmware = 1},
    };
    firc_resolve_table_t a = {0}, b = {0};
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(in, 3, NULL, &a));
    in[1].mark = 0x40040000u;
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(in, 2, &a, &b));
    firc_id_t out[4];
    size_t n = firc_resolve_table_stale(&a, &b, out, 4);
    ASSERT_EQ((size_t)2, n);
    ASSERT(firc_id_equal(out[0], h->id));
    ASSERT(firc_id_equal(out[1], k->id));
    ASSERT_EQm("cap bounds the list", (size_t)1, firc_resolve_table_stale(&a, &b, out, 1));
    ASSERT(firc_id_equal(out[0], h->id));
    ASSERT_EQ((size_t)0, firc_resolve_table_stale(&b, &b, out, 4));
    ASSERT_EQ((size_t)0, firc_resolve_table_stale(NULL, &b, out, 4));

    ASSERT_EQm("only k is gone", (size_t)1, firc_resolve_table_gone(&a, &b, out, 4));
    ASSERT(firc_id_equal(out[0], k->id));
    ASSERT_EQ((size_t)0, firc_resolve_table_gone(&b, &b, out, 4));
    ASSERT_EQ((size_t)0, firc_resolve_table_gone(NULL, &b, out, 4));
    firc_resolve_table_t empty = {0};
    ASSERT_EQm("everything is gone from an empty table", (size_t)2, firc_resolve_table_gone(&b, &empty, out, 4));
    firc_resolve_table_free(&a);
    firc_resolve_table_free(&b);
    firc_group_free(g);
    firc_group_free(h);
    firc_group_free(k);
    PASS();
}

static firc_dns_msg_t *aaaa_reply(const char *name)
{
    uint8_t b[300] = {0};
    b[1] = 0x42;
    b[2] = 0x81;
    b[3] = 0x80;
    b[5] = 1;
    b[7] = 1;
    size_t p = 12;
    for (const char *s = name; *s != '\0';) {
        const char *dot = strchr(s, '.');
        size_t l = dot != NULL ? (size_t)(dot - s) : strlen(s);
        b[p++] = (uint8_t)l;
        memcpy(b + p, s, l);
        p += l;
        s += l + (dot != NULL ? 1 : 0);
    }
    b[p++] = 0;
    const uint8_t tail[] = {0, 28, 0, 1, 0xc0, 12, 0, 28, 0, 1, 0, 0, 0, 60, 0, 16,
                            0x26, 0x06, 0x47, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1};
    memcpy(b + p, tail, sizeof(tail));
    p += sizeof(tail);
    firc_dns_msg_t *m = NULL;
    return firc_dns_msg_parse(b, p, &m) == FIRC_OK ? m : NULL;
}

static size_t answers_after(firc_dns_pipeline_t *pl, const char *name)
{
    firc_dns_msg_t *m = aaaa_reply(name);
    if (m == NULL) { return 99; }
    (void)firc_dns_pipeline_handle_message(pl, m, 1000, NULL, NULL);
    size_t n = m->n_answers;
    firc_dns_msg_free(m);
    return n;
}

/* Catches: the v6 flag not carried into the published route, or a republish not reaching the pipeline. */
TEST a_published_v6_flag_reaches_the_answer_and_flips_with_the_next_publish(void)
{
    firc_config_t cfg;
    ASSERT_EQ(FIRC_OK, firc_config_init_defaults(&cfg));
    firc_group_t *g = group("wg", "nwg0", 1);
    add_namespace_rule(g, "example.com");
    firc_config_add_group(&cfg, g);
    firc_fakeip_cfg_t pc = {0};
    pc.v4.base = (firc_ip_t){.b = {198, 18}, .len = 4};
    pc.v4.pool_cidr = 15;
    pc.v4.chunk_cidr = 24;
    pc.v6.base = (firc_ip_t){.b = {0xfd, 0x37, 0x9a}, .len = 16};
    pc.v6.pool_cidr = 48;
    pc.v6.chunk_cidr = 64;
    pc.max_names = 16;
    pc.idle_secs = 86400;
    pc.clamp_secs = 300;
    firc_fakeip_t *pool = NULL;
    ASSERT_EQ(FIRC_OK, firc_fakeip_new(&pc, &pool));
    firc_dns_pipeline_t *pl = firc_dns_pipeline_create();
    firc_resolve_router_t *r = firc_resolve_router_new(pl);
    ASSERT(pl != NULL && r != NULL);
    firc_dns_pipeline_set_snapshot(pl, firc_ruleset_snapshot_build(&cfg));
    firc_dns_pipeline_set_pool(pl, pool, 300);
    firc_dns_pipeline_set_v6_fn(pl, firc_resolve_router_carries_v6, r);

    firc_id_t unknown = {0};
    ASSERT_FALSEm("nothing published: no group carries v6", firc_resolve_router_carries_v6(r, g->id));
    firc_resolve_input_t in = {.group = g, .mark = MARK1, .v6_route = false};
    firc_resolve_table_t t = {0};
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, firc_resolve_router_table(r), &t));
    firc_resolve_router_publish(r, &t);
    ASSERT_EQ_FMTm("no v6 route: NODATA", (size_t)0, answers_after(pl, "a.example.com"), "%zu");

    in.v6_route = true;
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, firc_resolve_router_table(r), &t));
    firc_resolve_router_publish(r, &t);
    ASSERT(firc_resolve_router_carries_v6(r, g->id));
    ASSERT_FALSEm("a group the table lacks", firc_resolve_router_carries_v6(r, unknown));
    ASSERT_EQ_FMTm("a v6 route: the fake AAAA", (size_t)1, answers_after(pl, "a.example.com"), "%zu");

    in.v6_route = false;
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, firc_resolve_router_table(r), &t));
    firc_resolve_router_publish(r, &t);
    ASSERT_EQ_FMTm("and back", (size_t)0, answers_after(pl, "a.example.com"), "%zu");

    firc_resolve_router_free(r);
    firc_dns_pipeline_destroy(pl);
    firc_fakeip_free(pool);
    firc_config_clear(&cfg);
    PASS();
}

/* Catches: the cross flag granted on v6 alone, on kernel TCP alone, or for a group the table lacks. */
TEST cross_v6_needs_both_v6_and_kernel_tcp(void)
{
    firc_group_t *g[4] = {group("none", "tunvless0", 1), group("v6", "tunvless0", 2), group("ktcp", "tunvless0", 3),
                          group("both", "tunvless0", 4)};
    firc_resolve_input_t in[] = {
        {.group = g[0], .mark = MARK1, .v6_route = false, .ktcp = false},
        {.group = g[1], .mark = MARK1, .v6_route = true, .ktcp = false},
        {.group = g[2], .mark = MARK1, .v6_route = false, .ktcp = true},
        {.group = g[3], .mark = MARK1, .v6_route = true, .ktcp = true},
    };
    firc_resolve_router_t *r = firc_resolve_router_new(NULL);
    ASSERT(r != NULL);
    firc_resolve_table_t t = {0};
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(in, 4, NULL, &t));
    firc_resolve_router_publish(r, &t);
    ASSERT_FALSE(firc_resolve_router_cross_v6(r, g[0]->id));
    ASSERT_FALSE(firc_resolve_router_cross_v6(r, g[1]->id));
    ASSERT_FALSE(firc_resolve_router_cross_v6(r, g[2]->id));
    ASSERT(firc_resolve_router_cross_v6(r, g[3]->id));
    firc_id_t unknown = {0};
    ASSERT_FALSE(firc_resolve_router_cross_v6(r, unknown));
    ASSERT_FALSE(firc_resolve_router_cross_v6(NULL, g[3]->id));
    firc_resolve_router_free(r);
    for (size_t i = 0; i < 4; i++) { firc_group_free(g[i]); }
    PASS();
}

TEST a_route_names_the_active_entry_not_the_first(void)
{
    firc_group_t *g = group("listed", "up0", 1);
    firc_resolve_input_t in[] = {
        {.group = g, .mark = MARK1, .iface = "up1"},
        {.group = g, .mark = MARK1, .iface = NULL},
    };
    firc_resolve_table_t t = {0};
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(in, 2, NULL, &t));
    ASSERT_EQ((size_t)2, t.n);
    ASSERT_STR_EQ("up1", t.items[0].iface);
    ASSERT_STR_EQ("up0", t.items[1].iface);
    firc_resolve_table_free(&t);
    firc_group_free(g);
    PASS();
}

static const char *interned_urls[8];

static uint16_t fake_intern(void *ud, const char *url)
{
    (void)ud;
    for (uint16_t i = 0; i < 8 && interned_urls[i] != NULL; i++) {
        if (strcmp(interned_urls[i], url) == 0) { return (uint16_t)(i + 1); }
    }
    return 0;
}

static void intern_reset(const char *a, const char *b)
{
    memset(interned_urls, 0, sizeof(interned_urls));
    interned_urls[0] = a;
    interned_urls[1] = b;
}

TEST a_dns_group_is_usable_without_a_mark(void)
{
    firc_group_t *g = group("d", "dns", 1);
    firc_strset(&g->resolve.server, "9.9.9.9");
    firc_resolve_input_t in = {.group = g, .mark = 0};
    firc_resolve_table_t t = {0};
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t));
    ASSERT(t.items[0].dns_only);
    ASSERT_EQ((uint32_t)0, t.items[0].mark);
    ASSERT_EQ(FIRC_RESOLVE_SOURCE_GROUP, t.items[0].source);
    ASSERT_EQ((size_t)1, t.items[0].n_servers);
    ASSERT(firc_resolve_route_usable(&t.items[0]));
    firc_resolve_table_free(&t);
    firc_group_free(g);
    PASS();
}

TEST a_routed_group_without_a_mark_stays_unusable_with_doh(void)
{
    intern_reset("https://a.example/d", NULL);
    firc_group_t *g = group("g", "nwg0", 1);
    firc_strset(&g->resolve.server, "https://a.example/d");
    firc_resolve_input_t in = {.group = g, .mark = 0, .intern = fake_intern};
    firc_resolve_table_t t = {0};
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t));
    ASSERT_EQ((size_t)1, t.items[0].n_servers);
    ASSERT_FALSE(t.items[0].dns_only);
    ASSERT_FALSE(firc_resolve_route_usable(&t.items[0]));
    firc_resolve_table_free(&t);
    firc_group_free(g);
    PASS();
}

TEST a_dns_group_never_takes_the_firmwares_resolvers(void)
{
    firc_group_t *g = group("d", "dns", 1);
    firc_resolver_addr_t fw[1] = {addr("1.1.1.1")};
    firc_resolve_input_t in = {.group = g, .mark = MARK1, .firmware = fw, .n_firmware = 1};
    firc_resolve_table_t t = {0};
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t));
    ASSERT_EQ(FIRC_RESOLVE_SOURCE_GROUP, t.items[0].source);
    ASSERT_EQ((size_t)0, t.items[0].n_servers);
    ASSERT_EQ((uint32_t)0, t.items[0].mark);
    ASSERT_FALSE(firc_resolve_route_usable(&t.items[0]));
    firc_resolve_table_free(&t);
    firc_group_free(g);
    PASS();
}

TEST a_dns_route_carries_no_fallback(void)
{
    firc_group_t *g = group("d", "dns", 1);
    firc_strset(&g->resolve.server, "9.9.9.9");
    firc_resolve_input_t in = {.group = g};
    firc_resolve_table_t t = {0};
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t));
    ASSERT_FALSE(t.items[0].no_fallback);
    firc_resolve_table_free(&t);
    g->resolve.fallback = false;
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t));
    ASSERT(t.items[0].no_fallback);
    firc_resolve_table_free(&t);
    firc_group_free(g);
    PASS();
}

TEST a_doh_entry_reaches_the_route_as_a_url_id(void)
{
    intern_reset("https://a.example/d", "https://b.example/q");
    firc_group_t *g = group("g", "nwg0", 1);
    firc_strset(&g->resolve.server, "https://b.example/q, 9.9.9.9, https://a.example/d");
    firc_resolve_input_t in = {.group = g, .mark = MARK1, .intern = fake_intern};
    firc_resolve_table_t t = {0};
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t));
    const firc_resolve_route_t *r = &t.items[0];
    ASSERT_EQ((size_t)3, r->n_servers);
    ASSERT_FALSE(r->servers[0].doh);
    ASSERT_EQ((uint16_t)0, r->url_ids[0]);
    ASSERT(r->servers[1].doh);
    ASSERT_EQ((uint16_t)2, r->url_ids[1]);
    ASSERT(r->servers[2].doh);
    ASSERT_EQ((uint16_t)1, r->url_ids[2]);
    ASSERT_EQ(MARK1, r->mark);
    firc_resolve_table_free(&t);
    firc_group_free(g);
    PASS();
}

TEST a_doh_entry_is_dropped_when_the_intern_refuses_it(void)
{
    intern_reset(NULL, NULL);
    firc_group_t *g = group("g", "nwg0", 1);
    firc_strset(&g->resolve.server, "https://a.example/d, 9.9.9.9");
    firc_resolve_input_t in = {.group = g, .mark = MARK1, .intern = fake_intern};
    firc_resolve_table_t t = {0};
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t));
    ASSERT_EQ((size_t)1, t.items[0].n_servers);
    ASSERT_FALSE(t.items[0].servers[0].doh);
    firc_resolve_table_free(&t);
    firc_group_free(g);
    PASS();
}

TEST a_change_of_url_is_a_new_generation(void)
{
    intern_reset("https://a.example/d", "https://b.example/d");
    firc_group_t *g = group("d", "dns", 1);
    firc_strset(&g->resolve.server, "https://a.example/d");
    firc_resolve_input_t in = {.group = g, .intern = fake_intern};
    firc_resolve_table_t t1 = {0}, t2 = {0}, t3 = {0};
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t1));
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, &t1, &t2));
    ASSERT_EQ(t1.items[0].gen, t2.items[0].gen);
    firc_strset(&g->resolve.server, "https://b.example/d");
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, &t2, &t3));
    ASSERT(t3.items[0].gen != t2.items[0].gen);
    firc_resolve_table_free(&t1);
    firc_resolve_table_free(&t2);
    firc_resolve_table_free(&t3);
    firc_group_free(g);
    PASS();
}

TEST a_dns_route_says_where_its_names_go(void)
{
    intern_reset("https://dns.example:8443/dns-query", NULL);
    firc_group_t *g = group("d", "dns", 1);
    firc_strset(&g->resolve.server, "9.9.9.9, https://dns.example:8443/dns-query");
    firc_resolve_input_t in = {.group = g, .intern = fake_intern};
    firc_resolve_table_t t = {0};
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t));
    char buf[512];
    ASSERT(firc_resolve_route_describe(&t.items[0], buf, sizeof(buf)) > 0);
    ASSERT_STR_EQ("answers its names from resolve.server: 9.9.9.9, https://dns.example:8443/…,"
                  " the common upstream answers when they fail", buf);
    firc_resolve_table_free(&t);
    g->resolve.fallback = false;
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t));
    ASSERT(firc_resolve_route_describe(&t.items[0], buf, sizeof(buf)) > 0);
    ASSERT_STR_EQ("answers its names from resolve.server: 9.9.9.9, https://dns.example:8443/…,"
                  " never the common upstream", buf);
    firc_resolve_table_free(&t);
    firc_strset(&g->resolve.server, "https://gone.example/d");
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t));
    ASSERT(firc_resolve_route_describe(&t.items[0], buf, sizeof(buf)) > 0);
    ASSERT_STR_EQ("answers its names from resolve.server: no usable server, never the common upstream", buf);
    firc_resolve_table_free(&t);
    firc_group_free(g);
    PASS();
}

TEST a_dns_group_with_tunnel_off_still_has_its_route(void)
{
    firc_group_t *g = group("d", "dns", 1);
    firc_strset(&g->resolve.server, "9.9.9.9");
    g->resolve.tunnel = false;
    firc_resolve_input_t in = {.group = g};
    firc_resolve_table_t t = {0};
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t));
    ASSERT_EQ(FIRC_RESOLVE_SOURCE_GROUP, t.items[0].source);
    ASSERT(firc_resolve_route_usable(&t.items[0]));
    firc_resolve_table_free(&t);
    firc_group_free(g);
    PASS();
}

typedef struct {
    firc_config_t cfg;
    firc_dns_pipeline_t *pl;
    firc_resolve_router_t *router;
} dns_fx_t;

static void dns_fx_up(dns_fx_t *fx, const char *server, bool fallback, bool publish)
{
    memset(fx, 0, sizeof(*fx));
    firc_config_init_defaults(&fx->cfg);
    firc_group_t *g = group("d", "dns", 1);
    add_namespace_rule(g, "dns.test");
    if (server != NULL) { firc_strset(&g->resolve.server, server); }
    g->resolve.fallback = fallback;
    firc_config_add_group(&fx->cfg, g);
    fx->pl = firc_dns_pipeline_create();
    fx->router = firc_resolve_router_new(fx->pl);
    firc_dns_pipeline_set_snapshot(fx->pl, firc_ruleset_snapshot_build(&fx->cfg));
    if (publish) {
        firc_resolve_input_t in = {.group = g, .intern = fake_intern};
        firc_resolve_table_t t;
        firc_resolve_table_build(&in, 1, NULL, &t);
        firc_resolve_router_publish(fx->router, &t);
    }
}

static void dns_fx_down(dns_fx_t *fx)
{
    firc_resolve_router_free(fx->router);
    firc_dns_pipeline_destroy(fx->pl);
    firc_config_clear(&fx->cfg);
}

static bool dns_decide(dns_fx_t *fx, firc_dnsproxy_route_t *out)
{
    firc_ip_t c = {{192, 168, 1, 5}, 4};
    firc_dns_msg_t *q = query("a.dns.test", 1);
    bool got = firc_resolve_router_decide(fx->router, q, &c, out);
    firc_dns_msg_free(q);
    return got;
}

TEST a_dns_owner_is_claimed_with_its_servers_and_doh_ids(void)
{
    intern_reset("https://a.example/d", NULL);
    dns_fx_t fx;
    dns_fx_up(&fx, "9.9.9.9, https://a.example/d", false, true);
    firc_dnsproxy_route_t r;
    ASSERT(dns_decide(&fx, &r));
    ASSERT(r.dns_only);
    ASSERT(r.no_fallback);
    ASSERT_EQ((uint32_t)0, r.mark);
    ASSERT_EQ((size_t)2, r.n_servers);
    ASSERT_EQ((socklen_t)sizeof(struct sockaddr_in), r.server_lens[0]);
    ASSERT_EQ((uint16_t)0, r.url_ids[0]);
    ASSERT_EQ((socklen_t)0, r.server_lens[1]);
    ASSERT_EQ((uint16_t)1, r.url_ids[1]);
    ASSERT(firc_id_equal(fx.cfg.groups[0]->id, r.group_id));
    ASSERT_STR_EQ("d", r.group_name);
    ASSERT(r.gen != 0);
    dns_fx_down(&fx);
    PASS();
}

TEST a_dns_owner_with_fallback_is_claimed_with_fallback(void)
{
    dns_fx_t fx;
    dns_fx_up(&fx, "9.9.9.9", true, true);
    firc_dnsproxy_route_t r;
    ASSERT(dns_decide(&fx, &r));
    ASSERT(r.dns_only);
    ASSERT_FALSE(r.no_fallback);
    dns_fx_down(&fx);
    PASS();
}

TEST a_refused_doh_url_is_warned_with_its_group_once_per_build(void)
{
    intern_reset("https://ok.example/d", NULL);
    firc_group_t *g = group("g", "nwg0", 1);
    firc_strset(&g->resolve.server, "https://a.example/d, https://ok.example/d");
    firc_resolve_input_t in = {.group = g, .mark = MARK1, .intern = fake_intern};
    firc_resolve_table_t t = {0};
    int fds[2];
    ASSERT_EQ(0, pipe(fds));
    firc_log_set_output_floor(FIRC_LOG_INFO);
    firc_log_set_fd(fds[1]);
    ASSERT_EQ(FIRC_OK, firc_resolve_table_build(&in, 1, NULL, &t));
    firc_log_set_fd(STDOUT_FILENO);
    close(fds[1]);
    char log[1024];
    ssize_t n = read(fds[0], log, sizeof(log) - 1);
    close(fds[0]);
    ASSERT(n > 0);
    log[n] = '\0';
    ASSERTm(log, strstr(log, "https://a.example/…") != NULL);
    ASSERTm(log, strstr(log, "\"g\"") != NULL);
    ASSERTm(log, strstr(log, "https://ok.example/") == NULL);
    const char *first = strstr(log, "https://a.example/");
    ASSERTm(log, strstr(first + 1, "https://a.example/") == NULL);
    firc_resolve_table_free(&t);
    firc_group_free(g);
    PASS();
}

TEST a_dns_owner_without_a_usable_server_is_still_claimed(void)
{
    intern_reset(NULL, NULL);
    dns_fx_t fx;
    dns_fx_up(&fx, "https://gone.example/d", false, true);
    firc_dnsproxy_route_t r;
    ASSERT(dns_decide(&fx, &r));
    ASSERT(r.dns_only);
    ASSERT(r.no_fallback);
    ASSERT_EQ((size_t)0, r.n_servers);
    ASSERT_EQ((uint32_t)0, r.mark);
    dns_fx_down(&fx);
    PASS();
}

TEST a_dns_owner_before_the_table_is_published_is_still_claimed(void)
{
    dns_fx_t fx;
    dns_fx_up(&fx, "9.9.9.9", false, false);
    firc_dnsproxy_route_t r;
    ASSERT(dns_decide(&fx, &r));
    ASSERT(r.dns_only);
    ASSERT(r.no_fallback);
    ASSERT_EQ((size_t)0, r.n_servers);
    ASSERT(firc_id_equal(fx.cfg.groups[0]->id, r.group_id));
    dns_fx_down(&fx);
    dns_fx_up(&fx, "9.9.9.9", true, false);
    ASSERT(dns_decide(&fx, &r));
    ASSERT_FALSE(r.no_fallback);
    dns_fx_down(&fx);
    PASS();
}

TEST a_routed_owner_carries_its_doh_id_and_its_mark(void)
{
    intern_reset("https://a.example/d", NULL);
    firc_config_t cfg;
    firc_config_init_defaults(&cfg);
    firc_group_t *g = group("g", "nwg0", 1);
    add_namespace_rule(g, "dns.test");
    firc_strset(&g->resolve.server, "https://a.example/d");
    firc_config_add_group(&cfg, g);
    firc_dns_pipeline_t *pl = firc_dns_pipeline_create();
    firc_resolve_router_t *router = firc_resolve_router_new(pl);
    firc_dns_pipeline_set_snapshot(pl, firc_ruleset_snapshot_build(&cfg));
    firc_resolve_input_t in = {.group = g, .mark = MARK1, .intern = fake_intern};
    firc_resolve_table_t t;
    firc_resolve_table_build(&in, 1, NULL, &t);
    firc_resolve_router_publish(router, &t);
    firc_ip_t c = {{192, 168, 1, 5}, 4};
    firc_dns_msg_t *q = query("a.dns.test", 1);
    firc_dnsproxy_route_t r;
    ASSERT(firc_resolve_router_decide(router, q, &c, &r));
    firc_dns_msg_free(q);
    ASSERT_FALSE(r.dns_only);
    ASSERT_EQ(MARK1, r.mark);
    ASSERT_EQ((size_t)1, r.n_servers);
    ASSERT_EQ((socklen_t)0, r.server_lens[0]);
    ASSERT_EQ((uint16_t)1, r.url_ids[0]);
    firc_resolve_router_free(router);
    firc_dns_pipeline_destroy(pl);
    firc_config_clear(&cfg);
    PASS();
}

TEST a_routed_owner_without_a_usable_route_is_still_declined(void)
{
    firc_config_t cfg;
    firc_config_init_defaults(&cfg);
    firc_group_t *g = group("g", "nwg0", 1);
    add_namespace_rule(g, "dns.test");
    firc_strset(&g->resolve.server, "9.9.9.9");
    firc_config_add_group(&cfg, g);
    firc_dns_pipeline_t *pl = firc_dns_pipeline_create();
    firc_resolve_router_t *router = firc_resolve_router_new(pl);
    firc_dns_pipeline_set_snapshot(pl, firc_ruleset_snapshot_build(&cfg));
    firc_resolve_input_t in = {.group = g, .mark = 0};
    firc_resolve_table_t t;
    firc_resolve_table_build(&in, 1, NULL, &t);
    firc_resolve_router_publish(router, &t);
    firc_ip_t c = {{192, 168, 1, 5}, 4};
    firc_dns_msg_t *q = query("a.dns.test", 1);
    firc_dnsproxy_route_t r;
    ASSERT_FALSE(firc_resolve_router_decide(router, q, &c, &r));
    firc_dns_msg_free(q);
    firc_resolve_router_free(router);
    firc_dns_pipeline_destroy(pl);
    firc_config_clear(&cfg);
    PASS();
}

GREATEST_MAIN_DEFS();

int main(int argc, char **argv)
{
    GREATEST_MAIN_BEGIN();
    RUN_TEST(precedence_is_server_then_firmware_then_none);
    RUN_TEST(a_long_group_name_is_cut_on_a_character_in_the_route);
    RUN_TEST(a_group_with_no_mark_is_not_usable);
    RUN_TEST(v4_first_and_v6_only_with_a_v6_route);
    RUN_TEST(a_groups_own_servers_are_all_routed_in_order);
    RUN_TEST(a_doh_entry_adds_no_server_to_the_route);
    RUN_TEST(an_own_v6_server_needs_a_v6_route);
    RUN_TEST(the_generation_moves_only_on_a_real_change);
    RUN_TEST(each_route_is_described_for_the_log);
    RUN_TEST(a_block_route_says_firc_answers_its_names);
    RUN_TEST(a_publish_logs_what_changed_and_hands_out_copies);
    RUN_TEST(the_owner_of_a_covered_query_is_routed_by_value);
    RUN_TEST(a_query_with_no_client_address_is_routed_like_the_pipeline_covers_it);
    RUN_TEST(every_query_type_is_routed);
    RUN_TEST(what_is_not_routed_goes_to_the_common_upstream);
    RUN_TEST(a_lower_groups_device_is_resolved_by_its_group_and_never_through_another);
    RUN_TEST(the_stale_groups_are_the_removed_and_the_rewired);
    RUN_TEST(a_published_v6_flag_reaches_the_answer_and_flips_with_the_next_publish);
    RUN_TEST(a_route_names_the_active_entry_not_the_first);
    RUN_TEST(cross_v6_needs_both_v6_and_kernel_tcp);
    RUN_TEST(a_block_owner_is_claimed_without_a_route);
    RUN_TEST(the_owner_is_reported_when_the_route_is_declined);
    RUN_TEST(a_dns_group_is_usable_without_a_mark);
    RUN_TEST(a_routed_group_without_a_mark_stays_unusable_with_doh);
    RUN_TEST(a_dns_group_never_takes_the_firmwares_resolvers);
    RUN_TEST(a_dns_route_carries_no_fallback);
    RUN_TEST(a_doh_entry_reaches_the_route_as_a_url_id);
    RUN_TEST(a_doh_entry_is_dropped_when_the_intern_refuses_it);
    RUN_TEST(a_change_of_url_is_a_new_generation);
    RUN_TEST(a_dns_route_says_where_its_names_go);
    RUN_TEST(a_dns_group_with_tunnel_off_still_has_its_route);
    RUN_TEST(a_dns_owner_is_claimed_with_its_servers_and_doh_ids);
    RUN_TEST(a_dns_owner_with_fallback_is_claimed_with_fallback);
    RUN_TEST(a_refused_doh_url_is_warned_with_its_group_once_per_build);
    RUN_TEST(a_dns_owner_without_a_usable_server_is_still_claimed);
    RUN_TEST(a_dns_owner_before_the_table_is_published_is_still_claimed);
    RUN_TEST(a_routed_owner_carries_its_doh_id_and_its_mark);
    RUN_TEST(a_routed_owner_without_a_usable_route_is_still_declined);
    GREATEST_MAIN_END();
}
