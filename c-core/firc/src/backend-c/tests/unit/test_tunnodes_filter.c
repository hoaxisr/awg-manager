#include "firc/tunnodes.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define CHECK(cond)                                                                           \
    do {                                                                                      \
        if (!(cond)) {                                                                        \
            fprintf(stderr, "FAIL %s:%d: %s\n", __FILE__, __LINE__, #cond);                   \
            exit(1);                                                                          \
        }                                                                                     \
    } while (0)

uint64_t firc_tun_sub_headers_sig(const firc_tun_sub_t *sub)
{
    (void)sub;
    return 0;
}

typedef struct {
    firc_tunnel_t t;
    firc_tun_src_t src;
    firc_tun_body_t body;
    char url[64];
    char filter_buf[64];
    char exclude_buf[64];
} sub_fixture_t;

static void init_sub_fixture(sub_fixture_t *f, const char *sub_body, const char *filter,
                             const char *filter_exclude)
{
    memset(f, 0, sizeof *f);
    snprintf(f->t.id, sizeof f->t.id, "t1");
    snprintf(f->t.device, sizeof f->t.device, "tunvless0");
    f->t.enable = true;
    if (filter != NULL) {
        snprintf(f->filter_buf, sizeof f->filter_buf, "%s", filter);
        f->t.filter = f->filter_buf;
    }
    if (filter_exclude != NULL) {
        snprintf(f->exclude_buf, sizeof f->exclude_buf, "%s", filter_exclude);
        f->t.filter_exclude = f->exclude_buf;
    }

    snprintf(f->url, sizeof f->url, "https://sub.example.invalid/feed");
    snprintf(f->src.id, sizeof f->src.id, "sub1");
    f->src.kind = FIRC_TUN_SRC_SUB;
    snprintf(f->src.sub.name, sizeof f->src.sub.name, "Provider");
    f->src.sub.url = f->url;

    f->t.src = &f->src;
    f->t.n_src = 1;

    f->body.url = f->url;
    f->body.mark = 0;
    f->body.hsig = 0;
    f->body.body = sub_body;
    f->body.len = strlen(sub_body);
}

static void test_include_and_exclude_filters(void)
{
    const char *sub_body =
        "vless://11111111-1111-1111-1111-111111111111@203.0.113.1:443?encryption=none&type=tcp#DE-1\n"
        "vless://11111111-1111-1111-1111-111111111111@203.0.113.2:443?encryption=none&type=tcp#DE-RU-2\n"
        "vless://11111111-1111-1111-1111-111111111111@203.0.113.3:443?encryption=none&type=tcp#US-3\n";

    sub_fixture_t f;
    init_sub_fixture(&f, sub_body, "DE", "RU");

    firc_tun_nodes_t out;
    firc_err_t err = firc_tun_nodes_build(&f.t, 0, &f.body, 1, &out);
    CHECK(err == FIRC_OK);
    CHECK(out.total == 3);
    CHECK(out.matched == 1);
    CHECK(out.n == 1);
    CHECK(strcmp(out.v[0].name, "DE-1") == 0);
    CHECK(out.v[0].link != NULL);
    CHECK(out.n_filtered == 2);
    CHECK(strcmp(out.filtered[0].name, "DE-RU-2") == 0);
    CHECK(strcmp(out.filtered[1].name, "US-3") == 0);

    firc_tun_nodes_free(&out);
}

static void test_direct_link_bypasses_filters(void)
{
    char filter_buf[] = "DE";
    char exclude_buf[] = "RU";
    char link_buf[] =
        "vless://11111111-1111-1111-1111-111111111111@203.0.113.4:443?encryption=none&type=tcp#RU-Direct";

    firc_tunnel_t t;
    memset(&t, 0, sizeof t);
    snprintf(t.id, sizeof t.id, "t1");
    snprintf(t.device, sizeof t.device, "tunvless0");
    t.enable = true;
    t.filter = filter_buf;
    t.filter_exclude = exclude_buf;

    firc_tun_src_t src;
    memset(&src, 0, sizeof src);
    snprintf(src.id, sizeof src.id, "lnk1");
    src.kind = FIRC_TUN_SRC_LINK;
    src.link = link_buf;
    t.src = &src;
    t.n_src = 1;

    firc_tun_nodes_t out;
    firc_err_t err = firc_tun_nodes_build(&t, 0, NULL, 0, &out);
    CHECK(err == FIRC_OK);
    CHECK(out.total == 1);
    CHECK(out.matched == 1);
    CHECK(out.n == 1);
    CHECK(out.n_filtered == 0);
    CHECK(strcmp(out.v[0].name, "RU-Direct") == 0);
    CHECK(out.v[0].link != NULL);

    firc_tun_nodes_free(&out);
}

static void test_backtracking_include_filter_fails_closed(void)
{
    const char *sub_body =
        "vless://11111111-1111-1111-1111-111111111111@203.0.113.5:443?encryption=none&type=tcp#"
        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaac!\n";

    sub_fixture_t f;
    init_sub_fixture(&f, sub_body, "^(a|aa)+c$", NULL);

    firc_tun_nodes_t out;
    firc_err_t err = firc_tun_nodes_build(&f.t, 0, &f.body, 1, &out);
    CHECK(err == FIRC_ERR_LIMIT);
    CHECK(out.n == 0);
    CHECK(out.v == NULL);
    CHECK(out.n_filtered == 0);
    CHECK(out.filtered == NULL);
}

static void test_backtracking_exclude_filter_fails_closed(void)
{
    const char *sub_body =
        "vless://11111111-1111-1111-1111-111111111111@203.0.113.6:443?encryption=none&type=tcp#"
        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaac!\n";

    sub_fixture_t f;
    init_sub_fixture(&f, sub_body, NULL, "^(a|aa)+c$");

    firc_tun_nodes_t out;
    firc_err_t err = firc_tun_nodes_build(&f.t, 0, &f.body, 1, &out);
    CHECK(err == FIRC_ERR_LIMIT);
    CHECK(out.n == 0);
    CHECK(out.v == NULL);
    CHECK(out.n_filtered == 0);
    CHECK(out.filtered == NULL);
}

static void test_invalid_regex_returns_inval(void)
{
    const char *sub_body =
        "vless://11111111-1111-1111-1111-111111111111@203.0.113.7:443?encryption=none&type=tcp#DE-1\n";

    sub_fixture_t f;
    init_sub_fixture(&f, sub_body, NULL, "[unclosed");

    firc_tun_nodes_t out;
    firc_err_t err = firc_tun_nodes_build(&f.t, 0, &f.body, 1, &out);
    CHECK(err == FIRC_ERR_INVAL);
    CHECK(out.n == 0);
    CHECK(out.v == NULL);
}

int main(void)
{
    test_include_and_exclude_filters();
    test_direct_link_bypasses_filters();
    test_backtracking_include_filter_fails_closed();
    test_backtracking_exclude_filter_fails_closed();
    test_invalid_regex_returns_inval();
    return 0;
}
