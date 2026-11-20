#include "firc/port_remap.h"

#include <arpa/inet.h>
#include <errno.h>
#include <ifaddrs.h>
#include <netinet/in.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>

#include "firc/log.h"

struct firc_port_remap {
    char *chain_name;
    firc_remap_addr_t *addrs;
    size_t n_addrs;
    uint16_t from, to;
    firc_ipt_t *ipt4, *ipt6;
    bool enabled;
};

firc_port_remap_t *firc_port_remap_new(const char *chain_prefix, uint16_t from, uint16_t to,
                                   const firc_remap_addr_t *addrs, size_t n_addrs, firc_ipt_t *ipt4,
                                   firc_ipt_t *ipt6) {
    if (!chain_prefix) { return NULL; }
    firc_port_remap_t *p = calloc(1, sizeof(*p));
    if (!p) { return NULL; }

    static const char suffix[] = "DNSOR";
    size_t prefix_len = strlen(chain_prefix);
    if (prefix_len > SIZE_MAX - sizeof(suffix)) {
        free(p);
        return NULL;
    }
    p->chain_name = malloc(prefix_len + sizeof(suffix));
    if (!p->chain_name) {
        free(p);
        return NULL;
    }
    memcpy(p->chain_name, chain_prefix, prefix_len);
    memcpy(p->chain_name + prefix_len, suffix, sizeof(suffix));

    if (n_addrs > 0) {
        p->addrs = calloc(n_addrs, sizeof(*p->addrs));
        if (!p->addrs) {
            free(p->chain_name);
            free(p);
            return NULL;
        }
        memcpy(p->addrs, addrs, n_addrs * sizeof(*p->addrs));
    }
    p->n_addrs = n_addrs;
    p->from = from;
    p->to = to;
    p->ipt4 = ipt4;
    p->ipt6 = ipt6;
    return p;
}

static bool same_addr(const firc_remap_addr_t *a, const firc_remap_addr_t *b) {
    return a->family == b->family && a->iplen == b->iplen && memcmp(a->ip, b->ip, a->iplen) == 0;
}

bool firc_port_remap_same_addrs(const firc_port_remap_t *p, const firc_remap_addr_t *addrs, size_t n) {
    if (n != p->n_addrs) { return false; }
    for (size_t i = 0; i < n; i++) {
        if (!same_addr(&addrs[i], &p->addrs[i])) { return false; }
    }
    return true;
}

firc_err_t firc_port_remap_set_addrs(firc_port_remap_t *p, const firc_remap_addr_t *addrs, size_t n) {
    firc_remap_addr_t *copy = NULL;
    if (n > 0) {
        copy = calloc(n, sizeof(*copy));
        if (copy == NULL) { return FIRC_ERR_NOMEM; }
        memcpy(copy, addrs, n * sizeof(*copy));
    }
    free(p->addrs);
    p->addrs = copy;
    p->n_addrs = n;
    return FIRC_OK;
}

static bool remap_addr_of(const struct ifaddrs *a, firc_remap_addr_t *out) {
    memset(out, 0, sizeof(*out));
    if (a->ifa_addr == NULL) { return false; }
    if (a->ifa_addr->sa_family == AF_INET) {
        struct sockaddr_in sin;
        memcpy(&sin, a->ifa_addr, sizeof(sin));
        out->family = AF_INET;
        out->iplen = 4;
        memcpy(out->ip, &sin.sin_addr, 4);
        return true;
    }
    if (a->ifa_addr->sa_family == AF_INET6) {
        struct sockaddr_in6 sin6;
        memcpy(&sin6, a->ifa_addr, sizeof(sin6));
        out->family = AF_INET6;
        out->iplen = 16;
        memcpy(out->ip, &sin6.sin6_addr, 16);
        return true;
    }
    return false;
}

firc_err_t firc_port_remap_collect(char *const *names, size_t n_names, bool warn, firc_remap_addr_t **out,
                                   size_t *out_n) {
    *out = NULL;
    *out_n = 0;
    if (n_names == 0) { return FIRC_OK; }

    struct ifaddrs *ifap = NULL;
    if (getifaddrs(&ifap) != 0) { return firc_err_from_errno(errno); }

    size_t cap = 0;
    size_t n = 0;
    firc_remap_addr_t *arr = NULL;
    for (size_t i = 0; i < n_names; i++) {
        bool found = false;
        for (const struct ifaddrs *a = ifap; a != NULL; a = a->ifa_next) {
            firc_remap_addr_t addr;
            if (strcmp(a->ifa_name, names[i]) != 0 || !remap_addr_of(a, &addr)) { continue; }
            found = true;
            if (n == cap) {
                size_t ncap = cap ? cap * 2 : 8;
                firc_remap_addr_t *grown = realloc(arr, ncap * sizeof(*grown));
                if (grown == NULL) {
                    free(arr);
                    freeifaddrs(ifap);
                    return FIRC_ERR_NOMEM;
                }
                arr = grown;
                cap = ncap;
            }
            arr[n++] = addr;
        }
        if (!found && warn) { FIRC_WARN("link %s has no address: DNS sent to it is not redirected", names[i]); }
    }
    freeifaddrs(ifap);
    *out = arr;
    *out_n = n;
    return FIRC_OK;
}

void firc_port_remap_free(firc_port_remap_t *p) {
    if (!p) { return; }
    free(p->chain_name);
    free(p->addrs);
    free(p);
}

static firc_err_t build_rules(firc_port_remap_t *p, firc_ipt_t *ipt) {
    if (!ipt) { return FIRC_OK; }
    firc_ipt_proto_t proto = firc_ipt_proto(ipt);

    firc_err_t err = firc_ipt_register_chain_override(ipt, "nat", p->chain_name);
    if (err != FIRC_OK) { return err; }

    char from_str[8];
    char to_dest[16];
    snprintf(from_str, sizeof(from_str), "%u", p->from);
    snprintf(to_dest, sizeof(to_dest), ":%u", p->to);

    for (size_t i = 0; i < p->n_addrs; i++) {
        bool matches = (proto == FIRC_IPT_PROTO_IPV4 && p->addrs[i].iplen == 4) ||
                       (proto == FIRC_IPT_PROTO_IPV6 && p->addrs[i].iplen == 16);
        if (!matches) { continue; }

        char ip_str[INET6_ADDRSTRLEN];
        inet_ntop(p->addrs[i].iplen == 4 ? AF_INET : AF_INET6, p->addrs[i].ip, ip_str,
                  sizeof(ip_str));

        const char *tcp_args[] = {"-p", "tcp", "-d", ip_str, "--dport", from_str,
                                  "-j", "DNAT", "--to-destination", to_dest};
        err = firc_ipt_append(ipt, "nat", p->chain_name, tcp_args, 10);
        if (err != FIRC_OK) { return err; }

        const char *udp_args[] = {"-p", "udp", "-d", ip_str, "--dport", from_str,
                                  "-j", "DNAT", "--to-destination", to_dest};
        err = firc_ipt_append(ipt, "nat", p->chain_name, udp_args, 10);
        if (err != FIRC_OK) { return err; }
    }

    err = firc_ipt_register_chain_patch(ipt, "nat", "PREROUTING");
    if (err != FIRC_OK) { return err; }
    const char *pre_args[] = {"-j", p->chain_name};
    err = firc_ipt_insert(ipt, "nat", "PREROUTING", 1, pre_args, 2);
    if (err != FIRC_OK) { return err; }

    return FIRC_OK;
}

static firc_err_t insert_rules(firc_port_remap_t *p, firc_ipt_t *ipt) {
    if (!ipt) { return FIRC_OK; }

    firc_err_t err = build_rules(p, ipt);
    if (err != FIRC_OK) { return err; }
    return firc_ipt_commit(ipt);
}

firc_err_t firc_port_remap_prepare_iptables(firc_port_remap_t *p) {
    if (!p || !p->enabled) { return FIRC_OK; }

    firc_err_t e4 = build_rules(p, p->ipt4);
    firc_err_t e6 = build_rules(p, p->ipt6);
    return e4 != FIRC_OK ? e4 : e6;
}

static firc_err_t delete_rules(firc_port_remap_t *p, firc_ipt_t *ipt) {
    (void)p;
    if (!ipt) { return FIRC_OK; }
    firc_err_t err = firc_ipt_register_chain_patch(ipt, "nat", "PREROUTING");
    if (err == FIRC_OK) { err = firc_ipt_register_chain_patch(ipt, "nat", "POSTROUTING"); }
    if (err != FIRC_OK) {
        firc_ipt_discard(ipt);
        return err;
    }
    return firc_ipt_commit(ipt);
}

firc_err_t firc_port_remap_enable(firc_port_remap_t *p) {
    if (p->enabled) { return FIRC_OK; }

    firc_err_t err = insert_rules(p, p->ipt4);
    if (err == FIRC_OK) { err = insert_rules(p, p->ipt6); }
    if (err != FIRC_OK) {
        delete_rules(p, p->ipt4);
        delete_rules(p, p->ipt6);
        return err;
    }
    p->enabled = true;
    return FIRC_OK;
}

void firc_port_remap_arm(firc_port_remap_t *p) {
    if (p != NULL) { p->enabled = true; }
}

firc_err_t firc_port_remap_disable(firc_port_remap_t *p) {
    if (!p->enabled) { return FIRC_OK; }
    p->enabled = false;

    firc_err_t e1 = delete_rules(p, p->ipt4);
    firc_err_t e2 = delete_rules(p, p->ipt6);
    return e1 != FIRC_OK ? e1 : e2;
}
