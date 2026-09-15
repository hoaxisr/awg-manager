import { describe, it, expect } from 'vitest';
import { linkImportErrorText, translateKnownError } from './linkImportError';

describe('linkImportErrorText', () => {
	it('объясняет обфускацию внутри TLS (#904) и подсказывает выход', () => {
		const got = linkImportErrorText(
			'vlink: tcp headerType=http under tls: header obfuscation inside TLS has no sing-box equivalent'
		);
		expect(got).toContain('HTTP-заголовком внутри TLS');
		expect(got).toContain('headerType=http');
		expect(got).not.toContain('sing-box equivalent');
	});

	it('называет несуществующий заголовок TCP', () => {
		expect(linkImportErrorText('vlink: unsupported tcp headerType "srtp"')).toBe(
			'У транспорта TCP не бывает заголовка «srtp» — это заголовки mKCP'
		);
	});

	it('сохраняет номер строки подписки и снимает вложенные префиксы', () => {
		expect(linkImportErrorText('line 3 (clash:vless): clash vless: vlink: missing uuid')).toBe(
			'Строка 3: Не указан UUID'
		);
	});

	it('переводит формы, которых нет в списке правил поимённо', () => {
		expect(linkImportErrorText('vlink: unsupported transport "kcp"')).toBe(
			'Транспорт «kcp» sing-box не поддерживает'
		);
		expect(linkImportErrorText('clash trojan: missing or invalid port')).toBe(
			'Не указан порт'
		);
	});

	it('объясняет h2c (F324)', () => {
		const got = linkImportErrorText(
			'vlink: transport "h2" without TLS is h2c, which sing-box cannot dial (header obfuscation is type=tcp&headerType=http)'
		);
		expect(got).toContain('h2c');
		expect(got).toContain('Включите TLS');
		expect(got).not.toContain('cannot dial');
	});

	it('объясняет ограничения flow и плагинов', () => {
		expect(
			linkImportErrorText('vlink: vless: flow "xtls-rprx-vision" works only over plain tcp, not "ws"')
		).toBe('Параметр flow «xtls-rprx-vision» работает только поверх обычного TCP, а не «ws»');
		expect(
			linkImportErrorText(
				'vlink: shadowsocks: plugin "xray-plugin" is not supported by sing-box (only obfs-local and v2ray-plugin)'
			)
		).toContain('obfs-local');
	});

	it('не калечит сообщения с длинным или нестандартным хвостом', () => {
		// Раньше отсюда получалось «Нет поля «or»» и «Неверный порт «: strconv…»».
		expect(linkImportErrorText('singbox: missing or non-numeric server_port')).toBe(
			'Не указан или нечисловой порт'
		);
		expect(
			linkImportErrorText('ss: invalid port: strconv.ParseUint: parsing "x": invalid syntax')
		).toBe('Неверный порт');
		expect(linkImportErrorText('mieru: missing mieru:// prefix')).toBe(
			'Ссылка не похожа на «mieru://»'
		);
		expect(linkImportErrorText('unsupported clash type "ssr"')).toBe(
			'Протокол «ssr» sing-box не поддерживает'
		);
	});

	it('translateKnownError не подписывает чужие ошибки как ошибки разбора', () => {
		// Отказ обновления подписки несёт и сетевые ошибки — их трогать нельзя.
		expect(translateKnownError('Get "https://sub.example.com": dial tcp: timeout')).toBe(
			'Get "https://sub.example.com": dial tcp: timeout'
		);
		// А знакомую строку внутри длинного сообщения — переводит.
		expect(
			translateKnownError(
				'обновление не удалось: Первая ошибка парсера: vlink: transport "h2" without TLS is h2c, which sing-box cannot dial'
			)
		).toContain('h2c');
	});

	it('объясняет нулевой диапазон xhttp (#908)', () => {
		expect(
			linkImportErrorText(
				'sing-box:vless: sc_max_each_post_bytes 0 is not usable: the value must start above zero'
			)
		).toContain('sc_max_each_post_bytes');
		// И транспорт из того же входа ловится общим правилом.
		expect(linkImportErrorText('sing-box:vless: unsupported transport type "kcp"')).toBe(
			'Транспорт «kcp» sing-box не поддерживает'
		);
	});

	it('незнакомую строку не теряет, а показывает внутри общей фразы', () => {
		expect(linkImportErrorText('vlink: something entirely new')).toBe(
			'Ссылка не разобрана: something entirely new'
		);
		expect(linkImportErrorText('')).toBe('Ссылка не разобрана');
	});
});
