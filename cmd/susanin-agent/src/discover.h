#ifndef SUSANIN_DISCOVER_H
#define SUSANIN_DISCOVER_H

#include "config.h"

typedef struct {
    char egress_interface[256];
    char egress_address[64];
    char lan_interfaces[256];
    char lan_subnets[256];
    int routing_table;
} susanin_discovery;

int discover_defaults(susanin_discovery *d, const susanin_config *c);
void discover_print(const susanin_discovery *d);

#endif
