// Карта известных FATAL-строк sing-box → человекочитаемая подсказка.
// Первое совпадение выигрывает. Для неизвестных строк engineFatalHint вернёт
// null — модалка показывает ENGINE_FATAL_FALLBACK + сырую строку.

export interface FatalHintPattern {
	match: RegExp;
	hint: string;
}

export const FATAL_HINT_PATTERNS: FatalHintPattern[] = [
	{
		match: /Address Filter Fields/i,
		hint:
			'В DNS-правило добавлен IP-набор. Sing-box 1.14 это запрещает. ' +
			'Уберите IP-наборы из правил DNS — для IP не требуется осуществлять их резолв.',
	},
	{
		match: /cache-file: timeout/i,
		hint:
			'Файл кэша sing-box занят — обычно остался ещё один (зависший) процесс sing-box. ' +
			'Остановите движок, убедитесь, что процессов sing-box не осталось, и запустите снова. ' +
			'Для этого в SSH выполните killall sing-box',
	},
	{
		match: /rule-set[^\n]*no such file/i,
		hint:
			'Не найден локальный файл набора правил (.srs). Набор удалён или путь неверный — ' +
			'пересоздайте или удалите этот rule-set.',
	},
	{
		match: /outbound not found/i,
		hint:
			'Правило ссылается на несуществующий outbound (туннель/селектор удалён или переименован). ' +
			'Поправьте правило или верните outbound.',
	},
	{
		match: /missing fakeip record/i,
		hint: 'Включён FakeIP, но выключен кэш. Включите experimental.cache_file в настройках движка.',
	},
	{
		match: /address already in use/i,
		hint: 'Порт перехвата уже занят другим процессом. Освободите порт или измените его в настройках движка.',
	},
];

export const ENGINE_FATAL_FALLBACK =
	'Движок не смог запуститься. Ниже — техническая причина; скопируйте её, если будете обращаться за помощью.';

export function engineFatalHint(raw: string | null | undefined): string | null {
	if (!raw) return null;
	for (const p of FATAL_HINT_PATTERNS) {
		if (p.match.test(raw)) return p.hint;
	}
	return null;
}
