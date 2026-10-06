#define _GNU_SOURCE
#include "ops.h"
#include "backend.h"
#include "log.h"
#include "state.h"
#include "version.h"

#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

#define CHAIN "SUSANIN"

const char *ops_default_conf_path(void)
{
    const char *p = getenv("SUSANIN_CONF");
    return p && *p ? p : "/opt/susanin/etc/susanin.conf";
}

static const char *tool(const char *name)
{
    static char buf[4][128];
    static int used = 0;
    const char *dirs[] = { "/opt/sbin", "/opt/bin", "/usr/sbin", "/usr/bin" };
    char p[96];
    unsigned i;
    for (i = 0; i < sizeof(dirs) / sizeof(dirs[0]); i++) {
        snprintf(p, sizeof(p), "%s/%s", dirs[i], name);
        if (access(p, X_OK) == 0) {
            if (used >= 4) return name;
            snprintf(buf[used], sizeof(buf[used]), "%s", p);
            return buf[used++];
        }
    }
    return name;
}

/* Run exe with argv, capture stdout, return exit code (or -1). */
static int run_capture(const char *exe, char *const argv[], char *out, size_t outsz)
{
    int p[2];
    pid_t pid;
    int st;
    size_t n = 0;
    if (outsz > 0)
        out[0] = '\0';
    if (pipe(p) != 0)
        return -1;
    pid = fork();
    if (pid < 0) {
        close(p[0]); close(p[1]);
        return -1;
    }
    if (pid == 0) {
        int devnull = open("/dev/null", O_WRONLY);
        close(p[0]);
        dup2(p[1], 1);
        if (devnull >= 0) { dup2(devnull, 2); close(devnull); }
        close(p[1]);
        execv(exe, argv);
        _exit(127);
    }
    close(p[1]);
    {
        char tmp[4096];
        ssize_t r;
        while ((r = read(p[0], tmp, sizeof(tmp))) > 0) {
            size_t take = (size_t)r;
            if (n < outsz - 1) {
                size_t room = outsz - 1 - n;
                if (take > room)
                    take = room;
                memcpy(out + n, tmp, take);
                n += take;
            }
        }
    }
    close(p[0]);
    waitpid(pid, &st, 0);
    if (outsz > 0)
        out[n < outsz ? n : outsz - 1] = '\0';
    if (WIFEXITED(st))
        return WEXITSTATUS(st);
    return -1;
}

static int run_exit(const char *exe, char *const argv[])
{
    char out[16];
    return run_capture(exe, argv, out, sizeof(out));
}

static int cap_contains(const char *exe, char *const argv[], const char *needle)
{
    char out[8192];
    if (run_capture(exe, argv, out, sizeof(out)) != 0)
        return 0;
    return strstr(out, needle) != 0;
}

/* Count output lines that start with a digit (ipset members). Streamed so a
 * large set is not truncated by a fixed buffer (ok_max_entries can exceed a
 * 64 KiB capture). */
static int cap_count_digits(const char *exe, char *const argv[])
{
    int p[2];
    pid_t pid;
    int st, count = 0, at_bol = 1;
    if (pipe(p) != 0)
        return 0;
    pid = fork();
    if (pid < 0) {
        close(p[0]); close(p[1]);
        return 0;
    }
    if (pid == 0) {
        int devnull = open("/dev/null", O_WRONLY);
        close(p[0]);
        dup2(p[1], 1);
        if (devnull >= 0) { dup2(devnull, 2); close(devnull); }
        close(p[1]);
        execv(exe, argv);
        _exit(127);
    }
    close(p[1]);
    {
        char tmp[4096];
        ssize_t r;
        size_t i;
        while ((r = read(p[0], tmp, sizeof(tmp))) > 0) {
            for (i = 0; i < (size_t)r; i++) {
                char c = tmp[i];
                if (at_bol && c >= '0' && c <= '9')
                    count++;
                at_bol = (c == '\n');
            }
        }
    }
    close(p[0]);
    waitpid(pid, &st, 0);
    return count;
}

int ops_setup(const susanin_config *base, const char *conf_path, int argc, char **argv)
{
    susanin_config cfg = *base;
    int i;
    for (i = 2; i < argc; i++) {
        if (!strcmp(argv[i], "--egress") && i + 1 < argc)
            snprintf(cfg.egress_interface, sizeof(cfg.egress_interface), "%s", argv[++i]);
        else if (!strcmp(argv[i], "--lan") && i + 1 < argc)
            snprintf(cfg.lan_interfaces, sizeof(cfg.lan_interfaces), "%s", argv[++i]);
        else if (!strcmp(argv[i], "--table") && i + 1 < argc)
            cfg.routing_table = atoi(argv[++i]);
    }

    if (config_save(conf_path, &cfg) != 0) {
        fprintf(stderr, "cannot write config %s\n", conf_path);
        return 1;
    }
    printf("config written: %s\n", conf_path);
    config_print(&cfg);

    printf("provisioning data plane ...\n");
    if (backend_provision(&cfg) != 0) {
        fprintf(stderr, "datapath provisioning failed (see datapath.sh output)\n");
        return 1;
    }
    printf("data plane UP (table=%d dev=%s)\n", cfg.routing_table, cfg.egress_interface);
    printf("next: susanin-agent status ; SUSANIN_CONF=%s susanin-agent run\n", conf_path);
    return 0;
}

static void print_check(const char *name, int ok)
{
    printf("  %-28s %s\n", name, ok ? "OK" : "MISSING");
}

static int count_list_lines(const char *path)
{
    FILE *fp = fopen(path, "r");
    char line[320];
    int n = 0;
    if (!fp)
        return -1;
    while (fgets(line, sizeof(line), fp)) {
        char *p = line + strlen(line);
        while (p > line && (p[-1] == ' ' || p[-1] == '\t' || p[-1] == '\r' ||
                            p[-1] == '\n'))
            *--p = '\0';
        if (line[0] == '\0' || line[0] == '#')
            continue;
        n++;
    }
    fclose(fp);
    return n;
}

static int check_ip_rule(const char *ip_exe, unsigned long mark, int table)
{
    char *a[4];
    char out[8192];
    char mstr[32], tstr[32];
    char *save = NULL, *line;
    a[0] = (char *)ip_exe; a[1] = "rule"; a[2] = "show"; a[3] = NULL;
    if (run_capture(ip_exe, a, out, sizeof(out)) != 0)
        return 0;
    snprintf(mstr, sizeof(mstr), "0x%lx", mark);
    snprintf(tstr, sizeof(tstr), "lookup %d", table);
    line = strtok_r(out, "\r\n", &save);
    while (line) {
        if (strstr(line, mstr) && strstr(line, tstr))
            return 1;
        line = strtok_r(NULL, "\r\n", &save);
    }
    return 0;
}

int ops_status(const susanin_config *cfg, const char *conf_path)
{
    const char *ipt = tool("iptables");
    const char *ip = tool("ip");
    const char *ipset = tool("ipset");
    const char *state_path = "/opt/susanin/var/susanin.state";
    char *a[8];

    printf("susanin-agent %s\n", SUSANIN_VERSION);
    printf("config file: %s (%s)\n", conf_path,
           access(conf_path, R_OK) == 0 ? "present" : "absent");
    printf("egress=%s table=%d lan=%s\n", cfg->egress_interface,
           cfg->routing_table, cfg->lan_interfaces);

    printf("data plane:\n");
    a[0] = (char *)ipt; a[1] = "-t"; a[2] = "mangle"; a[3] = "-S"; a[4] = (char *)CHAIN; a[5] = NULL;
    print_check("chain SUSANIN", run_exit(ipt, a) == 0);

    a[0] = (char *)ipt; a[1] = "-t"; a[2] = "mangle"; a[3] = "-S"; a[4] = "PREROUTING"; a[5] = NULL;
    print_check("PREROUTING jump", cap_contains(ipt, a, "-j SUSANIN"));

    print_check("ip rule test->table", check_ip_rule(ip, cfg->mark_test, cfg->routing_table));
    print_check("ip rule ok->table", check_ip_rule(ip, cfg->mark_ok, cfg->routing_table));

    {
        char tbl[16];
        snprintf(tbl, sizeof(tbl), "%d", cfg->routing_table);
        a[0] = (char *)ip; a[1] = "route"; a[2] = "show"; a[3] = "table"; a[4] = tbl; a[5] = NULL;
        print_check("route default in table", cap_contains(ip, a, "default"));
    }

    printf("ipset sizes:\n");
    {
        static const char *names[6] = {
            "susanin_test_tcp", "susanin_test_udp",
            "susanin_ok_tcp", "susanin_ok_udp",
            "susanin_ok_net", "susanin_never"
        };
        int k;
        for (k = 0; k < 6; k++) {
            char *b[4];
            b[0] = (char *)ipset; b[1] = "list"; b[2] = (char *)names[k]; b[3] = NULL;
            printf("  %-18s = %d\n", names[k], cap_count_digits(ipset, b));
        }
    }

    printf("vpn_always (always-VPN list):\n");
    if (!cfg->vpn_always_file[0]) {
        printf("  disabled (vpn_always_file empty)\n");
    } else {
        int n = count_list_lines(cfg->vpn_always_file);
        printf("  file: %s (%s)\n", cfg->vpn_always_file,
               n < 0 ? "absent — disabled" : "present");
        if (n >= 0)
            printf("  domains: %d (refresh every %ds%s)\n", n,
                   cfg->vpn_always_interval,
                   cfg->vpn_always_dns[0] ? "" : ", resolver: auto");
    }

    printf("cache:\n");
    print_check("state file", access(state_path, R_OK) == 0);
    return 0;
}

int ops_forget(const susanin_config *cfg, const char *ip)
{
    const char *path = "/opt/susanin/var/susanin.state";
    susanin_state st;
    int udp, removed = 0;
    if (!ip || !ip[0]) {
        fprintf(stderr, "usage: susanin-agent forget <ip>\n");
        return 2;
    }
    state_init(&st);
    state_load(path, &st);
    for (udp = 0; udp < 2; udp++) {
        if (state_remove(st_test(&st, udp), ip))
            removed++;
        if (state_remove(st_ok(&st, udp), ip))
            removed++;
        if (state_remove(st_cool(&st, udp), ip))
            removed++;
        backend_ipset_del(cfg, udp, 0, ip);
        backend_ipset_del(cfg, udp, 1, ip);
    }
    state_save(path, &st);
    state_free(&st);
    printf("[susanin] forget %s: %d cached entries removed, ipsets cleared\n",
           ip, removed);
    return 0;
}

static int check_missing(const susanin_config *cfg, char *a[])
{
    const char *ipt = tool("iptables");
    const char *ip = tool("ip");
    char needle[160];
    char tbl[16];
    int missing = 0;

    a[0] = (char *)ipt; a[1] = "-t"; a[2] = "mangle"; a[3] = "-S"; a[4] = (char *)CHAIN; a[5] = NULL;
    if (run_exit(ipt, a) != 0) { missing++; printf("CREATE chain %s\n", CHAIN); }
    else printf("KEEP chain %s\n", CHAIN);

    a[0] = (char *)ipt; a[1] = "-t"; a[2] = "mangle"; a[3] = "-S"; a[4] = "PREROUTING"; a[5] = NULL;
    if (!cap_contains(ipt, a, "-j SUSANIN")) { missing++; printf("CREATE PREROUTING jump -> %s\n", CHAIN); }
    else printf("KEEP PREROUTING jump -> %s\n", CHAIN);

    a[0] = (char *)ip; a[1] = "rule"; a[2] = "show"; a[3] = NULL;
    snprintf(needle, sizeof(needle), "fwmark 0x%lx lookup %d", cfg->mark_test, cfg->routing_table);
    if (!cap_contains(ip, a, needle)) { missing++; printf("CREATE ip rule test (fwmark 0x%lx)\n", cfg->mark_test); }
    else printf("KEEP ip rule test\n");
    snprintf(needle, sizeof(needle), "fwmark 0x%lx lookup %d", cfg->mark_ok, cfg->routing_table);
    if (!cap_contains(ip, a, needle)) { missing++; printf("CREATE ip rule ok (fwmark 0x%lx)\n", cfg->mark_ok); }
    else printf("KEEP ip rule ok\n");

    snprintf(tbl, sizeof(tbl), "%d", cfg->routing_table);
    a[0] = (char *)ip; a[1] = "route"; a[2] = "show"; a[3] = "table"; a[4] = tbl; a[5] = NULL;
    if (!cap_contains(ip, a, "default")) { missing++; printf("CREATE default route in table %d\n", cfg->routing_table); }
    else printf("KEEP default route table %d\n", cfg->routing_table);

    return missing;
}

int ops_apply(const susanin_config *cfg, const char *conf_path, int dry_run)
{
    char *a[8];
    int missing;

    printf("=== susanin apply %s ===\n", dry_run ? "--dry-run" : "");
    if (access(conf_path, R_OK) != 0) {
        printf("config %s is MISSING; run: susanin-agent setup [--egress ... --lan ... --table N]\n",
               conf_path);
        return dry_run ? 1 : 2;
    }

    missing = check_missing(cfg, a);

    if (dry_run) {
        printf("Result: %s\n", missing ? "OUT OF SYNC" : "IN SYNC structurally");
        return missing ? 1 : 0;
    }

    if (missing) {
        printf("provisioning missing objects ...\n");
        backend_provision(cfg);
    }
    printf("Result: reconciled\n");
    return 0;
}
