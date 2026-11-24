#ifndef AWGM_XHTTPD_H
#define AWGM_XHTTPD_H

#ifdef _WIN32
#include "win32_compat.h"
#else
#define _GNU_SOURCE
#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>
#include <pthread.h>
#include <time.h>
#include <sys/types.h>
#include <sys/socket.h>
#include <netinet/in.h>
#endif

#define XHTTPD_VERSION "1.0.0-c"

/* FiniteProto (FP01) wire constants */
#define FP_PROTO_MAGIC       "FP01"
#define FP_PROTO_VERSION     "fp/1"
#define FP_REQ_HEADER_SIZE   54
#define FP_RESP_HEADER_SIZE  58

#define FP_OP_OPEN           1
#define FP_OP_UP             2
#define FP_OP_DOWN           3
#define FP_OP_CLOSE          4

#define FP_REQ_FLAG_EOF      (1 << 0)

#define FP_STATUS_OK              0
#define FP_STATUS_BAD_REQUEST     1
#define FP_STATUS_UNAUTHORIZED    2
#define FP_STATUS_FORBIDDEN       3
#define FP_STATUS_UNKNOWN_SESSION 4
#define FP_STATUS_RESOURCE_LIMIT  5
#define FP_STATUS_BACKPRESSURE    6
#define FP_STATUS_DIAL_ERROR      7

#define FP_RESP_FLAG_TARGET_EOF     (1 << 0)
#define FP_RESP_FLAG_TARGET_RESET   (1 << 1)
#define FP_RESP_FLAG_SESSION_CLOSED (1 << 2)

#define FP_MAX_REQ_PAYLOAD   (256 * 1024)
#define FP_MAX_RESP_PAYLOAD  (512 * 1024)
#define FP_MAX_SESS_BUF      (2 * 1024 * 1024)
#define FP_MAX_GLOBAL_BUF    (16 * 1024 * 1024)
#define FP_MAX_SESSIONS      512
#define FP_POLL_WAIT_MS      100
/* Worker pool (F870). Measured 24.11 on x86_64 and -m32 (glibc), daemon VmRSS with N idle
 * stream-one connections (each relayed one TLS ClientHello to an echo target, then idle):
 *   user memory ~60 KiB per idle stream (both ABIs); before the shared config snapshot it
 *   was ~180 KiB (115 KiB of it the per-request config copy). Of the 60 KiB about 39 KiB
 *   are touched pages of two thread stacks (worker + stream-one feed thread) and ~13 KiB
 *   heap (32 KiB TCP relay buffer, touched as far as data went).
 *   Kernel per stream, not in VmRSS: two kernel stacks (2 x 8 KiB on MIPS32, 2 x 16 KiB on
 *   x86_64) plus socket/pipe slab.
 *   N=512: VmRSS 34 MiB (x86_64) / 33 MiB (-m32); add ~8 MiB kernel stacks on MIPS32.
 *   64 streams pumping data: VmRSS ~10 MiB.
 * The per-IP cap in vless_xhttp.c (half the pool) keeps one client from taking it all. */
#define MAX_HTTP_WORKERS     512
/* Thread stack reserves (address space; RSS is only what a thread touches). Deepest worker
 * chain by gcc -fstack-usage (x86_64 -O2) is ~45 KiB (client_conn_thread 26.6 +
 * run_vless_stream 13 + dial/DNS ~3) plus getaddrinfo; helper threads (stream-one feed,
 * FP target reader, mux downlink) stay under ~20 KiB (pump_remaining_http_chunks 16.2 KiB). */
#define XHTTPD_WORKER_STACK  (256 * 1024)
#define XHTTPD_HELPER_STACK  (64 * 1024)

#define DEFAULT_STAGE2_ID    "stage2probe"

/* VLESS protocol constants */
#define VLESS_CMD_TCP        0x01
#define VLESS_CMD_UDP        0x02
#define VLESS_CMD_MUX        0x03

#define VLESS_ADDR_IPV4      0x01
#define VLESS_ADDR_DOMAIN    0x02
#define VLESS_ADDR_IPV6      0x03

/* Egress modes */
#define EGRESS_MODE_ROUTER    0
#define EGRESS_MODE_DIRECT    1
#define EGRESS_MODE_SOCKS     2
#define EGRESS_MODE_INTERFACE 3

#define FIRC_ROUTING_MARK     0xf1ec

#define MAX_CLIENTS           64
#define MAX_PREFIXES          16
#define MAX_FP_CREDS          (MAX_CLIENTS * 8 + 16)

typedef struct {
    uint8_t uuid[16];
    char    uuid_str[40];
    char    hex_no_dash[33];
} vless_client_t;

typedef struct {
    char    client_id[64];
    uint8_t secret[32];
    size_t  secret_len;
} fp_cred_t;

typedef struct {
    char           listen_host[64];
    uint16_t       listen_port;
    char           prefixes[MAX_PREFIXES][128];
    int            num_prefixes;
    vless_client_t clients[MAX_CLIENTS];
    int            num_clients;
    fp_cred_t      cfg_fp_creds[MAX_FP_CREDS];
    int            num_cfg_fp_creds;
    fp_cred_t      fp_creds[MAX_FP_CREDS];
    int            num_fp_creds;
    int            egress_mode; /* EGRESS_MODE_* */
    char           interface_name[32];
    char           socks_host[64];
    uint16_t       socks_port;
    char           dns_proxy_host[64];
    uint16_t       dns_proxy_port;
    bool           allow_private;
    bool           ipv4_only;   /* domain_strategy=ipv4_only: no AAAA fallback */
    int            idle_timeout_sec;
    int            max_sessions_per_ip;
    int            max_pre_auth_sessions;
    int            max_pre_auth_per_ip;
    size_t         max_pre_auth_bytes_per_ip;
    size_t         max_pre_auth_bytes_global;
    uint64_t       pre_auth_ttl_ms;
    char           trusted_proxies[16][64];
    int            num_trusted_proxies;
} xhttpd_config_t;

/* Live configuration snapshot (main.c): acquire a reference, use it read-only, release. */
const xhttpd_config_t *xhttpd_cfg_acquire(void);
void xhttpd_cfg_release(const xhttpd_config_t *cfg);
int  xhttpd_test_publish_config(const xhttpd_config_t *cfg);
int  xhttpd_test_live_cfg_snapshots(void);
extern bool g_diag_enabled;

/* Utility & logging */
uint64_t now_ms(void);
uint64_t epoch_ms(void);
void diag_log(const char *fmt, ...);

/* Crypto (crypto.c) */
void sha256_hash(const uint8_t *data, size_t len, uint8_t out[32]);
void hmac_sha256(const uint8_t *key, size_t key_len, const uint8_t *data, size_t data_len, uint8_t out[32]);
bool ct_memeq(const void *a, const void *b, size_t n);
int  hex_to_bytes(const char *hex, uint8_t *out, size_t max_out);
void bytes_to_hex(const uint8_t *in, size_t len, char *out);
int  base64url_encode(const uint8_t *in, size_t in_len, char *out, size_t max_out);
int  base64url_decode(const char *in, size_t in_len, uint8_t *out, size_t max_out);
bool parse_uuid_str(const char *str, uint8_t out[16]);
void format_uuid_canonical(const uint8_t u[16], char out[37]);
bool is_uuid_derived_fp_secret(const uint8_t secret[32], const uint8_t u[16]);
int  add_explicit_fp_cred(xhttpd_config_t *cfg, const char *client_id, const uint8_t secret[32]);
int  csprng_bytes(uint8_t *buf, size_t len);
int  rebuild_fp_credentials(xhttpd_config_t *cfg);

/* Sniffing, DNS & Egress Dialer (sniff_and_dial.c) */
bool is_firc_fake_ip_str(const char *host);
bool is_private_lan_ip_str(const char *host);
bool check_destination_acl(const char *host, bool allow_private);
bool check_destination_acl_ex(const char *host, bool allow_private, bool allow_fakeip);
int  sync_fakeip_dnat(const char *fake_ip);
void dnat_bump_generation(void);
bool should_defer_dial_for_sniff(const char *host, uint16_t port, int egress_mode);
int  sniff_tls_sni(const uint8_t *buf, size_t len, char *out_host, size_t out_sz);
int  sniff_http_host(const uint8_t *buf, size_t len, char *out_host, size_t out_sz);
int  resolve_host_ip(const char *host, int egress_mode, const char *dns_proxy_host, uint16_t dns_proxy_port, bool allow_private, bool ipv4_only, char *out_ip, size_t out_sz);
int  dial_target_tcp(const char *orig_host, uint16_t port, const uint8_t *first_chunk, size_t chunk_len, const xhttpd_config_t *cfg, int timeout_ms);

/* UDP egress handle. Direct: datagrams go to dest as is. SOCKS (socks_ctrl_fd >= 0):
 * dest is the UDP ASSOCIATE relay, every datagram carries socks_hdr (RFC 1928
 * RSV/FRAG/ATYP/DST for the target), and the TCP control connection must stay open for
 * the association lifetime: release it with udp_egress_close_ctrl() next to close(fd). */
typedef struct {
    struct sockaddr_storage dest;
    socklen_t               dest_len;
    int                     socks_ctrl_fd;
    uint8_t                 socks_hdr[22];
    uint8_t                 socks_hdr_len;
} udp_egress_t;

int     dial_target_udp(const char *host, uint16_t port, const xhttpd_config_t *cfg, udp_egress_t *out);
ssize_t udp_egress_send(int fd, const udp_egress_t *e, const uint8_t *data, size_t len);
ssize_t udp_egress_recv(int fd, const udp_egress_t *e, uint8_t *buf, size_t cap, int flags);
void    udp_egress_close_ctrl(udp_egress_t *e);

/* Buffered HTTP/1.1 reader for keep-alive connections */
typedef struct {
    int     fd;
    size_t  pos; /* header fields first: they share a page instead of bracketing buf */
    size_t  len;
    uint8_t buf[16384];
} conn_reader_t;

/* HTTP request representation */
typedef struct {
    char     method[16];
    char     path[512];
    char     query[512];
    char     fp_version[32];
    char     fp_auth[256];
    char     reload_token[65];
    char     session_id[128];
    char     xff[256];
    char     cf_ip[64];
    char     peer_ip[64];
    char     content_type[64];
    int64_t  content_length;
    bool     chunked;
    bool     chunked_incomplete;
    bool     keep_alive;
    uint8_t *body;
    size_t   body_len;
} http_req_t;

int read_http_request(conn_reader_t *cr, http_req_t *req);
int pump_remaining_http_chunks(conn_reader_t *cr, int out_pipe_fd);
void xhttpd_set_http_req_timeout_ms(uint64_t timeout_ms);
int  xhttpd_get_active_http_workers(void);
int  xhttpd_spawn_client_worker_for_test(int fd);
void xhttpd_test_set_reload_token(const char *token);
void xhttpd_test_set_config_path(const char *path);
int load_config_file(const char *path, xhttpd_config_t *out);
int write_all(int fd, const void *buf, size_t len);
int send_http_error(int fd, int status, const char *status_text, const char *msg, bool keep_alive);

/* FiniteProto engine (finiteproto.c) */
void   fp_engine_init(void);
void   fp_engine_reap_stale(void);
void   fp_engine_close_all(void);
void   fp_engine_revoke_unauthorised(const xhttpd_config_t *cfg);
int    fp_engine_get_active_sessions(void);
void   fp_test_set_pre_auth_hook(void (*pre_auth_hook)(void));
void   fp_test_set_dial_hooks(void (*pre_dial_hook)(void), void (*post_dial_hook)(void));
void   fp_test_set_write_hook(void (*write_hook)(void));
void   fp_test_set_initial_write_timeout_ms(int timeout_ms);
void   fp_test_set_target_sndbuf(int sndbuf_bytes);
int    fp_test_get_write_wouldblock_count(void);
size_t fp_engine_get_global_buf_bytes(void);
void   fp_test_set_alloc_fail_after(int count);
void   fp_test_set_max_global_buf(size_t max_bytes);
void   fp_test_set_reader_exit_hook(void (*hook)(void));
int    fp_test_find_session_slot(const uint8_t sid[16]);
int    fp_test_get_active_creds_count(void);
bool   fp_test_trylock_table(void);
bool   is_fp_http_request(const http_req_t *req);
int    handle_fp_http_request(int client_fd, const http_req_t *req, const xhttpd_config_t *cfg);

/* VLESS XHTTP engine (vless_xhttp.c) */
typedef struct {
    uint8_t  uuid[16];
    uint8_t  command;
    uint16_t port;
    char     host[256];
    size_t   header_len;
} vless_req_t;

int  parse_vless_header(const uint8_t *buf, size_t len, const xhttpd_config_t *cfg, vless_req_t *out);
void vless_engine_init(void);
void vless_engine_reap_stale(void);
void vless_engine_close_all(void);
void vless_engine_revoke_unauthorised(const xhttpd_config_t *cfg);
void vless_engine_get_stats(int *active_sessions, int *pre_auth_sessions, size_t *buffered_bytes, size_t *pre_auth_bytes);
int  handle_vless_http_request(int client_fd, conn_reader_t *cr, const http_req_t *req, const char *matched_prefix, const xhttpd_config_t *cfg);

#endif /* AWGM_XHTTPD_H */
