import { describe, it, expect } from 'vitest';
import {
	linkImportErrorText,
	translateKnownError,
	groupLinkImportErrors
} from './linkImportError';

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

describe('groupLinkImportErrors', () => {
	it('одинаковую причину показывает один раз со счётчиком, без номера строки', () => {
		const errors = [
			'line 0 (hysteria): vlink: unsupported protocol: hysteria',
			'line 3 (hysteria): vlink: unsupported protocol: hysteria',
			'line 7 (hysteria): vlink: unsupported protocol: hysteria'
		];
		expect(groupLinkImportErrors(errors)).toEqual([
			'Ссылка не разобрана: unsupported protocol: hysteria (×3)'
		]);
	});

	it('единственную причину оставляет с номером строки — по нему её и ищут', () => {
		expect(groupLinkImportErrors(['line 12 (vless): vlink: vless: missing uuid'])).toEqual([
			'Строка 12: Не указан UUID'
		]);
	});

	it('разные причины не смешивает и сохраняет порядок', () => {
		expect(
			groupLinkImportErrors([
				'line 0 (vless): vlink: vless: missing uuid',
				'line 1 (trojan): vlink: trojan: missing password',
				'line 2 (vless): vlink: vless: missing uuid'
			])
		).toEqual(['Не указан UUID (×2)', 'Строка 1: Не указан пароль']);
	});
});

describe('причины отказа узлов hysteria из Xray-подписки', () => {
	const cases: Array<[string, string]> = [
		[
			'vlink: hysteria: udp mask "sudoku" has no sing-box equivalent',
			'Маскировка «sudoku» в sing-box не выражается'
		],
		[
			'vlink: hysteria: quicParams congestion force-brutal has no sing-box equivalent',
			'Настройка QUIC force-brutal в sing-box не выражается'
		],
		[
			'vlink: hysteria: udphop mode perConnRemote has no sing-box equivalent',
			'Режим прыжков по портам perConnRemote в sing-box не выражается'
		],
		[
			'vlink: hysteria: quicParams maxIdleTimeout 300 is out of the 4..120 range',
			'Значение maxIdleTimeout = 300 вне допустимого диапазона 4..120'
		],
		[
			'vlink: hysteria: quicParams maxIncomingStreams 4 is below the minimum of 8',
			'Значение maxIncomingStreams = 4 меньше минимума 8'
		],
		[
			'vlink: hysteria: udphop interval 1s is below the 5s minimum',
			'Интервал прыжков 1s меньше минимума 5s'
		],
		[
			'vlink: hysteria: quicParams bbrProfile "TURBO" is unknown',
			'Неизвестное значение bbrProfile: «TURBO»'
		],
		[
			'vlink: hysteria: quicParams congestion "cubic" is unknown',
			'Неизвестное значение congestion: «cubic»'
		],
		[
			'vlink: hysteria: udphop mode "whatever" is unknown',
			'Неизвестный режим прыжков по портам: «whatever»'
		],
		[
			'vlink: hysteria: udp mask "salamander" is repeated, sing-box takes only one',
			'Маскировка «salamander» указана дважды, sing-box принимает одну'
		],
		[
			'vlink: hysteria: version mismatch: settings 2, hysteriaSettings 1',
			'Версия протокола указана по-разному в двух блоках: settings 2, hysteriaSettings 1'
		],
		[
			'vlink: hysteria: unsupported version 1 (only 2 is supported)',
			'Поддерживается только Hysteria 2, а здесь версия 1'
		],
		[
			'vlink: hysteria: invalid gecko packet size range 0-1200 (want 1..2048)',
			'Размеры пакетов обфускации 0-1200 вне допустимого диапазона 1..2048'
		],
		[
			'vlink: hysteria: udphop remotePorts "abc" is not a valid port range',
			'«abc» — не диапазон портов'
		],
		['vlink: hysteria: udphop interval is missing', 'Не указан интервал прыжков'],
		[
			'vlink: hysteria: security "none" is not usable, hysteria2 is always over TLS',
			'Hysteria 2 работает только поверх TLS, а в узле указано «none»'
		],
		['vlink: hysteria: obfs requires password', 'Не указан пароль обфускации'],
		[
			'vlink: hysteria: quicParams initStreamReceiveWindow and maxStreamReceiveWindow differ, sing-box has a single window',
			'Стартовое и предельное окно приёма различаются, а в sing-box окно одно'
		],
		[
			'vlink: hysteria: udp mask realm is not supported yet: its outbound has no server address',
			'Маскировка realm пока не поддерживается: у такого аутбаунда нет адреса сервера'
		],
		['vlink: xray: outbound is malformed', 'Блок узла не разобран'],
		['vlink: hysteria: invalid finalmask', 'Блок finalmask не разобран']
	];

	it.each(cases)('%s', (raw, want) => {
		expect(linkImportErrorText(raw)).toBe(want);
	});
});
