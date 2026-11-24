#include "greatest.h"

#include "firc/rulesnap.h"
#include <arpa/inet.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include "firc/log.h"

static firc_config_t make_cfg(void)
{
    firc_config_t cfg;
    firc_config_init_defaults(&cfg);
    return cfg;
}

static firc_group_t *make_group(firc_id_t id, const char *name, bool enable)
{
    firc_group_t *g = firc_group_new();
    g->id = id;
    firc_strset(&g->name, name);
    g->enable = enable;
    return g;
}

TEST the_snapshot_marks_a_dns_group(void)
{
    firc_config_t cfg = make_cfg();
    firc_group_t *d = make_group((firc_id_t){{1, 0, 0, 0}}, "d", true);
    firc_strset(&d->iface, FIRC_IFACE_DNS);
    firc_group_t *b = make_group((firc_id_t){{2, 0, 0, 0}}, "b", true);
    firc_strset(&b->iface, FIRC_IFACE_BLOCK);
    firc_group_t *r = make_group((firc_id_t){{3, 0, 0, 0}}, "r", true);
    firc_strset(&r->iface, "nwg0");
    firc_config_add_group(&cfg, d);
    firc_config_add_group(&cfg, b);
    firc_config_add_group(&cfg, r);
    firc_ruleset_snapshot_t *snap = firc_ruleset_snapshot_build(&cfg);
    ASSERT(snap != NULL);
    ASSERT_EQ(3u, (unsigned)snap->n_groups);
    ASSERT(snap->groups[0]->dns_only);
    ASSERT_FALSE(snap->groups[1]->dns_only);
    ASSERT_FALSE(snap->groups[2]->dns_only);
    ASSERT_EQ(-1, (int)snap->groups[0]->block);
    firc_ruleset_snapshot_free(snap);
    firc_config_clear(&cfg);
    PASS();
}

GREATEST_MAIN_DEFS();

int main(int argc, char **argv)
{
    GREATEST_MAIN_BEGIN();
    RUN_TEST(the_snapshot_marks_a_dns_group);
    GREATEST_MAIN_END();
}
