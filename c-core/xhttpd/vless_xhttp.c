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
#include <arpa/inet.h>
#endif

int parse_vless_header(const uint8_t *buf, size_t len, const xhttpd_config_t *cfg, vless_req_t *out) {
    if (!buf || len < 18 || !cfg || !out) return -1;
    memset(out, 0, sizeof(*out));
    if (buf[0] != 0x00) {
        diag_log("parse_vless_header fail v0: buf[0]=0x%02x", buf[0]);
        return -1; /* VLESS v0 */
    }

    bool authed = false;
    for (int i = 0; i < cfg->num_clients; i++) {
        if (ct_memeq(buf + 1, cfg->clients[i].uuid, 16)) {
            authed = true;
            break;
        }
    }
    if (!authed) return -2;

    memcpy(out->uuid, buf + 1, 16);
    size_t addons_len = buf[17];
    /* Addons несут только flow (xtls-rprx-vision): Vision мы не реализуем,
     * молча пропустить его — сломать клиенту поток посередине. Отказ. */
    if (addons_len != 0) {
        diag_log("parse_vless_header: flow addons unsupported (len=%zu)", addons_len);
        return -1;
    }
    size_t pos = 18;
    if (len < pos + 1) return -1;

    out->command = buf[pos++];
    if (out->command == VLESS_CMD_MUX) {
        out->port = 0;
        out->host[0] = '\0';
        out->header_len = pos;
        return 0;
    }
    if (out->command != VLESS_CMD_TCP && out->command != VLESS_CMD_UDP) {
        return -1;
    }
    if (len < pos + 3) return -1;
    out->port = ((uint16_t)buf[pos] << 8) | buf[pos + 1];
    pos += 2;
    uint8_t atyp = buf[pos++];

    if (atyp == VLESS_ADDR_IPV4) {
        if (len < pos + 4) return -1;
        snprintf(out->host, sizeof(out->host), "%u.%u.%u.%u",
                 buf[pos], buf[pos + 1], buf[pos + 2], buf[pos + 3]);
        pos += 4;
    } else if (atyp == VLESS_ADDR_DOMAIN) {
        if (len < pos + 1) return -1;
        size_t dlen = buf[pos++];
        if (dlen == 0 || len < pos + dlen) return -1;
        memcpy(out->host, buf + pos, dlen);
        out->host[dlen] = '\0';
        pos += dlen;
    } else if (atyp == VLESS_ADDR_IPV6) {
        if (len < pos + 16) return -1;
        inet_ntop(AF_INET6, buf + pos, out->host, sizeof(out->host));
        pos += 16;
    } else {
        return -1;
    }
    out->header_len = pos;
    return 0;
}

static int write_http_chunk(int fd, const uint8_t *data, size_t len) {
    if (len == 0) return 0;
    char hdr[32];
    int hlen = snprintf(hdr, sizeof(hdr), "%zx\r\n", len);
    if (hlen < 0 || (size_t)hlen >= sizeof(hdr)) return -1;
#ifdef _WIN32
    if (write_all(fd, hdr, (size_t)hlen) < 0) return -1;
    if (write_all(fd, data, len) < 0) return -1;
    if (write_all(fd, "\r\n", 2) < 0) return -1;
    return 0;
#else
    struct iovec iov[3] = {
        { .iov_base = hdr,         .iov_len = (size_t)hlen },
        { .iov_base = (void*)data, .iov_len = len },
        { .iov_base = "\r\n",      .iov_len = 2 }
    };
    struct msghdr msg;
    memset(&msg, 0, sizeof(msg));
    msg.msg_iov = iov;
    msg.msg_iovlen = 3;
    size_t total = (size_t)hlen + len + 2;
    while (total > 0) {
        ssize_t n = sendmsg(fd, &msg, MSG_NOSIGNAL);
        if (n <= 0) {
            if (n < 0 && errno == EINTR) continue;
            return -1;
        }
        total -= (size_t)n;
        if (total > 0) {
            /* Advance iov */
            size_t rem = (size_t)n;
            while (msg.msg_iovlen > 0 && rem >= msg.msg_iov[0].iov_len) {
                rem -= msg.msg_iov[0].iov_len;
                msg.msg_iov++;
                msg.msg_iovlen--;
            }
            if (msg.msg_iovlen > 0 && rem > 0) {
                msg.msg_iov[0].iov_base = (uint8_t *)msg.msg_iov[0].iov_base + rem;
                msg.msg_iov[0].iov_len -= rem;
            }
        }
    }
    return 0;
#endif
}

static int write_http_chunk_end(int fd) {
    return write_all(fd, "0\r\n\r\n", 5);
}

static int send_xhttp_stream_headers(int fd) {
    char pad[256];
    memset(pad, 'X', 200);
    pad[200] = '\0';
    char hdr[640];
    int n = snprintf(hdr, sizeof(hdr),
        "HTTP/1.1 200 OK\r\n"
        "Content-Type: application/octet-stream\r\n"
        "Transfer-Encoding: chunked\r\n"
        "Cache-Control: no-store, no-cache, must-revalidate, max-age=0\r\n"
        "Pragma: no-cache\r\n"
        "X-Accel-Buffering: no\r\n"
        "X-Padding: %s\r\n"
        "Connection: close\r\n"
        "\r\n", pad);
    if (n < 0 || (size_t)n >= sizeof(hdr)) return -1;
    return write_all(fd, hdr, (size_t)n);
}

/* abort_flag (may be NULL): checked every 100 ms while waiting, so a revoked stream does
 * not sit in a blocking read of the up pipe until the peer happens to close it. The wait
 * itself stays unbounded as a blocking read() was: the 15 s deadline covers EAGAIN only. */
static int read_exact(int fd, uint8_t *buf, size_t len, const bool *abort_flag) {
    size_t off = 0;
    uint64_t deadline = now_ms() + 15000ULL;
    while (off < len) {
        if (abort_flag) {
            if (__atomic_load_n(abort_flag, __ATOMIC_ACQUIRE)) return -1;
            struct pollfd apfd = { .fd = fd, .events = POLLIN };
            int apr = poll(&apfd, 1, 100);
            if (apr < 0 && errno != EINTR) return -1;
            if (apr <= 0) continue;
        }
        ssize_t r = read(fd, buf + off, len - off);
        if (r == 0) return -1;
        if (r < 0) {
            if (errno == EINTR) {
                if (now_ms() >= deadline) return -1;
                continue;
            }
            if (errno == EAGAIN || errno == EWOULDBLOCK) {
                uint64_t now = now_ms();
                if (now >= deadline) return -1;
                int rem = (int)(deadline - now);
                if (rem > 1000) rem = 1000;
                struct pollfd pfd = { .fd = fd, .events = POLLIN };
                if (poll(&pfd, 1, rem) < 0 && errno != EINTR) return -1;
                continue;
            }
            return -1;
        }
        off += (size_t)r;
        deadline = now_ms() + 15000ULL;
    }
    return 0;
}

static bool is_downlink_peer_closed(int down_fd) {
    if (down_fd < 0) return true;
    uint8_t drain[512];
    ssize_t n = recv(down_fd, drain, sizeof(drain), MSG_DONTWAIT);
    if (n == 0) return true;
    if (n < 0) {
        if (errno == EAGAIN || errno == EWOULDBLOCK || errno == EINTR) return false;
        return true;
    }
    return false;
}

#define VLESS_TCP_RELAY_BUF 32768

/* Relay VLESS TCP stream: up_fd (pipe/socket) <-> target_fd <-> down_fd (chunked HTTP response) */
static void handle_vless_tcp_stream(const vless_req_t *vreq, const uint8_t *initial_buf, size_t initial_len,
                                    int up_fd, int down_fd, const xhttpd_config_t *cfg,
                                    const bool *abort_flag, bool watch_down_eof) {
    /* initial_buf already holds the first data: sniff it in place (a copy would only add
     * resident stack pages to every stream) */
    uint8_t sniff_buf[4096];
    size_t sniff_len = 0;
    if (initial_len == 0 && should_defer_dial_for_sniff(vreq->host, vreq->port, cfg->egress_mode)) {
        struct pollfd pfd = { .fd = up_fd, .events = POLLIN };
        if (poll(&pfd, 1, 2500) > 0) {
            ssize_t r = read(up_fd, sniff_buf, sizeof(sniff_buf));
            if (r > 0) sniff_len = (size_t)r;
        }
    }

    int tfd = dial_target_tcp(vreq->host, vreq->port, sniff_len > 0 ? sniff_buf : initial_buf,
                              sniff_len > 0 ? sniff_len : initial_len, cfg, 8000);
    uint8_t vresp_hdr[2] = { 0x00, 0x00 };
    bool vresp_sent = false;

    if (tfd < 0) {
        write_http_chunk(down_fd, vresp_hdr, 2);
        return;
    }

    if (initial_len > 0) {
        if (write_all(tfd, initial_buf, initial_len) < 0) {
            close(tfd);
            write_http_chunk(down_fd, vresp_hdr, 2);
            return;
        }
    } else if (sniff_len > 0) {
        if (write_all(tfd, sniff_buf, sniff_len) < 0) {
            close(tfd);
            write_http_chunk(down_fd, vresp_hdr, 2);
            return;
        }
    }

    short down_events = POLLIN | POLLERR | POLLHUP;
#ifdef POLLRDHUP
    down_events |= POLLRDHUP;
#endif
    struct pollfd pfds[3];
    pfds[0].fd = up_fd;
    pfds[0].events = POLLIN;
    pfds[1].fd = tfd;
    pfds[1].events = POLLIN;
    pfds[2].fd = watch_down_eof ? down_fd : -1;
    pfds[2].events = down_events;

    bool up_eof = false;
    bool down_closed = false;
    uint8_t *io_buf = (uint8_t *)malloc(VLESS_TCP_RELAY_BUF);
    if (!io_buf) {
        close(tfd);
        write_http_chunk(down_fd, vresp_hdr, 2);
        return;
    }

    uint64_t idle_limit_ms = (uint64_t)((cfg && cfg->idle_timeout_sec > 5) ? cfg->idle_timeout_sec : 120) * 1000ULL;
    uint64_t last_io_ms = now_ms();

    while (1) {
        if (abort_flag && __atomic_load_n(abort_flag, __ATOMIC_ACQUIRE)) break;
        uint64_t now = now_ms();
        if (now > last_io_ms && (now - last_io_ms) >= idle_limit_ms) break;

        pfds[0].fd = up_eof ? -1 : up_fd;
        pfds[2].fd = watch_down_eof ? down_fd : -1;
        int step_ms = (abort_flag != NULL) ? 100 : 5000;
        int pr = poll(pfds, 3, step_ms);
        if (pr < 0) {
            if (errno == EINTR) continue;
            break;
        }
        if (abort_flag && __atomic_load_n(abort_flag, __ATOMIC_ACQUIRE)) break;
        if (pr == 0) continue;

        if (watch_down_eof && pfds[2].revents != 0) {
#ifdef POLLRDHUP
            if (pfds[2].revents & (POLLERR | POLLHUP | POLLRDHUP)) {
                down_closed = true;
                break;
            }
#else
            if (pfds[2].revents & (POLLERR | POLLHUP)) {
                down_closed = true;
                break;
            }
#endif
            if ((pfds[2].revents & POLLIN) && is_downlink_peer_closed(down_fd)) {
                down_closed = true;
                break;
            }
        }

        if (!up_eof && (pfds[0].revents & (POLLIN | POLLHUP | POLLERR))) {
            ssize_t n = read(up_fd, io_buf, VLESS_TCP_RELAY_BUF);
            if (n > 0) {
                last_io_ms = now_ms();
                if (write_all(tfd, io_buf, (size_t)n) < 0) break;
            } else {
                up_eof = true;
                if (abort_flag && __atomic_load_n(abort_flag, __ATOMIC_ACQUIRE)) break;
                shutdown(tfd, SHUT_WR);
            }
        }
        if (pfds[1].revents & (POLLIN | POLLHUP | POLLERR)) {
            ssize_t n = recv(tfd, io_buf + 2, VLESS_TCP_RELAY_BUF - 2, 0);
            if (n > 0) {
                last_io_ms = now_ms();
                if (!vresp_sent) {
                    io_buf[0] = 0x00;
                    io_buf[1] = 0x00;
                    vresp_sent = true;
                    if (write_http_chunk(down_fd, io_buf, (size_t)n + 2) < 0) { down_closed = true; break; }
                } else {
                    if (write_http_chunk(down_fd, io_buf + 2, (size_t)n) < 0) { down_closed = true; break; }
                }
            } else {
                break;
            }
        }
    }

    free(io_buf);
    if (!vresp_sent && !down_closed && !(abort_flag && __atomic_load_n(abort_flag, __ATOMIC_ACQUIRE))) {
        write_http_chunk(down_fd, vresp_hdr, 2);
    }
    close(tfd);
}

/* Relay VLESS UDP stream (2-byte length-prefixed packets) */
static void handle_vless_udp_stream(const vless_req_t *vreq, const uint8_t *initial_buf, size_t initial_len,
                                    int up_fd, int down_fd, const xhttpd_config_t *cfg,
                                    const bool *abort_flag, bool watch_down_eof) {
    uint8_t vresp_hdr[2] = { 0x00, 0x00 };
    if (vreq->port == 443) {
        /* Reject QUIC (UDP 443) so clients fall back to TCP 443 */
        write_http_chunk(down_fd, vresp_hdr, 2);
        return;
    }

    udp_egress_t dest;
    int ufd = dial_target_udp(vreq->host, vreq->port, cfg, &dest);
    if (ufd < 0) {
        write_http_chunk(down_fd, vresp_hdr, 2);
        return;
    }

    bool vresp_sent = false;
    size_t pos = 0;
    while (pos + 2 <= initial_len) {
        uint16_t plen = ((uint16_t)initial_buf[pos] << 8) | initial_buf[pos + 1];
        if (pos + 2 + plen > initial_len) break;
        pos += 2;
        if (plen > 0) {
            udp_egress_send(ufd, &dest, initial_buf + pos, plen);
        }
        pos += plen;
    }

    short down_events = POLLIN | POLLERR | POLLHUP;
#ifdef POLLRDHUP
    down_events |= POLLRDHUP;
#endif
    struct pollfd pfds[3];
    pfds[0].fd = up_fd;
    pfds[0].events = POLLIN;
    pfds[1].fd = ufd;
    pfds[1].events = POLLIN;
    pfds[2].fd = watch_down_eof ? down_fd : -1;
    pfds[2].events = down_events;
    bool up_eof = false;
    bool down_closed = false;
    uint8_t *pkt_buf = (uint8_t *)malloc(65536);
    if (!pkt_buf) {
        close(ufd);
        udp_egress_close_ctrl(&dest);
        write_http_chunk(down_fd, vresp_hdr, 2);
        return;
    }

    uint64_t last_io_ms = now_ms();
    while (1) {
        if (abort_flag && __atomic_load_n(abort_flag, __ATOMIC_ACQUIRE)) break;
        uint64_t now = now_ms();
        uint64_t idle_limit = up_eof ? 2000ULL : 15000ULL;
        if (now > last_io_ms && (now - last_io_ms) >= idle_limit) break;

        pfds[0].fd = up_eof ? -1 : up_fd;
        pfds[2].fd = watch_down_eof ? down_fd : -1;
        int step_ms = (abort_flag != NULL) ? 100 : (int)idle_limit;
        int pr = poll(pfds, 3, step_ms);
        if (pr < 0) {
            if (errno == EINTR) continue;
            break;
        }
        if (abort_flag && __atomic_load_n(abort_flag, __ATOMIC_ACQUIRE)) break;
        if (pr == 0) continue;

        if (watch_down_eof && pfds[2].revents != 0) {
#ifdef POLLRDHUP
            if (pfds[2].revents & (POLLERR | POLLHUP | POLLRDHUP)) {
                down_closed = true;
                break;
            }
#else
            if (pfds[2].revents & (POLLERR | POLLHUP)) {
                down_closed = true;
                break;
            }
#endif
            if ((pfds[2].revents & POLLIN) && is_downlink_peer_closed(down_fd)) {
                down_closed = true;
                break;
            }
        }

        if (!up_eof && (pfds[0].revents & (POLLIN | POLLHUP | POLLERR))) {
            uint8_t lbuf[2];
            if (read_exact(up_fd, lbuf, 2, abort_flag) < 0) {
                up_eof = true;
                last_io_ms = now_ms();
            } else {
                uint16_t plen = ((uint16_t)lbuf[0] << 8) | lbuf[1];
                if (plen > 0) {
                    if (read_exact(up_fd, pkt_buf, plen, abort_flag) < 0) {
                        up_eof = true;
                        last_io_ms = now_ms();
                    } else {
                        last_io_ms = now_ms();
                        udp_egress_send(ufd, &dest, pkt_buf, plen);
                    }
                }
            }
        }
        if (pfds[1].revents & (POLLIN | POLLHUP | POLLERR)) {
            ssize_t n = udp_egress_recv(ufd, &dest, pkt_buf + 4, 65500, 0);
            if (n > 0) {
                last_io_ms = now_ms();
                if (!vresp_sent) {
                    pkt_buf[0] = 0x00;
                    pkt_buf[1] = 0x00;
                    pkt_buf[2] = (uint8_t)((n >> 8) & 0xFF);
                    pkt_buf[3] = (uint8_t)(n & 0xFF);
                    vresp_sent = true;
                    if (write_http_chunk(down_fd, pkt_buf, (size_t)n + 4) < 0) { down_closed = true; break; }
                } else {
                    pkt_buf[2] = (uint8_t)((n >> 8) & 0xFF);
                    pkt_buf[3] = (uint8_t)(n & 0xFF);
                    if (write_http_chunk(down_fd, pkt_buf + 2, (size_t)n + 2) < 0) { down_closed = true; break; }
                }
            } else if (!(n < 0 && (errno == EAGAIN || errno == EWOULDBLOCK))) {
                break; /* EAGAIN: a dropped SOCKS datagram, see udp_egress_recv */
            }
        }
    }

    free(pkt_buf);
    if (!vresp_sent && !down_closed && !(abort_flag && __atomic_load_n(abort_flag, __ATOMIC_ACQUIRE))) {
        write_http_chunk(down_fd, vresp_hdr, 2);
    }
    close(ufd);
    udp_egress_close_ctrl(&dest);
}

/* ---- Mux.Cool / XUDP (VLESS command 0x03) ----
 * Frame: [u16 meta_len][u16 id][u8 status][u8 option][status New: u8 net, u16 port, u8 atyp, addr
 * (+8-byte XUDP GlobalID for UDP)] [option Data: u16 len, data]. A UDP Keep may carry
 * net/port/addr after option as well (XUDP); we send replies that way, with the
 * sub-session's target as the source, as the Go stack did before F854.
 *
 * Threads: the worker thread reads the uplink and dials (a dial stalls only the uplink, as
 * in the old Go handler); ONE downlink thread per mux stream polls every sub-session fd and
 * writes Keep/End frames. Chosen over a reader thread per sub-session: on MIPS each extra
 * thread costs a stack (XHTTPD_HELPER_STACK reserved, a few KiB touched) and an 8 KiB buffer, so a
 * full mux would cost MUX_MAX_SUBS of them instead of one. Only the downlink thread closes
 * sub-session fds (or the worker after joining it), and never while the worker writes to
 * one (busy), so an fd can not be closed and reused under a pending poll/recv/write. */
#define MUX_STATUS_NEW       0x01
#define MUX_STATUS_KEEP      0x02
#define MUX_STATUS_END       0x03
#define MUX_STATUS_KEEPALIVE 0x04
#define MUX_OPT_DATA         0x01
#define MUX_OPT_ERROR        0x02
#define MUX_NET_TCP          0x01
#define MUX_NET_UDP          0x02
/* Xray defaults: mux.concurrency 8 TCP + xudpConcurrency 16 UDP per connection = 24;
 * 32 leaves headroom without letting one stream hold an unbounded number of sockets. */
#define MUX_MAX_SUBS         32
/* Xray buf.Size: its mux PacketReader refuses UDP frames above 8 KiB; TCP uses the same chunk. */
#define MUX_DOWN_BUF         8192
#define MUX_HDR_MAX          272 /* 2 + 4 + net/port/atyp + 1 + 255 domain + 2 data len */
/* Largest real metadata is New with a 255-byte domain + XUDP GlobalID (~272 B); the Go
 * handler refused anything above 1024 too. */
#define MUX_META_MAX         1024

typedef struct {
    bool         used;
    bool         dead;     /* no more I/O; the downlink thread closes fd once busy == 0 */
    bool         pending;  /* TCP dial deferred until the first data (sniffing) */
    uint8_t      net;
    uint16_t     id;
    int          fd;
    int          busy;     /* worker is writing to / dialing for this sub-session */
    udp_egress_t ue;
    char         host[256];
    uint16_t     port;
} mux_sub_t;

typedef struct {
    mux_sub_t        subs[MUX_MAX_SUBS];
    pthread_mutex_t  mu;       /* subs and the flags below */
    pthread_mutex_t  down_mu;  /* serialises writes to down_fd */
    int              down_fd;
    int              wake[2];
    const bool      *abort_flag;
    bool             stop;        /* worker: end now */
    bool             up_eof;      /* worker: uplink ended, drain replies then end */
    bool             down_broken; /* downlink thread: client gone */
    uint64_t         tcp_idle_ms;
} mux_ctx_t;

static bool mux_aborted(const mux_ctx_t *m) {
    return m->abort_flag && __atomic_load_n(m->abort_flag, __ATOMIC_ACQUIRE);
}

static void mux_wake(mux_ctx_t *m) {
    ssize_t w = write(m->wake[1], "w", 1);
    (void)w;
}

/* Header for one frame; udp_host != NULL appends net/port/address (UDP Keep). Returns length. */
static size_t mux_encode_hdr(uint8_t *out, uint16_t id, uint8_t status, uint8_t opt,
                             const char *udp_host, uint16_t udp_port, size_t data_len) {
    size_t m = 2;
    out[m++] = (uint8_t)(id >> 8);
    out[m++] = (uint8_t)id;
    out[m++] = status;
    out[m++] = opt;
    if (udp_host) {
        out[m++] = MUX_NET_UDP;
        out[m++] = (uint8_t)(udp_port >> 8);
        out[m++] = (uint8_t)udp_port;
        struct in_addr a4;
        struct in6_addr a6;
        if (inet_pton(AF_INET, udp_host, &a4) == 1) {
            out[m++] = 0x01;
            memcpy(out + m, &a4, 4); m += 4;
        } else if (inet_pton(AF_INET6, udp_host, &a6) == 1) {
            out[m++] = 0x03;
            memcpy(out + m, &a6, 16); m += 16;
        } else {
            size_t hl = strlen(udp_host);
            if (hl > 255) hl = 255;
            out[m++] = 0x02;
            out[m++] = (uint8_t)hl;
            memcpy(out + m, udp_host, hl); m += hl;
        }
    }
    out[0] = (uint8_t)((m - 2) >> 8);
    out[1] = (uint8_t)(m - 2);
    if (opt & MUX_OPT_DATA) {
        out[m++] = (uint8_t)(data_len >> 8);
        out[m++] = (uint8_t)data_len;
    }
    return m;
}

static int mux_write_down(mux_ctx_t *m, const uint8_t *buf, size_t len) {
    pthread_mutex_lock(&m->down_mu);
    int rc = write_http_chunk(m->down_fd, buf, len);
    pthread_mutex_unlock(&m->down_mu);
    if (rc < 0) {
        pthread_mutex_lock(&m->mu);
        m->down_broken = true;
        pthread_mutex_unlock(&m->mu);
    }
    return rc;
}

static void mux_send_end(mux_ctx_t *m, uint16_t id, uint8_t opt) {
    uint8_t f[8];
    size_t n = mux_encode_hdr(f, id, MUX_STATUS_END, opt, NULL, 0, 0);
    (void)mux_write_down(m, f, n);
}

/* Caller holds m->mu or owns the table alone. */
static void mux_close_sub_fd(mux_sub_t *s) {
    if (s->fd >= 0) close(s->fd);
    s->fd = -1;
    if (s->net == MUX_NET_UDP) udp_egress_close_ctrl(&s->ue);
}

static void *mux_down_thread(void *arg) {
    mux_ctx_t *m = (mux_ctx_t *)arg;
    uint8_t *buf = (uint8_t *)malloc(MUX_HDR_MAX + MUX_DOWN_BUF);
    if (!buf) {
        pthread_mutex_lock(&m->mu);
        m->down_broken = true;
        pthread_mutex_unlock(&m->mu);
        return NULL;
    }
    uint64_t last_io = now_ms();
    for (;;) {
        struct pollfd pfds[MUX_MAX_SUBS + 1];
        int idx[MUX_MAX_SUBS];
        int n = 0;
        bool live = false, live_tcp = false;
        pthread_mutex_lock(&m->mu);
        if (m->stop || m->down_broken || mux_aborted(m)) {
            pthread_mutex_unlock(&m->mu);
            break;
        }
        for (int i = 0; i < MUX_MAX_SUBS; i++) {
            mux_sub_t *s = &m->subs[i];
            if (!s->used) continue;
            if (s->dead) {
                if (s->busy == 0) {
                    mux_close_sub_fd(s);
                    s->used = false;
                }
                continue;
            }
            live = true;
            if (s->net == MUX_NET_TCP) live_tcp = true;
            if (s->fd >= 0) {
                pfds[n].fd = s->fd;
                pfds[n].events = POLLIN;
                pfds[n].revents = 0;
                idx[n++] = i;
            }
        }
        bool up_eof = m->up_eof;
        pthread_mutex_unlock(&m->mu);
        /* After the uplink ends, deliver what is still coming: TCP until the targets close
         * (bounded by the idle timeout), UDP-only for 2 s of silence, as the UDP stream does. */
        if (up_eof && (!live || now_ms() - last_io >= (live_tcp ? m->tcp_idle_ms : 2000ULL))) break;

        pfds[n].fd = m->wake[0];
        pfds[n].events = POLLIN;
        pfds[n].revents = 0;
        int pr = poll(pfds, (nfds_t)(n + 1), 100);
        if (pr < 0 && errno != EINTR) break;
        if (pr <= 0) continue;
        if (pfds[n].revents & POLLIN) {
            uint8_t drain[64];
            while (read(m->wake[0], drain, sizeof(drain)) > 0) {}
        }
        for (int k = 0; k < n; k++) {
            if (!(pfds[k].revents & (POLLIN | POLLHUP | POLLERR))) continue;
            pthread_mutex_lock(&m->mu);
            mux_sub_t *s = &m->subs[idx[k]];
            if (!s->used || s->dead || s->fd != pfds[k].fd) {
                pthread_mutex_unlock(&m->mu);
                continue;
            }
            uint8_t *data = buf + MUX_HDR_MAX;
            ssize_t r;
            bool is_udp = (s->net == MUX_NET_UDP);
            if (is_udp) r = udp_egress_recv(s->fd, &s->ue, data, MUX_DOWN_BUF, MSG_DONTWAIT);
            else r = recv(s->fd, data, MUX_DOWN_BUF, MSG_DONTWAIT);
            int end_opt = -1;
            if (!is_udp && (r == 0 || (r < 0 && errno != EAGAIN && errno != EWOULDBLOCK && errno != EINTR))) {
                end_opt = (r == 0) ? 0 : MUX_OPT_ERROR;
                s->dead = true;
            }
            uint16_t id = s->id;
            char host[256];
            uint16_t port = s->port;
            if (is_udp) snprintf(host, sizeof(host), "%s", s->host);
            pthread_mutex_unlock(&m->mu);

            if (r > 0) {
                uint8_t hdr[MUX_HDR_MAX];
                size_t hl = mux_encode_hdr(hdr, id, MUX_STATUS_KEEP, MUX_OPT_DATA,
                                           is_udp ? host : NULL, port, (size_t)r);
                memcpy(data - hl, hdr, hl);
                last_io = now_ms();
                if (mux_write_down(m, data - hl, hl + (size_t)r) < 0) break;
            }
            if (end_opt >= 0) {
                last_io = now_ms();
                mux_send_end(m, id, (uint8_t)end_opt);
            }
        }
    }
    free(buf);
    return NULL;
}

/* net/port/atyp/address at meta[off]; returns bytes consumed or 0. */
static size_t mux_parse_target(const uint8_t *meta, size_t mlen, size_t off,
                               uint8_t *net, uint16_t *port, char *host, size_t host_sz) {
    if (off + 4 > mlen) return 0;
    *net = meta[off];
    *port = (uint16_t)((meta[off + 1] << 8) | meta[off + 2]);
    uint8_t atyp = meta[off + 3];
    size_t p = off + 4;
    if (atyp == 0x01) {
        if (p + 4 > mlen) return 0;
        snprintf(host, host_sz, "%u.%u.%u.%u", meta[p], meta[p + 1], meta[p + 2], meta[p + 3]);
        p += 4;
    } else if (atyp == 0x03) {
        if (p + 16 > mlen || !inet_ntop(AF_INET6, meta + p, host, (socklen_t)host_sz)) return 0;
        p += 16;
    } else if (atyp == 0x02) {
        if (p + 1 > mlen) return 0;
        size_t dl = meta[p++];
        if (dl == 0 || p + dl > mlen || dl >= host_sz) return 0;
        memcpy(host, meta + p, dl);
        host[dl] = '\0';
        p += dl;
    } else {
        return 0;
    }
    return p - off;
}

/* Worker side of a write to a sub-session (busy already taken). Returns 0 or -1. */
static int mux_sub_write(mux_sub_t *s, int fd, const uint8_t *data, size_t len) {
    if (len == 0) return 0;
    if (s->net == MUX_NET_UDP) {
        (void)udp_egress_send(fd, &s->ue, data, len); /* a lost datagram is UDP, not an error */
        return 0;
    }
    return write_all(fd, data, len);
}

/* Dials a TCP/UDP target; for TCP `first` feeds the SNI/Host sniffer. Returns fd or -1. */
static int mux_dial(mux_sub_t *s, const uint8_t *first, size_t first_len, const xhttpd_config_t *cfg) {
    if (s->net == MUX_NET_UDP) return dial_target_udp(s->host, s->port, cfg, &s->ue);
    return dial_target_tcp(s->host, s->port, first, first_len, cfg, 8000);
}

static void handle_vless_mux_stream(const uint8_t *initial_buf, size_t initial_len,
                                    int up_fd, int down_fd, const xhttpd_config_t *cfg,
                                    const bool *abort_flag) {
    int rfd = up_fd;
    bool vresp_sent = false;
    uint8_t vresp_hdr[2] = { 0x00, 0x00 };

    /* Read from initial_buf first via cursor, then up_fd */
    size_t init_pos = 0;
    #define MUX_READ_EXACT(dst, cnt) ({ \
        int _rc = 0; \
        size_t _rem = (cnt); \
        uint8_t *_d = (uint8_t *)(dst); \
        if (init_pos < initial_len) { \
            size_t _take = (initial_len - init_pos) < _rem ? (initial_len - init_pos) : _rem; \
            memcpy(_d, initial_buf + init_pos, _take); \
            init_pos += _take; \
            _d += _take; \
            _rem -= _take; \
        } \
        if (_rem > 0 && read_exact(rfd, _d, _rem, abort_flag) < 0) _rc = -1; \
        _rc; \
    })

    uint8_t *meta = (uint8_t *)malloc(MUX_META_MAX);
    uint8_t *payload = (uint8_t *)malloc(65536);
    mux_ctx_t *m = (mux_ctx_t *)calloc(1, sizeof(*m));
    if (!meta || !payload || !m || pipe2(m->wake, O_NONBLOCK | O_CLOEXEC) < 0) {
        free(meta);
        free(payload);
        free(m);
        write_http_chunk(down_fd, vresp_hdr, 2);
        return;
    }
    pthread_mutex_init(&m->mu, NULL);
    pthread_mutex_init(&m->down_mu, NULL);
    m->down_fd = down_fd;
    m->abort_flag = abort_flag;
    m->tcp_idle_ms = (uint64_t)((cfg && cfg->idle_timeout_sec > 5) ? cfg->idle_timeout_sec : 120) * 1000ULL;
    for (int i = 0; i < MUX_MAX_SUBS; i++) m->subs[i].fd = -1;

    pthread_t down_tid;
    pthread_attr_t attr;
    pthread_attr_init(&attr);
    pthread_attr_setstacksize(&attr, XHTTPD_HELPER_STACK);
    bool down_started = (pthread_create(&down_tid, &attr, mux_down_thread, m) == 0);
    pthread_attr_destroy(&attr);

    bool clean_eof = false;
    while (down_started) {
        /* Revocation stops the relay even for frames already buffered in the up pipe */
        if (mux_aborted(m)) break;
        pthread_mutex_lock(&m->mu);
        bool broken = m->down_broken;
        pthread_mutex_unlock(&m->mu);
        if (broken) break;

        uint8_t mlen_buf[2];
        if (MUX_READ_EXACT(mlen_buf, 2) < 0) { clean_eof = !mux_aborted(m); break; }
        uint16_t mlen = ((uint16_t)mlen_buf[0] << 8) | mlen_buf[1];
        if (mlen < 4 || mlen > MUX_META_MAX) break;
        if (MUX_READ_EXACT(meta, mlen) < 0) break;

        uint16_t sub_id = ((uint16_t)meta[0] << 8) | meta[1];
        uint8_t  status = meta[2];
        uint8_t  opt    = meta[3];

        size_t plen = 0;
        if (opt & MUX_OPT_DATA) {
            uint8_t plen_buf[2];
            if (MUX_READ_EXACT(plen_buf, 2) < 0) break;
            plen = ((uint16_t)plen_buf[0] << 8) | plen_buf[1];
            if (plen > 0 && MUX_READ_EXACT(payload, plen) < 0) break;
        }

        if (!vresp_sent) {
            vresp_sent = true;
            if (mux_write_down(m, vresp_hdr, 2) < 0) break;
        }

        if (status == MUX_STATUS_NEW) {
            mux_sub_t ns;
            memset(&ns, 0, sizeof(ns));
            ns.fd = -1;
            ns.ue.socks_ctrl_fd = -1;
            ns.id = sub_id;
            if (mux_parse_target(meta, mlen, 4, &ns.net, &ns.port, ns.host, sizeof(ns.host)) == 0 ||
                (ns.net != MUX_NET_TCP && ns.net != MUX_NET_UDP)) {
                mux_send_end(m, sub_id, MUX_OPT_ERROR);
                continue;
            }
            if (ns.net == MUX_NET_UDP && ns.port == 443) {
                /* Reject QUIC (UDP 443) so clients fall back to TCP 443 */
                mux_send_end(m, sub_id, MUX_OPT_ERROR);
                continue;
            }
            int slot = -1, used = 0;
            pthread_mutex_lock(&m->mu);
            for (int i = 0; i < MUX_MAX_SUBS; i++) {
                mux_sub_t *s = &m->subs[i];
                if (s->used && !s->dead && s->id == sub_id) s->dead = true; /* New replaces */
                if (s->used) used++;
                else if (slot < 0) slot = i;
            }
            pthread_mutex_unlock(&m->mu);
            if (slot < 0 || used >= MUX_MAX_SUBS) {
                mux_wake(m);
                mux_send_end(m, sub_id, MUX_OPT_ERROR);
                continue;
            }
            /* Plain IP:443/80 with no first data yet: dial on the first Keep, so the
             * sniffed domain decides routing, as handle_vless_tcp_stream does. */
            if (ns.net == MUX_NET_TCP && plen == 0 && should_defer_dial_for_sniff(ns.host, ns.port, cfg->egress_mode)) {
                ns.pending = true;
            } else {
                ns.fd = mux_dial(&ns, payload, plen, cfg);
                if (ns.fd < 0 || mux_sub_write(&ns, ns.fd, payload, plen) < 0) {
                    mux_close_sub_fd(&ns);
                    mux_send_end(m, sub_id, MUX_OPT_ERROR);
                    continue;
                }
            }
            ns.used = true;
            pthread_mutex_lock(&m->mu);
            m->subs[slot] = ns; /* slot stays free for us: only the worker allocates */
            pthread_mutex_unlock(&m->mu);
            mux_wake(m);
            continue;
        }
        if (status != MUX_STATUS_KEEP && status != MUX_STATUS_END) continue; /* KeepAlive */

        pthread_mutex_lock(&m->mu);
        mux_sub_t *s = NULL;
        for (int i = 0; i < MUX_MAX_SUBS; i++) {
            if (m->subs[i].used && !m->subs[i].dead && m->subs[i].id == sub_id) { s = &m->subs[i]; break; }
        }
        if (!s) {
            pthread_mutex_unlock(&m->mu);
            continue;
        }
        s->busy++;
        bool need_dial = s->pending && plen > 0;
        int fd = s->fd;
        pthread_mutex_unlock(&m->mu);

        int wrc = 0;
        if (need_dial) {
            /* busy keeps the entry; the downlink thread skips fd < 0 */
            fd = mux_dial(s, payload, plen, cfg);
            pthread_mutex_lock(&m->mu);
            s->fd = fd;
            s->pending = false;
            pthread_mutex_unlock(&m->mu);
            if (fd < 0) wrc = -1;
        }
        if (wrc == 0 && fd >= 0) wrc = mux_sub_write(s, fd, payload, plen);

        pthread_mutex_lock(&m->mu);
        s->busy--;
        if (wrc < 0 || status == MUX_STATUS_END) s->dead = true;
        pthread_mutex_unlock(&m->mu);
        if (wrc < 0) mux_send_end(m, sub_id, MUX_OPT_ERROR);
        mux_wake(m);
    }
    #undef MUX_READ_EXACT

    pthread_mutex_lock(&m->mu);
    if (clean_eof) {
        /* Half-close: TCP targets see EOF, replies keep flowing until they finish */
        m->up_eof = true;
        for (int i = 0; i < MUX_MAX_SUBS; i++) {
            mux_sub_t *s = &m->subs[i];
            if (s->used && !s->dead && s->net == MUX_NET_TCP && s->fd >= 0) shutdown(s->fd, SHUT_WR);
        }
    } else {
        m->stop = true;
    }
    pthread_mutex_unlock(&m->mu);
    if (down_started) {
        mux_wake(m);
        pthread_join(down_tid, NULL);
    }
    for (int i = 0; i < MUX_MAX_SUBS; i++) {
        if (m->subs[i].used) mux_close_sub_fd(&m->subs[i]);
    }
    if (!vresp_sent && !mux_aborted(m)) {
        write_http_chunk(down_fd, vresp_hdr, 2);
    }
    close(m->wake[0]);
    close(m->wake[1]);
    pthread_mutex_destroy(&m->mu);
    pthread_mutex_destroy(&m->down_mu);
    free(m);
    free(meta);
    free(payload);
}

/* VLESS XHTTP Session Table for packet-up and stream-up */
#define MAX_VLESS_SESSIONS    256
#define MAX_PENDING_SEQ       32
#define PRE_AUTH_MAX_PACKETS  4
#define MAX_RECENT_SID_RING   64
/* Every stream-one runs inside an HTTP worker, so one slot per worker means a live
 * stream-one always has a slot and stays revocable. A full table (only reachable by
 * calling the handler outside the worker pool) is refused with 503, never run unrevocable. */
#define MAX_STREAM_ONE_SLOTS  MAX_HTTP_WORKERS

typedef struct {
    bool     used;
    uint64_t seq;
    uint8_t *data;
    size_t   len;
} vless_seq_pkt_t;

typedef struct {
    bool            active;
    uint64_t        generation;
    char            sid[128];
    char            client_ip[64];
    uint8_t         client_uuid[16];
    int             up_pipe[2]; /* [0] = read by worker, [1] = written by POSTs */
    int             down_fd;
    int             stream_up_fd;
    bool            has_down;
    bool            down_running;
    int             up_writers;
    bool            authed;
    bool            closed;
    bool            revoked;
    uint64_t        next_seq;
    size_t          pre_auth_bytes;
    uint64_t        created_ms;
    uint64_t        last_active_ms;
    vless_seq_pkt_t pending[MAX_PENDING_SEQ];
    pthread_mutex_t up_write_mu;
    pthread_mutex_t mu;
    pthread_cond_t  cond;
} vless_session_t;

typedef struct {
    char     sid[128];
    uint64_t ts_ms;
} recent_sid_entry_t;

typedef struct {
    bool    active;
    uint8_t uuid[16];
    int     client_fd;
    bool    revoked;
    char    client_ip[64];
} stream_one_slot_t;

typedef struct {
    const uint8_t *initial_body;
    size_t         initial_len;
    conn_reader_t *cr;
    bool           chunked_incomplete;
    int            pipe_w;
} stream_feed_arg_t;

static vless_session_t    g_vless_sessions[MAX_VLESS_SESSIONS];
static recent_sid_entry_t g_closed_sids[MAX_RECENT_SID_RING];
static int                g_closed_sid_idx = 0;
static recent_sid_entry_t g_revoked_sids[MAX_RECENT_SID_RING];
static int                g_revoked_sid_idx = 0;
static stream_one_slot_t  g_stream_one_slots[MAX_STREAM_ONE_SLOTS];
static pthread_mutex_t    g_vless_table_mu = PTHREAD_MUTEX_INITIALIZER;

static void record_recent_sid_locked(recent_sid_entry_t *ring, int *idx, const char *sid, uint64_t now) {
    if (!sid || !sid[0]) return;
    for (int i = 0; i < MAX_RECENT_SID_RING; i++) {
        if (ring[i].sid[0] && strcmp(ring[i].sid, sid) == 0) {
            ring[i].ts_ms = now;
            return;
        }
    }
    int slot = (*idx) % MAX_RECENT_SID_RING;
    *idx = slot + 1;
    snprintf(ring[slot].sid, sizeof(ring[slot].sid), "%s", sid);
    ring[slot].ts_ms = now;
}

static bool is_sid_in_ring_locked(const recent_sid_entry_t *ring, const char *sid, uint64_t now, uint64_t ttl_ms) {
    if (!sid || !sid[0]) return false;
    for (int i = 0; i < MAX_RECENT_SID_RING; i++) {
        if (ring[i].sid[0] && strcmp(ring[i].sid, sid) == 0) {
            if (now >= ring[i].ts_ms && (now - ring[i].ts_ms) <= ttl_ms) {
                return true;
            }
        }
    }
    return false;
}

static void *stream_feed_worker(void *arg) {
    stream_feed_arg_t *fa = (stream_feed_arg_t *)arg;
    if (fa->initial_len > 0 && fa->initial_body) {
        write_all(fa->pipe_w, fa->initial_body, fa->initial_len);
    }
    if (fa->chunked_incomplete && fa->cr) {
        pump_remaining_http_chunks(fa->cr, fa->pipe_w);
    }
    if (fa->pipe_w >= 0) {
        close(fa->pipe_w);
    }
    return NULL;
}

/* Dispatch a VLESS stream once the header is read */
static void run_vless_stream(int up_fd, int down_fd, const xhttpd_config_t *cfg,
                             vless_session_t *sess, const bool *abort_flag) {
    bool watch_down_eof = (sess != NULL);
    short down_events = POLLIN | POLLERR | POLLHUP;
#ifdef POLLRDHUP
    down_events |= POLLRDHUP;
#endif
    uint64_t wait_limit_ms = (uint64_t)((cfg && cfg->idle_timeout_sec > 5) ? cfg->idle_timeout_sec : 120) * 1000ULL;
    uint64_t start_ms = now_ms();

    uint8_t buf[8192];
    ssize_t n = -1;
    while (1) {
        if (abort_flag && __atomic_load_n(abort_flag, __ATOMIC_ACQUIRE)) return;
        uint64_t now = now_ms();
        if (now > start_ms && (now - start_ms) >= wait_limit_ms) return;

        struct pollfd pfds[2] = {
            { .fd = up_fd, .events = POLLIN },
            { .fd = watch_down_eof ? down_fd : -1, .events = down_events },
        };
        int pr = poll(pfds, 2, (abort_flag != NULL) ? 100 : 2000);
        if (pr < 0) {
            if (errno == EINTR) continue;
            return;
        }
        if (pr == 0) continue;
        if (watch_down_eof && pfds[1].revents != 0) {
#ifdef POLLRDHUP
            if (pfds[1].revents & (POLLERR | POLLHUP | POLLRDHUP)) return;
#else
            if (pfds[1].revents & (POLLERR | POLLHUP)) return;
#endif
            if ((pfds[1].revents & POLLIN) && is_downlink_peer_closed(down_fd)) return;
        }
        if (pfds[0].revents & (POLLIN | POLLHUP | POLLERR)) {
            n = read(up_fd, buf, sizeof(buf));
            if (n < 0 && errno == EINTR) continue;
            break;
        }
    }
    diag_log("run_vless_stream initial read n=%zd", n);
    if (n <= 0) return;

    /* Ensure at least 24 bytes if fragmented */
    size_t total = (size_t)n;
    while (total < 24) {
        if (abort_flag && __atomic_load_n(abort_flag, __ATOMIC_ACQUIRE)) return;
        struct pollfd pfds[2] = {
            { .fd = up_fd, .events = POLLIN },
            { .fd = watch_down_eof ? down_fd : -1, .events = down_events },
        };
        if (poll(pfds, 2, (abort_flag != NULL) ? 100 : 2000) <= 0) {
            if (abort_flag != NULL) {
                if (now_ms() - start_ms >= 2000ULL) break;
                continue;
            }
            break;
        }
        if (watch_down_eof && pfds[1].revents != 0) {
#ifdef POLLRDHUP
            if (pfds[1].revents & (POLLERR | POLLHUP | POLLRDHUP)) return;
#else
            if (pfds[1].revents & (POLLERR | POLLHUP)) return;
#endif
            if ((pfds[1].revents & POLLIN) && is_downlink_peer_closed(down_fd)) return;
        }
        if (pfds[0].revents & (POLLIN | POLLHUP | POLLERR)) {
            ssize_t r = read(up_fd, buf + total, sizeof(buf) - total);
            if (r <= 0) break;
            total += (size_t)r;
        }
    }

    vless_req_t vreq;
    char hex_dbg[64] = {0};
    bytes_to_hex(buf, total < 28 ? total : 28, hex_dbg);
    int prc = parse_vless_header(buf, total, cfg, &vreq);
    diag_log("parse_vless_header total=%zu rc=%d cmd=%u host=%s port=%u hex=%s", total, prc, vreq.command, vreq.host, vreq.port, hex_dbg);
    if (prc != 0) {
        return;
    }

    if (sess) {
        pthread_mutex_lock(&g_vless_table_mu);
        pthread_mutex_lock(&sess->mu);
        if (!sess->closed && sess->down_running) {
            memcpy(sess->client_uuid, vreq.uuid, 16);
            sess->authed = true;
            sess->pre_auth_bytes = 0;
        }
        pthread_mutex_unlock(&sess->mu);
        pthread_mutex_unlock(&g_vless_table_mu);
    }

    const uint8_t *rem_ptr = buf + vreq.header_len;
    size_t rem_len = total - vreq.header_len;

    if (vreq.command == VLESS_CMD_TCP) {
        handle_vless_tcp_stream(&vreq, rem_ptr, rem_len, up_fd, down_fd, cfg, abort_flag, watch_down_eof);
    } else if (vreq.command == VLESS_CMD_UDP) {
        handle_vless_udp_stream(&vreq, rem_ptr, rem_len, up_fd, down_fd, cfg, abort_flag, watch_down_eof);
    } else if (vreq.command == VLESS_CMD_MUX) {
        handle_vless_mux_stream(rem_ptr, rem_len, up_fd, down_fd, cfg, abort_flag);
    }
}

static int check_vless_uuid_prefix(const uint8_t *buf, size_t len, const xhttpd_config_t *cfg) {
    if (!buf || len < 17 || !cfg) return 0; /* incomplete prefix */
    if (buf[0] != 0x00) return -1;
    for (int i = 0; i < cfg->num_clients; i++) {
        if (ct_memeq(buf + 1, cfg->clients[i].uuid, 16)) {
            return 1;
        }
    }
    return -2;
}

static bool is_valid_vless_sid(const char *sid) {
    if (!sid || !sid[0]) return false;
    size_t n = strlen(sid);
    if (n == 0 || n > 64) return false;
    for (size_t i = 0; i < n; i++) {
        unsigned char c = (unsigned char)sid[i];
        if ((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
            (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
            continue;
        }
        return false;
    }
    return true;
}

static bool ip_matches_cidr_v4(const char *ip_str, const char *cidr_str) {
    if (!ip_str || !ip_str[0] || !cidr_str || !cidr_str[0]) return false;
    struct in_addr ip_addr;
    if (inet_pton(AF_INET, ip_str, &ip_addr) != 1) return false;

    char net_part[64];
    int bits = 32;
    const char *slash = strchr(cidr_str, '/');
    if (slash) {
        size_t nl = (size_t)(slash - cidr_str);
        if (nl == 0 || nl >= sizeof(net_part)) return false;
        memcpy(net_part, cidr_str, nl);
        net_part[nl] = '\0';
        bits = atoi(slash + 1);
        if (bits < 0 || bits > 32) return false;
    } else {
        snprintf(net_part, sizeof(net_part), "%s", cidr_str);
    }
    struct in_addr net_addr;
    if (inet_pton(AF_INET, net_part, &net_addr) != 1) return false;
    uint32_t mask = (bits == 0) ? 0U : (~0U << (32 - bits));
    return (ntohl(ip_addr.s_addr) & mask) == (ntohl(net_addr.s_addr) & mask);
}

static void resolve_effective_client_ip(const http_req_t *req, const xhttpd_config_t *cfg, char *out_ip, size_t out_sz) {
    const char *peer = (req && req->peer_ip[0]) ? req->peer_ip : "127.0.0.1";
    snprintf(out_ip, out_sz, "%s", peer);
    if (!req || !cfg || cfg->num_trusted_proxies <= 0) return;

    bool trusted = false;
    for (int i = 0; i < cfg->num_trusted_proxies; i++) {
        if (ip_matches_cidr_v4(peer, cfg->trusted_proxies[i])) {
            trusted = true;
            break;
        }
    }
    if (!trusted) return;

    if (req->cf_ip[0]) {
        const char *p = req->cf_ip;
        while (*p == ' ' || *p == '\t') p++;
        if (*p) {
            snprintf(out_ip, out_sz, "%s", p);
            return;
        }
    }
    if (req->xff[0]) {
        const char *p = req->xff;
        while (*p == ' ' || *p == '\t') p++;
        const char *comma = strchr(p, ',');
        size_t len = comma ? (size_t)(comma - p) : strlen(p);
        while (len > 0 && (p[len - 1] == ' ' || p[len - 1] == '\t')) len--;
        if (len > 0 && len < out_sz) {
            memcpy(out_ip, p, len);
            out_ip[len] = '\0';
        }
    }
}

static int set_vless_fd_nonblocking(int fd, bool nonblock) {
#ifdef _WIN32
    u_long mode = nonblock ? 1 : 0;
    return ioctlsocket((SOCKET)fd, FIONBIO, &mode) == 0 ? 0 : -1;
#else
    int flags = fcntl(fd, F_GETFL, 0);
    if (flags < 0) return -1;
    if (nonblock) flags |= O_NONBLOCK;
    else flags &= ~O_NONBLOCK;
    return fcntl(fd, F_SETFL, flags) == 0 ? 0 : -1;
#endif
}

static bool vless_pipe_err_retryable(bool *out_eintr) {
#ifdef _WIN32
    int werr = WSAGetLastError();
    int cerr = errno;
    if (out_eintr) *out_eintr = (werr == WSAEINTR || cerr == EINTR);
    return (werr == WSAEWOULDBLOCK || werr == WSAEINTR ||
            cerr == EAGAIN || cerr == EWOULDBLOCK || cerr == EINTR);
#else
    int cerr = errno;
    if (out_eintr) *out_eintr = (cerr == EINTR);
    return (cerr == EAGAIN || cerr == EWOULDBLOCK || cerr == EINTR);
#endif
}

static int write_vless_pipe_raw_cancellable(vless_session_t *sess, uint64_t expected_gen, int wfd,
                                            const uint8_t *buf, size_t len, int timeout_ms) {
    if (len == 0) return 0;
    if (wfd < 0 || !buf) return -1;
    uint64_t deadline = now_ms() + (uint64_t)(timeout_ms > 0 ? timeout_ms : 10000);
    size_t off = 0;
    while (off < len) {
        pthread_mutex_lock(&sess->mu);
        bool cancelled = sess->closed || sess->generation != expected_gen ||
                         __atomic_load_n(&sess->revoked, __ATOMIC_ACQUIRE);
        pthread_mutex_unlock(&sess->mu);
        if (cancelled) return -1;

        uint64_t now = now_ms();
        if (now >= deadline) return -1;

        size_t chunk = (len - off) > 16384 ? 16384 : (len - off);
        ssize_t n = write(wfd, buf + off, chunk);
        if (n > 0) {
            off += (size_t)n;
            continue;
        }
        if (n == 0) return -1;

        bool is_eintr = false;
        if (!vless_pipe_err_retryable(&is_eintr)) return -1;
        if (is_eintr) continue;

        now = now_ms();
        if (now >= deadline) return -1;
        uint64_t rem_ms = deadline - now;
        int wait_ms = (rem_ms < 50ULL) ? (int)rem_ms : 50;
        struct pollfd pfd = { .fd = wfd, .events = POLLOUT };
        int pr = poll(&pfd, 1, wait_ms);
        if (pr < 0) {
            bool poll_eintr = false;
            if (vless_pipe_err_retryable(&poll_eintr) && poll_eintr) continue;
            return -1;
        }
        if (pr > 0 && (pfd.revents & (POLLERR | POLLHUP | POLLNVAL))) {
            return -1;
        }
    }
    return 0;
}

void vless_engine_init(void) {
    pthread_mutex_lock(&g_vless_table_mu);
    memset(g_closed_sids, 0, sizeof(g_closed_sids));
    g_closed_sid_idx = 0;
    memset(g_revoked_sids, 0, sizeof(g_revoked_sids));
    g_revoked_sid_idx = 0;
    memset(g_stream_one_slots, 0, sizeof(g_stream_one_slots));
    for (int i = 0; i < MAX_VLESS_SESSIONS; i++) {
        memset(&g_vless_sessions[i], 0, sizeof(vless_session_t));
        g_vless_sessions[i].up_pipe[0] = -1;
        g_vless_sessions[i].up_pipe[1] = -1;
        g_vless_sessions[i].down_fd = -1;
        g_vless_sessions[i].stream_up_fd = -1;
        pthread_mutex_init(&g_vless_sessions[i].up_write_mu, NULL);
        pthread_mutex_init(&g_vless_sessions[i].mu, NULL);
        pthread_cond_init(&g_vless_sessions[i].cond, NULL);
    }
    pthread_mutex_unlock(&g_vless_table_mu);
}

static void close_vless_session_locked(vless_session_t *s) {
    s->closed = true;
    s->active = false;
    s->authed = false;
    s->pre_auth_bytes = 0;
    if (s->down_fd >= 0) {
        shutdown(s->down_fd, SHUT_RDWR);
    }
    if (s->stream_up_fd >= 0) {
        /* Shut down only the read side so an active stream-up POST stops reading
         * chunks immediately while still able to write its HTTP 200 response
         * (avoiding an empty upstream EOF that cdndispatcher turns into 503). */
        shutdown(s->stream_up_fd, SHUT_RD);
    }
    if (!s->down_running && s->up_pipe[0] >= 0) {
        close(s->up_pipe[0]);
        s->up_pipe[0] = -1;
    }
    if (s->up_writers == 0 && s->up_pipe[1] >= 0) {
        close(s->up_pipe[1]);
        s->up_pipe[1] = -1;
    }
    for (int i = 0; i < MAX_PENDING_SEQ; i++) {
        if (s->pending[i].used && s->pending[i].data) {
            free(s->pending[i].data);
        }
        s->pending[i].used = false;
        s->pending[i].data = NULL;
        s->pending[i].len = 0;
    }
    pthread_cond_broadcast(&s->cond);
}

/* Concurrent VLESS connections (sessions + stream-one) allowed per effective client IP.
 * max_sessions_per_ip is clamped to half the worker pool: each such connection pins at
 * least one HTTP worker, and since F854 every REALITY TCP connection is a stream-one, so
 * without the clamp (vlessserver writes 512) one client IP could take all MAX_HTTP_WORKERS
 * and starve CDN and every other client with 503. */
static int vless_per_ip_conn_limit(const xhttpd_config_t *cfg) {
    int lim = (cfg && cfg->max_sessions_per_ip > 0) ? cfg->max_sessions_per_ip : 16;
    return lim < MAX_HTTP_WORKERS / 2 ? lim : MAX_HTTP_WORKERS / 2;
}

/* Caller holds g_vless_table_mu. */
static int count_stream_one_for_ip_locked(const char *client_ip) {
    int n = 0;
    if (!client_ip || !client_ip[0]) return 0;
    for (int i = 0; i < MAX_STREAM_ONE_SLOTS; i++) {
        if (g_stream_one_slots[i].active && strcmp(g_stream_one_slots[i].client_ip, client_ip) == 0) n++;
    }
    return n;
}

/* err_code: 429 = rate/session cap, 503 = table full */
static vless_session_t *get_or_create_vless_session(const char *sid, const char *client_ip,
                                                    bool creating_pre_auth_upload,
                                                    const xhttpd_config_t *cfg, int *err_code) {
    if (err_code) *err_code = 0;
    pthread_mutex_lock(&g_vless_table_mu);
    int free_slot = -1;
    int ip_total = 0;
    int ip_pre_auth = 0;
    int global_pre_auth = 0;

    for (int i = 0; i < MAX_VLESS_SESSIONS; i++) {
        vless_session_t *cand = &g_vless_sessions[i];
        pthread_mutex_lock(&cand->mu);
        if (cand->active && !cand->closed) {
            if (strcmp(cand->sid, sid) == 0) {
                pthread_mutex_unlock(&cand->mu);
                pthread_mutex_unlock(&g_vless_table_mu);
                return cand;
            }
            if (!cand->authed) {
                global_pre_auth++;
            }
            if (client_ip && client_ip[0] && strcmp(cand->client_ip, client_ip) == 0) {
                ip_total++;
                if (!cand->authed) {
                    ip_pre_auth++;
                }
            }
        } else if (free_slot < 0 && !cand->active && !cand->down_running && cand->up_writers == 0) {
            free_slot = i;
        }
        pthread_mutex_unlock(&cand->mu);
    }

    ip_total += count_stream_one_for_ip_locked(client_ip);
    if (ip_total >= vless_per_ip_conn_limit(cfg)) {
        pthread_mutex_unlock(&g_vless_table_mu);
        if (err_code) *err_code = 429;
        return NULL;
    }
    if (creating_pre_auth_upload) {
        int max_pa_global = (cfg && cfg->max_pre_auth_sessions > 0) ? cfg->max_pre_auth_sessions : 64;
        int max_pa_ip = (cfg && cfg->max_pre_auth_per_ip > 0) ? cfg->max_pre_auth_per_ip : 8;
        if (global_pre_auth >= max_pa_global || ip_pre_auth >= max_pa_ip) {
            pthread_mutex_unlock(&g_vless_table_mu);
            if (err_code) *err_code = 429;
            return NULL;
        }
    }

    if (free_slot < 0) {
        pthread_mutex_unlock(&g_vless_table_mu);
        if (err_code) *err_code = 503;
        return NULL;
    }
    vless_session_t *s = &g_vless_sessions[free_slot];
    pthread_mutex_lock(&s->mu);
    close_vless_session_locked(s);
    if (pipe(s->up_pipe) < 0) {
        pthread_mutex_unlock(&s->mu);
        pthread_mutex_unlock(&g_vless_table_mu);
        if (err_code) *err_code = 503;
        return NULL;
    }
    (void)set_vless_fd_nonblocking(s->up_pipe[1], true);
    uint64_t now = now_ms();
    s->generation++;
    if (s->generation == 0) s->generation = 1;
    s->active = true;
    s->closed = false;
    __atomic_store_n(&s->revoked, false, __ATOMIC_RELEASE);
    s->down_fd = -1;
    s->stream_up_fd = -1;
    s->has_down = false;
    s->down_running = false;
    s->up_writers = 0;
    s->authed = false;
    memset(s->client_uuid, 0, sizeof(s->client_uuid));
    s->next_seq = 0;
    s->pre_auth_bytes = 0;
    s->created_ms = now;
    s->last_active_ms = now;
    snprintf(s->sid, sizeof(s->sid), "%s", sid);
    snprintf(s->client_ip, sizeof(s->client_ip), "%s", (client_ip && client_ip[0]) ? client_ip : "127.0.0.1");
    pthread_mutex_unlock(&s->mu);
    pthread_mutex_unlock(&g_vless_table_mu);
    return s;
}

static bool check_and_add_pre_auth_bytes(vless_session_t *target, size_t add_len, const xhttpd_config_t *cfg) {
    if (add_len == 0) return true;
    size_t max_ip_bytes = (cfg && cfg->max_pre_auth_bytes_per_ip > 0) ? cfg->max_pre_auth_bytes_per_ip : (256 * 1024);
    size_t max_glob_bytes = (cfg && cfg->max_pre_auth_bytes_global > 0) ? cfg->max_pre_auth_bytes_global : (4 * 1024 * 1024);

    pthread_mutex_lock(&g_vless_table_mu);
    char target_ip[64] = {0};
    pthread_mutex_lock(&target->mu);
    snprintf(target_ip, sizeof(target_ip), "%s", target->client_ip);
    pthread_mutex_unlock(&target->mu);

    size_t ip_bytes = 0;
    size_t glob_bytes = 0;
    for (int i = 0; i < MAX_VLESS_SESSIONS; i++) {
        vless_session_t *s = &g_vless_sessions[i];
        pthread_mutex_lock(&s->mu);
        if (s->active && !s->closed && !s->authed) {
            glob_bytes += s->pre_auth_bytes;
            if (strcmp(s->client_ip, target_ip) == 0) {
                ip_bytes += s->pre_auth_bytes;
            }
        }
        pthread_mutex_unlock(&s->mu);
    }
    if (ip_bytes + add_len > max_ip_bytes || glob_bytes + add_len > max_glob_bytes) {
        pthread_mutex_unlock(&g_vless_table_mu);
        return false;
    }
    pthread_mutex_lock(&target->mu);
    target->pre_auth_bytes += add_len;
    pthread_mutex_unlock(&target->mu);
    pthread_mutex_unlock(&g_vless_table_mu);
    return true;
}

static void extract_query_param(const char *query, const char *key, char *out, size_t out_sz) {
    out[0] = '\0';
    if (!query || !query[0]) return;
    size_t klen = strlen(key);
    const char *p = query;
    while (*p) {
        const char *amp = strchr(p, '&');
        size_t seg_len = amp ? (size_t)(amp - p) : strlen(p);
        if (seg_len > klen + 1 && strncmp(p, key, klen) == 0 && p[klen] == '=') {
            size_t vlen = seg_len - (klen + 1);
            if (vlen >= out_sz) vlen = out_sz - 1;
            memcpy(out, p + klen + 1, vlen);
            out[vlen] = '\0';
            return;
        }
        if (!amp) break;
        p = amp + 1;
    }
}

static void extract_sid_and_seq(const http_req_t *req, const char *prefix,
                                char *out_sid, size_t sid_sz, char *out_seq, size_t seq_sz) {
    out_sid[0] = '\0';
    out_seq[0] = '\0';
    const char *rest = req->path;
    size_t plen = strlen(prefix);
    if (strncmp(rest, prefix, plen) == 0) {
        rest += plen;
    }
    while (*rest == '/') rest++;
    if (*rest) {
        const char *slash = strchr(rest, '/');
        if (!slash) {
            size_t l1 = strlen(rest);
            if (l1 >= sid_sz) l1 = sid_sz - 1;
            memcpy(out_sid, rest, l1);
            out_sid[l1] = '\0';
        } else {
            size_t l1 = (size_t)(slash - rest);
            if (l1 >= sid_sz) l1 = sid_sz - 1;
            memcpy(out_sid, rest, l1);
            out_sid[l1] = '\0';
            slash++;
            const char *slash2 = strchr(slash, '/');
            size_t l2 = slash2 ? (size_t)(slash2 - slash) : strlen(slash);
            if (l2 >= seq_sz) l2 = seq_sz - 1;
            memcpy(out_seq, slash, l2);
            out_seq[l2] = '\0';
        }
    }
    if (!out_sid[0]) {
        extract_query_param(req->query, "sid", out_sid, sid_sz);
        if (!out_sid[0]) extract_query_param(req->query, "session", out_sid, sid_sz);
        if (!out_sid[0]) extract_query_param(req->query, "x_session", out_sid, sid_sz);
        if (!out_sid[0] && req->session_id[0]) {
            snprintf(out_sid, sid_sz, "%s", req->session_id);
        }
    }
    if (!out_seq[0]) {
        extract_query_param(req->query, "seq", out_seq, seq_sz);
        if (!out_seq[0]) extract_query_param(req->query, "page", out_seq, seq_sz);
    }
}

static vless_session_t *lookup_existing_vless_session(const char *sid) {
    pthread_mutex_lock(&g_vless_table_mu);
    for (int i = 0; i < MAX_VLESS_SESSIONS; i++) {
        vless_session_t *s = &g_vless_sessions[i];
        pthread_mutex_lock(&s->mu);
        if (s->active && !s->closed && strcmp(s->sid, sid) == 0) {
            pthread_mutex_unlock(&s->mu);
            pthread_mutex_unlock(&g_vless_table_mu);
            return s;
        }
        pthread_mutex_unlock(&s->mu);
    }
    pthread_mutex_unlock(&g_vless_table_mu);
    return NULL;
}

int handle_vless_http_request(int client_fd, conn_reader_t *cr, const http_req_t *req, const char *matched_prefix, const xhttpd_config_t *cfg) {
    char sid[128] = {0};
    char seq_str[64] = {0};
    extract_sid_and_seq(req, matched_prefix, sid, sizeof(sid), seq_str, sizeof(seq_str));
    diag_log("vless_http method=%s path=%s query=%s sid=%s seq=%s body=%zu",
             req->method, req->path, req->query, sid, seq_str, req->body_len);

    if (sid[0] && !is_valid_vless_sid(sid)) {
        return send_http_error(client_fd, 400, "Bad Request", "invalid session id", req->keep_alive);
    }

    char client_ip[64] = {0};
    resolve_effective_client_ip(req, cfg, client_ip, sizeof(client_ip));

    /* 1. packet-up POST (or GET) with sid != "" and seq != "" */
    if (sid[0] && seq_str[0]) {
        char *endptr = NULL;
        uint64_t seq = strtoull(seq_str, &endptr, 10);
        if (!endptr || *endptr != '\0') {
            return send_http_error(client_fd, 400, "Bad Request", "invalid seq", req->keep_alive);
        }

        vless_session_t *existing = lookup_existing_vless_session(sid);
        if (!existing && seq > 0) {
            uint64_t check_now = now_ms();
            bool was_revoked = false;
            bool was_closed = false;
            pthread_mutex_lock(&g_vless_table_mu);
            was_revoked = is_sid_in_ring_locked(g_revoked_sids, sid, check_now, 30000ULL);
            if (!was_revoked) {
                was_closed = is_sid_in_ring_locked(g_closed_sids, sid, check_now, 15000ULL);
            }
            pthread_mutex_unlock(&g_vless_table_mu);
            if (was_revoked) {
                return send_http_error(client_fd, 403, "Forbidden", "session revoked", req->keep_alive);
            }
            if (was_closed) {
                char ok_hdr[256];
                int hlen = snprintf(ok_hdr, sizeof(ok_hdr),
                    "HTTP/1.1 200 OK\r\n"
                    "Content-Length: 0\r\n"
                    "Cache-Control: no-store\r\n"
                    "Connection: %s\r\n\r\n",
                    req->keep_alive ? "keep-alive" : "close");
                if (hlen < 0 || (size_t)hlen >= sizeof(ok_hdr)) return -1;
                if (write_all(client_fd, ok_hdr, (size_t)hlen) < 0) return -1;
                return req->keep_alive ? 0 : -1;
            }
        }
        if (!existing && seq > PRE_AUTH_MAX_PACKETS) {
            return send_http_error(client_fd, 429, "Too Many Requests", "pre-auth seq out of window", req->keep_alive);
        }

        int seq0_auth = 0;
        if (seq == 0) {
            seq0_auth = check_vless_uuid_prefix(req->body, req->body_len, cfg);
            if (seq0_auth == -2) {
                if (existing) {
                    pthread_mutex_lock(&g_vless_table_mu);
                    pthread_mutex_lock(&existing->mu);
                    if (!existing->authed) {
                        close_vless_session_locked(existing);
                    }
                    pthread_mutex_unlock(&existing->mu);
                    pthread_mutex_unlock(&g_vless_table_mu);
                }
                return send_http_error(client_fd, 403, "Forbidden", "unauthorized uuid", req->keep_alive);
            }
            if (seq0_auth == -1) {
                return send_http_error(client_fd, 400, "Bad Request", "invalid vless version", req->keep_alive);
            }
            vless_req_t vreq0;
            if (parse_vless_header(req->body, req->body_len, cfg, &vreq0) == 0 &&
                vreq0.command != VLESS_CMD_MUX &&
                !check_destination_acl_ex(vreq0.host, cfg->allow_private, cfg->egress_mode == EGRESS_MODE_ROUTER)) {
                if (existing) {
                    pthread_mutex_lock(&g_vless_table_mu);
                    pthread_mutex_lock(&existing->mu);
                    if (!existing->authed) {
                        close_vless_session_locked(existing);
                    }
                    pthread_mutex_unlock(&existing->mu);
                    pthread_mutex_unlock(&g_vless_table_mu);
                }
                return send_http_error(client_fd, 403, "Forbidden", "destination forbidden", req->keep_alive);
            }
        }

        int err_code = 0;
        vless_session_t *sess = get_or_create_vless_session(sid, client_ip, seq0_auth != 1, cfg, &err_code);
        if (!sess) {
            if (err_code == 429) {
                return send_http_error(client_fd, 429, "Too Many Requests", "session rate limit", req->keep_alive);
            }
            return send_http_error(client_fd, 503, "Service Unavailable", "session limit", req->keep_alive);
        }

        if (seq0_auth == 1) {
            bool uuid_conflict = false;
            pthread_mutex_lock(&g_vless_table_mu);
            pthread_mutex_lock(&sess->mu);
            if (sess->authed && !ct_memeq(sess->client_uuid, req->body + 1, 16)) {
                uuid_conflict = true;
            } else if (!sess->closed) {
                sess->authed = true;
                sess->pre_auth_bytes = 0;
                memcpy(sess->client_uuid, req->body + 1, 16);
            }
            pthread_mutex_unlock(&sess->mu);
            pthread_mutex_unlock(&g_vless_table_mu);
            if (uuid_conflict) {
                return send_http_error(client_fd, 403, "Forbidden", "session owner mismatch", req->keep_alive);
            }
        } else {
            bool is_authed = false;
            pthread_mutex_lock(&g_vless_table_mu);
            pthread_mutex_lock(&sess->mu);
            is_authed = sess->authed;
            pthread_mutex_unlock(&sess->mu);
            pthread_mutex_unlock(&g_vless_table_mu);
            if (!is_authed) {
                if (seq > PRE_AUTH_MAX_PACKETS || !check_and_add_pre_auth_bytes(sess, req->body_len, cfg)) {
                    return send_http_error(client_fd, 429, "Too Many Requests", "pre-auth buffer limit", req->keep_alive);
                }
            }
        }

        pthread_mutex_lock(&sess->up_write_mu);
        pthread_mutex_lock(&sess->mu);
        sess->last_active_ms = now_ms();
        if (sess->closed || sess->up_pipe[1] < 0) {
            bool rev = __atomic_load_n(&sess->revoked, __ATOMIC_ACQUIRE);
            pthread_mutex_unlock(&sess->mu);
            pthread_mutex_unlock(&sess->up_write_mu);
            if (rev) {
                return send_http_error(client_fd, 403, "Forbidden", "session revoked", req->keep_alive);
            }
            return send_http_error(client_fd, 410, "Gone", "session closed", req->keep_alive);
        }

        if (seq == sess->next_seq) {
            size_t total_len = req->body_len;
            uint64_t scan_seq = seq + 1;
            bool scan_more = true;
            while (scan_more) {
                scan_more = false;
                for (int i = 0; i < MAX_PENDING_SEQ; i++) {
                    if (sess->pending[i].used && sess->pending[i].seq == scan_seq) {
                        total_len += sess->pending[i].len;
                        scan_seq++;
                        scan_more = true;
                        break;
                    }
                }
            }

            uint8_t *to_write = NULL;
            if (total_len > 0) {
                to_write = (uint8_t *)malloc(total_len);
                if (!to_write) {
                    pthread_mutex_unlock(&sess->mu);
                    pthread_mutex_unlock(&sess->up_write_mu);
                    return send_http_error(client_fd, 503, "Service Unavailable", "out of memory", req->keep_alive);
                }
                size_t woff = 0;
                if (req->body_len > 0) {
                    memcpy(to_write, req->body, req->body_len);
                    woff += req->body_len;
                }
                sess->next_seq++;
                bool more = true;
                while (more) {
                    more = false;
                    for (int i = 0; i < MAX_PENDING_SEQ; i++) {
                        if (sess->pending[i].used && sess->pending[i].seq == sess->next_seq) {
                            if (sess->pending[i].len > 0) {
                                memcpy(to_write + woff, sess->pending[i].data, sess->pending[i].len);
                                woff += sess->pending[i].len;
                            }
                            free(sess->pending[i].data);
                            sess->pending[i].data = NULL;
                            sess->pending[i].len = 0;
                            sess->pending[i].used = false;
                            sess->next_seq++;
                            more = true;
                            break;
                        }
                    }
                }
            } else {
                sess->next_seq++;
                for (int i = 0; i < MAX_PENDING_SEQ; i++) {
                    if (sess->pending[i].used && sess->pending[i].seq == sess->next_seq) {
                        free(sess->pending[i].data);
                        sess->pending[i].data = NULL;
                        sess->pending[i].len = 0;
                        sess->pending[i].used = false;
                        sess->next_seq++;
                        i = -1;
                    }
                }
            }

            uint64_t my_gen = sess->generation;
            int wpipe = sess->up_pipe[1];
            sess->up_writers++;
            pthread_mutex_unlock(&sess->mu);

            int wrc = 0;
            if (to_write && total_len > 0) {
                wrc = write_vless_pipe_raw_cancellable(sess, my_gen, wpipe, to_write, total_len, 10000);
                free(to_write);
            }

            pthread_mutex_lock(&sess->mu);
            sess->up_writers--;
            if (sess->closed && sess->up_writers == 0 && sess->up_pipe[1] >= 0) {
                close(sess->up_pipe[1]);
                sess->up_pipe[1] = -1;
            }
            bool rev_after = __atomic_load_n(&sess->revoked, __ATOMIC_ACQUIRE);
            pthread_mutex_unlock(&sess->mu);
            pthread_mutex_unlock(&sess->up_write_mu);

            if (wrc < 0) {
                if (rev_after) {
                    return send_http_error(client_fd, 403, "Forbidden", "session revoked", req->keep_alive);
                }
                return send_http_error(client_fd, 410, "Gone", "session closed", req->keep_alive);
            }
        } else if (seq > sess->next_seq) {
            if ((uint64_t)(seq - sess->next_seq) > (uint64_t)MAX_PENDING_SEQ) {
                pthread_mutex_unlock(&sess->mu);
                pthread_mutex_unlock(&sess->up_write_mu);
                return send_http_error(client_fd, 400, "Bad Request", "seq window exceeded", req->keep_alive);
            }
            bool dup_seq = false;
            int slot = -1;
            for (int i = 0; i < MAX_PENDING_SEQ; i++) {
                if (sess->pending[i].used && sess->pending[i].seq == seq) {
                    dup_seq = true;
                    break;
                }
                if (!sess->pending[i].used && slot < 0) { slot = i; }
            }
            if (!dup_seq && slot >= 0) {
                uint8_t *cp = (uint8_t *)malloc(req->body_len ? req->body_len : 1);
                if (cp) {
                    if (req->body_len > 0) memcpy(cp, req->body, req->body_len);
                    sess->pending[slot].used = true;
                    sess->pending[slot].seq = seq;
                    sess->pending[slot].data = cp;
                    sess->pending[slot].len = req->body_len;
                }
            }
            pthread_mutex_unlock(&sess->mu);
            pthread_mutex_unlock(&sess->up_write_mu);
        } else {
            pthread_mutex_unlock(&sess->mu);
            pthread_mutex_unlock(&sess->up_write_mu);
        }

        char ok_hdr[256];
        int hlen = snprintf(ok_hdr, sizeof(ok_hdr),
            "HTTP/1.1 200 OK\r\n"
            "Content-Length: 0\r\n"
            "Cache-Control: no-store\r\n"
            "Connection: %s\r\n\r\n",
            req->keep_alive ? "keep-alive" : "close");
        if (hlen < 0 || (size_t)hlen >= sizeof(ok_hdr)) return -1;
        return write_all(client_fd, ok_hdr, (size_t)hlen);
    }

    /* 2. GET downlink stream for sid */
    if (strcmp(req->method, "GET") == 0 && sid[0]) {
        int err_code = 0;
        vless_session_t *sess = get_or_create_vless_session(sid, client_ip, false, cfg, &err_code);
        if (!sess) {
            if (err_code == 429) {
                return send_http_error(client_fd, 429, "Too Many Requests", "session rate limit", false);
            }
            return send_http_error(client_fd, 503, "Service Unavailable", "session limit", false);
        }
        pthread_mutex_lock(&sess->mu);
        if (sess->closed || sess->has_down || sess->down_running || sess->up_pipe[0] < 0) {
            bool is_closed = sess->closed;
            bool is_rev = __atomic_load_n(&sess->revoked, __ATOMIC_ACQUIRE);
            pthread_mutex_unlock(&sess->mu);
            if (is_closed) {
                if (is_rev) return send_http_error(client_fd, 403, "Forbidden", "session revoked", false);
                return send_http_error(client_fd, 410, "Gone", "session closed", false);
            }
            return send_http_error(client_fd, 409, "Conflict", "duplicate down stream", false);
        }
        sess->has_down = true;
        sess->down_running = true;
        sess->down_fd = client_fd;
        sess->last_active_ms = now_ms();
        int rpipe = sess->up_pipe[0];
        pthread_mutex_unlock(&sess->mu);

        if (send_xhttp_stream_headers(client_fd) < 0) {
            pthread_mutex_lock(&g_vless_table_mu);
            pthread_mutex_lock(&sess->mu);
            sess->down_running = false;
            sess->down_fd = -1;
            close_vless_session_locked(sess);
            pthread_mutex_unlock(&sess->mu);
            pthread_mutex_unlock(&g_vless_table_mu);
            return -1;
        }

        run_vless_stream(rpipe, client_fd, cfg, sess, &sess->revoked);
        if (!__atomic_load_n(&sess->revoked, __ATOMIC_ACQUIRE)) {
            write_http_chunk_end(client_fd);
        }

        pthread_mutex_lock(&g_vless_table_mu);
        pthread_mutex_lock(&sess->mu);
        if (!__atomic_load_n(&sess->revoked, __ATOMIC_ACQUIRE) && sess->sid[0]) {
            record_recent_sid_locked(g_closed_sids, &g_closed_sid_idx, sess->sid, now_ms());
        }
        sess->down_running = false;
        sess->down_fd = -1;
        close_vless_session_locked(sess);
        pthread_mutex_unlock(&sess->mu);
        pthread_mutex_unlock(&g_vless_table_mu);
        return -1; /* Close HTTP connection after stream finishes */
    }

    /* 3. stream-up POST with sid != "" and seq == "" */
    if (strcmp(req->method, "POST") == 0 && sid[0] && !seq_str[0]) {
        int auth_rc = check_vless_uuid_prefix(req->body, req->body_len, cfg);
        if (auth_rc == -2) {
            vless_session_t *existing = lookup_existing_vless_session(sid);
            if (existing) {
                pthread_mutex_lock(&g_vless_table_mu);
                pthread_mutex_lock(&existing->mu);
                if (!existing->authed) {
                    close_vless_session_locked(existing);
                }
                pthread_mutex_unlock(&existing->mu);
                pthread_mutex_unlock(&g_vless_table_mu);
            }
            return send_http_error(client_fd, 403, "Forbidden", "unauthorized uuid", req->keep_alive);
        }
        if (auth_rc == -1) {
            return send_http_error(client_fd, 400, "Bad Request", "invalid vless version", req->keep_alive);
        }
        vless_req_t vreq_su;
        if (parse_vless_header(req->body, req->body_len, cfg, &vreq_su) == 0 &&
            vreq_su.command != VLESS_CMD_MUX &&
            !check_destination_acl_ex(vreq_su.host, cfg->allow_private, cfg->egress_mode == EGRESS_MODE_ROUTER)) {
            vless_session_t *existing = lookup_existing_vless_session(sid);
            if (existing) {
                pthread_mutex_lock(&g_vless_table_mu);
                pthread_mutex_lock(&existing->mu);
                if (!existing->authed) {
                    close_vless_session_locked(existing);
                }
                pthread_mutex_unlock(&existing->mu);
                pthread_mutex_unlock(&g_vless_table_mu);
            }
            return send_http_error(client_fd, 403, "Forbidden", "destination forbidden", req->keep_alive);
        }

        int err_code = 0;
        vless_session_t *sess = get_or_create_vless_session(sid, client_ip, auth_rc != 1, cfg, &err_code);
        if (!sess) {
            if (err_code == 429) {
                return send_http_error(client_fd, 429, "Too Many Requests", "session rate limit", false);
            }
            return send_http_error(client_fd, 503, "Service Unavailable", "session limit", false);
        }
        if (auth_rc == 1) {
            bool uuid_conflict = false;
            pthread_mutex_lock(&g_vless_table_mu);
            pthread_mutex_lock(&sess->mu);
            if (sess->authed && !ct_memeq(sess->client_uuid, req->body + 1, 16)) {
                uuid_conflict = true;
            } else if (!sess->closed) {
                sess->authed = true;
                sess->pre_auth_bytes = 0;
                memcpy(sess->client_uuid, req->body + 1, 16);
            }
            pthread_mutex_unlock(&sess->mu);
            pthread_mutex_unlock(&g_vless_table_mu);
            if (uuid_conflict) {
                return send_http_error(client_fd, 403, "Forbidden", "session owner mismatch", req->keep_alive);
            }
        }
        pthread_mutex_lock(&sess->mu);
        if (sess->closed || sess->up_pipe[1] < 0) {
            bool rev = __atomic_load_n(&sess->revoked, __ATOMIC_ACQUIRE);
            pthread_mutex_unlock(&sess->mu);
            if (rev) return send_http_error(client_fd, 403, "Forbidden", "session revoked", req->keep_alive);
            return send_http_error(client_fd, 410, "Gone", "session closed", req->keep_alive);
        }
        sess->last_active_ms = now_ms();
        uint64_t my_gen = sess->generation;
        int wpipe = sess->up_pipe[1];
        sess->up_writers++;
        sess->stream_up_fd = client_fd;
        pthread_mutex_unlock(&sess->mu);

        if (wpipe >= 0 && req->body_len > 0) {
            (void)write_vless_pipe_raw_cancellable(sess, my_gen, wpipe, req->body, req->body_len, 15000);
        }
        if (wpipe >= 0 && req->chunked_incomplete && cr) {
            pump_remaining_http_chunks(cr, wpipe);
        }

        pthread_mutex_lock(&sess->mu);
        sess->stream_up_fd = -1;
        sess->up_writers--;
        bool sess_closed = sess->closed;
        bool sess_revoked = __atomic_load_n(&sess->revoked, __ATOMIC_ACQUIRE);
        if (sess->up_writers == 0 && sess->up_pipe[1] >= 0) {
            close(sess->up_pipe[1]);
            sess->up_pipe[1] = -1;
        }
        pthread_mutex_unlock(&sess->mu);

        if (sess_revoked) {
            (void)send_http_error(client_fd, 403, "Forbidden", "session revoked", false);
            return -1;
        }

        bool keep_conn = req->keep_alive && !sess_closed;
        char ok_hdr[256];
        int hlen = snprintf(ok_hdr, sizeof(ok_hdr),
            "HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: %s\r\n\r\n",
            keep_conn ? "keep-alive" : "close");
        if (hlen < 0 || (size_t)hlen >= sizeof(ok_hdr)) return -1;
        if (write_all(client_fd, ok_hdr, (size_t)hlen) < 0) return -1;
        /* Close the socket after SHUT_RD so the worker exits cleanly instead of polling EOF. */
        return sess_closed ? -1 : 0;
    }

    /* 4. stream-one POST (no sid, no seq) */
    if (strcmp(req->method, "POST") == 0 && !sid[0]) {
        vless_req_t vreq_one;
        int prc = parse_vless_header(req->body, req->body_len, cfg, &vreq_one);
        if (prc == -2) {
            return send_http_error(client_fd, 403, "Forbidden", "unauthorized uuid", req->keep_alive);
        }
        if (prc != 0) {
            return send_http_error(client_fd, 400, "Bad Request", "invalid vless header", req->keep_alive);
        }
        if (vreq_one.command != VLESS_CMD_MUX &&
            !check_destination_acl_ex(vreq_one.host, cfg->allow_private, cfg->egress_mode == EGRESS_MODE_ROUTER)) {
            return send_http_error(client_fd, 403, "Forbidden", "destination forbidden", req->keep_alive);
        }
        /* The slot is taken before the 200 goes out: no slot means no stream (revocation
         * finds streams only through their slot), and the per-IP cap answers 429. */
        int slot_idx = -1;
        bool over_ip_cap = false;
        pthread_mutex_lock(&g_vless_table_mu);
        int ip_conns = count_stream_one_for_ip_locked(client_ip);
        for (int i = 0; i < MAX_VLESS_SESSIONS; i++) {
            vless_session_t *cand = &g_vless_sessions[i];
            pthread_mutex_lock(&cand->mu);
            if (cand->active && !cand->closed && strcmp(cand->client_ip, client_ip) == 0) ip_conns++;
            pthread_mutex_unlock(&cand->mu);
        }
        if (ip_conns >= vless_per_ip_conn_limit(cfg)) {
            over_ip_cap = true;
        } else {
            for (int i = 0; i < MAX_STREAM_ONE_SLOTS; i++) {
                if (!g_stream_one_slots[i].active) {
                    slot_idx = i;
                    g_stream_one_slots[i].active = true;
                    __atomic_store_n(&g_stream_one_slots[i].revoked, false, __ATOMIC_RELEASE);
                    g_stream_one_slots[i].client_fd = client_fd;
                    memcpy(g_stream_one_slots[i].uuid, vreq_one.uuid, 16);
                    snprintf(g_stream_one_slots[i].client_ip, sizeof(g_stream_one_slots[i].client_ip), "%s", client_ip);
                    break;
                }
            }
        }
        pthread_mutex_unlock(&g_vless_table_mu);
        /* -1 closes the connection: the rest of a duplex (chunked) body must not be parsed
         * as the next request */
        if (over_ip_cap) {
            (void)send_http_error(client_fd, 429, "Too Many Requests", "per-ip connection limit", false);
            return -1;
        }
        if (slot_idx < 0) {
            (void)send_http_error(client_fd, 503, "Service Unavailable", "stream limit", false);
            return -1;
        }
        const bool *abort_ptr = &g_stream_one_slots[slot_idx].revoked;

        int spipe[2] = { -1, -1 };
        if (pipe(spipe) < 0 || send_xhttp_stream_headers(client_fd) < 0) {
            if (spipe[0] >= 0) { close(spipe[0]); close(spipe[1]); }
            pthread_mutex_lock(&g_vless_table_mu);
            g_stream_one_slots[slot_idx].active = false;
            g_stream_one_slots[slot_idx].client_fd = -1;
            pthread_mutex_unlock(&g_vless_table_mu);
            return -1;
        }

        bool spawned_feed = false;
        pthread_t feed_tid;
        stream_feed_arg_t feed_arg = {
            .initial_body       = req->body,
            .initial_len        = req->body_len,
            .cr                 = cr,
            .chunked_incomplete = req->chunked_incomplete,
            .pipe_w             = spipe[1],
        };
        if (req->chunked_incomplete || req->body_len > 32768) {
            pthread_attr_t attr;
            pthread_attr_init(&attr);
            pthread_attr_setstacksize(&attr, XHTTPD_HELPER_STACK);
            if (pthread_create(&feed_tid, &attr, stream_feed_worker, &feed_arg) == 0) {
                spawned_feed = true;
            } else {
                close(spipe[1]);
            }
            pthread_attr_destroy(&attr);
        } else {
            if (req->body_len > 0) {
                write_all(spipe[1], req->body, req->body_len);
            }
            close(spipe[1]);
        }

        run_vless_stream(spipe[0], client_fd, cfg, NULL, abort_ptr);
        close(spipe[0]);
        if (spawned_feed) {
            shutdown(client_fd, SHUT_RD);
            pthread_join(feed_tid, NULL);
        }
        bool was_revoked = __atomic_load_n(&g_stream_one_slots[slot_idx].revoked, __ATOMIC_ACQUIRE);
        pthread_mutex_lock(&g_vless_table_mu);
        g_stream_one_slots[slot_idx].active = false;
        g_stream_one_slots[slot_idx].client_fd = -1;
        pthread_mutex_unlock(&g_vless_table_mu);
        if (!was_revoked) {
            write_http_chunk_end(client_fd);
        }
        return -1;
    }

    return send_http_error(client_fd, 404, "Not Found", "not found", req->keep_alive);
}

static bool is_uuid_in_cfg(const uint8_t uuid[16], const xhttpd_config_t *cfg) {
    if (!cfg) return false;
    for (int i = 0; i < cfg->num_clients; i++) {
        if (ct_memeq(uuid, cfg->clients[i].uuid, 16)) return true;
    }
    return false;
}

void vless_engine_revoke_unauthorised(const xhttpd_config_t *cfg) {
    uint64_t now = now_ms();
    pthread_mutex_lock(&g_vless_table_mu);
    for (int i = 0; i < MAX_VLESS_SESSIONS; i++) {
        vless_session_t *s = &g_vless_sessions[i];
        pthread_mutex_lock(&s->mu);
        if (!s->active) {
            pthread_mutex_unlock(&s->mu);
            continue;
        }
        if (s->authed && !is_uuid_in_cfg(s->client_uuid, cfg)) {
            __atomic_store_n(&s->revoked, true, __ATOMIC_RELEASE);
            record_recent_sid_locked(g_revoked_sids, &g_revoked_sid_idx, s->sid, now);
            close_vless_session_locked(s);
        } else if (!s->authed && (!cfg || cfg->num_clients == 0)) {
            __atomic_store_n(&s->revoked, true, __ATOMIC_RELEASE);
            close_vless_session_locked(s);
        }
        pthread_mutex_unlock(&s->mu);
    }
    for (int i = 0; i < MAX_STREAM_ONE_SLOTS; i++) {
        if (g_stream_one_slots[i].active && !is_uuid_in_cfg(g_stream_one_slots[i].uuid, cfg)) {
            __atomic_store_n(&g_stream_one_slots[i].revoked, true, __ATOMIC_RELEASE);
            if (g_stream_one_slots[i].client_fd >= 0) {
                shutdown(g_stream_one_slots[i].client_fd, SHUT_RDWR);
            }
        }
    }
    pthread_mutex_unlock(&g_vless_table_mu);
}

void vless_engine_get_stats(int *active_sessions, int *pre_auth_sessions, size_t *buffered_bytes, size_t *pre_auth_bytes) {
    int act = 0, pa = 0;
    size_t buf = 0, pa_buf = 0;
    pthread_mutex_lock(&g_vless_table_mu);
    for (int i = 0; i < MAX_VLESS_SESSIONS; i++) {
        vless_session_t *s = &g_vless_sessions[i];
        pthread_mutex_lock(&s->mu);
        if (s->active && !s->closed) {
            act++;
            if (!s->authed) {
                pa++;
                pa_buf += s->pre_auth_bytes;
            }
            for (int j = 0; j < MAX_PENDING_SEQ; j++) {
                if (s->pending[j].used) {
                    buf += s->pending[j].len;
                }
            }
        }
        pthread_mutex_unlock(&s->mu);
    }
    pthread_mutex_unlock(&g_vless_table_mu);
    if (active_sessions) *active_sessions = act;
    if (pre_auth_sessions) *pre_auth_sessions = pa;
    if (buffered_bytes) *buffered_bytes = buf + pa_buf;
    if (pre_auth_bytes) *pre_auth_bytes = pa_buf;
}

void vless_engine_reap_stale(void) {
    uint64_t now = now_ms();
    const xhttpd_config_t *cfg = xhttpd_cfg_acquire();
    uint64_t pa_ttl = (cfg && cfg->pre_auth_ttl_ms > 0) ? cfg->pre_auth_ttl_ms : 10000ULL;
    uint64_t est_ttl = (uint64_t)((cfg && cfg->idle_timeout_sec > 5) ? cfg->idle_timeout_sec : 120) * 1000ULL;
    xhttpd_cfg_release(cfg);

    pthread_mutex_lock(&g_vless_table_mu);
    for (int i = 0; i < MAX_VLESS_SESSIONS; i++) {
        vless_session_t *s = &g_vless_sessions[i];
        pthread_mutex_lock(&s->mu);
        if (!s->active) {
            pthread_mutex_unlock(&s->mu);
            continue;
        }
        uint64_t idle = now > s->last_active_ms ? (now - s->last_active_ms) : 0;
        uint64_t limit = est_ttl;
        if (!s->authed && !s->has_down) {
            limit = pa_ttl;
        } else if (!s->has_down) {
            limit = 10000ULL;
        }
        if (idle > limit) {
            close_vless_session_locked(s);
        }
        pthread_mutex_unlock(&s->mu);
    }
    pthread_mutex_unlock(&g_vless_table_mu);
}

void vless_engine_close_all(void) {
    pthread_mutex_lock(&g_vless_table_mu);
    for (int i = 0; i < MAX_VLESS_SESSIONS; i++) {
        pthread_mutex_lock(&g_vless_sessions[i].mu);
        if (g_vless_sessions[i].active) {
            close_vless_session_locked(&g_vless_sessions[i]);
        }
        pthread_mutex_unlock(&g_vless_sessions[i].mu);
    }
    pthread_mutex_unlock(&g_vless_table_mu);
}
