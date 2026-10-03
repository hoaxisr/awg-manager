#define _GNU_SOURCE
#include "conntrack.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static int startswith(const char *s, const char *pre)
{
    return strncmp(s, pre, strlen(pre)) == 0;
}

int conntrack_parse_line(const char *line, ct_flow *f)
{
    char buf[2048];
    char *save = NULL;
    char *tok;
    int idx = 0;
    int seen_src = 0, seen_dst = 0, seen_sport = 0, seen_dport = 0;
    int seen_op = 0, seen_rp = 0;

    if (!line || !f)
        return -1;
    memset(f, 0, sizeof(*f));
    snprintf(buf, sizeof(buf), "%s", line);

    tok = strtok_r(buf, " \t", &save);
    while (tok) {
        if (startswith(tok, "src=")) {
            if (!seen_src++) snprintf(f->src, sizeof(f->src), "%s", tok + 4);
        } else if (startswith(tok, "dst=")) {
            if (!seen_dst++) snprintf(f->dst, sizeof(f->dst), "%s", tok + 4);
        } else if (startswith(tok, "sport=")) {
            if (!seen_sport++) f->sport = (unsigned)strtoul(tok + 6, NULL, 10);
            else f->r_sport = (unsigned)strtoul(tok + 6, NULL, 10);
        } else if (startswith(tok, "dport=")) {
            if (!seen_dport++) f->dport = (unsigned)strtoul(tok + 6, NULL, 10);
            else f->r_dport = (unsigned)strtoul(tok + 6, NULL, 10);
        } else if (startswith(tok, "packets=")) {
            if (!seen_op++) f->op = strtoul(tok + 8, NULL, 10);
            else f->rp = strtoul(tok + 8, NULL, 10);
        } else if (startswith(tok, "bytes=")) {
            if (!seen_rp++) f->ob = strtoul(tok + 6, NULL, 10);
            else f->rb = strtoul(tok + 6, NULL, 10);
        } else if (startswith(tok, "mark=")) {
            f->ctmark = strtoul(tok + 5, NULL, 0);
        } else if (!strcmp(tok, "tcp") || !strcmp(tok, "udp") || !strcmp(tok, "icmp")) {
            snprintf(f->proto, sizeof(f->proto), "%s", tok);
            f->l4proto = !strcmp(tok, "tcp") ? 6 : (!strcmp(tok, "udp") ? 17 : 1);
        } else if (idx == 5 && f->l4proto == 6) {
            snprintf(f->tcp_state, sizeof(f->tcp_state), "%s", tok);
        } else if (strstr(tok, "FASTNAT")) {
            f->fastnat = 1;
        }
        idx++;
        tok = strtok_r(NULL, " \t", &save);
    }

    f->has_reply = (f->rp > 0);
    if (f->proto[0] == '\0' || f->dst[0] == '\0' || f->src[0] == '\0')
        return -1;
    return 0;
}

int conntrack_scan(const char *path, ct_cb cb, void *ud)
{
    FILE *fp;
    char line[4096];
    int n = 0;

    fp = fopen(path ? path : "/proc/net/nf_conntrack", "r");
    if (!fp)
        return -1;
    while (fgets(line, sizeof(line), fp)) {
        ct_flow f;
        if (conntrack_parse_line(line, &f) == 0) {
            n++;
            if (cb && cb(ud, &f) != 0)
                break;
        }
    }
    fclose(fp);
    return n;
}
