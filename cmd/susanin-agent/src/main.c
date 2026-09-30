#include "config.h"
#include "conntrack.h"
#include "backend.h"
#include "discover.h"
#include "engine.h"
#include "log.h"
#include "ops.h"
#include "version.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

static const char *cfg_path(void)
{
    const char *p = getenv("SUSANIN_CONF");
    return p && *p ? p : "/opt/susanin/etc/susanin.conf";
}

static void usage(void)
{
    printf(
        "susanin-agent %s\n"
        "Usage:\n"
        "  susanin-agent version\n"
        "  susanin-agent config show\n"
        "  susanin-agent discover\n"
        "  susanin-agent ct-scan\n"
        "  susanin-agent datapath {up|down|status|flush|add|del}\n"
        "  susanin-agent run\n"
        "  susanin-agent status\n"
        "  susanin-agent setup\n"
        "  susanin-agent apply [--dry-run]\n"
        "  susanin-agent install\n"
        "  susanin-agent uninstall\n"
        "  susanin-agent forget <ip>\n"
        "  susanin-agent diag [start|stop|sample|errors]\n",
        SUSANIN_VERSION);
}

static int print_flow(void *ud, const ct_flow *f)
{
    (void)ud;
    printf("%-4s %-12s %s:%u->%s:%u op=%lu rp=%lu ob=%lu rb=%lu mark=0x%lx%s%s\n",
           f->proto, f->tcp_state, f->src, f->sport, f->dst, f->dport,
           f->op, f->rp, f->ob, f->rb, f->ctmark,
           f->has_reply ? "" : " NOREPLY", f->fastnat ? " FASTNAT" : "");
    return 0;
}

static int cmd_ct_scan(void)
{
    int n = conntrack_scan("/proc/net/nf_conntrack", print_flow, NULL);
    if (n < 0) {
        fprintf(stderr, "cannot read /proc/net/nf_conntrack\n");
        return 1;
    }
    printf("%d flows\n", n);
    return 0;
}

static int cmd_datapath(int argc, char **argv)
{
    const char *script = "/opt/susanin/tools/datapath.sh";
    char *newargv[16];
    int n = 0, i;

    if (argc < 3) {
        fprintf(stderr, "usage: susanin-agent datapath {up|down|status|flush|add <ip> <tcp|udp> <test|ok>|del <ip> <tcp|udp>}\n");
        return 2;
    }
    newargv[n++] = (char *)script;
    for (i = 2; i < argc && n < 15; i++)
        newargv[n++] = argv[i];
    newargv[n] = NULL;
    execv(script, newargv);
    fprintf(stderr, "cannot exec %s\n", script);
    return 1;
}

int main(int argc, char **argv)
{
    susanin_config cfg;
    susanin_discovery disc;
    const char *cmd = argc > 1 ? argv[1] : "help";

    if (!strcmp(cmd, "version")) {
        printf("%s\n", SUSANIN_VERSION);
        return 0;
    }
    if (!strcmp(cmd, "help") || !strcmp(cmd, "--help") || !strcmp(cmd, "-h")) {
        usage();
        return 0;
    }

    if (config_load(cfg_path(), &cfg) != 0) {
        fprintf(stderr, "warning: cannot read config %s; using defaults\n", cfg_path());
        config_set_defaults(&cfg);
    }

    if (!strcmp(cmd, "config")) {
        if (argc > 2 && !strcmp(argv[2], "show")) {
            config_print(&cfg);
            return 0;
        }
        if (argc > 2 && !strcmp(argv[2], "set")) {
            fprintf(stderr, "config set: implemented in Phase 5\n");
            return 2;
        }
        usage();
        return 2;
    }

    if (!strcmp(cmd, "discover")) {
        discover_defaults(&disc, &cfg);
        discover_print(&disc);
        return 0;
    }

    if (!strcmp(cmd, "ct-scan"))
        return cmd_ct_scan();

    if (!strcmp(cmd, "datapath"))
        return cmd_datapath(argc, argv);

    if (!strcmp(cmd, "run")) {
        slog_init(cfg.log_level);
        return engine_run(&cfg, cfg_path());
    }

    if (!strcmp(cmd, "setup"))
        return ops_setup(&cfg, cfg_path(), argc, argv);

    if (!strcmp(cmd, "status"))
        return ops_status(&cfg, cfg_path());

    if (!strcmp(cmd, "apply")) {
        int dry = argc > 2 && !strcmp(argv[2], "--dry-run");
        return ops_apply(&cfg, cfg_path(), dry);
    }

    if (!strcmp(cmd, "install"))
        return ops_setup(&cfg, cfg_path(), argc, argv);

    if (!strcmp(cmd, "forget"))
        return ops_forget(&cfg, argc > 2 ? argv[2] : NULL);

    if (!strcmp(cmd, "uninstall")) {
        printf("removing Susanin data plane ...\n");
        backend_teardown(&cfg);
        return 0;
    }

    if (!strcmp(cmd, "diag")) {
        printf("diag: not implemented yet (Phase 7)\n");
        return 2;
    }

    usage();
    return 2;
}
