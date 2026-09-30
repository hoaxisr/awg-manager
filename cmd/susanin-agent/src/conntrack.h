#ifndef SUSANIN_CONNTRACK_H
#define SUSANIN_CONNTRACK_H

#include <stddef.h>

typedef struct {
    int l4proto;        /* 6=tcp, 17=udp, 1=icmp */
    char proto[8];      /* "tcp"/"udp"/"icmp" */
    char src[64];
    char dst[64];
    unsigned sport;
    unsigned dport;
    unsigned r_sport;
    unsigned r_dport;
    char tcp_state[16]; /* for tcp: SYN_SENT/ESTABLISHED/... */
    unsigned long op;   /* orig packets */
    unsigned long ob;   /* orig bytes */
    unsigned long rp;   /* reply packets */
    unsigned long rb;   /* reply bytes */
    unsigned long ctmark;
    int has_reply;      /* reply direction saw packets */
    int fastnat;        /* [FASTNAT]/offloaded */
} ct_flow;

typedef int (*ct_cb)(void *ud, const ct_flow *f);

int conntrack_parse_line(const char *line, ct_flow *f);
int conntrack_scan(const char *path, ct_cb cb, void *ud);

#endif
