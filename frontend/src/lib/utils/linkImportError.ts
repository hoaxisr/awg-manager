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

/** `line 3 (clash:vless): ...` — префикс ошибок подписки (ParseError.Error). */
const LINE_PREFIX = /^line\s+(\d+)\s+\([^)]*\):\s*/i;

/** Служебные префиксы пакета: пользователю они ничего не говорят. */
const NOISE_PREFIX = /^(vlink|clash [a-z0-9]+|xray|amnezia|mieru):\s*/i;

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

type Rule = { re: RegExp; text: (m: RegExpExecArray) => string };

const RULES: Rule[] = [
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
	let rest = raw.replace(LINE_PREFIX, (_, n: string) => {
		line = `Строка ${n}: `;
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
