/* Хост-тесты разбора строки add (спека §3.3). */
#include <assert.h>
#include <stdio.h>
#include "../src/config.h"

static int parse(const char *s, struct awgmr_cfg *c)
{
	char buf[AWGMR_LINE_MAX];

	strcpy(buf, s);
	return awgmr_config_parse(buf, c);
}

int main(void)
{
	struct awgmr_cfg c;
	u16 port;

	assert(parse("127.0.0.1:39001 176.109.110.182:51900 transform=phobos key=6b3d masking=media max-dummy=4 obfuscate-bytes=16", &c) == 0);
	assert(c.listen_port == 39001 && c.target_port == 51900);
	assert(c.target_ip[0] == 176 && c.target_ip[3] == 182);
	assert(c.phobos.key_len == 2 && c.phobos.key[0] == 0x6b && c.phobos.key[1] == 0x3d);
	assert(c.phobos.mask == AWGMR_MASK_MEDIA && c.phobos.max_dummy == 4 && c.phobos.obf_bytes == 16);

	/* max-dummy/obfuscate-bytes необязательны, по умолчанию 0 */
	assert(parse("127.0.0.1:39002 1.2.3.4:5 transform=phobos key=00 masking=none", &c) == 0);
	assert(c.phobos.max_dummy == 0 && c.phobos.obf_bytes == 0 && c.phobos.key[0] == 0);

	/* обязательные ключи */
	assert(parse("127.0.0.1:39002 1.2.3.4:5 key=00 masking=none", &c) == -EINVAL);
	assert(parse("127.0.0.1:39002 1.2.3.4:5 transform=phobos masking=none", &c) == -EINVAL);
	assert(parse("127.0.0.1:39002 1.2.3.4:5 transform=phobos key=00", &c) == -EINVAL);
	/* плохие значения */
	assert(parse("127.0.0.2:39002 1.2.3.4:5 transform=phobos key=00 masking=none", &c) == -EINVAL);
	assert(parse("127.0.0.1:39002 [::1]:5 transform=phobos key=00 masking=none", &c) == -EINVAL);
	assert(parse("127.0.0.1:39002 1.2.3.4:0 transform=phobos key=00 masking=none", &c) == -EINVAL);
	assert(parse("127.0.0.1:39002 1.2.3.256:5 transform=phobos key=00 masking=none", &c) == -EINVAL);
	assert(parse("127.0.0.1:39002 1.2.3.4:5 transform=dementia key=00 masking=none", &c) == -EINVAL);
	assert(parse("127.0.0.1:39002 1.2.3.4:5 transform=phobos key=0 masking=none", &c) == -EINVAL);
	assert(parse("127.0.0.1:39002 1.2.3.4:5 transform=phobos key=zz masking=none", &c) == -EINVAL);
	assert(parse("127.0.0.1:39002 1.2.3.4:5 transform=phobos key=00 masking=auto", &c) == -EINVAL);
	assert(parse("127.0.0.1:39002 1.2.3.4:5 transform=phobos key=00 masking=none max-dummy=1025", &c) == -EINVAL);
	assert(parse("127.0.0.1:39002 1.2.3.4:5 transform=phobos key=00 masking=none bind=eth3", &c) == -EINVAL);
	{
		char k[2 * 256 + 64] = "127.0.0.1:1 1.2.3.4:5 transform=phobos masking=none key=";
		int i, n = (int)strlen(k);
		for (i = 0; i < 2 * 256; i++) k[n + i] = 'a';
		k[n + 2 * 256] = 0;
		assert(parse(k, &c) == -EINVAL); /* 256 байт ключа > 255 */
	}
	assert(awgmr_parse_listen("127.0.0.1:39005", &port) == 0 && port == 39005);
	assert(awgmr_parse_listen("10.0.0.1:39005", &port) == -EINVAL);
	printf("test_config: OK\n");
	return 0;
}
