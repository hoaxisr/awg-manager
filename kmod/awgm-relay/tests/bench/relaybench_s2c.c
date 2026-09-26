// relaybench_s2c: поток сервер -> релей -> клиент (направление s2c, F472).
//   клиент (как WG) шлёт handshake type 1 на listen релея; «сервер» на
//   127.0.0.1:SERVER_PORT узнаёт remote-сокет релея, отвечает закодированным
//   type 2 (открывает гейт userspace-релея) и флудит закодированными type 4.
//   Клиент считает, что дошло. RELAY_PORT = 0 — без релея, сервер шлёт
//   клиенту напрямую (ёмкость самой loopback-доставки).
// usage: relaybench_s2c RELAY_PORT SERVER_PORT SECONDS SIZE [pps]
// Ключ/режим как у run.sh: benchkey-0123456789, masking none, max-dummy 4.
#define _GNU_SOURCE
#include <arpa/inet.h>
#include <pthread.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/time.h>
#include <time.h>
#include <unistd.h>

#include "../../src/phobos.h"

#define B 32
static volatile long got, gotb;
static volatile int stop;
static int cfd;

static u32 xs = 0x5EED;
static u32 xs_next(void *c) { (void)c; u32 x = xs; x ^= x << 13; x ^= x >> 17; x ^= x << 5; return xs = x; }
static const struct awgmr_rng rng = { xs_next, NULL };

static double now(void) { struct timespec t; clock_gettime(CLOCK_MONOTONIC, &t); return t.tv_sec + t.tv_nsec / 1e9; }
static struct sockaddr_in lo(int p) { struct sockaddr_in a = {0}; a.sin_family = AF_INET; a.sin_port = htons(p); a.sin_addr.s_addr = htonl(0x7f000001); return a; }

static void *client_loop(void *a)
{
	static uint8_t buf[B][2048];
	struct mmsghdr h[B];
	struct iovec v[B];
	struct timeval tv = { 0, 200000 };

	(void)a;
	setsockopt(cfd, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));
	while (!stop) {
		int i, n;

		for (i = 0; i < B; i++) {
			v[i].iov_base = buf[i]; v[i].iov_len = 2048;
			memset(&h[i], 0, sizeof(h[i]));
			h[i].msg_hdr.msg_iov = &v[i]; h[i].msg_hdr.msg_iovlen = 1;
		}
		n = recvmmsg(cfd, h, B, 0, NULL);
		for (i = 0; i < n; i++)
			if (buf[i][0] == 4) { got++; gotb += h[i].msg_len; }
	}
	return NULL;
}

int main(int c, char **v)
{
	int rp = atoi(v[1]), sp = atoi(v[2]), secs = atoi(v[3]), size = atoi(v[4]);
	long pps = c > 5 ? atol(v[5]) : 0, sent = 0;
	struct awgmr_phobos_cfg cfg = { .key_len = 19, .max_dummy = 4, .mask = AWGMR_MASK_NONE };
	int sfd = socket(AF_INET, SOCK_DGRAM, 0), rcv = 4 << 20, i;
	struct sockaddr_in sa = lo(sp), ca = lo(0), to;
	socklen_t tl = sizeof(to);
	uint8_t tmp[2048];
	static uint8_t d[B][2048];
	struct mmsghdr h[B];
	struct iovec iv[B];
	struct timeval tv = { 3, 0 };
	pthread_t th;
	double t0, end, el;
	int out;

	memcpy(cfg.key, "benchkey-0123456789", 19);
	awgmr_crc8_init();
	cfd = socket(AF_INET, SOCK_DGRAM, 0);
	setsockopt(cfd, SOL_SOCKET, SO_RCVBUF, &rcv, sizeof(rcv));
	if (bind(sfd, (void *)&sa, sizeof(sa)) || bind(cfd, (void *)&ca, sizeof(ca))) { perror("bind"); return 1; }
	setsockopt(sfd, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));

	if (rp) {
		struct sockaddr_in ra = lo(rp);
		uint8_t hs[148] = { 1 };

		sendto(cfd, hs, sizeof(hs), 0, (void *)&ra, sizeof(ra));
		if (recvfrom(sfd, tmp, sizeof(tmp), 0, (void *)&to, &tl) < 0) { fprintf(stderr, "нет handshake от релея\n"); return 1; }
		memset(tmp, 0, 92); tmp[0] = 2;
		out = awgmr_encode(&cfg, tmp, 92, sizeof(tmp), &rng);
		sendto(sfd, tmp, out, 0, (void *)&to, tl);
		usleep(300000);
	} else {
		getsockname(cfd, (void *)&to, &tl);
	}

	for (i = 0; i < B; i++) {
		memset(d[i], 0, size); d[i][0] = 4;
		iv[i].iov_base = d[i];
		iv[i].iov_len = awgmr_encode(&cfg, d[i], size, sizeof(d[i]), &rng);
		if (!rp) { /* напрямую — клиенту незакодированный type 4 */
			memset(d[i], 0, size); d[i][0] = 4; iv[i].iov_len = size;
		}
		memset(&h[i], 0, sizeof(h[i]));
		h[i].msg_hdr.msg_iov = &iv[i]; h[i].msg_hdr.msg_iovlen = 1;
		h[i].msg_hdr.msg_name = &to; h[i].msg_hdr.msg_namelen = sizeof(to);
	}
	pthread_create(&th, NULL, client_loop, NULL);
	t0 = now(); end = t0 + secs;
	while (now() < end) {
		int n = sendmmsg(sfd, h, B, 0);

		if (n > 0)
			sent += n;
		if (pps) {
			double w = t0 + (double)sent / pps - now();
			if (w > 0)
				usleep(w * 1e6);
		}
	}
	el = now() - t0;
	usleep(500000);
	stop = 1;
	pthread_join(th, NULL);
	printf("sent=%ld pps  recv=%ld pps  recv=%.1f Mbit/s  loss=%.1f%%\n",
	       (long)(sent / el), (long)(got / el), gotb * 8 / el / 1e6, 100.0 * (1 - (double)got / sent));
	return 0;
}
