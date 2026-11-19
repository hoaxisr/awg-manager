#ifndef FIRC_DNSREWRITE_H
#define FIRC_DNSREWRITE_H

#include <stdint.h>

#include "firc/dnswire.h"
#include "firc/fakeip_addr.h"

/* Replaces the answer's A/AAAA/CNAME with A(fake4)/AAAA(fake6) on the question's
 * name and drops HTTPS/SVCB everywhere. The message is untouched on error. */
firc_err_t firc_dns_msg_collapse(firc_dns_msg_t *msg, const firc_ip_t *fake4,
                                 const firc_ip_t *fake6, uint32_t ttl);

#endif /* FIRC_DNSREWRITE_H */
