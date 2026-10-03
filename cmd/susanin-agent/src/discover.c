#include "discover.h"

#include <stdio.h>
#include <string.h>

int discover_defaults(susanin_discovery *d, const susanin_config *c)
{
    if (!d || !c)
        return -1;
    memset(d, 0, sizeof(*d));
    snprintf(d->egress_interface, sizeof(d->egress_interface), "%s",
             c->egress_interface[0] ? c->egress_interface : "nwg0");
    snprintf(d->egress_address, sizeof(d->egress_address), "%s",
             c->egress_address[0] ? c->egress_address : "10.8.1.1");
    snprintf(d->lan_interfaces, sizeof(d->lan_interfaces), "%s",
             c->lan_interfaces[0] ? c->lan_interfaces : "br0");
    snprintf(d->lan_subnets, sizeof(d->lan_subnets), "%s",
             c->lan_subnets[0] ? c->lan_subnets : "192.168.1.0/24");
    d->routing_table = c->routing_table ? c->routing_table : 100;
    return 0;
}

void discover_print(const susanin_discovery *d)
{
    if (!d)
        return;
    printf("egress_interface=%s\n", d->egress_interface);
    printf("egress_address=%s\n", d->egress_address);
    printf("lan_interfaces=%s\n", d->lan_interfaces);
    printf("lan_subnets=%s\n", d->lan_subnets);
    printf("routing_table=%d\n", d->routing_table);
}
