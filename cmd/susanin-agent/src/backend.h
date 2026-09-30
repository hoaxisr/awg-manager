#ifndef SUSANIN_BACKEND_H
#define SUSANIN_BACKEND_H

#include "config.h"
#include "conntrack.h"
#include <stddef.h>
#include <time.h>

int backend_provision(const susanin_config *c);
int backend_teardown(const susanin_config *c);
int backend_ready(const susanin_config *c);

/* Environment check for the data plane (tools + egress interface). Returns 0
 * or -1 with a reason in err. */
int backend_preflight(const susanin_config *c, char *err, size_t errsz);

/* Failover: switch the VPN table default route to another egress interface. */
int backend_set_egress(const susanin_config *c, const char *iface);
/* Drop conntrack entries carrying the VPN mark (after an egress switch). */
int backend_ct_flush_vpn(const susanin_config *c);
/* Drop conntrack entries to a single IP (after vpn_never pin changes). */
int backend_ct_flush_ip(const char *ip);
int backend_ipset_add(const susanin_config *c, int proto_udp, int phase_ok,
                      const char *ip, int ttl);
int backend_ipset_del(const susanin_config *c, int proto_udp, int phase_ok,
                      const char *ip);
int backend_ipset_flush(const susanin_config *c);

/* Forced CIDR ranges (vpn_always): hash:net susanin_ok_net, timeout ttl. */
int backend_net_add(const susanin_config *c, const char *cidr, int ttl);
int backend_net_del(const susanin_config *c, const char *cidr);

/* Generic named ipset entry ops (e.g. susanin_never). */
int backend_set_add(const susanin_config *c, const char *set, const char *val,
                    int ttl);
int backend_set_del(const susanin_config *c, const char *set, const char *val);
int backend_ct_delete(const ct_flow *f);

#endif
