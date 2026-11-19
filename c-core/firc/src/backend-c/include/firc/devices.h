#ifndef FIRC_DEVICES_H
#define FIRC_DEVICES_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/err.h"
#include "firc/fakeip_addr.h"

#define FIRC_DEVSEL_POLICY_PREFIX "policy:"
#define FIRC_DEVSEL_MAC_PREFIX "mac:"

typedef struct firc_mac {
    uint8_t b[6]; /* all zero: no MAC known */
} firc_mac_t;

/* Exactly 17 chars, one separator (':' or '-') throughout, either case; refuses the all-zero address. */
bool firc_mac_parse(const char *text, firc_mac_t *out);

/* Writes lower-case ':'-separated hex; out must hold at least 18 bytes. */
void firc_mac_format(const firc_mac_t *mac, char out[18]);

/* Most addresses one device is matched by; addresses past this are not consulted. */
#define FIRC_DEVSEL_MAX_DEVICE_ADDRS 64

typedef enum {
    FIRC_DEVSEL_ADDR = 0,   /* an address or a prefix */
    FIRC_DEVSEL_POLICY = 1, /* policy:<name> */
    FIRC_DEVSEL_MAC = 2,    /* mac:<MAC> */
} firc_devsel_kind_t;

/* One selector entry; policy points into the text it was parsed from (after "policy:"). */
typedef struct firc_devsel_entry {
    firc_devsel_kind_t kind;
    firc_ip_t addr;     /* ADDR; a v4-mapped entry is normalized to v4 */
    uint8_t prefix;     /* ADDR */
    firc_mac_t mac;     /* MAC */
    const char *policy; /* POLICY */
} firc_devsel_entry_t;

/* FIRC_ERR_INVAL for an entry a selector refuses (firc_devsel_entry_why says why); FIRC_ERR_NOMEM. */
firc_err_t firc_devsel_entry_parse(const char *text, firc_devsel_entry_t *out);

/* Whether net/prefix covers addr. False across families. */
bool firc_devsel_prefix_covers(const firc_ip_t *net, uint8_t prefix, const firc_ip_t *addr);

/* NULL if entry is accepted; otherwise why not, as a phrase following the quoted entry in a message. */
const char *firc_devsel_entry_why(const char *entry);

/* Rewrites a valid mac: entry in place to lower-case ':' form; anything else is left untouched. */
void firc_devsel_entry_canon(char *entry);

typedef struct firc_devsel firc_devsel_t;

/* FIRC_ERR_INVAL for an entry that is not an address, a prefix, policy:<name> or mac:<MAC>; FIRC_ERR_NOMEM. */
firc_err_t firc_devsel_compile(const char *const *allow, size_t n_allow, const char *const *deny,
                               size_t n_deny, firc_devsel_t **out);
void firc_devsel_free(firc_devsel_t *s);

/* True if client is in Keenetic policy `policy`; NULL when the build cannot ask (matches nothing). */
typedef bool (*firc_devsel_policy_fn)(const char *policy, const firc_ip_t *client, void *ud);

/* Fills out with up to cap other addresses of the device at client, and *mac if known; NULL: no lookup. */
typedef size_t (*firc_devsel_device_fn)(const firc_ip_t *client, firc_ip_t *out, size_t cap, firc_mac_t *mac,
                                        void *ud);

/* Packet-path lookups against the firmware's host table; deny says which list the entry is in.
 * allow renders only addresses the answer is sure of; deny renders every maybe it might deny --
 * swapped, either direction routes the wrong device. */
typedef bool (*firc_devsel_mark_fn)(const char *policy, uint32_t *mark, void *ud);
typedef void (*firc_devsel_addr_fn)(const firc_ip_t *addr, void *cb_ud);
typedef void (*firc_devsel_net_fn)(const firc_ip_t *net, uint8_t prefix, void *cb_ud);
typedef firc_err_t (*firc_devsel_hosts_fn)(const firc_ip_t *net, uint8_t prefix, bool deny, firc_devsel_addr_fn cb,
                                           void *cb_ud, void *ud);
typedef firc_err_t (*firc_devsel_policy_hosts_fn)(const char *policy, bool deny, firc_devsel_addr_fn cb, void *cb_ud,
                                                  void *ud);
typedef firc_err_t (*firc_devsel_policy_nets_fn)(const char *policy, bool deny, firc_devsel_net_fn net_cb,
                                                 void *cb_ud, void *ud);

/* ud is passed to both policy and device callbacks. */
bool firc_devsel_allows(const firc_devsel_t *s, const firc_ip_t *client, firc_devsel_policy_fn policy,
                        firc_devsel_device_fn device, void *ud);

/* Both lists empty: the selector is "every device". */
bool firc_devsel_is_empty(const firc_devsel_t *s);

/* True if the selector has a policy or mac: entry (needs the hotspot table to match). */
bool firc_devsel_names_a_policy_or_mac(const firc_devsel_t *s);

#endif /* FIRC_DEVICES_H */
