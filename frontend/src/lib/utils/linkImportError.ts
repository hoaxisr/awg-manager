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

/**
 * `line 3 (clash:vless): ...` — префикс ошибок подписки (ParseError.Error).
 * У подписок в формате JSON и YAML строк нет, и бэкенд пишет там `node N` —
 * номер сервера по порядку.
 */
const LINE_PREFIX = /^(line|node)\s+(\d+)\s+\([^)]*\):\s*/i;

/** Служебные префиксы пакета: пользователю они ничего не говорят. */
const NOISE_PREFIX = /^(vlink|clash [a-z0-9]+|xray|amnezia|mieru|hysteria|trusttunnel):\s*/i;

/** Готовые фразы, а не существительные: род и число у них разные. */
const MISSING: Record<string, string> = {
	password: 'не указан пароль',
	server: 'не указан адрес сервера',
	host: 'не указан адрес сервера',
	uuid: 'не указан UUID',
	username: 'не указано имя пользователя',
	cipher: 'не указан шифр',
	method: 'не указан метод шифрования',
	credentials: 'не указаны логин и пароль',
	port: 'не указан порт',
	'server port': 'не указан порт',
	server_port: 'не указан или нечисловой порт',
};

/** Части ссылки реле: в родительном падеже, как их видит пользователь. */
const REALM_PART: Record<string, string> = {
	token: 'токена',
	id: 'идентификатора',
	host: 'адреса',
};

type Rule = { re: RegExp; text: (m: RegExpExecArray) => string };

const RULES: Rule[] = [
	// Отказы разбора hysteria из Xray-подписки. Подсистемы названы так, как их
	// видит пользователь: udp mask — маскировка, udphop — прыжки по портам,
	// quicParams — настройки QUIC.
	{
		// Обёрнутая ошибка полосы: значение в кавычках, поле — до двоеточия.
		re: /quicParams (brutalUp|brutalDown): "([^"]*)" is below the minimum of (\d+) bytes per second/i,
		text: (m) => `значение ${m[1]} «${m[2]}» меньше минимума ${m[3]} байт в секунду`,
	},
	{
		re: /quicParams (brutalUp|brutalDown): unsupported bandwidth unit in "([^"]*)"/i,
		text: (m) => `в значении ${m[1]} «${m[2]}» неизвестная единица измерения`,
	},
	{
		re: /quicParams (brutalUp|brutalDown): invalid bandwidth "([^"]*)"/i,
		text: (m) => `значение ${m[1]} «${m[2]}» не похоже на скорость`,
	},
	{
		re: /(\w+) "([^"]*)" is not a duration like/i,
		text: (m) => `значение ${m[1]} «${m[2]}» — не длительность, нужна единица (например 30s)`,
	},
	{
		re: /bbr_profile "([^"]*)" is unknown/i,
		text: (m) => `неизвестное значение bbr_profile: «${m[1]}»`,
	},
	{
		re: /realm portMapping requires IPv4/i,
		text: () => 'проброс порта у реле работает только с IPv4, а указан IPv6',
	},
	{
		re: /finalmask has (\d+) mask\(s\) with no sing-box equivalent/i,
		text: (m) => `маскировок в узле: ${m[1]} — sing-box их не выражает`,
	},
	{
		re: /udp mask realm cannot be combined with udphop/i,
		text: () => 'реле и прыжки по портам вместе sing-box не принимает',
	},
	{
		re: /realm (\w+) has no sing-box equivalent/i,
		text: (m) => `настройка реле ${m[1]} в sing-box не выражается`,
	},
	{
		re: /realm url scheme "([^"]*)" is not realm or realm\+http/i,
		text: (m) => `ссылка реле начинается не с realm:// или realm+http://, а с «${m[1]}»`,
	},
	{
		re: /realm url has no (\w+)/i,
		text: (m) => `в ссылке реле нет ${REALM_PART[m[1].toLowerCase()] ?? m[1]}`,
	},
	{
		re: /realm url (?:is malformed|port "[^"]*" is not valid)/i,
		text: () => 'ссылка реле разобрана неверно',
	},
	{
		re: /realm stunServers is empty/i,
		text: () => 'у реле не указаны STUN-серверы',
	},
	{
		re: /realm stunServers "([^"]*)" is not host:port/i,
		text: (m) => `STUN-сервер «${m[1]}» указан не как host:port`,
	},
	{
		re: /udp mask "?([\w-]+)"? has no sing-box equivalent/i,
		text: (m) => `маскировка «${m[1]}» в sing-box не выражается`,
	},
	{
		re: /udp mask "?([\w-]+)"? is repeated/i,
		text: (m) => `маскировка «${m[1]}» указана дважды, sing-box принимает одну`,
	},
	{
		re: /udphop mode (\S+) has no sing-box equivalent/i,
		text: (m) => `режим прыжков по портам ${m[1]} в sing-box не выражается`,
	},
	{
		re: /udphop (\S+) has no sing-box equivalent/i,
		text: (m) => `настройка ${m[1]} у прыжков по портам в sing-box не выражается`,
	},
	{
		re: /quicParams (?:congestion )?(\S+) has no sing-box equivalent/i,
		text: (m) => `настройка QUIC ${m[1]} в sing-box не выражается`,
	},
	{
		re: /quicParams (\w+) (\d+) is out of the (\S+) range/i,
		text: (m) => `значение ${m[1]} = ${m[2]} вне допустимого диапазона ${m[3]}`,
	},
	{
		re: /quicParams (\w+) (\d+) is below the minimum of (\d+)/i,
		text: (m) => `значение ${m[1]} = ${m[2]} меньше минимума ${m[3]}`,
	},
	{
		re: /udphop interval (\S+) is below the (\S+) minimum/i,
		text: (m) => `интервал прыжков ${m[1]} меньше минимума ${m[2]}`,
	},
	{
		re: /udphop interval is missing/i,
		text: () => 'не указан интервал прыжков',
	},
	{
		re: /udphop remotePorts "([^"]*)" is not a valid port range/i,
		text: (m) => `«${m[1]}» — не диапазон портов`,
	},
	{
		re: /quicParams (\w+) "?([\w-]+)"? is unknown/i,
		text: (m) => `неизвестное значение ${m[1]}: «${m[2]}»`,
	},
	{
		re: /udphop mode "([^"]*)" is unknown/i,
		text: (m) => `неизвестный режим прыжков по портам: «${m[1]}»`,
	},
	{
		re: /version mismatch: (.+)$/i,
		text: (m) => `версия протокола указана по-разному в двух блоках: ${m[1]}`,
	},
	{
		re: /unsupported version (\d+) \(only 2 is supported\)/i,
		text: (m) => `поддерживается только Hysteria 2, а здесь версия ${m[1]}`,
	},
	{
		re: /invalid gecko packet size range (\S+) \(want (\S+)\)/i,
		text: (m) => `размеры пакетов обфускации ${m[1]} вне допустимого диапазона ${m[2]}`,
	},
	{
		re: /security "([^"]*)" is not usable, hysteria2 is always over TLS/i,
		text: (m) => `Hysteria 2 работает только поверх TLS, а в узле указано «${m[1]}»`,
	},
	{
		re: /obfs requires password/i,
		text: () => 'не указан пароль обфускации',
	},
	{
		re: /init\w+ and max\w+ differ, sing-box has a single window/i,
		text: () => 'стартовое и предельное окно приёма различаются, а в sing-box окно одно',
	},
	{
		re: /outbound is malformed/i,
		text: () => 'блок узла не разобран',
	},
	{
		re: /invalid (finalmask|hysteriaSettings|realm settings|hysteria settings|udphop settings|salamander obfs settings)/i,
		text: (m) => `блок ${m[1].replace(/ settings$/, '')} не разобран`,
	},
	{
		re: /tcp headerType=http under \w+/i,
		text: () =>
			'обфускация HTTP-заголовком внутри TLS — sing-box так не умеет. ' +
			'Уберите из ссылки headerType=http или security=tls',
	},
	{
		re: /unsupported tcp headerType "?([^"]+)"?/i,
		text: (m) => `у транспорта TCP не бывает заголовка «${m[1]}» — это заголовки mKCP`,
	},
	{
		re: /transport "([^"]*)" without TLS is h2c/i,
		text: (m) =>
			`«${m[1]}» без TLS — это h2c, HTTP/2 открытым текстом; sing-box так не умеет. ` +
			'Включите TLS, а для обфускации HTTP-заголовком используйте type=tcp&headerType=http',
	},
	{
		re: /unsupported flow "([^"]*)"/i,
		text: (m) => `поддерживается только flow xtls-rprx-vision, а не «${m[1]}»`,
	},
	{
		re: /flow "([^"]*)" requires TLS/i,
		text: (m) => `параметр flow «${m[1]}» работает только под TLS или Reality`,
	},
	{
		re: /flow "([^"]*)" works only over plain tcp, not "([^"]*)"/i,
		text: (m) => `параметр flow «${m[1]}» работает только поверх обычного TCP, а не «${m[2]}»`,
	},
	{
		re: /([a-z_]+) (\S+) is not usable: the value must start above zero/i,
		text: (m) => `значение ${m[1]} = ${m[2]} должно начинаться выше нуля`,
	},
	{
		re: /plugin "([^"]*)" is not supported by sing-box/i,
		text: (m) =>
			`плагин «${m[1]}» sing-box не поддерживает — из плагинов shadowsocks он умеет ` +
			'только obfs-local и v2ray-plugin',
	},
	{
		re: /unsupported transport(?: type)? "([^"]*)"/i,
		text: (m) => `транспорт «${m[1]}» sing-box не поддерживает`,
	},
	{
		re: /unsupported xhttp mode "([^"]*)"/i,
		text: (m) => `режим xhttp «${m[1]}» не поддерживается`,
	},
	{
		re: /unknown security "([^"]*)"/i,
		text: (m) => `неизвестный режим безопасности «${m[1]}»`,
	},
	{
		re: /reality sid "([^"]*)" must be/i,
		text: (m) => `short id «${m[1]}» длиннее 16 hex-символов`,
	},
	{
		re: /unsupported cipher 'auto'/i,
		text: () => 'шифр auto не поддерживается — укажите конкретный',
	},
	{
		re: /scheme intentionally dropped \(vmess\)/i,
		text: () => 'vmess не поддерживается',
	},
	{
		re: /unsupported clash type "([^"]*)"/i,
		text: (m) => `протокол «${m[1]}» sing-box не поддерживает`,
	},
	{ re: /unsupported scheme/i, text: () => 'схема ссылки не поддерживается' },
	{
		// "missing <что-то> prefix" — это не отсутствующее поле, а не та схема.
		re: /missing (\S+) prefix/i,
		text: (m) => `ссылка не похожа на «${m[1]}»`,
	},
	{
		// Гейт сборки sing-box, а не отсутствующее поле: общее правило ниже
		// оборвало бы «sing-box» на дефисе и выдало «Нет поля «sing»».
		re: /missing sing-box build tag "([^"]+)"/i,
		text: (m) => `sing-box собран без тега «${m[1]}» — обновите sing-box`,
	},
	{
		// Хвост после missing бывает разный: "or invalid", "or non-numeric".
		// Без явного перечисления сюда попадало само слово "or".
		re: /missing (?:or (?:invalid|non-numeric) )?(server port|server_port|[a-z]+)\b/i,
		text: (m) => MISSING[m[1].toLowerCase()] ?? `нет поля «${m[1]}»`,
	},
	{
		// Значение показываем только в кавычках сразу после "port": у Go-ошибок
		// («invalid port: strconv.ParseUint: …») хвост в сообщение не годится.
		re: /invalid (?:server_)?port(?: range)? "([^"]+)"/i,
		text: (m) => `неверный порт «${m[1]}»`,
	},
	{ re: /invalid (?:server_)?port/i, text: () => 'неверный порт' },
];

/** Общая часть: снять префиксы и применить правила. null — правила не подошли. */
function translate(raw: string): { line: string; rest: string; text: string | null } {
	let line = '';
	let rest = raw.replace(LINE_PREFIX, (_, unit: string, n: string) => {
		line = unit.toLowerCase() === 'node' ? `Сервер ${n}: ` : `Строка ${n}: `;
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
		const m = rule.re.exec(rest);
		if (m) return { line, rest, text: capitalize(rule.text(m)) };
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
	if (!input) return { line: '', text: 'Ссылка не разобрана' };
	const { line, rest, text: translated } = translate(input);
	return { line, text: translated ?? `Ссылка не разобрана: ${rest}` };
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
