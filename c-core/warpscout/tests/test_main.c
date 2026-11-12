/* Test-only WireGuard responder + round-trip test for awgm-warpscout.
 * Not linked into the production binary. */
/* main.c подключён целиком ради его static-функций; его main переименован */
#define main ws_main
#include "../src/main.c"
#undef main

typedef struct {
    uint8_t priv[32], pub[32];
    uint8_t C[32], H[32];
    uint8_t ei[32], si[32];
    uint32_t remote_idx, local_idx;
    uint8_t send_key[32], recv_key[32];
    uint64_t send_ctr;
} responder;

static int resp_consume_init(responder *r, const uint8_t *msg) {
    uint8_t k[32], dh[32], ts[12], mac1key[32], mac[16], m[WG_INIT_LEN];
    uint8_t c0[32];
    memcpy(m, msg, WG_INIT_LEN);
    m[1] = m[2] = m[3] = 0; /* reserved bytes are not covered (Xray/sing-box semantics) */
    if (m[0] != WG_MSG_INIT) return -1;
    wg_hash2(mac1key, (const uint8_t *)WG_LABEL_MAC1, 8, r->pub, 32);
    blake2s(mac, 16, mac1key, 32, m, 116);
    if (!ct_memeq(mac, m + 116, 16)) return -2;
    blake2s(c0, 32, NULL, 0, WG_CONSTRUCTION, strlen(WG_CONSTRUCTION));
    memcpy(r->C, c0, 32);
    wg_hash2(r->H, c0, 32, (const uint8_t *)WG_IDENTIFIER, strlen(WG_IDENTIFIER));
    wg_hash2(r->H, r->H, 32, r->pub, 32);
    memcpy(r->ei, m + 8, 32);
    wg_kdf(r->C, NULL, NULL, r->C, r->ei, 32);
    wg_hash2(r->H, r->H, 32, r->ei, 32);
    x25519_dh(dh, r->priv, r->ei);
    wg_kdf(r->C, k, NULL, r->C, dh, 32);
    if (aead_open(r->si, k, 0, m + 40, 48, r->H, 32) != 0) return -3;
    wg_hash2(r->H, r->H, 32, m + 40, 48);
    x25519_dh(dh, r->priv, r->si);
    wg_kdf(r->C, k, NULL, r->C, dh, 32);
    if (aead_open(ts, k, 0, m + 88, 28, r->H, 32) != 0) return -4;
    wg_hash2(r->H, r->H, 32, m + 88, 28);
    if (ts[0] != 0x40) return -5;
    r->remote_idx = wg_ld32(m + 4);
    return 0;
}

static void resp_create_response(responder *r, uint8_t out[WG_RESP_LEN], const uint8_t psk[32]) {
    uint8_t er_priv[32], er[32], dh[32], t[32], k[32], mac1key[32];
    memset(out, 0, WG_RESP_LEN);
    os_random(er_priv, 32);
    x25519_clamp(er_priv);
    x25519_pub(er, er_priv);
    r->local_idx = 0x11223344;
    out[0] = WG_MSG_RESP;
    wg_st32(out + 4, r->local_idx);
    wg_st32(out + 8, r->remote_idx);
    memcpy(out + 12, er, 32);
    wg_kdf(r->C, NULL, NULL, r->C, er, 32);
    wg_hash2(r->H, r->H, 32, er, 32);
    x25519_dh(dh, er_priv, r->ei);
    wg_kdf(r->C, NULL, NULL, r->C, dh, 32);
    x25519_dh(dh, er_priv, r->si);
    wg_kdf(r->C, NULL, NULL, r->C, dh, 32);
    wg_kdf(r->C, t, k, r->C, psk, 32);
    wg_hash2(r->H, r->H, 32, t, 32);
    aead_seal(out + 44, k, 0, NULL, 0, r->H, 32);
    wg_hash2(r->H, r->H, 32, out + 44, 16);
    wg_hash2(mac1key, (const uint8_t *)WG_LABEL_MAC1, 8, r->si, 32);
    blake2s(out + 60, 16, mac1key, 32, out, 60);
    /* responder: recv = T1 (initiator send), send = T2 */
    wg_kdf(r->recv_key, r->send_key, NULL, r->C, NULL, 0);
    out[1] = 0xaa; out[2] = 0xbb; out[3] = 0xcc; /* nonzero reserved on the wire */
}

static int roundtrip(const uint8_t *psk) {
    responder r;
    wg_identity id;
    wg_hs hs;
    uint8_t ipriv[32], init[WG_INIT_LEN], resp[WG_RESP_LEN], buf[256], pkt[128], dec[256];
    memset(&r, 0, sizeof(r));
    os_random(r.priv, 32);
    x25519_clamp(r.priv);
    x25519_pub(r.pub, r.priv);
    os_random(ipriv, 32);
    if (wg_identity_init(&id, ipriv, r.pub, psk) != 0) return 1;
    if (wg_create_initiation(&id, &hs, 0xdeadbeef, NULL, init) != 0) return 2;
    init[1] = 0x01; init[2] = 0x02; init[3] = 0x03; /* reserved after mac1 */
    int rc = resp_consume_init(&r, init);
    if (rc != 0) { fprintf(stderr, "responder rejected init: %d\n", rc); return 3; }
    if (memcmp(r.si, id.static_pub, 32) != 0) return 4;
    static const uint8_t zero[32] = {0};
    resp_create_response(&r, resp, psk ? psk : zero);
    resp[1] = resp[2] = resp[3] = 0; /* the scanner zeroes reserved before parsing */
    if (wg_consume_response(&id, &hs, resp, sizeof(resp)) != 0) return 5;
    if (memcmp(hs.send_key, r.recv_key, 32) || memcmp(hs.recv_key, r.send_key, 32)) return 6;
    /* initiator -> responder: ICMP echo */
    size_t n = pkt_icmp_echo(pkt, 0xAC100002, 0x01010101, 1, 77, 0, (const uint8_t *)"ping", 4);
    size_t m = wg_encrypt_transport(&hs, pkt, n, buf);
    if (m != 16 + ((n + 15) & ~15u) + 16) return 7;
    if (wg_ld32(buf + 4) != r.local_idx) return 8;
    {
        wg_hs rh;
        memset(&rh, 0, sizeof(rh));
        memcpy(rh.recv_key, r.recv_key, 32);
        int d = wg_decrypt_transport(&rh, buf, m, dec);
        pkt_info pi;
        if (d < (int)n || pkt_parse(dec, (size_t)d, &pi) != 0 || pi.icmp_id != 77) return 9;
        /* responder -> initiator */
        memcpy(rh.send_key, r.send_key, 32);
        rh.remote_idx = hs.local_idx;
        dec[20] = 0; /* echo reply (checksum now wrong; just bytes) */
        size_t m2 = wg_encrypt_transport(&rh, dec, (size_t)d, buf);
        int d2 = wg_decrypt_transport(&hs, buf, m2, pkt);
        if (d2 != d || memcmp(pkt, dec, (size_t)d) != 0) return 10;
        buf[20] ^= 0x80;
        if (wg_decrypt_transport(&hs, buf, m2, pkt) >= 0) return 11;
    }
    return 0;
}

static int json_rejects(const char *text, size_t len) {
    jval v;
    char err[128];
    if (json_parse(text, len, &v, err, sizeof(err)) != 0) return 1;
    json_free(&v);
    return 0;
}

static int test_json(void) {
    jval v;
    char err[128];
    if (!json_rejects("1\0002", 3)) return 1;          /* NUL внутри числа */
    if (!json_rejects("\"a\\u0000b\"", 10)) return 2; /* \u0000 обрезал бы строку */
    if (json_parse("[-1.5e2,\"\\u0041\"]", 17, &v, err, sizeof(err)) != 0) return 3;
    int ok = v.n == 2 && v.items[0].num == -150.0 && strcmp(v.items[1].str, "A") == 0;
    json_free(&v);
    return ok ? 0 : 4;
}

static int cfg_rc(const char *extra) {
    char text[512];
    jval v;
    char err[128];
    int n = snprintf(text, sizeof(text), "{\"private_key\":\"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\"%s}", extra);
    if (json_parse(text, (size_t)n, &v, err, sizeof(err)) != 0) return -2;
    int rc = load_config(&v);
    json_free(&v);
    return rc;
}

static int test_config(void) {
    if (cfg_rc("") != 0) return 1;
    if (cfg_rc(",\"seed\":1e30") != -1) return 2;
    if (cfg_rc(",\"seed\":-1") != -1) return 3;
    if (cfg_rc(",\"seed\":42") != 0 || C.seed != 42) return 4;
    if (cfg_rc(",\"ifname\":\"abcdefghijklmnop\"") != -1) return 5; /* 16 > IFNAMSIZ-1 */
    if (cfg_rc(",\"ifname\":\"abcdefghijklmno\"") != 0) return 6;
    return 0;
}

static int test_trace_alphabet(void) {
    static cand c;
    static const char resp[] = "HTTP/1.1 200 OK\r\n\r\ncolo=FR\xc3\nloc=D\"E\nwarp=off\n";
    memset(&c, 0, sizeof(c));
    memcpy(c.rbuf, resp, sizeof(resp) - 1);
    c.contig = sizeof(resp) - 1;
    parse_trace(&c);
    if (c.colo[0] || c.loc[0] || strcmp(c.warp, "off") != 0) return 1;
    static const char good[] = "HTTP/1.1 200 OK\r\n\r\ncolo=FRA\nloc=DE\nwarp=plus\n";
    memset(&c, 0, sizeof(c));
    memcpy(c.rbuf, good, sizeof(good) - 1);
    c.contig = sizeof(good) - 1;
    parse_trace(&c);
    if (strcmp(c.colo, "FRA") || strcmp(c.loc, "DE") || strcmp(c.warp, "plus") || !c.trace_ok) return 2;
    return 0;
}

/* Ложный FIN: сегмент чуть раньше irs+1. На старом коде падает только при 32-битном size_t. */
static int test_trace_fin(void) {
    static cand c;
    static const uint8_t data[16] = {0};
    pkt_info pi;
    memset(&c, 0, sizeof(c));
    c.fd = WS_BAD_SOCK;
    c.state = CS_PROBE;
    c.trace.state = TCP_EST;
    c.trace.irs = 1000;
    memset(&pi, 0, sizeof(pi));
    pi.flags = TCPF_FIN | TCPF_ACK;
    pi.seq = c.trace.irs + 1 - 16;
    pi.data = data;
    pi.dlen = sizeof(data);
    trace_input(&c, &pi);
    if (c.trace.fin_seen || c.trace_done) return 1;
    pi.seq = c.trace.irs + 1; /* настоящий FIN по-прежнему принимается */
    pi.dlen = 1;
    trace_input(&c, &pi);
    if (!c.trace.fin_seen || c.trace.fin_off != 1) return 2;
    return 0;
}

/* 7 из 8 адресов исключены: до правки хеш-таблица выборки заполнялась и поиск ячейки зависал. */
static int test_sampling_excluded(void) {
    static prefix4 pfx = {0x0A000000, 29};
    static uint16_t ports[40000];
    static endpoint excl[7];
    memset(&C, 0, sizeof(C));
    for (int i = 0; i < 40000; i++) ports[i] = (uint16_t)(i + 1);
    for (int i = 0; i < 7; i++) excl[i].ip = 0x0A000000 + (uint32_t)i;
    C.pfx = &pfx; C.n_pfx = 1;
    C.ports = ports; C.n_ports = 40000;
    C.excl = excl; C.n_excl = 7;
    C.max_candidates = 1000;
    g_prng = 1;
#ifndef _WIN32
    alarm(20);
#endif
    if (build_candidates() != 0) return 1;
#ifndef _WIN32
    alarm(0);
#endif
    if (g_ncands == 0 || g_ncands > 1000) return 2;
    for (size_t i = 0; i < g_ncands; i++)
        if (g_cands[i].ip != 0x0A000007) return 3;
    free(g_cands);
    return 0;
}

int main(void) {
    int fails = crypto_selftest(1);
    int rc = roundtrip(NULL);
    fprintf(stderr, "test %-38s %s (%d)\n", "wg handshake round-trip (psk=0)", rc ? "FAIL" : "ok", rc);
    fails += rc != 0;
    uint8_t psk[32];
    os_random(psk, 32);
    rc = roundtrip(psk);
    fprintf(stderr, "test %-38s %s (%d)\n", "wg handshake round-trip (psk)", rc ? "FAIL" : "ok", rc);
    fails += rc != 0;
    for (int i = 0; i < 50 && !fails; i++) fails += roundtrip(NULL) != 0;
    fprintf(stderr, "test %-38s %s\n", "50x randomized round-trips", fails ? "FAIL" : "ok");
    rc = test_json();
    fprintf(stderr, "test %-38s %s (%d)\n", "json: NUL in number, \\u0000", rc ? "FAIL" : "ok", rc);
    fails += rc != 0;
    rc = test_config();
    fprintf(stderr, "test %-38s %s (%d)\n", "config: seed range, ifname length", rc ? "FAIL" : "ok", rc);
    fails += rc != 0;
    rc = test_trace_alphabet();
    fprintf(stderr, "test %-38s %s (%d)\n", "trace: field alphabet", rc ? "FAIL" : "ok", rc);
    fails += rc != 0;
    rc = test_trace_fin();
    fprintf(stderr, "test %-38s %s (%d)\n", "trace: FIN before window", rc ? "FAIL" : "ok", rc);
    fails += rc != 0;
    rc = test_sampling_excluded();
    fprintf(stderr, "test %-38s %s (%d)\n", "candidates: sampling vs excludes", rc ? "FAIL" : "ok", rc);
    fails += rc != 0;
    fprintf(stderr, "tests: %s\n", fails ? "FAILED" : "all passed");
    return fails ? 1 : 0;
}
