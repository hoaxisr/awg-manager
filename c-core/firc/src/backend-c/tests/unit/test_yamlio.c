#include "greatest.h"
#include "test_ids.h"

#include <stdio.h>
#include <stdlib.h>
#include <inttypes.h>
#include <string.h>

#include "firc/yamlio.h"
#include <unistd.h>
#include "firc/events.h"
#include "firc/log.h"

static firc_err_t load_str(firc_config_t *cfg, const char *doc)
{
    return firc_config_load_buffer(cfg, doc, strlen(doc));
}

#define LIST_GROUP_DOC                                                                             \
    "configVersion: 0.7.0\n"                                                                       \
    "groups:\n"                                                                                    \
    "  - id: 5eba1111\n"                                                                           \
    "    name: ads\n"                                                                              \
    "    interface: nwg0\n"                                                                        \
    "    enable: true\n"                                                                           \
    "    list:\n"                                                                                  \
    "      url: 'https://example.invalid/l.txt'\n"                                                 \
    "      interval: 86400\n"

#define WEIRD_COMMENT "Meta: \xc2\xab" "CDN\xc2\xbb #1 \\ \"x\""

/* Loads a YAML document into `cfg` and copies what was logged at INFO and above into `out`. */
static firc_err_t load_str_logged(firc_config_t *cfg, const char *doc, char *out, size_t cap)
{
    int fds[2];
    if (pipe(fds) != 0) { return FIRC_ERR_IO; }
    firc_log_set_output_floor(FIRC_LOG_INFO);
    firc_log_set_fd(fds[1]);
    firc_err_t err = load_str(cfg, doc);
    firc_log_set_fd(STDOUT_FILENO);
    close(fds[1]);
    ssize_t n = read(fds[0], out, cap - 1);
    close(fds[0]);
    if (n < 0) { n = 0; }
    out[n] = '\0';
    return err;
}

TEST a_group_resolver_may_be_a_doh_url(void)
{
    const char *good =
        "configVersion: 0.7.0\n"
        "groups:\n"
        "  - id: 0a1b2c3d\n"
        "    name: nl\n"
        "    interface: nwg0\n"
        "    enable: true\n"
        "    resolve: {server: '1.1.1.1, https://dns.comss.one/dns-query'}\n"
        "    rules: []\n";
    firc_config_t cfg;
    ASSERT_EQ(FIRC_OK, firc_config_init_defaults(&cfg));
    ASSERT_EQ(FIRC_OK, load_str(&cfg, good));
    ASSERT_STR_EQ("1.1.1.1, https://dns.comss.one/dns-query", cfg.groups[0]->resolve.server);
    firc_config_clear(&cfg);

    const char *bad = "    resolve: {server: 'http://dns.comss.one/dns-query'}\n";
    char doc[512];
    snprintf(doc, sizeof(doc),
             "configVersion: 0.7.0\ngroups:\n  - id: 0a1b2c3d\n    name: nl\n    interface: nwg0\n"
             "    enable: true\n%s    rules: []\n",
             bad);
    ASSERT_EQ(FIRC_OK, firc_config_init_defaults(&cfg));
    char log[2048];
    ASSERT_FALSE(load_str_logged(&cfg, doc, log, sizeof(log)) == FIRC_OK);
    ASSERT(strstr(log, "resolve.server \"http://dns.comss.one/dns-query\"") != NULL);
    firc_config_clear(&cfg);
    PASS();
}

static char *save_groups(const firc_config_t *cfg)
{
    char *out = NULL;
    size_t len = 0;
    if (firc_config_save_buffer_part(cfg, "0.0.0", FIRC_CFG_GROUPS, &out, &len) != FIRC_OK) { return NULL; }
    return out;
}

TEST a_dns_group_loads_and_round_trips(void)
{
    static const char doc[] = "groups:\n  - id: 0000af01\n    name: D\n    interface: dns\n    resolve:\n"
                              "      server: 9.9.9.9\n      fallback: false\n"
                              "  - id: 0000af02\n    name: E\n    interface: dns\n    resolve:\n      server: 1.1.1.1\n";
    firc_config_t cfg;
    ASSERT_EQ(FIRC_OK, firc_config_init_defaults(&cfg));
    ASSERT_EQ(FIRC_OK, load_str(&cfg, doc));
    ASSERT(firc_group_is_dns(cfg.groups[0]));
    ASSERT_FALSE(firc_group_is_block(cfg.groups[0]));
    ASSERT_FALSE(cfg.groups[0]->resolve.fallback);
    ASSERT(cfg.groups[1]->resolve.fallback);
    char *out = save_groups(&cfg);
    ASSERT(out != NULL);
    const char *first = strstr(out, "fallback: false\n");
    ASSERT(first != NULL);
    ASSERT_EQ(NULL, strstr(first + 1, "fallback"));
    ASSERT(strstr(out, "interface: dns\n") != NULL);
    ASSERT_EQ(NULL, strstr(out, "tunnel"));
    firc_config_t back;
    ASSERT_EQ(FIRC_OK, firc_config_init_defaults(&back));
    ASSERT_EQ(FIRC_OK, load_str(&back, out));
    ASSERT_FALSE(back.groups[0]->resolve.fallback);
    ASSERT(back.groups[1]->resolve.fallback);
    free(out);
    firc_config_clear(&back);
    firc_config_clear(&cfg);
    PASS();
}

TEST a_routed_groups_fallback_is_never_written(void)
{
    static const char doc[] = "groups:\n  - id: 0000af03\n    name: R\n    interface: nwg0\n    resolve:\n"
                              "      server: 9.9.9.9\n";
    firc_config_t cfg;
    ASSERT_EQ(FIRC_OK, firc_config_init_defaults(&cfg));
    ASSERT_EQ(FIRC_OK, load_str(&cfg, doc));
    ASSERT(cfg.groups[0]->resolve.fallback);
    char *out = save_groups(&cfg);
    ASSERT(out != NULL);
    ASSERT_EQ(NULL, strstr(out, "fallback"));
    free(out);
    firc_config_clear(&cfg);
    PASS();
}

TEST dns_cannot_be_one_of_several(void)
{
    firc_config_t cfg;
    ASSERT_EQ(FIRC_OK, firc_config_init_defaults(&cfg));
    ASSERT_EQ(FIRC_ERR_INVAL, load_str(&cfg, "groups:\n  - id: 0000af04\n    interface: [dns, nwg0]\n"));
    firc_config_clear(&cfg);
    PASS();
}

TEST a_dns_group_without_a_server_fails_the_load(void)
{
    firc_config_t cfg;
    ASSERT_EQ(FIRC_OK, firc_config_init_defaults(&cfg));
    ASSERT_EQ(FIRC_ERR_INVAL, load_str(&cfg, "groups:\n  - id: 0000af05\n    name: D\n    interface: dns\n"));
    firc_config_clear(&cfg);
    PASS();
}

TEST a_dns_group_with_a_subnet_rule_fails_the_load(void)
{
    static const char *const types[] = {"subnet", "subnet6"};
    static const char *const texts[] = {"10.0.0.0/8", "2001:db8::/32"};
    for (size_t i = 0; i < 2; i++) {
        char doc[400];
        snprintf(doc, sizeof(doc),
                 "groups:\n  - id: 0000af06\n    name: D\n    interface: dns\n    resolve:\n      server: 9.9.9.9\n"
                 "    rules:\n      - rule: %s\n        type: %s\n        enable: true\n",
                 texts[i], types[i]);
        firc_config_t cfg;
        ASSERT_EQ(FIRC_OK, firc_config_init_defaults(&cfg));
        if (load_str(&cfg, doc) != FIRC_ERR_INVAL) { FAILm(types[i]); }
        firc_config_clear(&cfg);
    }
    PASS();
}

TEST a_dns_group_with_name_rules_loads(void)
{
    static const char doc[] = "groups:\n  - id: 0000af07\n    name: D\n    interface: dns\n    resolve:\n"
                              "      server: 9.9.9.9\n    rules:\n      - rule: example.com\n        type: namespace\n"
                              "        enable: true\n";
    firc_config_t cfg;
    ASSERT_EQ(FIRC_OK, firc_config_init_defaults(&cfg));
    ASSERT_EQ(FIRC_OK, load_str(&cfg, doc));
    ASSERT_EQ(1u, (unsigned)cfg.groups[0]->n_rules);
    firc_config_clear(&cfg);
    PASS();
}

TEST fallback_off_on_a_routed_group_fails_the_load(void)
{
    firc_config_t cfg;
    ASSERT_EQ(FIRC_OK, firc_config_init_defaults(&cfg));
    ASSERT_EQ(FIRC_ERR_INVAL, load_str(&cfg, "groups:\n  - id: 0000af08\n    name: R\n    interface: nwg0\n"
                                             "    resolve:\n      fallback: false\n"));
    firc_config_clear(&cfg);
    PASS();
}

TEST cloudflare_on_a_dns_group_fails_the_load(void)
{
    firc_config_t cfg;
    ASSERT_EQ(FIRC_OK, firc_config_init_defaults(&cfg));
    ASSERT_EQ(FIRC_ERR_INVAL,
              load_str(&cfg, "groups:\n  - id: 0000af09\n    name: D\n    interface: dns\n    cloudflare: true\n"
                             "    resolve:\n      server: 9.9.9.9\n"));
    firc_config_clear(&cfg);
    PASS();
}

GREATEST_MAIN_DEFS();

int main(int argc, char **argv)
{
    GREATEST_MAIN_BEGIN();
    RUN_TEST(a_dns_group_loads_and_round_trips);
    RUN_TEST(a_routed_groups_fallback_is_never_written);
    RUN_TEST(dns_cannot_be_one_of_several);
    RUN_TEST(a_dns_group_without_a_server_fails_the_load);
    RUN_TEST(a_dns_group_with_a_subnet_rule_fails_the_load);
    RUN_TEST(a_dns_group_with_name_rules_loads);
    RUN_TEST(fallback_off_on_a_routed_group_fails_the_load);
    RUN_TEST(cloudflare_on_a_dns_group_fails_the_load);
    RUN_TEST(a_group_resolver_may_be_a_doh_url);
    GREATEST_MAIN_END();
}
