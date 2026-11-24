#ifndef FIRC_DNSREWRITE_H
#define FIRC_DNSREWRITE_H

#include <stdint.h>

#include "firc/dnswire.h"
#include "firc/fakeip_addr.h"

/* Replaces the answer's A/AAAA/CNAME with one A per fake4 and one AAAA per fake6, in order, on the question's
 * name and drops HTTPS/SVCB everywhere. The message is untouched on error. */
firc_err_t firc_dns_msg_collapse(firc_dns_msg_t *msg, const firc_ip_t *fake4, size_t n4,
                                 const firc_ip_t *fake6, size_t n6, uint32_t ttl);

#endif /* FIRC_DNSREWRITE_H */
