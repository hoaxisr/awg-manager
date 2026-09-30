#ifndef SUSANIN_CONFIG_H
#define SUSANIN_CONFIG_H

#define CFG_MAX_LAN 8
#define CFG_PATH_MAX 256
#define CFG_MAX_EGRESS 4

typedef struct {
    char egress_interface[CFG_PATH_MAX];
    char egress_address[64];
    /* Parsed egress lists (comma-separated config, aligned by index).
     * egress_list[i] — interface, egress_addr[i] — its tunnel address. */
    char egress_list[CFG_MAX_EGRESS][64];
    char egress_addr[CFG_MAX_EGRESS][64];
    int n_egress;
    char lan_interfaces[CFG_PATH_MAX];
    char lan_subnets[CFG_PATH_MAX];
    int routing_table;
    unsigned long mark_test;
    unsigned long mark_ok;
    unsigned long mark_mask;
    unsigned long policy_mark;
    int ip_rule_priority_start;
    int fast_interval;
    int soft_interval;
    int judge_interval;
    int health_interval;
    int fast_syn_min_op;
    int ok_ttl;
    int ok_refresh_below;
    int ok_evict_misses;    /* сколько подряд «сбоев» до снятия из ok (гистерезис) */
    int promo_per_min;      /* лимит новых «проб» (перевод в VPN) в минуту; 0=без лимита */
    int ok_max_entries;     /* bounded GC: per-proto ok-cache limit (0=off) */
    int test_ttl;
    int cooldown_ttl;
    int cooldown_ok_ttl;
    int watch_ttl;
    int watch_retry_below;
    int health_miss_debounce;
    int late_stall_bytes;   /* порог байт для быстрого STALL-443 (default 16384) */
    char health_probe[CFG_PATH_MAX];
    char health_probe_src[64];
    char vpn_always_file[CFG_PATH_MAX];
    char vpn_always_dns[CFG_PATH_MAX];
    int vpn_always_interval;
    char vpn_never_file[CFG_PATH_MAX];
    int vpn_never_interval;
    char log_level[16];
    int diagnostics;
    char disk_mode[8];      /* normal | soft: soft = минимум записей на диск */
    int soft_state_interval; /* soft: как часто сохранять состояние (сек; 0=никогда) */
    char learn_exclude_ports[CFG_PATH_MAX]; /* порты, которые не учим (скан-шум) */
} susanin_config;

void config_set_defaults(susanin_config *c);
int config_load(const char *path, susanin_config *c);
int config_save(const char *path, const susanin_config *c);
void config_print(const susanin_config *c);

#endif
