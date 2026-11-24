#include "greatest.h"

#include <arpa/inet.h>
#include <netinet/in.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include "firc/fakeip.h"
#include "firc/fakeip_from_config.h"
#include "firc/models.h"
#include "firc/resolve_check.h"
#include "firc/resolver_addr.h"
#include "firc/yamlio.h"

static firc_resolver_addr_res_t parse(const char *s, firc_resolver_addr_t *a)
{
    memset(a, 0xAB, sizeof(*a));
    return firc_resolver_addr_parse(s, a);
}

/* Catches: a valid address shape refused, a port read off the wrong side of v6, or no default 53. */
TEST each_shape_parses(void)
{
    firc_resolver_addr_t a;
    ASSERT_EQ(FIRC_RESOLVER_ADDR_OK, parse("9.9.9.9", &a));
    ASSERT_EQ(4, a.ip.len);
    ASSERT_EQ(9, a.ip.b[0]);
    ASSERT_EQ(9, a.ip.b[3]);
    ASSERT_EQ(53, a.port);

    ASSERT_EQ(FIRC_RESOLVER_ADDR_OK, parse("9.9.9.9:5353", &a));
    ASSERT_EQ(5353, a.port);

    ASSERT_EQ(FIRC_RESOLVER_ADDR_OK, parse("2620:fe::fe", &a));
    ASSERT_EQ(16, a.ip.len);
    ASSERT_EQ(0x26, a.ip.b[0]);
    ASSERT_EQ(0xfe, a.ip.b[15]);
    ASSERT_EQ(53, a.port);

    ASSERT_EQ(FIRC_RESOLVER_ADDR_OK, parse("[2620:fe::fe]:853", &a));
    ASSERT_EQ(16, a.ip.len);
    ASSERT_EQ(853, a.port);

    ASSERT_EQ(FIRC_RESOLVER_ADDR_OK, parse("[2620:fe::fe]", &a));
    ASSERT_EQ(53, a.port);

    ASSERT_EQ(FIRC_RESOLVER_ADDR_OK, parse("2620:fe::fe:53", &a));
    ASSERT_EQ(16, a.ip.len);
    ASSERT_EQ(0x00, a.ip.b[14]);
    ASSERT_EQ(0x53, a.ip.b[15]);
    ASSERT_EQ(53, a.port);
    PASS();
}

/* Catches: a host name, partial address, bracketed v4 or out-of-range port accepted. */
TEST what_is_not_an_address_is_refused(void)
{
    firc_resolver_addr_t a;
    const char *syntax[] = {"",           "dns.google",   "9.9.9",        "9.9.9.9:",
                            "[9.9.9.9]",  "[9.9.9.9]:53", "[2620:fe::fe", "2620:fe::fe]",
                            "9.9.9.9:53x", " 9.9.9.9",    "9.9.9.9 ",     "[2620:fe::fe]53"};
    for (size_t i = 0; i < sizeof(syntax) / sizeof(syntax[0]); i++) {
        ASSERT_EQm(syntax[i], FIRC_RESOLVER_ADDR_SYNTAX, parse(syntax[i], &a));
    }
    const char *port[] = {"9.9.9.9:0",     "9.9.9.9:65536",    "9.9.9.9:+53", "9.9.9.9:053535",
                          "[2620:fe::fe]:0", "9.9.9.9:-1",      "9.9.9.9:+",   "[2620:fe::fe]:-"};
    for (size_t i = 0; i < sizeof(port) / sizeof(port[0]); i++) {
        ASSERT_EQm(port[i], FIRC_RESOLVER_ADDR_PORT, parse(port[i], &a));
    }
    ASSERT_EQ(FIRC_RESOLVER_ADDR_OK, parse("9.9.9.9:65535", &a));
    ASSERT_EQ(65535, a.port);
    ASSERT_EQ(FIRC_RESOLVER_ADDR_OK, parse("9.9.9.9:1", &a));
    ASSERT_EQ(1, a.port);
    ASSERT_EQ(FIRC_RESOLVER_ADDR_SYNTAX, firc_resolver_addr_parse(NULL, &a));
    PASS();
}

/* Catches: 127/8 narrowed, a sink's neighbour refused, or a v4-mapped sink let through. */
TEST sinks_are_refused_and_their_neighbours_are_not(void)
{
    firc_resolver_addr_t a;
    const char *sinks[] = {"0.0.0.0", "127.0.0.1", "127.255.255.254:53", "::", "::1", "[::1]:53", "[::]:5353"};
    for (size_t i = 0; i < sizeof(sinks) / sizeof(sinks[0]); i++) {
        ASSERT_EQm(sinks[i], FIRC_RESOLVER_ADDR_SINK, parse(sinks[i], &a));
    }
    ASSERT_EQ(FIRC_RESOLVER_ADDR_OK, parse("126.255.255.255", &a));
    ASSERT_EQ(FIRC_RESOLVER_ADDR_OK, parse("128.0.0.1", &a));
    ASSERT_EQ(FIRC_RESOLVER_ADDR_OK, parse("0.0.0.1", &a));
    ASSERT_EQ(FIRC_RESOLVER_ADDR_OK, parse("::2", &a));
    ASSERT_EQ(FIRC_RESOLVER_ADDR_MAPPED, parse("::ffff:127.0.0.1", &a));
    ASSERT_EQ(FIRC_RESOLVER_ADDR_MAPPED, parse("::ffff:9.9.9.9", &a));
    PASS();
}

/* Catches: answers and validation disagreeing on sinks, or a v4-mapped AAAA not read as v4. */
TEST the_sink_test_takes_a_bare_address(void)
{
    firc_ip_t ip = {.len = 4};
    ASSERT(firc_resolver_ip_is_sink(&ip));
    ip.b[0] = 127; ip.b[3] = 9;
    ASSERT(firc_resolver_ip_is_sink(&ip));
    ip.b[0] = 9;
    ASSERT_FALSE(firc_resolver_ip_is_sink(&ip));
    firc_ip_t v6 = {.len = 16};
    ASSERT(firc_resolver_ip_is_sink(&v6));
    v6.b[15] = 1;
    ASSERT(firc_resolver_ip_is_sink(&v6));
    v6.b[15] = 2;
    ASSERT_FALSE(firc_resolver_ip_is_sink(&v6));
    firc_ip_t mapped = {.len = 16};
    mapped.b[10] = 0xff;
    mapped.b[11] = 0xff;
    ASSERT(firc_resolver_ip_is_sink(&mapped));
    mapped.b[12] = 127;
    mapped.b[15] = 9;
    ASSERT(firc_resolver_ip_is_sink(&mapped));
    mapped.b[12] = 9;
    ASSERT_FALSE(firc_resolver_ip_is_sink(&mapped));
    PASS();
}

/* Catches: port 53 written, a v6 port without brackets, or a small buffer overrun. */
TEST an_address_formats_back_to_what_parses(void)
{
    const char *in[] = {"9.9.9.9", "9.9.9.9:5353", "2620:fe::fe", "[2620:fe::fe]:853"};
    for (size_t i = 0; i < sizeof(in) / sizeof(in[0]); i++) {
        firc_resolver_addr_t a;
        ASSERT_EQ(FIRC_RESOLVER_ADDR_OK, parse(in[i], &a));
        char buf[FIRC_RESOLVER_ADDR_STRLEN];
        ASSERT(firc_resolver_addr_format(&a, buf, sizeof(buf)) > 0);
        ASSERT_STR_EQ(in[i], buf);
    }
    firc_resolver_addr_t a;
    ASSERT_EQ(FIRC_RESOLVER_ADDR_OK, parse("[2620:fe::fe]", &a));
    char buf[FIRC_RESOLVER_ADDR_STRLEN];
    ASSERT(firc_resolver_addr_format(&a, buf, sizeof(buf)) > 0);
    ASSERT_STR_EQ("2620:fe::fe", buf);
    char tiny[4];
    ASSERT_EQ((size_t)0, firc_resolver_addr_format(&a, tiny, sizeof(tiny)));
    PASS();
}

/* Catches: the port left in host order, or the wrong family. */
TEST an_address_becomes_a_sockaddr(void)
{
    firc_resolver_addr_t a;
    struct sockaddr_storage ss;
    socklen_t len = 0;
    ASSERT_EQ(FIRC_RESOLVER_ADDR_OK, parse("1.0.0.1:5353", &a));
    firc_resolver_addr_sockaddr(&a, &ss, &len);
    const struct sockaddr_in *s4 = (const struct sockaddr_in *)&ss;
    ASSERT_EQ(AF_INET, s4->sin_family);
    ASSERT_EQ((socklen_t)sizeof(struct sockaddr_in), len);
    ASSERT_EQ(htons(5353), s4->sin_port);
    ASSERT_EQ(htonl(0x01000001u), s4->sin_addr.s_addr);
    ASSERT_EQ(FIRC_RESOLVER_ADDR_OK, parse("2620:fe::fe", &a));
    firc_resolver_addr_sockaddr(&a, &ss, &len);
    const struct sockaddr_in6 *s6 = (const struct sockaddr_in6 *)&ss;
    ASSERT_EQ(AF_INET6, s6->sin6_family);
    ASSERT_EQ((socklen_t)sizeof(struct sockaddr_in6), len);
    ASSERT_EQ(htons(53), s6->sin6_port);
    ASSERT_EQ(0xfe, s6->sin6_addr.s6_addr[15]);
    PASS();
}

/* Catches: a comma list read as one address, blanks kept, the written order lost, or a v6 port misread. */
TEST a_list_of_servers_splits_in_order(void)
{
    firc_resolver_list_t l;
    ASSERT_EQ(FIRC_RESOLVER_LIST_OK, firc_resolver_list_parse("1.1.1.1, 9.9.9.9", &l, NULL, 0));
    ASSERT_EQ((size_t)2, l.n);
    ASSERT_EQ(1, l.servers[0].ip.b[0]);
    ASSERT_EQ(9, l.servers[1].ip.b[3]);
    ASSERT_EQ(53, l.servers[1].port);

    ASSERT_EQ(FIRC_RESOLVER_LIST_OK, firc_resolver_list_parse("[2606:4700::1111]:53,1.1.1.1:5353", &l, NULL, 0));
    ASSERT_EQ((size_t)2, l.n);
    ASSERT_EQ(16, l.servers[0].ip.len);
    ASSERT_EQ(0x26, l.servers[0].ip.b[0]);
    ASSERT_EQ(0x11, l.servers[0].ip.b[15]);
    ASSERT_EQ(53, l.servers[0].port);
    ASSERT_EQ(4, l.servers[1].ip.len);
    ASSERT_EQ(5353, l.servers[1].port);

    ASSERT_EQ(FIRC_RESOLVER_LIST_OK, firc_resolver_list_parse("\t9.9.9.9 ", &l, NULL, 0));
    ASSERT_EQ((size_t)1, l.n);
    ASSERT_EQ(9, l.servers[0].ip.b[0]);

    ASSERT_EQ(FIRC_RESOLVER_LIST_OK, firc_resolver_list_parse("1.1.1.1,1.0.0.1, 9.9.9.9 ,2620:fe::fe", &l, NULL, 0));
    ASSERT_EQ((size_t)4, l.n);
    ASSERT_EQ(1, l.servers[1].ip.b[3]);
    ASSERT_EQ(0xfe, l.servers[3].ip.b[15]);
    PASS();
}

/* Catches: a fifth server kept or silently dropped, an empty entry skipped, or a twin accepted. */
TEST a_bad_list_is_refused_naming_what_is_wrong(void)
{
    firc_resolver_list_t l;
    char why[256];
    ASSERT_EQ(FIRC_RESOLVER_LIST_TOO_MANY,
              firc_resolver_list_parse("1.1.1.1,1.0.0.1,9.9.9.9,8.8.8.8,8.8.4.4", &l, why, sizeof(why)));
    ASSERT_STR_EQ("\"1.1.1.1,1.0.0.1,9.9.9.9,8.8.8.8,8.8.4.4\" names more than 4 servers", why);

    const char *empty[] = {"1.1.1.1,,9.9.9.9", "1.1.1.1,", ",1.1.1.1", "1.1.1.1, ,9.9.9.9", " ", ""};
    for (size_t i = 0; i < sizeof(empty) / sizeof(empty[0]); i++) {
        ASSERT_EQm(empty[i], FIRC_RESOLVER_LIST_EMPTY, firc_resolver_list_parse(empty[i], &l, why, sizeof(why)));
        ASSERTm(why, strstr(why, "has an empty entry") != NULL);
    }
    ASSERT_EQ(FIRC_RESOLVER_LIST_EMPTY, firc_resolver_list_parse("1.1.1.1,", &l, why, sizeof(why)));
    ASSERT_STR_EQ("\"1.1.1.1,\" has an empty entry", why);

    ASSERT_EQ(FIRC_RESOLVER_LIST_DUPLICATE, firc_resolver_list_parse("9.9.9.9, 1.1.1.1, 9.9.9.9:53", &l, why, sizeof(why)));
    ASSERT_STR_EQ("\"9.9.9.9, 1.1.1.1, 9.9.9.9:53\" names \"9.9.9.9:53\" twice", why);
    ASSERT_EQ(FIRC_RESOLVER_LIST_DUPLICATE, firc_resolver_list_parse("[2620:fe::fe]:53, 2620:fe::fe", &l, why, sizeof(why)));
    ASSERT_STR_EQ("\"[2620:fe::fe]:53, 2620:fe::fe\" names \"2620:fe::fe\" twice", why);
    ASSERT_EQ(FIRC_RESOLVER_LIST_OK, firc_resolver_list_parse("9.9.9.9, 9.9.9.9:5353", &l, NULL, 0));
    ASSERT_EQ((size_t)2, l.n);

    ASSERT_EQ(FIRC_RESOLVER_LIST_ADDR, firc_resolver_list_parse("1.1.1.1, dns.google", &l, why, sizeof(why)));
    ASSERT_STR_EQ("\"dns.google\" is not an address literal (addr, addr:port, [v6addr] or [v6addr]:port)", why);
    ASSERT_EQ(FIRC_RESOLVER_LIST_ADDR, firc_resolver_list_parse("127.0.0.1 ,1.1.1.1", &l, why, sizeof(why)));
    ASSERT_STR_EQ("\"127.0.0.1\" is a sink address (0.0.0.0, 127.0.0.0/8, ::, ::1), which no resolver answers from", why);
    ASSERT_EQ(FIRC_RESOLVER_LIST_ADDR, firc_resolver_list_parse("1.1.1.1,9.9.9.9:0", &l, why, sizeof(why)));
    ASSERT_STR_EQ("\"9.9.9.9:0\" has a port outside 1..65535", why);
    ASSERT_EQ(FIRC_RESOLVER_LIST_ADDR, firc_resolver_list_parse("1.1.1.1 9.9.9.9", &l, why, sizeof(why)));
    ASSERT_STR_EQ("\"1.1.1.1 9.9.9.9\" is not an address literal (addr, addr:port, [v6addr] or [v6addr]:port)", why);
    ASSERT_EQ(FIRC_RESOLVER_LIST_ADDR, firc_resolver_list_parse(NULL, &l, NULL, 0));

    char tiny[8];
    ASSERT_EQ(FIRC_RESOLVER_LIST_ADDR, firc_resolver_list_parse("dns.google", &l, tiny, sizeof(tiny)));
    ASSERT_STR_EQ("\"dns.go", tiny);
    PASS();
}

static char g_ula[192];

/* A pool from the shipped defaults (198.18.0.0/15) with fd00:1234::/48 as its v6 half. */
static firc_fakeip_t *pool_for_test(void)
{
    const char *dir = getenv("TMPDIR") ? getenv("TMPDIR") : "/tmp";
    snprintf(g_ula, sizeof(g_ula), "%s/resolver_addr_ula", dir);
    unlink(g_ula);
    firc_config_t cfg;
    if (firc_config_init_defaults(&cfg) != FIRC_OK) { return NULL; }
    firc_fakeip_t *f = NULL;
    firc_fakeip_cfg_t pc;
    memset(&pc, 0, sizeof(pc));
    if (firc_strset(&cfg.app.fakeip.v6.pool, "fd00:1234::/48") == FIRC_OK &&
        firc_fakeip_cfg_from_app(&cfg.app, g_ula, &pc) == FIRC_OK) {
        (void)firc_fakeip_new(&pc, &f);
    }
    firc_config_clear(&cfg);
    return f;
}

/* Catches: a resolve server inside the pool accepted, checked against the wrong family, or off the edge. */
TEST a_server_inside_the_pool_is_refused_at_startup(void)
{
    firc_fakeip_t *pool = pool_for_test();
    ASSERT(pool != NULL);
    ASSERT(firc_resolve_server_in_pool("198.18.0.1", pool, NULL, 0));
    ASSERT(firc_resolve_server_in_pool("198.19.255.255:5353", pool, NULL, 0));
    ASSERT_FALSE(firc_resolve_server_in_pool("198.20.0.0", pool, NULL, 0));
    ASSERT_FALSE(firc_resolve_server_in_pool("198.17.255.255", pool, NULL, 0));
    ASSERT(firc_resolve_server_in_pool("fd00:1234::53", pool, NULL, 0));
    ASSERT(firc_resolve_server_in_pool("[fd00:1234::ffff:1]:53", pool, NULL, 0));
    ASSERT_FALSE(firc_resolve_server_in_pool("[fd00:1234:ffff::1]:53", pool, NULL, 0));
    ASSERT_FALSE(firc_resolve_server_in_pool("fd00:1235::1", pool, NULL, 0));
    ASSERT_FALSE(firc_resolve_server_in_pool("", pool, NULL, 0));
    ASSERT_FALSE(firc_resolve_server_in_pool(NULL, pool, NULL, 0));
    ASSERT(firc_resolve_server_in_pool("9.9.9.9, 198.18.0.1", pool, NULL, 0));
    ASSERT(firc_resolve_server_in_pool("1.1.1.1,[fd00:1234::53]:5353,9.9.9.9", pool, NULL, 0));
    ASSERT_FALSE(firc_resolve_server_in_pool("9.9.9.9, 198.20.0.1", pool, NULL, 0));

    firc_group_t *g[3] = {firc_group_new(), firc_group_new(), firc_group_new()};
    ASSERT(g[0] && g[1] && g[2]);
    ASSERT_EQ(FIRC_OK, firc_strset(&g[1]->resolve.server, "9.9.9.9"));
    ASSERT_EQ(FIRC_OK, firc_strset(&g[2]->resolve.server, "198.18.7.7"));
    char hit[FIRC_RESOLVER_ADDR_STRLEN];
    ASSERT(firc_resolve_server_in_pool("2620:fe::fe, 2620:fe::9, 2606:4700::1111, 198.19.0.53:5353", pool, hit,
                                       sizeof(hit)));
    ASSERT_STR_EQ("198.19.0.53:5353", hit);
    ASSERT(firc_resolve_server_in_pool("1.1.1.1,[fd00:1234::53]:53", pool, hit, sizeof(hit)));
    ASSERT_STR_EQ("fd00:1234::53", hit);
    ASSERT_EQ(g[2], firc_resolve_groups_in_pool(g, 3, pool, hit, sizeof(hit)));
    ASSERT_STR_EQ("198.18.7.7", hit);
    ASSERT_EQ(NULL, firc_resolve_groups_in_pool(g, 2, pool, NULL, 0));
    for (int i = 0; i < 3; i++) { firc_group_free(g[i]); }
    firc_fakeip_free(pool);
    unlink(g_ula);
    PASS();
}

TEST a_doh_url_is_an_entry(void)
{
    firc_resolver_list_t l;
    ASSERT_EQ(FIRC_RESOLVER_LIST_OK, firc_resolver_list_parse("  https://dns.comss.one/dns-query ", &l, NULL, 0));
    ASSERT_EQ((size_t)1, l.n);
    ASSERT(l.servers[0].doh);
    ASSERT_STR_EQ("https://dns.comss.one/dns-query", l.urls[0]);
    ASSERT_EQ(0, l.servers[0].ip.len);
    ASSERT_EQ(FIRC_RESOLVER_LIST_OK, firc_resolver_list_parse("HTTPS://[2606:4700::1111]:8443/q", &l, NULL, 0));
    ASSERT_STR_EQ("HTTPS://[2606:4700::1111]:8443/q", l.urls[0]);
    ASSERT_EQ(FIRC_RESOLVER_LIST_OK, firc_resolver_list_parse("https://1.1.1.1/", &l, NULL, 0));
    ASSERT(l.servers[0].doh);
    PASS();
}

TEST literals_and_urls_mix_up_to_four(void)
{
    firc_resolver_list_t l;
    ASSERT_EQ(FIRC_RESOLVER_LIST_OK,
              firc_resolver_list_parse("1.1.1.1, https://a.example/dns, [2620:fe::fe]:853,https://b.example:444/x", &l,
                                       NULL, 0));
    ASSERT_EQ((size_t)4, l.n);
    ASSERT_FALSE(l.servers[0].doh);
    ASSERT(l.servers[1].doh);
    ASSERT_FALSE(l.servers[2].doh);
    ASSERT(l.servers[3].doh);
    ASSERT_STR_EQ("https://a.example/dns", l.urls[1]);
    ASSERT_STR_EQ("https://b.example:444/x", l.urls[3]);
    ASSERT_EQ(4, l.servers[0].ip.len);
    ASSERT_EQ(853, l.servers[2].port);
    PASS();
}

TEST a_fifth_entry_is_refused_whatever_its_kind(void)
{
    firc_resolver_list_t l;
    ASSERT_EQ(FIRC_RESOLVER_LIST_TOO_MANY,
              firc_resolver_list_parse("1.1.1.1,9.9.9.9,https://a.example/d,https://b.example/d,https://c.example/d",
                                       &l, NULL, 0));
    ASSERT_EQ(FIRC_RESOLVER_LIST_TOO_MANY,
              firc_resolver_list_parse("https://a.example/d,https://b.example/d,https://c.example/d,"
                                       "https://d.example/d,1.1.1.1",
                                       &l, NULL, 0));
    PASS();
}

TEST a_url_that_is_not_https_or_not_well_formed_is_refused(void)
{
    const char *bad[] = {
        "http://dns.example/dns-query",
        "ftp://dns.example/x",
        "https:///dns-query",
        "https://:443/dns-query",
        "https://dns.example",
        "https://dns.example:443",
        "https://user@dns.example/dns-query",
        "https://dns.example/dns-query?x=1",
        "https://dns.example/dns-query#f",
        "https://dns.example/dns query",
        "https://dns.example:0/x",
        "https://dns.example:65536/x",
        "https://dns.example:44x/x",
        "https://[2606:4700::1111/x",
        "https://[]/x",
        "https://[abc]/x",
        "https://[1.1.1.1]/x",
    };
    for (size_t i = 0; i < sizeof(bad) / sizeof(bad[0]); i++) {
        firc_resolver_list_t l;
        ASSERT_EQm(bad[i], FIRC_RESOLVER_LIST_URL, firc_resolver_list_parse(bad[i], &l, NULL, 0));
        ASSERT_EQm(bad[i], (size_t)0, l.n);
    }
    PASS();
}

/* Catches: a literal a plain entry refuses accepted as a DoH host, or its neighbours refused. */
TEST a_urls_literal_is_held_to_a_plain_entrys_rules(void)
{
    const char *bad[] = {"https://127.0.0.1/q",  "https://127.9.9.9:8443/q",     "https://0.0.0.0/q",
                         "https://[::1]/q",      "https://[::]/q",               "https://[::ffff:127.0.0.1]/q",
                         "https://[::ffff:9.9.9.9]/q"};
    for (size_t i = 0; i < sizeof(bad) / sizeof(bad[0]); i++) {
        firc_resolver_list_t l;
        ASSERT_EQm(bad[i], FIRC_RESOLVER_LIST_URL, firc_resolver_list_parse(bad[i], &l, NULL, 0));
        ASSERT_EQm(bad[i], (size_t)0, l.n);
    }
    char why[256];
    firc_resolver_list_t l;
    ASSERT_EQ(FIRC_RESOLVER_LIST_URL, firc_resolver_list_parse("https://127.0.0.1/q", &l, why, sizeof(why)));
    ASSERT_STR_EQ("\"https://127.0.0.1/q\" is a sink address (0.0.0.0, 127.0.0.0/8, ::, ::1), which no resolver answers from", why);
    ASSERT_EQ(FIRC_RESOLVER_LIST_OK, firc_resolver_list_parse("https://128.0.0.1/q, https://192.168.1.1/q, https://[::2]/q", &l, NULL, 0));
    PASS();
}

/* Catches: a provider's account id in a URL's path written to the log. */
TEST the_log_shows_a_url_without_its_path(void)
{
    char s[64];
    ASSERT(firc_resolver_url_show("https://dns.example:8443/account-id/q", s, sizeof(s)) > 0);
    ASSERT_STR_EQ("https://dns.example:8443/…", s);
    ASSERT(firc_resolver_url_show("https://[2620:fe::fe]/", s, sizeof(s)) > 0);
    ASSERT_STR_EQ("https://[2620:fe::fe]/", s);
    ASSERT_EQ((size_t)0, firc_resolver_url_show("https://dns.example/q", s, 8));
    ASSERT_STR_EQ("", s);
    PASS();
}

TEST http_is_refused(void)
{
    firc_resolver_list_t l;
    char why[256];
    ASSERT_EQ(FIRC_RESOLVER_LIST_URL, firc_resolver_list_parse("1.1.1.1, http://dns.example/dns-query", &l, why, sizeof(why)));
    ASSERT(strstr(why, "https") != NULL);
    PASS();
}

TEST a_url_twin_is_refused(void)
{
    firc_resolver_list_t l;
    char why[256];
    ASSERT_EQ(FIRC_RESOLVER_LIST_DUPLICATE,
              firc_resolver_list_parse("https://a.example/d, 1.1.1.1, https://a.example/d", &l, why, sizeof(why)));
    ASSERT_STR_EQ("\"https://a.example/d, 1.1.1.1, https://a.example/d\" names \"https://a.example/d\" twice", why);
    ASSERT_EQ(FIRC_RESOLVER_LIST_OK, firc_resolver_list_parse("https://a.example/d, https://a.example/D", &l, NULL, 0));
    ASSERT_EQ((size_t)2, l.n);
    PASS();
}

TEST a_url_without_a_host_is_refused(void)
{
    firc_resolver_list_t l;
    ASSERT_EQ(FIRC_RESOLVER_LIST_URL, firc_resolver_list_parse("https:///", &l, NULL, 0));
    ASSERT_EQ(FIRC_RESOLVER_LIST_URL, firc_resolver_list_parse("https://", &l, NULL, 0));
    PASS();
}

TEST a_256_byte_url_is_refused(void)
{
    char url[300];
    firc_resolver_list_t l;
    size_t pre = strlen("https://a.example/");
    memcpy(url, "https://a.example/", pre);
    memset(url + pre, 'x', 255 - pre);
    url[255] = '\0';
    ASSERT_EQ(FIRC_RESOLVER_LIST_OK, firc_resolver_list_parse(url, &l, NULL, 0));
    ASSERT_EQ((size_t)255, strlen(l.urls[0]));
    memset(url + pre, 'x', 256 - pre);
    url[256] = '\0';
    ASSERT_EQ(FIRC_RESOLVER_LIST_URL, firc_resolver_list_parse(url, &l, NULL, 0));
    PASS();
}

TEST the_why_names_the_bad_url(void)
{
    firc_resolver_list_t l;
    char why[256];
    ASSERT_EQ(FIRC_RESOLVER_LIST_URL,
              firc_resolver_list_parse("1.1.1.1, https://a.example", &l, why, sizeof(why)));
    ASSERT(strncmp(why, "\"https://a.example\" ", 20) == 0);
    ASSERT(strstr(why, "https://host[:port]/path") != NULL);
    char tiny[8];
    ASSERT_EQ(FIRC_RESOLVER_LIST_URL, firc_resolver_list_parse("https://a.example", &l, tiny, sizeof(tiny)));
    ASSERT_STR_EQ("\"https:", tiny);
    PASS();
}

TEST the_pool_check_skips_a_url_but_not_a_literal_host_inside_the_pool(void)
{
    firc_fakeip_t *pool = pool_for_test();
    ASSERT(pool != NULL);
    char hit[FIRC_RESOLVER_ADDR_STRLEN];
    ASSERT_FALSE(firc_resolve_server_in_pool("https://dns.example/q, 1.1.1.1", pool, hit, sizeof(hit)));
    ASSERT_FALSE(firc_resolve_server_in_pool("https://198.20.0.1/q", pool, NULL, 0));
    ASSERT(firc_resolve_server_in_pool("https://dns.example/q,https://198.18.0.9:8443/q", pool, hit, sizeof(hit)));
    ASSERT_STR_EQ("198.18.0.9", hit);
    ASSERT(firc_resolve_server_in_pool("https://[fd00:1234::53]/q", pool, NULL, 0));
    ASSERT_FALSE(firc_resolve_server_in_pool("https://[fd00:1235::53]/q", pool, NULL, 0));
    firc_fakeip_free(pool);
    unlink(g_ula);
    PASS();
}

TEST a_doh_entry_formats_as_its_url(void)
{
    firc_resolver_list_t l;
    char buf[300];
    ASSERT_EQ(FIRC_RESOLVER_LIST_OK, firc_resolver_list_parse("https://a.example/d", &l, NULL, 0));
    ASSERT_EQ((size_t)0, firc_resolver_addr_format(&l.servers[0], buf, sizeof(buf)));
    ASSERT_STR_EQ("", buf);
    PASS();
}

/* Catches: firc_group_new not defaulting to the tunnel. */
TEST a_new_group_resolves_through_its_tunnel(void)
{
    firc_group_t *g = firc_group_new();
    ASSERT(g != NULL);
    ASSERT(g->resolve.tunnel);
    ASSERT_EQ(NULL, g->resolve.server);
    firc_group_free(g);
    PASS();
}

GREATEST_MAIN_DEFS();

int main(int argc, char **argv)
{
    GREATEST_MAIN_BEGIN();
    RUN_TEST(each_shape_parses);
    RUN_TEST(what_is_not_an_address_is_refused);
    RUN_TEST(sinks_are_refused_and_their_neighbours_are_not);
    RUN_TEST(the_sink_test_takes_a_bare_address);
    RUN_TEST(an_address_formats_back_to_what_parses);
    RUN_TEST(an_address_becomes_a_sockaddr);
    RUN_TEST(a_list_of_servers_splits_in_order);
    RUN_TEST(a_bad_list_is_refused_naming_what_is_wrong);
    RUN_TEST(a_server_inside_the_pool_is_refused_at_startup);
    RUN_TEST(a_doh_url_is_an_entry);
    RUN_TEST(literals_and_urls_mix_up_to_four);
    RUN_TEST(a_fifth_entry_is_refused_whatever_its_kind);
    RUN_TEST(a_url_that_is_not_https_or_not_well_formed_is_refused);
    RUN_TEST(a_urls_literal_is_held_to_a_plain_entrys_rules);
    RUN_TEST(the_log_shows_a_url_without_its_path);
    RUN_TEST(http_is_refused);
    RUN_TEST(a_url_twin_is_refused);
    RUN_TEST(a_url_without_a_host_is_refused);
    RUN_TEST(a_256_byte_url_is_refused);
    RUN_TEST(the_why_names_the_bad_url);
    RUN_TEST(the_pool_check_skips_a_url_but_not_a_literal_host_inside_the_pool);
    RUN_TEST(a_doh_entry_formats_as_its_url);
    RUN_TEST(a_new_group_resolves_through_its_tunnel);
    GREATEST_MAIN_END();
}
