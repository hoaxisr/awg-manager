/* Хост-тесты таблицы трансформаций и трансформации Phobos через её операции. */
#include <assert.h>
#include <stdio.h>
#include "../src/config.h"
#include "../src/phobos_transform.h"

static u32 xs_state = 0x1234;
static u32 xs_next(void *ctx) { (void)ctx; u32 x = xs_state; x ^= x << 13; x ^= x >> 17; x ^= x << 5; return xs_state = x; }
static const struct awgmr_rng rng = { xs_next, NULL };

/* send_remote каркаса: запоминает служебку, отправленную серверу. */
static u8 sent[4][64];
static int sent_len[4], sent_n;
static int capture_send(void *relay, const u8 *buf, int len)
{
	(void)relay;
	assert(sent_n < 4 && len <= 64);
	memcpy(sent[sent_n], buf, len);
	sent_len[sent_n++] = len;
	return 0;
}

static struct awgmr_cfg cfg;
static struct awgmr_tctx tctx;

static void setup(const char *line)
{
	char buf[AWGMR_LINE_MAX];

	strcpy(buf, line);
	assert(awgmr_config_parse(buf, &cfg) == 0);
	memset(&tctx, 0, sizeof(tctx));
	tctx.priv = cfg.tpriv;
	tctx.rng = &rng;
	tctx.send_remote = capture_send;
	memcpy(tctx.peer_ip, cfg.target_ip, 4);
	tctx.peer_port[0] = cfg.target_port >> 8;
	tctx.peer_port[1] = cfg.target_port & 0xFF;
	if (cfg.t->init)
		cfg.t->init(&tctx);
	sent_n = 0;
}

static void test_registry(void)
{
	assert(awgmr_transform_find("phobos") == &awgmr_phobos_transform);
	assert(awgmr_transform_find("dementia") == NULL);
	assert(awgmr_transform_find("") == NULL);
	assert(awgmr_phobos_transform.priv_size <= AWGMR_TPRIV_MAX);
	assert(awgmr_phobos_transform.headroom <= AWGMR_HEADROOM_MAX);
}

/* c2s encode → s2c decode той же трансформацией: провод симметричен. */
static void roundtrip(const char *line, int hdr)
{
	u8 frame[AWGMR_HEADROOM_MAX + 2048], orig[200], *start;
	u8 *payload = frame + AWGMR_HEADROOM_MAX;
	int n, off, i;

	setup(line);
	memset(orig, 0, sizeof(orig));
	orig[0] = 4;
	for (i = 4; i < (int)sizeof(orig); i++)
		orig[i] = (u8)i;
	memcpy(payload, orig, sizeof(orig));
	n = cfg.t->encode(&tctx, payload, sizeof(orig), 2048, &start);
	assert(n > 0 && start == payload - hdr);
	n = cfg.t->decode(&tctx, start, n, &off);
	assert(n == (int)sizeof(orig) && !memcmp(start + off, orig, sizeof(orig)));
}

static void test_roundtrip(void)
{
	roundtrip("127.0.0.1:1 1.2.3.4:5 transform=phobos key=6b masking=none", 0);
	roundtrip("127.0.0.1:1 1.2.3.4:5 transform=phobos key=6b masking=stun max-dummy=4", AWGMR_STUN_HDR);
	roundtrip("127.0.0.1:1 1.2.3.4:5 transform=phobos key=6b masking=media obfuscate-bytes=16", AWGMR_RTP_HDR);
}

/* Binding Request перед handshake (type 1) и keepalive только после type 2. */
static void test_stun_control(void)
{
	u8 buf[AWGMR_HEADROOM_MAX + 2048], *start, *p = buf + AWGMR_HEADROOM_MAX;
	char d[64];

	setup("127.0.0.1:1 176.109.110.182:51900 transform=phobos key=6b masking=stun");
	assert(cfg.t->timer_ms(cfg.tpriv) == 10000);
	cfg.t->on_timer(&tctx);
	assert(sent_n == 0); /* рукопожатия ещё не было — keepalive молчит */

	memset(p, 0, 148); p[0] = 1;
	assert(cfg.t->encode(&tctx, p, 148, 2048, &start) > 0);
	assert(sent_n == 1 && sent_len[0] == AWGMR_STUN_REQ_LEN && sent[0][1] == 0x01);

	memset(p, 0, 92); p[0] = 2; /* ответ рукопожатия от WG (сервер инициировал) */
	assert(cfg.t->encode(&tctx, p, 92, 2048, &start) > 0);
	cfg.t->on_timer(&tctx);
	assert(sent_n == 2 && sent_len[1] == AWGMR_STUN_REQ_LEN);

	/* Binding Request сервера → Binding Success с адресом сервера, поглощено */
	{
		u8 req[AWGMR_STUN_REQ_LEN];
		int off;

		awgmr_stun_binding_request(req, &rng);
		assert(cfg.t->decode(&tctx, req, sizeof(req), &off) == 0);
		assert(sent_n == 3 && sent_len[2] == AWGMR_STUN_OK_LEN);
		assert(sent[2][28] == (176 ^ 0x21) && sent[2][31] == (182 ^ 0x42));
	}
	assert(cfg.t->describe(cfg.tpriv, d, sizeof(d)) > 0 && !strcmp(d, "masking=stun"));

	setup("127.0.0.1:1 1.2.3.4:5 transform=phobos key=6b masking=media");
	assert(cfg.t->timer_ms(cfg.tpriv) == 5000);
	setup("127.0.0.1:1 1.2.3.4:5 transform=phobos key=6b masking=none");
	assert(cfg.t->timer_ms(cfg.tpriv) == 0);
	memset(p, 0, 148); p[0] = 1;
	assert(cfg.t->encode(&tctx, p, 148, 2048, &start) > 0 && sent_n == 0); /* NONE — без служебки */
}

/* F480: MEDIA без obfuscate-bytes — 16, как у сервера Phobos (MEDIA_OBFUSCATE_BYTES_DEFAULT). */
static void test_media_obf_default(void)
{
	const struct awgmr_phobos_priv *p = (const void *)cfg.tpriv;

	setup("127.0.0.1:1 1.2.3.4:5 transform=phobos key=6b masking=media");
	assert(p->cfg.obf_bytes == 16);
	setup("127.0.0.1:1 1.2.3.4:5 transform=phobos key=6b masking=media obfuscate-bytes=32");
	assert(p->cfg.obf_bytes == 32);
	setup("127.0.0.1:1 1.2.3.4:5 transform=phobos key=6b masking=stun");
	assert(p->cfg.obf_bytes == 0);
}

/* Мусор от сервера — ошибка разбора, не данные. */
static void test_decode_rejects(void)
{
	u8 junk[64];
	int off;

	setup("127.0.0.1:1 1.2.3.4:5 transform=phobos key=6b masking=stun");
	memset(junk, 0xAB, sizeof(junk));
	assert(cfg.t->decode(&tctx, junk, sizeof(junk), &off) < 0);
}

int main(void)
{
	awgmr_transforms_init(); /* как module_init */
	test_registry();
	test_roundtrip();
	test_stun_control();
	test_decode_rejects();
	test_media_obf_default();
	printf("test_transform: OK\n");
	return 0;
}
