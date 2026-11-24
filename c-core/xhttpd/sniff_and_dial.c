#include "xhttpd.h"
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <ctype.h>
#include <errno.h>
#ifndef _WIN32
#include <strings.h>
#include <unistd.h>
#include <fcntl.h>
#include <poll.h>
#include <sys/socket.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <signal.h>
#include <netinet/tcp.h>
#include <arpa/inet.h>
#include <netdb.h>
#endif

static bool parse_ipv4_octets(const char *host, uint8_t oct[4]) {
    if (!host) return false;
    struct in_addr addr;
    if (inet_pton(AF_INET, host, &addr) != 1) return false;
    uint32_t h = ntohl(addr.s_addr);
    oct[0] = (uint8_t)((h >> 24) & 0xFF);
    oct[1] = (uint8_t)((h >> 16) & 0xFF);
    oct[2] = (uint8_t)((h >> 8) & 0xFF);
    oct[3] = (uint8_t)(h & 0xFF);
    return true;
}

bool is_firc_fake_ip_str(const char *host) {
    uint8_t oct[4];
    if (!parse_ipv4_octets(host, oct)) return false;
    if (oct[0] == 198 && (oct[1] == 18 || oct[1] == 19)) return true;
    if (oct[0] == 10 && oct[1] == 255) return true;
    return false;
}

bool is_private_lan_ip_str(const char *host) {
    uint8_t oct[4];
    if (!parse_ipv4_octets(host, oct)) return false;
    if (is_firc_fake_ip_str(host)) return false;
    if (oct[0] == 10) return true;
    if (oct[0] == 100 && (oct[1] >= 64 && oct[1] <= 127)) return true;
    if (oct[0] == 172 && (oct[1] >= 16 && oct[1] <= 31)) return true;
    if (oct[0] == 192 && oct[1] == 168) return true;
    return false;
}

static bool is_special_use_ipv4_octets(const uint8_t oct[4], bool allow_fakeip) {
    if (allow_fakeip) {
        if ((oct[0] == 198 && (oct[1] == 18 || oct[1] == 19)) ||
            (oct[0] == 10 && oct[1] == 255)) {
            return false;
        }
    }
    if (oct[0] == 0 || oct[0] == 10 || oct[0] == 127 || oct[0] >= 224) return true;
    if (oct[0] == 100 && (oct[1] >= 64 && oct[1] <= 127)) return true; /* CGNAT 100.64.0.0/10 */
    if (oct[0] == 169 && oct[1] == 254) return true;                   /* Link-local 169.254.0.0/16 */
    if (oct[0] == 172 && (oct[1] >= 16 && oct[1] <= 31)) return true;  /* RFC1918 172.16.0.0/12 */
    if (oct[0] == 192 && oct[1] == 0 && (oct[2] == 0 || oct[2] == 2)) return true; /* 192.0.0.0/24, 192.0.2.0/24 */
    if (oct[0] == 192 && oct[1] == 168) return true;                   /* RFC1918 192.168.0.0/16 */
    if (oct[0] == 198 && (oct[1] == 18 || oct[1] == 19)) return true;  /* Benchmarking 198.18.0.0/15 */
    if (oct[0] == 198 && oct[1] == 51 && oct[2] == 100) return true;   /* TEST-NET-2 198.51.100.0/24 */
    if (oct[0] == 203 && oct[1] == 0 && oct[2] == 113) return true;    /* TEST-NET-3 203.0.113.0/24 */
    return false;
}

static bool has_case_suffix(const char *str, const char *sfx) {
    size_t slen = strlen(str);
    size_t xlen = strlen(sfx);
    return slen >= xlen && strcasecmp(str + slen - xlen, sfx) == 0;
}

bool check_destination_acl_ex(const char *host, bool allow_private, bool allow_fakeip) {
    if (!host || !host[0]) return false;
    if (allow_private) return true;

    char clean[256];
    size_t hlen = strlen(host);
    if (hlen >= sizeof(clean)) return false;
    if (host[0] == '[' && hlen > 2 && host[hlen - 1] == ']') {
        memcpy(clean, host + 1, hlen - 2);
        clean[hlen - 2] = '\0';
    } else {
        memcpy(clean, host, hlen + 1);
    }
    size_t clen = strlen(clean);
    while (clen > 0 && clean[clen - 1] == '.') {
        clean[--clen] = '\0';
    }
    if (clen == 0) return false;

    if (strcasecmp(clean, "localhost") == 0 ||
        has_case_suffix(clean, ".localhost") ||
        has_case_suffix(clean, ".local") ||
        has_case_suffix(clean, ".lan") ||
        has_case_suffix(clean, ".home.arpa")) {
        return false;
    }

    uint8_t oct[4];
    if (parse_ipv4_octets(clean, oct)) {
        return !is_special_use_ipv4_octets(oct, allow_fakeip);
    }

    struct in6_addr a6;
    if (inet_pton(AF_INET6, clean, &a6) == 1) {
        if (IN6_IS_ADDR_LOOPBACK(&a6) || IN6_IS_ADDR_LINKLOCAL(&a6) ||
            IN6_IS_ADDR_SITELOCAL(&a6) || IN6_IS_ADDR_UNSPECIFIED(&a6) ||
            IN6_IS_ADDR_MULTICAST(&a6) || (a6.s6_addr[0] & 0xfe) == 0xfc) {
            return false;
        }
        /* Documentation 2001:db8::/32 */
        if (a6.s6_addr[0] == 0x20 && a6.s6_addr[1] == 0x01 &&
            a6.s6_addr[2] == 0x0d && a6.s6_addr[3] == 0xb8) {
            return false;
        }
        /* IPv4-mapped ::ffff:0:0/96 or IPv4-compatible ::/96 */
        bool first10_zero = true;
        for (int i = 0; i < 10; i++) {
            if (a6.s6_addr[i] != 0) { first10_zero = false; break; }
        }
        if (first10_zero) {
            if ((a6.s6_addr[10] == 0xff && a6.s6_addr[11] == 0xff) ||
                (a6.s6_addr[10] == 0x00 && a6.s6_addr[11] == 0x00)) {
                uint8_t v4[4] = { a6.s6_addr[12], a6.s6_addr[13], a6.s6_addr[14], a6.s6_addr[15] };
                return !is_special_use_ipv4_octets(v4, false);
            }
        }
        /* Teredo 2001::/32: the real peer hides behind a relay, nothing to classify */
        if (a6.s6_addr[0] == 0x20 && a6.s6_addr[1] == 0x01 &&
            a6.s6_addr[2] == 0x00 && a6.s6_addr[3] == 0x00) {
            return false;
        }
        /* NAT64 well-known 64:ff9b::/96 leads to the embedded IPv4 (64:ff9b::7f00:1 is loopback).
         * Local-use 64:ff9b:1::/48 places the IPv4 by operator prefix length, so it is refused
         * rather than guessed (same split as Go internal/ipinspect). */
        if (a6.s6_addr[0] == 0x00 && a6.s6_addr[1] == 0x64 &&
            a6.s6_addr[2] == 0xff && a6.s6_addr[3] == 0x9b) {
            bool wk = true;
            for (int i = 4; i < 12; i++) {
                if (a6.s6_addr[i] != 0) { wk = false; break; }
            }
            if (wk) {
                uint8_t v4[4] = { a6.s6_addr[12], a6.s6_addr[13], a6.s6_addr[14], a6.s6_addr[15] };
                return !is_special_use_ipv4_octets(v4, false);
            }
            if (a6.s6_addr[4] == 0x00 && a6.s6_addr[5] == 0x01) return false;
        }
        /* 6to4 2002::/16 embedded IPv4 */
        if (a6.s6_addr[0] == 0x20 && a6.s6_addr[1] == 0x02) {
            uint8_t v4[4] = { a6.s6_addr[2], a6.s6_addr[3], a6.s6_addr[4], a6.s6_addr[5] };
            if (is_special_use_ipv4_octets(v4, false)) return false;
        }
        return true;
    }
    return true;
}

bool check_destination_acl(const char *host, bool allow_private) {
    return check_destination_acl_ex(host, allow_private, false);
}

bool should_defer_dial_for_sniff(const char *host, uint16_t port, int egress_mode) {
    if (!host || !host[0]) return false;
    uint8_t oct[4];
    bool is_v4 = parse_ipv4_octets(host, oct);
    struct in6_addr a6;
    bool is_v6 = !is_v4 && (inet_pton(AF_INET6, host, &a6) == 1);
    if (!is_v4 && !is_v6) return false;
    if (is_v4 && is_private_lan_ip_str(host)) return false;
    if (is_v4 && is_firc_fake_ip_str(host)) return true;
    if ((port == 443 || port == 80 || port == 8443 || port == 8080) &&
        (egress_mode == EGRESS_MODE_ROUTER || egress_mode == EGRESS_MODE_SOCKS || is_v6)) {
        return true;
    }
    return false;
}

static bool is_valid_sniffed_domain(const char *h) {
    if (!h) return false;
    size_t len = strlen(h);
    if (len == 0 || len > 253 || !strchr(h, '.')) return false;
    uint8_t oct[4];
    if (parse_ipv4_octets(h, oct)) return false;
    for (size_t i = 0; i < len; i++) {
        char c = h[i];
        if ((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
            (c >= '0' && c <= '9') || c == '-' || c == '.' || c == '_') {
            continue;
        }
        return false;
    }
    return true;
}

static int parse_client_hello_sni(const uint8_t *p, size_t plen, char *out_host, size_t out_sz);

#define TLS_REASSEMBLY_MAX 16384

int sniff_tls_sni(const uint8_t *b, size_t len, char *out_host, size_t out_sz) {
    if (!b || len < 5 || b[0] != 0x16 || b[1] != 0x03) return -1;
    uint16_t rec_len = ((uint16_t)b[3] << 8) | b[4];

    if (5 + (size_t)rec_len >= len) return parse_client_hello_sni(b + 5, len - 5, out_host, out_sz);
    if (b[5 + rec_len] != 0x16) return parse_client_hello_sni(b + 5, rec_len, out_host, out_sz);

    /* ClientHello split over several records (rare): reassemble on the heap, a 16 KiB stack
     * array would push every stream's stack deeper for the common single-record case */
    uint8_t *reassembled = (uint8_t *)malloc(TLS_REASSEMBLY_MAX);
    if (!reassembled) return -1;
    size_t plen = 0;
    size_t off = 0;
    while (off + 5 <= len && b[off] == 0x16 && b[off + 1] == 0x03) {
        uint16_t rlen = ((uint16_t)b[off + 3] << 8) | b[off + 4];
        size_t end = off + 5 + rlen;
        if (end > len) end = len;
        size_t chunk = end - (off + 5);
        if (plen + chunk > TLS_REASSEMBLY_MAX) chunk = TLS_REASSEMBLY_MAX - plen;
        memcpy(reassembled + plen, b + off + 5, chunk);
        plen += chunk;
        off = end;
        if (plen == TLS_REASSEMBLY_MAX) break;
    }
    int rc = parse_client_hello_sni(reassembled, plen, out_host, out_sz);
    free(reassembled);
    return rc;
}

static int parse_client_hello_sni(const uint8_t *p, size_t plen, char *out_host, size_t out_sz) {
    if (plen < 38 || p[0] != 0x01) return -1;
    p += 38;
    plen -= 38;

    /* SessionID */
    if (plen < 1) return -1;
    size_t sid_len = p[0];
    p++; plen--;
    if (plen < sid_len) return -1;
    p += sid_len; plen -= sid_len;

    /* CipherSuites */
    if (plen < 2) return -1;
    size_t cs_len = ((size_t)p[0] << 8) | p[1];
    p += 2; plen -= 2;
    if (plen < cs_len) return -1;
    p += cs_len; plen -= cs_len;

    /* CompressionMethods */
    if (plen < 1) return -1;
    size_t cm_len = p[0];
    p++; plen--;
    if (plen < cm_len) return -1;
    p += cm_len; plen -= cm_len;

    /* Extensions */
    if (plen < 2) return -1;
    size_t ext_total = ((size_t)p[0] << 8) | p[1];
    p += 2; plen -= 2;
    if (plen > ext_total) plen = ext_total;

    while (plen >= 4) {
        uint16_t ext_type = ((uint16_t)p[0] << 8) | p[1];
        uint16_t ext_len  = ((uint16_t)p[2] << 8) | p[3];
        p += 4; plen -= 4;
        if (plen < ext_len) return -1;
        const uint8_t *ed = p;
        size_t ed_len = ext_len;
        p += ext_len; plen -= ext_len;

        if (ext_type == 0x0000) {
            if (ed_len < 2) return -1;
            size_t list_len = ((size_t)ed[0] << 8) | ed[1];
            ed += 2; ed_len -= 2;
            if (ed_len > list_len) ed_len = list_len;
            while (ed_len >= 3) {
                uint8_t name_type = ed[0];
                uint16_t name_len = ((uint16_t)ed[1] << 8) | ed[2];
                ed += 3; ed_len -= 3;
                if (ed_len < name_len) return -1;
                if (name_type == 0x00 && name_len > 0 && name_len < out_sz) {
                    memcpy(out_host, ed, name_len);
                    out_host[name_len] = '\0';
                    while (name_len > 0 && out_host[name_len - 1] == '.') {
                        out_host[--name_len] = '\0';
                    }
                    for (size_t i = 0; i < name_len; i++) {
                        out_host[i] = (char)tolower((unsigned char)out_host[i]);
                    }
                    if (is_valid_sniffed_domain(out_host)) return 0;
                    return -1;
                }
                ed += name_len; ed_len -= name_len;
            }
            return -1;
        }
    }
    return -1;
}

int sniff_http_host(const uint8_t *b, size_t len, char *out_host, size_t out_sz) {
    if (!b || len < 10) return -1;
    size_t limit = len > 4096 ? 4096 : len;
    if (memcmp(b, "GET ", 4) != 0 && memcmp(b, "POST ", 5) != 0 &&
        memcmp(b, "HEAD ", 5) != 0 && memcmp(b, "PUT ", 4) != 0 &&
        memcmp(b, "DELETE ", 7) != 0 && memcmp(b, "OPTIONS ", 8) != 0 &&
        memcmp(b, "PATCH ", 6) != 0 && memcmp(b, "CONNECT ", 8) != 0) {
        return -1;
    }
    size_t pos = 0;
    while (pos < limit && b[pos] != '\n') pos++;
    if (pos < limit) pos++;

    while (pos < limit) {
        size_t line_start = pos;
        while (pos < limit && b[pos] != '\n') pos++;
        size_t line_end = pos;
        if (pos < limit) pos++;
        if (line_end > line_start && b[line_end - 1] == '\r') line_end--;
        size_t l_len = line_end - line_start;
        if (l_len == 0) break;
        if (l_len > 5 && strncasecmp((const char *)(b + line_start), "Host:", 5) == 0) {
            const char *v = (const char *)(b + line_start + 5);
            size_t vlen = l_len - 5;
            while (vlen > 0 && (*v == ' ' || *v == '\t')) { v++; vlen--; }
            while (vlen > 0 && (v[vlen - 1] == ' ' || v[vlen - 1] == '\t')) vlen--;
            /* Strip :port if present */
            for (size_t i = 0; i < vlen; i++) {
                if (v[i] == ':') {
                    vlen = i;
                    break;
                }
            }
            while (vlen > 0 && v[vlen - 1] == '.') vlen--;
            if (vlen == 0 || vlen >= out_sz) return -1;
            for (size_t i = 0; i < vlen; i++) {
                out_host[i] = (char)tolower((unsigned char)v[i]);
            }
            out_host[vlen] = '\0';
            if (is_valid_sniffed_domain(out_host)) return 0;
            return -1;
        }
    }
    return -1;
}

/* In-memory DNS & FEXT_DNAT caches */
typedef struct {
    char     key[280];
    char     ip[48];
    uint64_t expires_ms;
} dns_cache_entry_t;

#define DNS_CACHE_MAX 512
static dns_cache_entry_t g_dns_cache[DNS_CACHE_MAX];
static int g_dns_cache_count = 0;
static pthread_mutex_t g_dns_cache_mu = PTHREAD_MUTEX_INITIALIZER;

typedef struct {
    char     fake_ip[24];
    char     real_target[48];
    uint64_t synced_ms;
    uint64_t generation;
} dnat_cache_entry_t;

#define DNAT_CACHE_MAX 512
static dnat_cache_entry_t g_dnat_cache[DNAT_CACHE_MAX];
static int g_dnat_cache_count = 0;
static uint64_t g_dnat_generation = 1;
static pthread_mutex_t g_dnat_mu = PTHREAD_MUTEX_INITIALIZER;

void dnat_bump_generation(void) {
    pthread_mutex_lock(&g_dnat_mu);
    g_dnat_generation++;
    if (g_dnat_generation == 0) g_dnat_generation = 1;
    g_dnat_cache_count = 0;
    pthread_mutex_unlock(&g_dnat_mu);
}

static void evict_dnat_cache_locked(const char *fake_ip) {
    for (int i = 0; i < g_dnat_cache_count; i++) {
        if (strcmp(g_dnat_cache[i].fake_ip, fake_ip) == 0) {
            g_dnat_cache[i].synced_ms = 0;
            g_dnat_cache[i].generation = 0;
        }
    }
}

static int run_cmd_capture(const char *const argv[], int timeout_ms, char *out_buf, size_t out_sz) {
#ifdef _WIN32
    (void)argv; (void)timeout_ms; (void)out_buf; (void)out_sz;
    return -1;
#else
    if (out_buf && out_sz > 0) out_buf[0] = '\0';
    int pfd[2];
    if (pipe(pfd) < 0) return -1;

    pid_t pid = fork();
    if (pid < 0) {
        close(pfd[0]);
        close(pfd[1]);
        return -1;
    }
    if (pid == 0) {
        close(pfd[0]);
        dup2(pfd[1], STDOUT_FILENO);
        int devnull = open("/dev/null", O_WRONLY);
        if (devnull >= 0) {
            dup2(devnull, STDERR_FILENO);
            close(devnull);
        }
        close(pfd[1]);
        execvp(argv[0], (char *const *)argv);
        _exit(127);
    }

    close(pfd[1]);
    int flags = fcntl(pfd[0], F_GETFL, 0);
    if (flags >= 0) fcntl(pfd[0], F_SETFL, flags | O_NONBLOCK);

    uint64_t deadline = now_ms() + (uint64_t)(timeout_ms > 0 ? timeout_ms : 1500);
    size_t off = 0;
    bool eof = false;

    while (!eof) {
        uint64_t now = now_ms();
        if (now >= deadline) {
            kill(pid, SIGKILL);
            waitpid(pid, NULL, 0);
            close(pfd[0]);
            return -1;
        }
        struct pollfd pf = { .fd = pfd[0], .events = POLLIN };
        int pr = poll(&pf, 1, (int)(deadline - now));
        if (pr < 0) {
            if (errno == EINTR) continue;
            break;
        }
        if (pr == 0) {
            kill(pid, SIGKILL);
            waitpid(pid, NULL, 0);
            close(pfd[0]);
            return -1;
        }
        char tmp[1024];
        ssize_t r = read(pfd[0], tmp, sizeof(tmp));
        if (r > 0) {
            if (out_buf && out_sz > 1 && off + 1 < out_sz) {
                size_t copy = (size_t)r;
                if (off + copy >= out_sz) copy = out_sz - 1 - off;
                memcpy(out_buf + off, tmp, copy);
                off += copy;
                out_buf[off] = '\0';
            }
        } else if (r == 0) {
            eof = true;
        } else if (errno != EAGAIN && errno != EINTR) {
            break;
        }
    }
    close(pfd[0]);

    int status = 0;
    while (waitpid(pid, &status, WNOHANG) == 0) {
        if (now_ms() >= deadline) {
            kill(pid, SIGKILL);
            waitpid(pid, &status, 0);
            return -1;
        }
        usleep(5000);
    }
    if (!WIFEXITED(status) || WEXITSTATUS(status) != 0) {
        return -1;
    }
    return 0;
#endif
}

static bool extract_to_destination(const char *line, char *out_target, size_t out_sz) {
    const char *p = strstr(line, "--to-destination ");
    if (!p) return false;
    p += 17;
    while (*p == ' ' || *p == '\t') p++;
    size_t idx = 0;
    while (*p && *p != ' ' && *p != '\t' && *p != '\r' && *p != '\n') {
        if (idx + 1 >= out_sz) return false;
        char c = *p++;
        if (!((c >= '0' && c <= '9') || c == '.' || c == ':' || c == '-')) return false;
        out_target[idx++] = c;
    }
    if (idx == 0) return false;
    out_target[idx] = '\0';
    return true;
}

int sync_fakeip_dnat(const char *fake_ip) {
    if (!is_firc_fake_ip_str(fake_ip)) return -1;
    uint64_t now = now_ms();

    pthread_mutex_lock(&g_dnat_mu);
    uint64_t gen = g_dnat_generation;
    for (int i = 0; i < g_dnat_cache_count; i++) {
        if (strcmp(g_dnat_cache[i].fake_ip, fake_ip) == 0 &&
            g_dnat_cache[i].generation == gen &&
            g_dnat_cache[i].synced_ms > 0 &&
            (now - g_dnat_cache[i].synced_ms) < 25000) {
            pthread_mutex_unlock(&g_dnat_mu);
            return 0;
        }
    }

    const char *ipt = getenv("XHTTPD_IPTABLES_BIN");
    if (!ipt || !ipt[0]) ipt = "iptables";

    char *out_buf = (char *)malloc(65536);
    if (!out_buf) {
        evict_dnat_cache_locked(fake_ip);
        pthread_mutex_unlock(&g_dnat_mu);
        return -1;
    }

    const char *list_firc_argv[] = { ipt, "-w", "-t", "nat", "-S", "FIRC_DNAT", NULL };
    if (run_cmd_capture(list_firc_argv, 1500, out_buf, 65536) != 0) {
        evict_dnat_cache_locked(fake_ip);
        free(out_buf);
        pthread_mutex_unlock(&g_dnat_mu);
        return -1;
    }

    char needle[48];
    snprintf(needle, sizeof(needle), "-A FIRC_DNAT -d %s/32 ", fake_ip);
    char real_target[48] = {0};

    char *saveptr = NULL;
    for (char *line = strtok_r(out_buf, "\n", &saveptr); line; line = strtok_r(NULL, "\n", &saveptr)) {
        if (strncmp(line, needle, strlen(needle)) == 0 && strstr(line, "-p tcp")) {
            if (extract_to_destination(line, real_target, sizeof(real_target))) {
                break;
            }
        }
    }

    if (!real_target[0]) {
        evict_dnat_cache_locked(fake_ip);
        free(out_buf);
        pthread_mutex_unlock(&g_dnat_mu);
        return -1;
    }

    const char *list_fext_argv[] = { ipt, "-w", "-t", "nat", "-S", "FEXT_DNAT", NULL };
    if (run_cmd_capture(list_fext_argv, 1500, out_buf, 65536) != 0) {
        const char *new_fext_argv[] = { ipt, "-w", "-t", "nat", "-N", "FEXT_DNAT", NULL };
        run_cmd_capture(new_fext_argv, 1500, NULL, 0);
        if (run_cmd_capture(list_fext_argv, 1500, out_buf, 65536) != 0) {
            evict_dnat_cache_locked(fake_ip);
            free(out_buf);
            pthread_mutex_unlock(&g_dnat_mu);
            return -1;
        }
    }

    char fext_needle[48];
    snprintf(fext_needle, sizeof(fext_needle), "-A FEXT_DNAT -d %s/32 ", fake_ip);
    bool already_present = false;
    char stale_targets[16][48];
    int stale_count = 0;

    saveptr = NULL;
    for (char *line = strtok_r(out_buf, "\n", &saveptr); line; line = strtok_r(NULL, "\n", &saveptr)) {
        if (strncmp(line, fext_needle, strlen(fext_needle)) != 0) continue;
        char t[48] = {0};
        if (!extract_to_destination(line, t, sizeof(t))) continue;
        bool has_mark = (strstr(line, "0x40000000") != NULL);
        if (has_mark && strcmp(t, real_target) == 0 && !already_present) {
            already_present = true;
        } else if (stale_count < 16) {
            snprintf(stale_targets[stale_count++], sizeof(stale_targets[0]), "%s", t);
        }
    }

    char fake_cidr[32];
    snprintf(fake_cidr, sizeof(fake_cidr), "%s/32", fake_ip);

    if (!already_present) {
        const char *ins_argv[] = {
            ipt, "-w", "-t", "nat", "-I", "FEXT_DNAT", "1",
            "-d", fake_cidr, "-p", "tcp",
            "-m", "mark", "--mark", "0x40000000/0x40000000",
            "-j", "DNAT", "--to-destination", real_target, NULL
        };
        if (run_cmd_capture(ins_argv, 1500, NULL, 0) != 0) {
            evict_dnat_cache_locked(fake_ip);
            free(out_buf);
            pthread_mutex_unlock(&g_dnat_mu);
            return -1;
        }
    }

    for (int i = 0; i < stale_count; i++) {
        const char *del_argv[] = {
            ipt, "-w", "-t", "nat", "-D", "FEXT_DNAT",
            "-d", fake_cidr, "-p", "tcp",
            "-m", "mark", "--mark", "0x40000000/0x40000000",
            "-j", "DNAT", "--to-destination", stale_targets[i], NULL
        };
        if (run_cmd_capture(del_argv, 1500, NULL, 0) != 0) {
            evict_dnat_cache_locked(fake_ip);
            free(out_buf);
            pthread_mutex_unlock(&g_dnat_mu);
            return -1;
        }
    }

    free(out_buf);

    int slot = -1;
    for (int i = 0; i < g_dnat_cache_count; i++) {
        if (strcmp(g_dnat_cache[i].fake_ip, fake_ip) == 0) {
            slot = i;
            break;
        }
    }
    if (slot < 0) {
        if (g_dnat_cache_count < DNAT_CACHE_MAX) {
            slot = g_dnat_cache_count++;
        } else {
            slot = 0;
        }
    }
    snprintf(g_dnat_cache[slot].fake_ip, sizeof(g_dnat_cache[slot].fake_ip), "%s", fake_ip);
    snprintf(g_dnat_cache[slot].real_target, sizeof(g_dnat_cache[slot].real_target), "%s", real_target);
    g_dnat_cache[slot].synced_ms = now;
    g_dnat_cache[slot].generation = gen;
    pthread_mutex_unlock(&g_dnat_mu);
    return 0;
}

/* IPv4 or IPv6 literal + port -> sockaddr; returns its length, 0 if ip_str is not a literal. */
static socklen_t fill_sockaddr(const char *ip_str, uint16_t port, struct sockaddr_storage *ss) {
    memset(ss, 0, sizeof(*ss));
    struct sockaddr_in *s4 = (struct sockaddr_in *)ss;
    if (inet_pton(AF_INET, ip_str, &s4->sin_addr) == 1) {
        s4->sin_family = AF_INET;
        s4->sin_port = htons(port);
        return (socklen_t)sizeof(*s4);
    }
    struct sockaddr_in6 *s6 = (struct sockaddr_in6 *)ss;
    if (inet_pton(AF_INET6, ip_str, &s6->sin6_addr) == 1) {
        s6->sin6_family = AF_INET6;
        s6->sin6_port = htons(port);
        return (socklen_t)sizeof(*s6);
    }
    return 0;
}

#define DNS_QTYPE_A    1
#define DNS_QTYPE_AAAA 28

/* Raw UDP DNS query (A or AAAA) to dns_host:dns_port.
 * Returns 0 with out_ip set, 1 when the server answered NOERROR without a record of
 * qtype (NODATA: worth asking the other family), -1 on any other failure. */
static int udp_dns_query(const char *domain, uint16_t qtype, const char *dns_host, uint16_t dns_port, char *out_ip, size_t out_sz) {
    uint8_t pkt[512];
    memset(pkt, 0, sizeof(pkt));
    uint8_t rnd[2];
    if (csprng_bytes(rnd, 2) < 0) return -1;
    uint16_t txid = (uint16_t)(((uint16_t)rnd[0] << 8) | rnd[1]);
    pkt[0] = (uint8_t)(txid >> 8);
    pkt[1] = (uint8_t)(txid & 0xFF);
    pkt[2] = 0x01; /* RD = 1 */
    pkt[5] = 0x01; /* QDCOUNT = 1 */

    size_t off = 12;
    const char *p = domain;
    while (*p) {
        const char *dot = strchr(p, '.');
        size_t lab_len = dot ? (size_t)(dot - p) : strlen(p);
        if (lab_len == 0 || lab_len > 63 || off + 1 + lab_len + 5 >= sizeof(pkt)) return -1;
        pkt[off++] = (uint8_t)lab_len;
        for (size_t i = 0; i < lab_len; i++) {
            pkt[off++] = (uint8_t)tolower((unsigned char)p[i]);
        }
        if (!dot) break;
        p = dot + 1;
    }
    pkt[off++] = 0x00;
    pkt[off++] = (uint8_t)(qtype >> 8); pkt[off++] = (uint8_t)qtype; /* QTYPE */
    pkt[off++] = 0x00; pkt[off++] = 0x01; /* QCLASS = IN */

    struct sockaddr_storage sa;
    socklen_t sa_len = fill_sockaddr(dns_host, dns_port, &sa);
    if (sa_len == 0) return -1;
    int fd = socket(sa.ss_family, SOCK_DGRAM | SOCK_CLOEXEC, 0);
    if (fd < 0) return -1;

    if (sendto(fd, pkt, off, 0, (struct sockaddr *)&sa, sa_len) < 0) {
        close(fd);
        return -1;
    }

    struct pollfd pfd = { .fd = fd, .events = POLLIN };
    if (poll(&pfd, 1, 2500) <= 0) {
        close(fd);
        return -1;
    }

    uint8_t resp[512];
    ssize_t rlen = recv(fd, resp, sizeof(resp), 0);
    close(fd);
    if (rlen < 12) return -1;
    if (resp[0] != pkt[0] || resp[1] != pkt[1]) return -1;
    if ((resp[3] & 0x0F) != 0) return -1; /* RCODE != 0 */

    uint16_t qdcount = ((uint16_t)resp[4] << 8) | resp[5];
    uint16_t ancount = ((uint16_t)resp[6] << 8) | resp[7];
    if (ancount == 0) return 1;

    size_t roff = 12;
    /* Skip questions */
    for (uint16_t q = 0; q < qdcount; q++) {
        while (roff < (size_t)rlen) {
            uint8_t l = resp[roff];
            if (l == 0) { roff++; break; }
            if ((l & 0xC0) == 0xC0) { roff += 2; break; }
            roff += 1 + l;
        }
        roff += 4;
        if (roff > (size_t)rlen) return -1;
    }

    /* Parse answers */
    for (uint16_t a = 0; a < ancount && roff < (size_t)rlen; a++) {
        while (roff < (size_t)rlen) {
            uint8_t l = resp[roff];
            if (l == 0) { roff++; break; }
            if ((l & 0xC0) == 0xC0) { roff += 2; break; }
            roff += 1 + l;
        }
        if (roff + 10 > (size_t)rlen) return -1;
        uint16_t rtype  = ((uint16_t)resp[roff] << 8) | resp[roff + 1];
        uint16_t rdlen  = ((uint16_t)resp[roff + 8] << 8) | resp[roff + 9];
        roff += 10;
        if (roff + rdlen > (size_t)rlen) return -1;
        if (qtype == DNS_QTYPE_A && rtype == DNS_QTYPE_A && rdlen == 4) {
            snprintf(out_ip, out_sz, "%u.%u.%u.%u",
                     resp[roff], resp[roff + 1], resp[roff + 2], resp[roff + 3]);
            return 0;
        }
        if (qtype == DNS_QTYPE_AAAA && rtype == DNS_QTYPE_AAAA && rdlen == 16) {
            return inet_ntop(AF_INET6, resp + roff, out_ip, (socklen_t)out_sz) ? 0 : -1;
        }
        roff += rdlen;
    }
    return 1; /* e.g. only a CNAME chain */
}

static bool is_local_or_keendns_host(const char *host) {
    if (!host) return false;
    if (!strchr(host, '.')) return true;
    if (strcasestr(host, ".crazedns.ru") || strcasestr(host, ".keenetic.") ||
        strcasestr(host, ".netcraze.") || strcasestr(host, ".local") ||
        strcasestr(host, ".lan") || strcasestr(host, ".home.arpa")) {
        return true;
    }
    return false;
}

/* A first, AAAA only when the name has no A (and ipv4_only is off): IPv4 stays the
 * default path for dual-stack names, IPv6-only names still work. */
int resolve_host_ip(const char *host, int egress_mode, const char *dns_proxy_host, uint16_t dns_proxy_port, bool allow_private, bool ipv4_only, char *out_ip, size_t out_sz) {
    if (!host || !host[0]) return -1;
    bool allow_fakeip = (egress_mode == EGRESS_MODE_ROUTER);
    if (!check_destination_acl_ex(host, allow_private, allow_fakeip)) return -1;

    uint8_t oct[4];
    if (parse_ipv4_octets(host, oct)) {
        snprintf(out_ip, out_sz, "%s", host);
        if (egress_mode == EGRESS_MODE_ROUTER && is_firc_fake_ip_str(out_ip)) {
            sync_fakeip_dnat(out_ip);
        }
        return 0;
    }
    struct in6_addr a6;
    if (inet_pton(AF_INET6, host, &a6) == 1) {
        snprintf(out_ip, out_sz, "%s", host);
        return 0;
    }

    bool use_firc_dns = (egress_mode == EGRESS_MODE_ROUTER) && dns_proxy_host && dns_proxy_host[0] && dns_proxy_port > 0 && !is_local_or_keendns_host(host);
    char cache_key[280];
    snprintf(cache_key, sizeof(cache_key), "%s|%d|%d|%d|%d", host, egress_mode, use_firc_dns ? 1 : 0, allow_private ? 1 : 0, ipv4_only ? 1 : 0);

    uint64_t now = now_ms();
    pthread_mutex_lock(&g_dns_cache_mu);
    for (int i = 0; i < g_dns_cache_count; i++) {
        if (strcasecmp(g_dns_cache[i].key, cache_key) == 0 && now < g_dns_cache[i].expires_ms) {
            snprintf(out_ip, out_sz, "%s", g_dns_cache[i].ip);
            pthread_mutex_unlock(&g_dns_cache_mu);
            if (!check_destination_acl_ex(out_ip, allow_private, allow_fakeip)) return -1;
            if (use_firc_dns && is_firc_fake_ip_str(out_ip)) {
                sync_fakeip_dnat(out_ip);
            }
            return 0;
        }
    }
    pthread_mutex_unlock(&g_dns_cache_mu);

    /* FIRC DNS proxy (router mode), then router local DNS (127.0.0.1:53), then getaddrinfo */
    const char *dns_hosts[2];
    uint16_t dns_ports[2];
    int num_dns = 0;
    if (use_firc_dns) {
        dns_hosts[num_dns] = dns_proxy_host;
        dns_ports[num_dns++] = dns_proxy_port;
    }
    dns_hosts[num_dns] = "127.0.0.1";
    dns_ports[num_dns++] = 53;
    int rc = -1;
    for (int i = 0; i < num_dns && rc != 0; i++) {
        rc = udp_dns_query(host, DNS_QTYPE_A, dns_hosts[i], dns_ports[i], out_ip, out_sz);
        if (rc == 1 && !ipv4_only) {
            rc = udp_dns_query(host, DNS_QTYPE_AAAA, dns_hosts[i], dns_ports[i], out_ip, out_sz);
        }
    }
    if (rc != 0) {
        struct addrinfo hints, *res = NULL;
        memset(&hints, 0, sizeof(hints));
        hints.ai_family = ipv4_only ? AF_INET : AF_UNSPEC;
        hints.ai_socktype = SOCK_STREAM;
        if (getaddrinfo(host, NULL, &hints, &res) == 0 && res) {
            const struct addrinfo *pick = NULL;
            for (const struct addrinfo *ai = res; ai; ai = ai->ai_next) {
                if (ai->ai_family == AF_INET) { pick = ai; break; }
                if (!pick && ai->ai_family == AF_INET6) pick = ai;
            }
            if (pick && pick->ai_family == AF_INET) {
                inet_ntop(AF_INET, &((const struct sockaddr_in *)pick->ai_addr)->sin_addr, out_ip, (socklen_t)out_sz);
                rc = 0;
            } else if (pick) {
                inet_ntop(AF_INET6, &((const struct sockaddr_in6 *)pick->ai_addr)->sin6_addr, out_ip, (socklen_t)out_sz);
                rc = 0;
            }
            freeaddrinfo(res);
        }
    }
    if (rc != 0) return -1;
    if (!check_destination_acl_ex(out_ip, allow_private, allow_fakeip)) return -1;

    uint64_t ttl_ms = 60000;
    if (use_firc_dns && is_firc_fake_ip_str(out_ip)) {
        sync_fakeip_dnat(out_ip);
        ttl_ms = 25000;
    }

    pthread_mutex_lock(&g_dns_cache_mu);
    int slot = -1;
    for (int i = 0; i < g_dns_cache_count; i++) {
        if (strcasecmp(g_dns_cache[i].key, cache_key) == 0) {
            slot = i;
            break;
        }
    }
    if (slot < 0) {
        if (g_dns_cache_count < DNS_CACHE_MAX) slot = g_dns_cache_count++;
        else slot = (int)(now % DNS_CACHE_MAX);
    }
    snprintf(g_dns_cache[slot].key, sizeof(g_dns_cache[slot].key), "%s", cache_key);
    snprintf(g_dns_cache[slot].ip, sizeof(g_dns_cache[slot].ip), "%s", out_ip);
    g_dns_cache[slot].expires_ms = now + ttl_ms;
    pthread_mutex_unlock(&g_dns_cache_mu);

    return 0;
}

static int connect_nonblock(const char *ip_str, uint16_t port, uint32_t fwmark, const char *bind_if, int timeout_ms) {
    struct sockaddr_storage sa;
    socklen_t sa_len = fill_sockaddr(ip_str, port, &sa);
    if (sa_len == 0) return -1;
#ifdef _WIN32
    SOCKET s = socket(sa.ss_family, SOCK_STREAM, 0);
    if (s == INVALID_SOCKET) return -1;
    u_long nonblock = 1;
    ioctlsocket(s, FIONBIO, &nonblock);
    int fd = (int)s;
#else
    int fd = socket(sa.ss_family, SOCK_STREAM | SOCK_NONBLOCK | SOCK_CLOEXEC, 0);
    if (fd < 0) return -1;
#endif

    int one = 1;
    setsockopt(fd, IPPROTO_TCP, TCP_NODELAY, (const char *)&one, sizeof(one));
    setsockopt(fd, SOL_SOCKET, SO_KEEPALIVE, (const char *)&one, sizeof(one));

#ifdef SO_MARK
    if (fwmark != 0) {
        if (setsockopt(fd, SOL_SOCKET, SO_MARK, &fwmark, sizeof(fwmark)) < 0) {
            close(fd);
            return -1;
        }
    }
#else
    (void)fwmark;
#endif
#ifdef SO_BINDTODEVICE
    if (bind_if && bind_if[0]) {
        if (setsockopt(fd, SOL_SOCKET, SO_BINDTODEVICE, bind_if, (socklen_t)strlen(bind_if)) < 0) {
            close(fd);
            return -1;
        }
    }
#else
    (void)bind_if;
#endif

    int r = connect(fd, (struct sockaddr *)&sa, sa_len);
#ifdef _WIN32
    if (r < 0 && WSAGetLastError() != WSAEWOULDBLOCK && errno != EINPROGRESS) {
        close(fd);
        return -1;
    }
    if (r != 0) {
        fd_set wfds, efds;
        FD_ZERO(&wfds);
        FD_ZERO(&efds);
        FD_SET((SOCKET)fd, &wfds);
        FD_SET((SOCKET)fd, &efds);
        int tmo = timeout_ms > 0 ? timeout_ms : 8000;
        struct timeval tv;
        tv.tv_sec = tmo / 1000;
        tv.tv_usec = (tmo % 1000) * 1000;
        int sr = select(0, NULL, &wfds, &efds, &tv);
        if (sr <= 0 || FD_ISSET((SOCKET)fd, &efds)) {
            close(fd);
            return -1;
        }
        int so_err = 0;
        socklen_t slen = sizeof(so_err);
        if (getsockopt(fd, SOL_SOCKET, SO_ERROR, (char *)&so_err, &slen) < 0 || so_err != 0) {
            close(fd);
            return -1;
        }
    }
#else
    if (r < 0 && errno != EINPROGRESS) {
        close(fd);
        return -1;
    }
    if (r != 0) {
        struct pollfd pfd = { .fd = fd, .events = POLLOUT };
        int pr = poll(&pfd, 1, timeout_ms > 0 ? timeout_ms : 8000);
        if (pr <= 0) {
            close(fd);
            return -1;
        }
        int so_err = 0;
        socklen_t slen = sizeof(so_err);
        if (getsockopt(fd, SOL_SOCKET, SO_ERROR, (char *)&so_err, &slen) < 0 || so_err != 0) {
            close(fd);
            return -1;
        }
    }
#endif

    /* Switch back to blocking mode with reasonable socket timeouts */
#ifdef _WIN32
    nonblock = 0;
    ioctlsocket((SOCKET)fd, FIONBIO, &nonblock);
#else
    int flags = fcntl(fd, F_GETFL, 0);
    if (flags >= 0) fcntl(fd, F_SETFL, flags & ~O_NONBLOCK);
    struct timeval tv = { .tv_sec = 15, .tv_usec = 0 };
    setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));
    setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &tv, sizeof(tv));
#endif
    return fd;
}

/* SOCKS5 ATYP + address + port for an IPv4/IPv6 literal or a domain; returns its length, 0 if unusable. */
static size_t socks5_put_addr(uint8_t *p, const char *host, uint16_t port) {
    uint8_t v4_oct[4];
    struct in6_addr v6_addr;
    size_t n;
    if (inet_pton(AF_INET6, host, &v6_addr) == 1) {
        p[0] = 0x04; /* IPv6 */
        memcpy(p + 1, v6_addr.s6_addr, 16);
        n = 17;
    } else if (parse_ipv4_octets(host, v4_oct)) {
        p[0] = 0x01; /* IPv4 */
        memcpy(p + 1, v4_oct, 4);
        n = 5;
    } else {
        size_t hlen = strlen(host);
        if (hlen == 0 || hlen > 255) return 0;
        p[0] = 0x03; /* DOMAIN */
        p[1] = (uint8_t)hlen;
        memcpy(p + 2, host, hlen);
        n = 2 + hlen;
    }
    p[n] = (uint8_t)(port >> 8);
    p[n + 1] = (uint8_t)(port & 0xFF);
    return n + 2;
}

/* No-auth greeting + one request (cmd 1 CONNECT / 3 UDP ASSOCIATE). Returns the control
 * connection; bnd (when not NULL) gets the reply BND.ADDR:BND.PORT if it is an IP. */
static int socks5_request(const char *socks_host, uint16_t socks_port, uint8_t cmd, const char *target_host,
                          uint16_t target_port, int timeout_ms, struct sockaddr_storage *bnd, socklen_t *bnd_len) {
    int fd = connect_nonblock(socks_host, socks_port, 0, NULL, timeout_ms);
    if (fd < 0) return -1;

    struct timeval tv = { .tv_sec = 5, .tv_usec = 0 };
    setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));
    setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &tv, sizeof(tv));

    uint8_t greet[3] = { 0x05, 0x01, 0x00 };
    if (write_all(fd, greet, 3) < 0) { close(fd); return -1; }
    uint8_t gr_resp[2];
    if (recv(fd, gr_resp, 2, MSG_WAITALL) != 2 || gr_resp[0] != 0x05 || gr_resp[1] != 0x00) {
        close(fd);
        return -1;
    }

    uint8_t req[300] = { 0x05, cmd, 0x00 };
    size_t alen = socks5_put_addr(req + 3, target_host, target_port);
    if (alen == 0 || write_all(fd, req, 3 + alen) < 0) { close(fd); return -1; }

    uint8_t rhdr[4];
    if (recv(fd, rhdr, 4, MSG_WAITALL) != 4 || rhdr[0] != 0x05 || rhdr[1] != 0x00) {
        close(fd);
        return -1;
    }
    size_t skip = 0;
    if (rhdr[3] == 0x01) skip = 4 + 2;
    else if (rhdr[3] == 0x04) skip = 16 + 2;
    else if (rhdr[3] == 0x03) {
        uint8_t dlen = 0;
        if (recv(fd, &dlen, 1, MSG_WAITALL) != 1) { close(fd); return -1; }
        skip = (size_t)dlen + 2;
    } else {
        close(fd);
        return -1;
    }
    uint8_t baddr[260];
    if (skip > 0 && recv(fd, baddr, skip, MSG_WAITALL) != (ssize_t)skip) {
        close(fd);
        return -1;
    }
    if (bnd && bnd_len) {
        *bnd_len = 0;
        memset(bnd, 0, sizeof(*bnd));
        uint16_t bport = (uint16_t)((baddr[skip - 2] << 8) | baddr[skip - 1]);
        if (rhdr[3] == 0x01) {
            struct sockaddr_in *s4 = (struct sockaddr_in *)bnd;
            s4->sin_family = AF_INET;
            memcpy(&s4->sin_addr, baddr, 4);
            s4->sin_port = htons(bport);
            *bnd_len = (socklen_t)sizeof(*s4);
        } else if (rhdr[3] == 0x04) {
            struct sockaddr_in6 *s6 = (struct sockaddr_in6 *)bnd;
            s6->sin6_family = AF_INET6;
            memcpy(&s6->sin6_addr, baddr, 16);
            s6->sin6_port = htons(bport);
            *bnd_len = (socklen_t)sizeof(*s6);
        }
    }
    return fd;
}

static int dial_socks5(const char *socks_host, uint16_t socks_port, const char *target_host, uint16_t target_port, int timeout_ms) {
    return socks5_request(socks_host, socks_port, 0x01, target_host, target_port, timeout_ms, NULL, NULL);
}

/* RFC 1928 UDP ASSOCIATE. The UDP socket is connect()ed to the relay, so the kernel drops
 * datagrams from any other source; the control connection is handed to the caller and
 * must stay open while the association is used. */
static int socks5_udp_associate(const xhttpd_config_t *cfg, const char *target_ip, uint16_t target_port,
                                udp_egress_t *out) {
    struct sockaddr_storage relay;
    socklen_t relay_len = 0;
    int ctrl = socks5_request(cfg->socks_host, cfg->socks_port, 0x03, "0.0.0.0", 0, 5000, &relay, &relay_len);
    if (ctrl < 0) return -1;
    if (relay_len == 0) { close(ctrl); return -1; }
    /* Unspecified BND.ADDR means "the SOCKS server's own address" */
    bool unspec = (relay.ss_family == AF_INET && ((struct sockaddr_in *)&relay)->sin_addr.s_addr == 0) ||
                  (relay.ss_family == AF_INET6 && IN6_IS_ADDR_UNSPECIFIED(&((struct sockaddr_in6 *)&relay)->sin6_addr));
    if (unspec) {
        uint16_t rport = relay.ss_family == AF_INET ? ntohs(((struct sockaddr_in *)&relay)->sin_port)
                                                    : ntohs(((struct sockaddr_in6 *)&relay)->sin6_port);
        relay_len = fill_sockaddr(cfg->socks_host, rport, &relay);
        if (relay_len == 0) { close(ctrl); return -1; }
    }

    out->socks_hdr[0] = 0x00; /* RSV */
    out->socks_hdr[1] = 0x00;
    out->socks_hdr[2] = 0x00; /* FRAG: we never fragment */
    size_t alen = socks5_put_addr(out->socks_hdr + 3, target_ip, target_port);
    if (alen == 0 || alen > sizeof(out->socks_hdr) - 3) { close(ctrl); return -1; }
    out->socks_hdr_len = (uint8_t)(3 + alen);

    int fd = socket(relay.ss_family, SOCK_DGRAM | SOCK_CLOEXEC, 0);
    if (fd < 0) { close(ctrl); return -1; }
    if (connect(fd, (struct sockaddr *)&relay, relay_len) < 0) {
        close(fd);
        close(ctrl);
        return -1;
    }
    out->dest = relay;
    out->dest_len = relay_len;
    out->socks_ctrl_fd = ctrl;
    return fd;
}

int dial_target_tcp(const char *orig_host, uint16_t port, const uint8_t *first_chunk, size_t chunk_len, const xhttpd_config_t *cfg, int timeout_ms) {
    if (!orig_host || !orig_host[0] || !cfg) return -1;

    /* 1. In router mode, forward TCP port 53 directly to fircd (:3553) */
    if (cfg->egress_mode == EGRESS_MODE_ROUTER && port == 53 && cfg->dns_proxy_host[0] && cfg->dns_proxy_port > 0) {
        return connect_nonblock(cfg->dns_proxy_host, cfg->dns_proxy_port, 0, NULL, timeout_ms);
    }

    char sniffed[256] = {0};
    if (first_chunk && chunk_len > 0) {
        if (port == 443 || port == 8443) {
            sniff_tls_sni(first_chunk, chunk_len, sniffed, sizeof(sniffed));
        }
        if (!sniffed[0]) {
            sniff_http_host(first_chunk, chunk_len, sniffed, sizeof(sniffed));
        }
    }

    const char *lookup_host = orig_host;
    if (sniffed[0] && should_defer_dial_for_sniff(orig_host, port, cfg->egress_mode)) {
        lookup_host = sniffed;
    }

    if (cfg->egress_mode == EGRESS_MODE_SOCKS) {
        if (!check_destination_acl_ex(lookup_host, cfg->allow_private, false)) return -1;
        if (!cfg->allow_private) {
            char verified_ip[48] = {0};
            if (resolve_host_ip(lookup_host, cfg->egress_mode, cfg->dns_proxy_host, cfg->dns_proxy_port, false, cfg->ipv4_only, verified_ip, sizeof(verified_ip)) != 0) {
                if (lookup_host != orig_host && !is_firc_fake_ip_str(orig_host)) {
                    if (resolve_host_ip(orig_host, cfg->egress_mode, cfg->dns_proxy_host, cfg->dns_proxy_port, false, cfg->ipv4_only, verified_ip, sizeof(verified_ip)) != 0) {
                        return -1;
                    }
                } else {
                    return -1;
                }
            }
            if (is_firc_fake_ip_str(verified_ip)) return -1;
            return dial_socks5(cfg->socks_host, cfg->socks_port, verified_ip, port, timeout_ms);
        }
        char lan_ip[48] = {0};
        if (resolve_host_ip(lookup_host, EGRESS_MODE_DIRECT, cfg->dns_proxy_host, cfg->dns_proxy_port, true, cfg->ipv4_only, lan_ip, sizeof(lan_ip)) == 0 &&
            is_private_lan_ip_str(lan_ip)) {
            return connect_nonblock(lan_ip, port, 0, NULL, timeout_ms);
        }
        return dial_socks5(cfg->socks_host, cfg->socks_port, lookup_host, port, timeout_ms);
    }

    char resolved_ip[48] = {0};
    if (resolve_host_ip(lookup_host, cfg->egress_mode, cfg->dns_proxy_host, cfg->dns_proxy_port, cfg->allow_private, cfg->ipv4_only, resolved_ip, sizeof(resolved_ip)) != 0) {
        /* If sniffed lookup failed and orig_host was a real public IPv4 (not a FakeIP), fallback to orig_host */
        if (lookup_host != orig_host && !is_firc_fake_ip_str(orig_host)) {
            if (resolve_host_ip(orig_host, cfg->egress_mode, cfg->dns_proxy_host, cfg->dns_proxy_port, cfg->allow_private, cfg->ipv4_only, resolved_ip, sizeof(resolved_ip)) != 0) {
                return -1;
            }
        } else {
            return -1;
        }
    }

    /* Never dial an unresolved FakeIP in direct/interface mode */
    if (cfg->egress_mode != EGRESS_MODE_ROUTER && is_firc_fake_ip_str(resolved_ip)) {
        return -1;
    }

    uint32_t mark = 0;
    const char *bind_if = NULL;
    if (cfg->egress_mode == EGRESS_MODE_ROUTER) {
        if (is_firc_fake_ip_str(resolved_ip)) {
            mark = FIRC_ROUTING_MARK;
        }
    } else if (cfg->egress_mode == EGRESS_MODE_INTERFACE) {
        if (!cfg->interface_name[0]) return -1;
        if (!(cfg->allow_private && is_private_lan_ip_str(resolved_ip))) {
            bind_if = cfg->interface_name;
        }
    }

    uint64_t t0 = now_ms();
    int fd = connect_nonblock(resolved_ip, port, mark, bind_if, timeout_ms);
    if (fd < 0 && mark == FIRC_ROUTING_MARK) {
        /* Force re-sync FEXT_DNAT once and retry */
        dnat_bump_generation();
        sync_fakeip_dnat(resolved_ip);
        fd = connect_nonblock(resolved_ip, port, mark, bind_if, timeout_ms);
    }
    diag_log("dial_tcp orig=%s:%u sni=%s resolved=%s:%u mode=%d mark=0x%x dur=%llums fd=%d",
             orig_host, port, sniffed[0] ? sniffed : "-", resolved_ip, port, cfg->egress_mode, mark,
             (unsigned long long)(now_ms() - t0), fd);
    return fd;
}

int dial_target_udp(const char *host, uint16_t port, const xhttpd_config_t *cfg, udp_egress_t *out) {
    if (!host || !cfg || !out) return -1;
    memset(out, 0, sizeof(*out));
    out->socks_ctrl_fd = -1;

    char resolved_ip[64] = {0};
    uint16_t dest_port = port;
    uint32_t mark = 0;
    const char *bind_if = NULL;

    if (cfg->egress_mode == EGRESS_MODE_ROUTER && port == 53 && cfg->dns_proxy_host[0] && cfg->dns_proxy_port > 0) {
        snprintf(resolved_ip, sizeof(resolved_ip), "%s", cfg->dns_proxy_host);
        dest_port = cfg->dns_proxy_port;
    } else {
        if (resolve_host_ip(host, cfg->egress_mode, cfg->dns_proxy_host, cfg->dns_proxy_port, cfg->allow_private, cfg->ipv4_only, resolved_ip, sizeof(resolved_ip)) != 0) {
            return -1;
        }
        if (cfg->egress_mode != EGRESS_MODE_ROUTER && is_firc_fake_ip_str(resolved_ip)) {
            return -1;
        }
        if (cfg->egress_mode == EGRESS_MODE_ROUTER && is_firc_fake_ip_str(resolved_ip)) {
            mark = FIRC_ROUTING_MARK;
        } else if (cfg->egress_mode == EGRESS_MODE_INTERFACE) {
            if (!cfg->interface_name[0]) return -1;
            if (!(cfg->allow_private && is_private_lan_ip_str(resolved_ip))) {
                bind_if = cfg->interface_name;
            }
        }
    }
    /* SOCKS egress carries UDP too, otherwise it leaks straight to WAN (F864).
     * LAN targets with allow_private stay direct, as dial_target_tcp does. */
    if (cfg->egress_mode == EGRESS_MODE_SOCKS && !(cfg->allow_private && is_private_lan_ip_str(resolved_ip))) {
        return socks5_udp_associate(cfg, resolved_ip, dest_port, out);
    }

    out->dest_len = fill_sockaddr(resolved_ip, dest_port, &out->dest);
    if (out->dest_len == 0) return -1;

    int fd = socket(out->dest.ss_family, SOCK_DGRAM | SOCK_CLOEXEC, 0);
    if (fd < 0) return -1;
#ifdef SO_MARK
    if (mark != 0) {
        if (setsockopt(fd, SOL_SOCKET, SO_MARK, &mark, sizeof(mark)) < 0) {
            close(fd);
            return -1;
        }
    }
#else
    (void)mark;
#endif
#ifdef SO_BINDTODEVICE
    if (bind_if && bind_if[0]) {
        if (setsockopt(fd, SOL_SOCKET, SO_BINDTODEVICE, bind_if, (socklen_t)strlen(bind_if)) < 0) {
            close(fd);
            return -1;
        }
    }
#else
    (void)bind_if;
#endif
    return fd;
}

ssize_t udp_egress_send(int fd, const udp_egress_t *e, const uint8_t *data, size_t len) {
    if (e->socks_ctrl_fd < 0) {
        return sendto(fd, (const char *)data, len, 0, (const struct sockaddr *)&e->dest, e->dest_len);
    }
#ifdef _WIN32
    errno = ENOTSUP; /* SOCKS UDP needs sendmsg; the daemon only ships for Linux */
    return -1;
#else
    struct iovec iov[2] = {
        { .iov_base = (void *)e->socks_hdr, .iov_len = e->socks_hdr_len },
        { .iov_base = (void *)data,         .iov_len = len },
    };
    struct msghdr msg;
    memset(&msg, 0, sizeof(msg));
    msg.msg_iov = iov;
    msg.msg_iovlen = 2;
    ssize_t n = sendmsg(fd, &msg, MSG_NOSIGNAL);
    return n < 0 ? n : (n >= (ssize_t)e->socks_hdr_len ? n - (ssize_t)e->socks_hdr_len : 0);
#endif
}

/* SOCKS: strips the RFC 1928 UDP header in place. A fragmented (FRAG != 0) or malformed
 * datagram is dropped and reported as -1/EAGAIN, so callers treat it like "nothing yet". */
ssize_t udp_egress_recv(int fd, const udp_egress_t *e, uint8_t *buf, size_t cap, int flags) {
    ssize_t n = recv(fd, (char *)buf, cap, flags);
    if (n < 0 || e->socks_ctrl_fd < 0) return n;
    size_t hl = 0;
    if (n >= 4 && buf[0] == 0 && buf[1] == 0 && buf[2] == 0) {
        if (buf[3] == 0x01) hl = 4 + 4 + 2;
        else if (buf[3] == 0x04) hl = 4 + 16 + 2;
        else if (buf[3] == 0x03 && n >= 5) hl = 4 + 1 + (size_t)buf[4] + 2;
    }
    if (hl == 0 || (size_t)n < hl) {
        errno = EAGAIN;
        return -1;
    }
    memmove(buf, buf + hl, (size_t)n - hl);
    return n - (ssize_t)hl;
}

void udp_egress_close_ctrl(udp_egress_t *e) {
    if (e && e->socks_ctrl_fd >= 0) {
        close(e->socks_ctrl_fd);
        e->socks_ctrl_fd = -1;
    }
}
