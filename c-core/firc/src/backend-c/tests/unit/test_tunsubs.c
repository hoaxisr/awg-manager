#define _GNU_SOURCE /* NOLINT(bugprone-reserved-identifier) */

#include "firc/tunsubs.h"

#include <arpa/inet.h>
#include <dirent.h>
#include <errno.h>
#include <netinet/in.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <time.h>
#include <unistd.h>

#include "fake_rtnl.h"
#include "firc/sub_fetch.h"
#include "firc/tunrun.h"
#include "greatest.h"

static const char *k_two_nodes =
    "vless://11111111-1111-1111-1111-111111111111@203.0.113.1:443?encryption=none&type=tcp#DE-1\n"
    "vless://11111111-1111-1111-1111-111111111111@203.0.113.2:443?encryption=none&type=tcp#NL-2\n";

static const char *k_three_nodes =
    "vless://11111111-1111-1111-1111-111111111111@203.0.113.1:443?encryption=none&type=tcp#DE-1\n"
    "vless://11111111-1111-1111-1111-111111111111@203.0.113.2:443?encryption=none&type=tcp#NL-2\n"
    "vless://11111111-1111-1111-1111-111111111111@203.0.113.3:443?encryption=none&type=tcp#PL-3\n";

typedef struct {
    int listen_fd;
    uint16_t port;
    pthread_t thr;
    pthread_mutex_t mu;
    bool running;
    bool stop;
    int status;
    const char *body;
    int hits;
} mock_http_t;

typedef struct {
    mock_http_t srv;
    char dir[64];
    bool has_dir;
    firc_loop_t *loop;
    fake_rtnl_t *frtnl;
    firc_rtnl_t *rtnl;
    firc_tunrun_t *run;
    firc_tunsubs_t *subs;
} test_env_t;

static test_env_t g_env;
static _Atomic uint32_t g_seen_mark;
static _Atomic int g_mark_calls;

static bool send_all(int fd, const char *buf, size_t len)
{
    size_t off = 0;
    while (off < len) {
        ssize_t w = send(fd, buf + off, len - off, MSG_NOSIGNAL);
        if (w < 0 && errno == EINTR) {
            continue;
        }
        if (w <= 0) {
            return false;
        }
        off += (size_t)w;
    }
    return true;
}

static void drain_request(int fd)
{
    char req[4096];
    size_t used = 0;
    while (used + 1 < sizeof req) {
        ssize_t r = recv(fd, req + used, sizeof req - 1 - used, 0);
        if (r < 0 && errno == EINTR) {
            continue;
        }
        if (r <= 0) {
            break;
        }
        used += (size_t)r;
        req[used] = '\0';
        if (strstr(req, "\r\n\r\n") != NULL) {
            break;
        }
    }
}

static void *http_thread(void *ud)
{
    mock_http_t *m = ud;
    for (;;) {
        struct sockaddr_in sa;
        socklen_t sl = sizeof sa;
        int cfd = accept(m->listen_fd, (struct sockaddr *)&sa, &sl);
        if (cfd < 0) {
            if (errno == EINTR) {
                continue;
            }
            return NULL;
        }
        pthread_mutex_lock(&m->mu);
        bool stop = m->stop;
        int status = m->status;
        const char *body = m->body != NULL ? m->body : "";
        if (!stop) {
            m->hits++;
        }
        pthread_mutex_unlock(&m->mu);
        if (stop) {
            close(cfd);
            return NULL;
        }
        drain_request(cfd);
        char hdr[256];
        size_t blen = strlen(body);
        const char *reason = status == 200 ? "OK" : "Service Unavailable";
        int hlen = snprintf(hdr, sizeof hdr,
                            "HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nContent-Length: %zu\r\n"
                            "Connection: close\r\n\r\n",
                            status, reason, blen);
        if (hlen > 0 && (size_t)hlen < sizeof hdr && send_all(cfd, hdr, (size_t)hlen)) {
            (void)send_all(cfd, body, blen);
        }
        (void)shutdown(cfd, SHUT_WR);
        close(cfd);
    }
}

static bool mock_http_start(mock_http_t *m, int status, const char *body)
{
    memset(m, 0, sizeof *m);
    m->listen_fd = -1;
    m->status = status;
    m->body = body;
    if (pthread_mutex_init(&m->mu, NULL) != 0) {
        return false;
    }
    int fd = socket(AF_INET, SOCK_STREAM, 0);
    if (fd < 0) {
        pthread_mutex_destroy(&m->mu);
        return false;
    }
    int one = 1;
    (void)setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &one, sizeof one);
    struct sockaddr_in sa;
    memset(&sa, 0, sizeof sa);
    sa.sin_family = AF_INET;
    sa.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    sa.sin_port = 0;
    if (bind(fd, (struct sockaddr *)&sa, sizeof sa) != 0 || listen(fd, 8) != 0) {
        close(fd);
        pthread_mutex_destroy(&m->mu);
        return false;
    }
    socklen_t sl = sizeof sa;
    if (getsockname(fd, (struct sockaddr *)&sa, &sl) != 0) {
        close(fd);
        pthread_mutex_destroy(&m->mu);
        return false;
    }
    m->listen_fd = fd;
    m->port = ntohs(sa.sin_port);
    if (pthread_create(&m->thr, NULL, http_thread, m) != 0) {
        close(fd);
        m->listen_fd = -1;
        pthread_mutex_destroy(&m->mu);
        return false;
    }
    m->running = true;
    return true;
}

static void mock_http_set(mock_http_t *m, int status, const char *body)
{
    pthread_mutex_lock(&m->mu);
    m->status = status;
    m->body = body;
    pthread_mutex_unlock(&m->mu);
}

static int mock_http_hits(mock_http_t *m)
{
    pthread_mutex_lock(&m->mu);
    int h = m->hits;
    pthread_mutex_unlock(&m->mu);
    return h;
}

static void mock_http_stop(mock_http_t *m)
{
    if (!m->running) {
        return;
    }
    pthread_mutex_lock(&m->mu);
    m->stop = true;
    pthread_mutex_unlock(&m->mu);
    int wake = socket(AF_INET, SOCK_STREAM, 0);
    if (wake >= 0) {
        struct sockaddr_in sa;
        memset(&sa, 0, sizeof sa);
        sa.sin_family = AF_INET;
        sa.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
        sa.sin_port = htons(m->port);
        (void)connect(wake, (struct sockaddr *)&sa, sizeof sa);
        close(wake);
    }
    pthread_join(m->thr, NULL);
    close(m->listen_fd);
    m->listen_fd = -1;
    m->running = false;
    pthread_mutex_destroy(&m->mu);
}

static void cleanup_dir(const char *dir)
{
    DIR *d = opendir(dir);
    if (d != NULL) {
        struct dirent *de;
        while ((de = readdir(d)) != NULL) {
            if (strcmp(de->d_name, ".") == 0 || strcmp(de->d_name, "..") == 0) {
                continue;
            }
            char path[600];
            snprintf(path, sizeof path, "%s/%s", dir, de->d_name);
            (void)unlink(path);
        }
        closedir(d);
    }
    (void)rmdir(dir);
}

static void teardown_env(void *ud)
{
    (void)ud;
    if (g_env.run != NULL) {
        firc_tunrun_set_subs(g_env.run, NULL);
    }
    if (g_env.subs != NULL) {
        firc_tunsubs_free(g_env.subs);
        g_env.subs = NULL;
    }
    firc_sub_fetch_set_setsockopt_for_test(NULL);
    if (g_env.run != NULL) {
        firc_tunrun_free(g_env.run);
        g_env.run = NULL;
    }
    if (g_env.rtnl != NULL) {
        firc_rtnl_close(g_env.rtnl);
        g_env.rtnl = NULL;
    }
    if (g_env.frtnl != NULL) {
        fake_rtnl_stop(g_env.frtnl);
        g_env.frtnl = NULL;
    }
    if (g_env.loop != NULL) {
        firc_loop_destroy(g_env.loop);
        g_env.loop = NULL;
    }
    mock_http_stop(&g_env.srv);
    if (g_env.has_dir) {
        cleanup_dir(g_env.dir);
        g_env.has_dir = false;
    }
}

static bool init_env(int status, const char *body, void (*changed)(void *), void *ud)
{
    memset(&g_env, 0, sizeof g_env);
    if (!mock_http_start(&g_env.srv, status, body)) {
        return false;
    }
    snprintf(g_env.dir, sizeof g_env.dir, "/tmp/firc-test-tunsubs-XXXXXX");
    if (mkdtemp(g_env.dir) == NULL) {
        return false;
    }
    g_env.has_dir = true;
    if (firc_loop_create(&g_env.loop) != FIRC_OK) {
        return false;
    }
    g_env.subs = firc_tunsubs_new(g_env.loop, g_env.dir, changed, ud);
    return g_env.subs != NULL;
}

static void on_changed(void *ud)
{
    int *count = ud;
    (*count)++;
}

static void make_sub_tunnel(firc_tunnel_t *t, firc_tun_src_t *src, const char *id, const char *dev, bool enable,
                            firc_uplink_kind_t uplink, const char *uplink_ref, char *url, int interval_s)
{
    memset(t, 0, sizeof *t);
    memset(src, 0, sizeof *src);
    snprintf(t->id, sizeof t->id, "%s", id);
    snprintf(t->device, sizeof t->device, "%s", dev);
    t->enable = enable;
    t->uplink = uplink;
    if (uplink_ref != NULL) {
        snprintf(t->uplink_ref, sizeof t->uplink_ref, "%s", uplink_ref);
    }
    snprintf(src->id, sizeof src->id, "0b47b9d5");
    src->kind = FIRC_TUN_SRC_SUB;
    snprintf(src->sub.name, sizeof src->sub.name, "Sub");
    src->sub.url = url;
    src->sub.interval_s = interval_s;
    t->src = src;
    t->n_src = 1;
}

static void stop_loop_cb(firc_loop_t *loop, void *ud)
{
    (void)ud;
    firc_loop_stop(loop);
}

static void pump_ms(firc_loop_t *loop, uint64_t ms)
{
    int tid = 0;
    if (firc_loop_add_timer(loop, ms, 0, stop_loop_cb, NULL, &tid) == FIRC_OK) {
        (void)firc_loop_run(loop);
    }
}

static bool pump_until_idle(firc_loop_t *loop, firc_tunsubs_t *s, const char *url, uint32_t mark, uint64_t hsig,
                            int max_ms)
{
    int waited = 0;
    while (waited < max_ms) {
        pump_ms(loop, 20);
        firc_tun_sub_state_t st;
        if (firc_tunsubs_state(s, url, mark, hsig, &st) == 1 && !st.fetching && st.last_try_ms > 0) {
            return true;
        }
        waited += 20;
    }
    return false;
}

static bool pump_until_changes(firc_loop_t *loop, const int *changes, int want, int max_ms)
{
    int waited = 0;
    while (waited < max_ms) {
        pump_ms(loop, 20);
        if (*changes >= want) {
            return true;
        }
        waited += 20;
    }
    return false;
}

static int fake_setsockopt_record(int fd, int level, int optname, const void *optval, socklen_t optlen)
{
    (void)fd;
    if (level == SOL_SOCKET && optname == SO_MARK && optval != NULL && optlen == sizeof(uint32_t)) {
        uint32_t val = 0;
        memcpy(&val, optval, sizeof val);
        atomic_store(&g_seen_mark, val);
        atomic_fetch_add(&g_mark_calls, 1);
    }
    return 0;
}

TEST disabled_auto_fetches_once_refreshes_on_demand_and_never_polls(void)
{
    int changes = 0;
    ASSERT(init_env(200, k_two_nodes, on_changed, &changes));

    char url[128];
    snprintf(url, sizeof url, "http://127.0.0.1:%u/sub", (unsigned)g_env.srv.port);
    firc_tunnel_t tun;
    firc_tun_src_t src;
    make_sub_tunnel(&tun, &src, "tun_off", "tunvless1", false, FIRC_UPLINK_AUTO, NULL, url, 60);
    firc_tunnels_t set = {.t = &tun, .n = 1};
    uint32_t marks[1] = {0};

    firc_tunsubs_want(g_env.subs, &set, marks);
    ASSERT(pump_until_changes(g_env.loop, &changes, 1, 3000));
    ASSERT_EQ(1, mock_http_hits(&g_env.srv));

    firc_tun_sub_state_t st;
    ASSERT_EQ(1u, firc_tunsubs_state(g_env.subs, url, 0, firc_tun_sub_headers_sig(&src.sub), &st));
    ASSERT_FALSE(st.fetching);
    ASSERT_EQ(2u, st.nodes);
    ASSERT(st.last_ok_ms > 0);
    ASSERT_STR_EQ("", st.error);

    size_t nb = 0;
    const firc_tun_body_t *bodies = firc_tunsubs_bodies(g_env.subs, &nb);
    ASSERT_EQ(1u, nb);
    ASSERT_EQ(0u, bodies[0].mark);
    firc_tun_nodes_t nodes;
    ASSERT_EQ(FIRC_OK, firc_tun_nodes_build(&tun, 0, bodies, nb, &nodes));
    size_t n_nodes = nodes.n;
    firc_tun_nodes_free(&nodes);
    ASSERT_EQ(2u, n_nodes);

    /* Calling want() again or advancing past interval must NOT re-fetch a disabled tunnel. */
    firc_tunsubs_want(g_env.subs, &set, marks);
    firc_tunsubs_advance_for_test(g_env.subs, 3600LL * 1000LL);
    firc_tunsubs_due(g_env.subs);
    pump_ms(g_env.loop, 120);
    ASSERT_EQ(1, mock_http_hits(&g_env.srv));

    /* Explicit manual refresh MUST enqueue a fetch even while disabled (uplink == auto). */
    mock_http_set(&g_env.srv, 200, k_three_nodes);
    ASSERT_EQ(FIRC_OK, firc_tunsubs_refresh(g_env.subs, "tun_off"));
    ASSERT(pump_until_changes(g_env.loop, &changes, 2, 3000));
    ASSERT_EQ(2, mock_http_hits(&g_env.srv));
    ASSERT_EQ(1u, firc_tunsubs_state(g_env.subs, url, 0, firc_tun_sub_headers_sig(&src.sub), &st));
    ASSERT_EQ(3u, st.nodes);

    /* Recreating tunsubs against the same cache dir must load cached nodes without network fetch. */
    firc_tunsubs_free(g_env.subs);
    g_env.subs = NULL;
    int changes2 = 0;
    g_env.subs = firc_tunsubs_new(g_env.loop, g_env.dir, on_changed, &changes2);
    ASSERT(g_env.subs != NULL);
    firc_tunsubs_want(g_env.subs, &set, marks);
    pump_ms(g_env.loop, 120);
    ASSERT_EQ(2, mock_http_hits(&g_env.srv));
    ASSERT_EQ(0, changes2);
    ASSERT_EQ(1u, firc_tunsubs_state(g_env.subs, url, 0, firc_tun_sub_headers_sig(&src.sub), &st));
    ASSERT_FALSE(st.fetching);
    ASSERT_EQ(3u, st.nodes);
    bodies = firc_tunsubs_bodies(g_env.subs, &nb);
    ASSERT_EQ(1u, nb);
    PASS();
}

TEST disabled_non_auto_uplink_never_fetches_or_steals_auto_bodies(void)
{
    int changes = 0;
    ASSERT(init_env(200, k_two_nodes, on_changed, &changes));
    g_env.frtnl = fake_rtnl_start(&g_env.rtnl);
    ASSERT(g_env.frtnl != NULL);

    /* Wire a real tunrun + tunsubs so tunrun_bodies_changed updates sent[] on fetch completion. */
    firc_tunsubs_free(g_env.subs);
    g_env.subs = NULL;
    g_env.run = firc_tunrun_new(g_env.loop, g_env.rtnl, 100, "/bin/true", NULL);
    ASSERT(g_env.run != NULL);
    g_env.subs = firc_tunsubs_new(g_env.loop, g_env.dir, firc_tunrun_bodies_changed, g_env.run);
    ASSERT(g_env.subs != NULL);
    firc_tunrun_set_subs(g_env.run, g_env.subs);

    char yaml[1024];
    snprintf(yaml, sizeof yaml,
             "tunnels:\n"
             "  - id: tun_auto\n"
             "    device: tunvless1\n"
             "    enable: false\n"
             "    uplink: auto\n"
             "    sources:\n"
             "      - id: 0b47b9d5\n"
             "        subscription:\n"
             "          name: Sub\n"
             "          url: http://127.0.0.1:%u/shared-sub\n"
             "          interval: 600s\n"
             "  - id: tun_wg\n"
             "    device: tunvless2\n"
             "    enable: false\n"
             "    uplink: iface:wg0\n"
             "    sources:\n"
             "      - id: 11223344\n"
             "        subscription:\n"
             "          name: Sub\n"
             "          url: http://127.0.0.1:%u/shared-sub\n"
             "          interval: 600s\n",
             (unsigned)g_env.srv.port, (unsigned)g_env.srv.port);
    firc_tunnels_t want;
    firc_tun_err_t perr;
    ASSERT_EQ(FIRC_OK, firc_tunnels_load_buffer(&want, yaml, strlen(yaml), &perr));
    firc_err_t apply_err = firc_tunrun_apply(g_env.run, &want);
    firc_tunnels_free(&want);
    ASSERT_EQ(FIRC_OK, apply_err);

    char url[128];
    snprintf(url, sizeof url, "http://127.0.0.1:%u/shared-sub", (unsigned)g_env.srv.port);
    uint64_t hsig = firc_tun_sub_headers_sig(&firc_tunrun_config(g_env.run)->t[0].src[0].sub);
    ASSERT(pump_until_idle(g_env.loop, g_env.subs, url, 0, hsig, 3000));
    ASSERT_EQ(1, mock_http_hits(&g_env.srv));

    /* Auto-uplink disabled tunnel sees its 2 fetched nodes; non-auto disabled tunnel must NOT steal them. */
    const firc_tun_nodes_t *auto_nodes = firc_tunrun_nodes(g_env.run, "tun_auto");
    const firc_tun_nodes_t *wg_nodes = firc_tunrun_nodes(g_env.run, "tun_wg");
    ASSERT(auto_nodes != NULL);
    ASSERT_EQ(2u, auto_nodes->n);
    ASSERT(wg_nodes != NULL);
    ASSERT_EQ(0u, wg_nodes->n);

    /* Manual refresh of the disabled non-auto tunnel is refused (no WAN leak). */
    ASSERT_EQ(FIRC_ERR_NOENT, firc_tunsubs_refresh(g_env.subs, "tun_wg"));
    ASSERT_EQ(1, mock_http_hits(&g_env.srv));
    PASS();
}

TEST enabled_non_auto_with_zero_mark_never_fetches_over_wan(void)
{
    int changes = 0;
    ASSERT(init_env(200, k_two_nodes, on_changed, &changes));

    char url[128];
    snprintf(url, sizeof url, "http://127.0.0.1:%u/zero-mark", (unsigned)g_env.srv.port);
    firc_tunnel_t tun;
    firc_tun_src_t src;
    make_sub_tunnel(&tun, &src, "tun_nomark", "tunvless2", true, FIRC_UPLINK_IFACE, "wg0", url, 60);
    firc_tunnels_t set = {.t = &tun, .n = 1};
    uint32_t marks[1] = {0};

    firc_tunsubs_want(g_env.subs, &set, marks);
    pump_ms(g_env.loop, 120);
    ASSERT_EQ(0, mock_http_hits(&g_env.srv));
    ASSERT_EQ(FIRC_ERR_NOENT, firc_tunsubs_refresh(g_env.subs, "tun_nomark"));

    size_t nb = 0;
    (void)firc_tunsubs_bodies(g_env.subs, &nb);
    ASSERT_EQ(0u, nb);
    PASS();
}

TEST disabled_auto_error_is_reported_without_background_retry(void)
{
    int changes = 0;
    ASSERT(init_env(503, "down", on_changed, &changes));

    char url[128];
    snprintf(url, sizeof url, "http://127.0.0.1:%u/err-sub", (unsigned)g_env.srv.port);
    firc_tunnel_t tun;
    firc_tun_src_t src;
    make_sub_tunnel(&tun, &src, "tun_err", "tunvless3", false, FIRC_UPLINK_AUTO, NULL, url, 60);
    firc_tunnels_t set = {.t = &tun, .n = 1};
    uint32_t marks[1] = {0};
    uint64_t hsig = firc_tun_sub_headers_sig(&src.sub);

    firc_tunsubs_want(g_env.subs, &set, marks);
    ASSERT(pump_until_idle(g_env.loop, g_env.subs, url, 0, hsig, 3000));
    ASSERT_EQ(1, mock_http_hits(&g_env.srv));

    firc_tun_sub_state_t st;
    ASSERT_EQ(1u, firc_tunsubs_state(g_env.subs, url, 0, hsig, &st));
    ASSERT_STR_EQ("HTTP 503", st.error);
    ASSERT_EQ(0u, st.nodes);

    /* Advancing past RETRY_MS must NOT auto-retry a disabled tunnel. */
    firc_tunsubs_advance_for_test(g_env.subs, 120000);
    firc_tunsubs_due(g_env.subs);
    pump_ms(g_env.loop, 120);
    ASSERT_EQ(1, mock_http_hits(&g_env.srv));

    /* Manual refresh DOES retry and recovers when the server comes back. */
    mock_http_set(&g_env.srv, 200, k_two_nodes);
    ASSERT_EQ(FIRC_OK, firc_tunsubs_refresh(g_env.subs, "tun_err"));
    ASSERT(pump_until_changes(g_env.loop, &changes, 1, 3000));
    ASSERT_EQ(2, mock_http_hits(&g_env.srv));
    ASSERT_EQ(1u, firc_tunsubs_state(g_env.subs, url, 0, hsig, &st));
    ASSERT_STR_EQ("", st.error);
    ASSERT_EQ(2u, st.nodes);
    PASS();
}

TEST enabling_previously_disabled_auto_tunnel_activates_polling(void)
{
    int changes = 0;
    ASSERT(init_env(200, k_two_nodes, on_changed, &changes));

    char url[128];
    snprintf(url, sizeof url, "http://127.0.0.1:%u/promote-sub", (unsigned)g_env.srv.port);
    firc_tunnel_t tun;
    firc_tun_src_t src;
    make_sub_tunnel(&tun, &src, "tun_prom", "tunvless4", false, FIRC_UPLINK_AUTO, NULL, url, 60);
    firc_tunnels_t set = {.t = &tun, .n = 1};
    uint32_t marks[1] = {0};

    firc_tunsubs_want(g_env.subs, &set, marks);
    ASSERT(pump_until_changes(g_env.loop, &changes, 1, 3000));
    ASSERT_EQ(1, mock_http_hits(&g_env.srv));

    /* Enabling the tunnel starts an active refresh and allows subsequent due() polls. */
    mock_http_set(&g_env.srv, 200, k_three_nodes);
    tun.enable = true;
    firc_tunsubs_want(g_env.subs, &set, marks);
    ASSERT(pump_until_changes(g_env.loop, &changes, 2, 3000));
    ASSERT_EQ(2, mock_http_hits(&g_env.srv));

    mock_http_set(&g_env.srv, 200, k_two_nodes);
    firc_tunsubs_advance_for_test(g_env.subs, 61000);
    firc_tunsubs_due(g_env.subs);
    ASSERT(pump_until_changes(g_env.loop, &changes, 3, 3000));
    ASSERT_EQ(3, mock_http_hits(&g_env.srv));

    /* Enabled non-auto tunnel sets its own SO_MARK even alongside a disabled auto tunnel. */
    atomic_store(&g_seen_mark, 0u);
    atomic_store(&g_mark_calls, 0);
    firc_sub_fetch_set_setsockopt_for_test(fake_setsockopt_record);
    firc_tunnel_t pair[2];
    firc_tun_src_t srcs[2];
    make_sub_tunnel(&pair[0], &srcs[0], "tun_a", "tunvless5", false, FIRC_UPLINK_AUTO, NULL, url, 60);
    make_sub_tunnel(&pair[1], &srcs[1], "tun_b", "tunvless6", true, FIRC_UPLINK_IFACE, "wg0", url, 60);
    firc_tunnels_t two = {.t = pair, .n = 2};
    uint32_t pair_marks[2] = {0, 0x42};
    firc_tunsubs_want(g_env.subs, &two, pair_marks);
    ASSERT(pump_until_idle(g_env.loop, g_env.subs, url, 0x42, firc_tun_sub_headers_sig(&srcs[1].sub), 3000));
    ASSERT_EQ(4, mock_http_hits(&g_env.srv));
    ASSERT_EQ(1, atomic_load(&g_mark_calls));
    ASSERT_EQ(0x42u, atomic_load(&g_seen_mark));
    PASS();
}

SUITE(tunsubs_suite)
{
    GREATEST_SET_TEARDOWN_CB(teardown_env, NULL);
    RUN_TEST(disabled_auto_fetches_once_refreshes_on_demand_and_never_polls);
    RUN_TEST(disabled_non_auto_uplink_never_fetches_or_steals_auto_bodies);
    RUN_TEST(enabled_non_auto_with_zero_mark_never_fetches_over_wan);
    RUN_TEST(disabled_auto_error_is_reported_without_background_retry);
    RUN_TEST(enabling_previously_disabled_auto_tunnel_activates_polling);
}

GREATEST_MAIN_DEFS();

int main(int argc, char **argv)
{
    firc_sub_fetch_global_init();
    GREATEST_MAIN_BEGIN();
    RUN_SUITE(tunsubs_suite);
    firc_sub_fetch_global_cleanup();
    GREATEST_MAIN_END();
}
