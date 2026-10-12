/**
 * Русская обёртка над техническим текстом ошибки разбора ссылки.
 *
 * Бэкенд (`internal/singbox/vlink`) отдаёт английские строки вида
 * `vlink: vless: missing uuid`, и они показываются пользователю дословно — при
 * импорте ссылки и при обновлении подписки. Здесь они превращаются во фразу на
 * языке интерфейса, а незнакомая строка не теряется: она уезжает внутрь общей
 * фразы целиком, чтобы причина оставалась видна.
 *
 * Правила — по форме сообщений, а не по их полному перечню: бэкенд пишет их
 * единообразно (`missing X`, `unsupported X "Y"`, `invalid X`), поэтому новая
 * ошибка той же формы переводится сама.
 */

import { m } from '$lib/i18n';

/**
 * `line 3 (clash:vless): ...` — префикс ошибок подписки (ParseError.Error).
 * У подписок в формате JSON и YAML строк нет, и бэкенд пишет там `node N` —
 * номер сервера по порядку.
 */
const LINE_PREFIX = /^(line|node)\s+(\d+)\s+\([^)]*\):\s*/i;

/** Служебные префиксы пакета: пользователю они ничего не говорят. */
const NOISE_PREFIX = /^(vlink|clash [a-z0-9]+|xray|amnezia|mieru|hysteria|trusttunnel):\s*/i;

/** Готовые фразы, а не существительные: род и число у них разные. */
const MISSING: Record<string, () => string> = {
	password: m.link_import_missing_password,
	server: m.link_import_missing_server,
	host: m.link_import_missing_server,
	uuid: m.link_import_missing_uuid,
	username: m.link_import_missing_username,
	cipher: m.link_import_missing_cipher,
	method: m.link_import_missing_method,
	credentials: m.link_import_missing_credentials,
	port: m.link_import_missing_port,
	'server port': m.link_import_missing_port,
	server_port: m.link_import_missing_port_numeric,
};

/** Части ссылки реле: готовые фразы, как их видит пользователь. */
const REALM_MISSING: Record<string, () => string> = {
	token: m.link_import_realm_url_no_token,
	id: m.link_import_realm_url_no_id,
	host: m.link_import_realm_url_no_host,
};

type Rule = { re: RegExp; text: (r: RegExpExecArray) => string };

const RULES: Rule[] = [
	// Отказы разбора hysteria из Xray-подписки. Подсистемы названы так, как их
	// видит пользователь: udp mask — маскировка, udphop — прыжки по портам,
	// quicParams — настройки QUIC.
	{
		// Обёрнутая ошибка полосы: значение в кавычках, поле — до двоеточия.
		re: /quicParams (brutalUp|brutalDown): "([^"]*)" is below the minimum of (\d+) bytes per second/i,
		text: (r) => m.link_import_bw_below_min({ field: r[1], value: r[2], min: r[3] }),
	},
	{
		re: /quicParams (brutalUp|brutalDown): unsupported bandwidth unit in "([^"]*)"/i,
		text: (r) => m.link_import_bw_unit_unknown({ field: r[1], value: r[2] }),
	},
	{
		re: /quicParams (brutalUp|brutalDown): invalid bandwidth "([^"]*)"/i,
		text: (r) => m.link_import_bw_invalid({ field: r[1], value: r[2] }),
	},
	{
		re: /(\w+) "([^"]*)" is not a duration like/i,
		text: (r) => m.link_import_not_duration({ field: r[1], value: r[2] }),
	},
	{
		re: /bbr_profile "([^"]*)" is unknown/i,
		text: (r) => m.link_import_bbr_unknown({ value: r[1] }),
	},
	{
		re: /realm portMapping requires IPv4/i,
		text: () => m.link_import_realm_port_ipv4(),
	},
	{
		re: /finalmask has (\d+) mask\(s\) with no sing-box equivalent/i,
		text: (r) => m.link_import_finalmask_count({ count: r[1] }),
	},
	{
		re: /udp mask realm cannot be combined with udphop/i,
		text: () => m.link_import_realm_udphop_conflict(),
	},
	{
		re: /realm (\w+) has no sing-box equivalent/i,
		text: (r) => m.link_import_realm_option_unsupported({ name: r[1] }),
	},
	{
		re: /realm url scheme "([^"]*)" is not realm or realm\+http/i,
		text: (r) => m.link_import_realm_scheme({ scheme: r[1] }),
	},
	{
		re: /realm url has no (\w+)/i,
		text: (r) =>
			(REALM_MISSING[r[1].toLowerCase()] ?? (() => m.link_import_realm_url_no_part({ name: r[1] })))(),
	},
	{
		re: /realm url (?:is malformed|port "[^"]*" is not valid)/i,
		text: () => m.link_import_realm_url_malformed(),
	},
	{
		re: /realm stunServers is empty/i,
		text: () => m.link_import_realm_stun_empty(),
	},
	{
		re: /realm stunServers "([^"]*)" is not host:port/i,
		text: (r) => m.link_import_realm_stun_format({ server: r[1] }),
	},
	{
		re: /udp mask "?([\w-]+)"? has no sing-box equivalent/i,
		text: (r) => m.link_import_mask_unsupported({ name: r[1] }),
	},
	{
		re: /udp mask "?([\w-]+)"? is repeated/i,
		text: (r) => m.link_import_mask_repeated({ name: r[1] }),
	},
	{
		re: /udphop mode (\S+) has no sing-box equivalent/i,
		text: (r) => m.link_import_udphop_mode_unsupported({ mode: r[1] }),
	},
	{
		re: /udphop (\S+) has no sing-box equivalent/i,
		text: (r) => m.link_import_udphop_option_unsupported({ name: r[1] }),
	},
	{
		re: /quicParams (?:congestion )?(\S+) has no sing-box equivalent/i,
		text: (r) => m.link_import_quic_option_unsupported({ name: r[1] }),
	},
	{
		re: /quicParams (\w+) (\d+) is out of the (\S+) range/i,
		text: (r) => m.link_import_quic_out_of_range({ field: r[1], value: r[2], range: r[3] }),
	},
	{
		re: /quicParams (\w+) (\d+) is below the minimum of (\d+)/i,
		text: (r) => m.link_import_quic_below_min({ field: r[1], value: r[2], min: r[3] }),
	},
	{
		re: /udphop interval (\S+) is below the (\S+) minimum/i,
		text: (r) => m.link_import_udphop_interval_below_min({ interval: r[1], min: r[2] }),
	},
	{
		re: /udphop interval is missing/i,
		text: () => m.link_import_udphop_interval_missing(),
	},
	{
		re: /udphop remotePorts "([^"]*)" is not a valid port range/i,
		text: (r) => m.link_import_udphop_ports_invalid({ value: r[1] }),
	},
	{
		re: /quicParams (\w+) "?([\w-]+)"? is unknown/i,
		text: (r) => m.link_import_quic_value_unknown({ field: r[1], value: r[2] }),
	},
	{
		re: /udphop mode "([^"]*)" is unknown/i,
		text: (r) => m.link_import_udphop_mode_unknown({ mode: r[1] }),
	},
	{
		re: /version mismatch: (.+)$/i,
		text: (r) => m.link_import_version_mismatch({ detail: r[1] }),
	},
	{
		re: /unsupported version (\d+) \(only 2 is supported\)/i,
		text: (r) => m.link_import_hysteria_version({ version: r[1] }),
	},
	{
		re: /invalid gecko packet size range (\S+) \(want (\S+)\)/i,
		text: (r) => m.link_import_gecko_size({ size: r[1], range: r[2] }),
	},
	{
		re: /security "([^"]*)" is not usable, hysteria2 is always over TLS/i,
		text: (r) => m.link_import_hysteria_tls({ security: r[1] }),
	},
	{
		re: /obfs requires password/i,
		text: () => m.link_import_obfs_password(),
	},
	{
		re: /init\w+ and max\w+ differ, sing-box has a single window/i,
		text: () => m.link_import_window_differs(),
	},
	{
		re: /outbound is malformed/i,
		text: () => m.link_import_outbound_malformed(),
	},
	{
		re: /invalid (finalmask|hysteriaSettings|realm settings|hysteria settings|udphop settings|salamander obfs settings)/i,
		text: (r) => m.link_import_block_malformed({ block: r[1].replace(/ settings$/, '') }),
	},
	{
		re: /tcp headerType=http under \w+/i,
		text: () => m.link_import_tcp_http_tls(),
	},
	{
		re: /unsupported tcp headerType "?([^"]+)"?/i,
		text: (r) => m.link_import_tcp_header_unsupported({ header: r[1] }),
	},
	{
		re: /transport "([^"]*)" without TLS is h2c/i,
		text: (r) => m.link_import_h2c({ transport: r[1] }),
	},
	{
		re: /unsupported flow "([^"]*)"/i,
		text: (r) => m.link_import_flow_unsupported({ flow: r[1] }),
	},
	{
		re: /flow "([^"]*)" requires TLS/i,
		text: (r) => m.link_import_flow_requires_tls({ flow: r[1] }),
	},
	{
		re: /flow "([^"]*)" works only over plain tcp, not "([^"]*)"/i,
		text: (r) => m.link_import_flow_plain_tcp({ flow: r[1], transport: r[2] }),
	},
	{
		re: /([a-z_]+) (\S+) is not usable: the value must start above zero/i,
		text: (r) => m.link_import_must_start_above_zero({ field: r[1], value: r[2] }),
	},
	{
		re: /plugin "([^"]*)" is not supported by sing-box/i,
		text: (r) => m.link_import_plugin_unsupported({ plugin: r[1] }),
	},
	{
		re: /unsupported transport(?: type)? "([^"]*)"/i,
		text: (r) => m.link_import_transport_unsupported({ transport: r[1] }),
	},
	{
		re: /unsupported xhttp mode "([^"]*)"/i,
		text: (r) => m.link_import_xhttp_mode({ mode: r[1] }),
	},
	{
		re: /unknown security "([^"]*)"/i,
		text: (r) => m.link_import_unknown_security({ security: r[1] }),
	},
	{
		re: /reality sid "([^"]*)" must be/i,
		text: (r) => m.link_import_reality_sid({ value: r[1] }),
	},
	{
		re: /unsupported cipher 'auto'/i,
		text: () => m.link_import_cipher_auto(),
	},
	{
		re: /scheme intentionally dropped \(vmess\)/i,
		text: () => m.link_import_vmess_unsupported(),
	},
	{
		re: /unsupported clash type "([^"]*)"/i,
		text: (r) => m.link_import_clash_type({ type: r[1] }),
	},
	{ re: /unsupported scheme/i, text: () => m.link_import_scheme_unsupported() },
	{
		// "missing <что-то> prefix" — это не отсутствующее поле, а не та схема.
		re: /missing (\S+) prefix/i,
		text: (r) => m.link_import_link_mismatch({ prefix: r[1] }),
	},
	{
		// Гейт сборки sing-box, а не отсутствующее поле: общее правило ниже
		// оборвало бы «sing-box» на дефисе и выдало «Нет поля «sing»».
		re: /missing sing-box build tag "([^"]+)"/i,
		text: (r) => m.link_import_singbox_build_tag({ tag: r[1] }),
	},
	{
		// Хвост после missing бывает разный: "or invalid", "or non-numeric".
		// Без явного перечисления сюда попадало само слово "or".
		re: /missing (?:or (?:invalid|non-numeric) )?(server port|server_port|[a-z]+)\b/i,
		text: (r) => (MISSING[r[1].toLowerCase()] ?? (() => m.link_import_missing_field({ field: r[1] })))(),
	},
	{
		// Значение показываем только в кавычках сразу после "port": у Go-ошибок
		// («invalid port: strconv.ParseUint: …») хвост в сообщение не годится.
		re: /invalid (?:server_)?port(?: range)? "([^"]+)"/i,
		text: (r) => m.link_import_invalid_port_value({ port: r[1] }),
	},
	{ re: /invalid (?:server_)?port/i, text: () => m.link_import_invalid_port() },
];

/** Общая часть: снять префиксы и применить правила. null — правила не подошли. */
function translate(raw: string): { line: string; rest: string; text: string | null } {
	let line = '';
	let rest = raw.replace(LINE_PREFIX, (_, unit: string, n: string) => {
		line =
			unit.toLowerCase() === 'node'
				? m.link_import_node_prefix({ n })
				: m.link_import_line_prefix({ n });
		return '';
	});
	// Префиксы снимаются по одному: сообщение бывает вложенным
	// (`clash vless: vlink: ...`).
	let stripped = rest.replace(NOISE_PREFIX, '');
	while (stripped !== rest) {
		rest = stripped;
		stripped = rest.replace(NOISE_PREFIX, '');
	}

	for (const rule of RULES) {
		const hit = rule.re.exec(rest);
		if (hit) return { line, rest, text: capitalize(rule.text(hit)) };
	}
	return { line, rest, text: null };
}

/**
 * Переводит одну строку ошибки разбора в понятную пользователю фразу.
 * Номер строки, если он есть в исходном сообщении, сохраняется. Незнакомая
 * строка не теряется — уезжает внутрь общей фразы.
 */
export function linkImportErrorText(raw: string): string {
	const { line, text } = errorParts(raw);
	return line + text;
}

/** Причина отдельно от номера строки: номер мешает группировать одинаковые. */
function errorParts(raw: string): { line: string; text: string } {
	const input = (raw ?? '').trim();
	if (!input) return { line: '', text: m.link_import_not_parsed() };
	const { line, rest, text: translated } = translate(input);
	return { line, text: translated ?? m.link_import_not_parsed_detail({ detail: rest }) };
}

/**
 * Схлопывает список причин отказа для одного уведомления: одинаковая причина
 * показывается один раз со счётчиком, единственная сохраняет номер строки — по
 * нему ссылку и ищут.
 *
 * Группировка идёт по причине БЕЗ номера: номер у каждого узла свой, и
 * дедупликация по готовой фразе не срабатывала вовсе — подписка на сотню узлов
 * одного неподдерживаемого протокола давала сотню «разных» причин.
 */
export function groupLinkImportErrors(errors: string[]): string[] {
	const groups = new Map<string, { line: string; count: number }>();
	for (const raw of errors) {
		const { line, text } = errorParts(raw);
		const seen = groups.get(text);
		if (seen) seen.count++;
		else groups.set(text, { line, count: 1 });
	}
	return [...groups].map(([text, g]) => (g.count > 1 ? `${text} (×${g.count})` : g.line + text));
}

/**
 * То же, но для сообщений, которые НЕ обязаны быть ошибкой разбора: отказ
 * обновления подписки несёт внутри и сетевые, и HTTP-ошибки. Незнакомую строку
 * возвращает как есть, а не подписывает «Ссылка не разобрана».
 */
export function translateKnownError(raw: string): string {
	const text = (raw ?? '').trim();
	if (!text) return text;
	const { line, text: translated } = translate(text);
	return translated ? line + translated : text;
}

function capitalize(s: string): string {
	return s ? s[0].toUpperCase() + s.slice(1) : s;
}
