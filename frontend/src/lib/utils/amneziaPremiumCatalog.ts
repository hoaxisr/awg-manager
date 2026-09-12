import type { AmneziaPremiumCountry, AmneziaPremiumIssuedConfig } from '$lib/types';

// Решения списка стран мастера Amnezia Premium: чистые функции над ответом
// GET /api/amnezia/premium/catalog и списком туннелей. Ни сети, ни
// localStorage, ни Date.now() — обе отметки времени приходят с данными,
// поэтому сравнивать их с часами машины не нужно.
//
// Имена полей — НАШИ (camelCase). Портальный snake_case (source_type,
// server_country_code, worker_last_updated, last_downloaded) до фронта не
// доезжает, и чтение по ним дало бы undefined молча: все записи стали бы
// переиздаваемыми, а устаревание — невозможным.

/** Единственный протокол, которым мастер умеет забрать конфигурацию. */
const PREMIUM_USABLE_PROTOCOL = 'awg';

/** Код страны в сравнимый вид: ни портал, ни запись туннеля регистр не нормализуют. */
function premiumCountryCode(value: unknown): string {
	return String(value ?? '')
		.trim()
		.toLowerCase();
}

/**
 * Отдаёт ли подписка страну протоколом, который мастер умеет забрать.
 *
 * Различие пустого списка и отсутствующего поля значимо и схлопыванию не
 * подлежит: пустой список — портал сказал, что страна не отдаётся ничем
 * (отказ); поля нет (null/undefined) — старый ответ портала его не содержал,
 * и отбрасывать по нему страну нельзя (молчание, а не отказ).
 */
export function isPremiumCountryAvailable(country: AmneziaPremiumCountry): boolean {
	const protocols = country.protocols;
	if (protocols == null) return true;
	return protocols.some((p) => premiumCountryCode(p) === PREMIUM_USABLE_PROTOCOL);
}

/**
 * Туннель, уже созданный из конфигурации этой страны, — или undefined.
 *
 * Пустой код страны не совпадает ни с чем: иначе страна без кода
 * «соответствовала» бы каждому туннелю без amneziaCountry.
 */
export function findPremiumCountryTunnel<T extends { amneziaCountry?: string }>(
	tunnels: readonly T[],
	code: string
): T | undefined {
	const cc = premiumCountryCode(code);
	if (!cc) return undefined;
	return tunnels.find((t) => premiumCountryCode(t.amneziaCountry) === cc);
}

/**
 * Состояние выданной конфигурации относительно портала.
 *
 * `unknown` — «сравнивать нечем»: отметки нет или она непарсима. Это НЕ
 * `fresh`: схлопнув их в один false, мастер молча спрятал бы метку
 * «конфигурация устарела» ровно там, где данных не хватает, — то есть выдал
 * бы неизвестное состояние за норму.
 */
export type PremiumConfigFreshness = 'stale' | 'fresh' | 'unknown';

/** Портал обновил конфигурацию позже, чем мы её скачали, — значит выдача устарела. */
export function premiumIssuedConfigFreshness(
	ic: AmneziaPremiumIssuedConfig
): PremiumConfigFreshness {
	const portalMs = Date.parse(ic.portalUpdatedAt?.trim() ?? '');
	const issuedMs = Date.parse(ic.lastIssuedAt?.trim() ?? '');
	if (!Number.isFinite(portalMs) || !Number.isFinite(issuedMs)) return 'unknown';
	return portalMs > issuedMs ? 'stale' : 'fresh';
}

export function premiumIssuedConfigSourceType(ic: AmneziaPremiumIssuedConfig): string {
	return String(ic.sourceType ?? '')
		.trim()
		.toLowerCase();
}

export function isPremiumIssuedConfigActiveDevice(ic: AmneziaPremiumIssuedConfig): boolean {
	return premiumIssuedConfigSourceType(ic) === 'gateway_account';
}

export function isPremiumIssuedConfigReissuable(ic: AmneziaPremiumIssuedConfig): boolean {
	return !isPremiumIssuedConfigActiveDevice(ic);
}

export function premiumIssuedConfigsForCountry(
	issued: AmneziaPremiumIssuedConfig[],
	code: string
): AmneziaPremiumIssuedConfig[] {
	const cc = premiumCountryCode(code);
	return issued.filter((ic) => {
		if (!isPremiumIssuedConfigReissuable(ic)) return false;
		return premiumCountryCode(ic.countryCode) === cc;
	});
}

export function premiumActiveDevicesForCountry(
	issued: AmneziaPremiumIssuedConfig[],
	code: string
): AmneziaPremiumIssuedConfig[] {
	const cc = premiumCountryCode(code);
	return issued.filter((ic) => {
		if (!isPremiumIssuedConfigActiveDevice(ic)) return false;
		return premiumCountryCode(ic.countryCode) === cc;
	});
}

export function isPremiumCountryIssued(issued: AmneziaPremiumIssuedConfig[], code: string): boolean {
	return premiumIssuedConfigsForCountry(issued, code).length > 0;
}

/**
 * Состояние выдач страны целиком: устаревшая запись важнее непонятной, а
 * непонятная — важнее свежей. Выдач нет — сравнивать нечем, `unknown`.
 */
export function premiumCountryConfigFreshness(
	issued: AmneziaPremiumIssuedConfig[],
	code: string
): PremiumConfigFreshness {
	const configs = premiumIssuedConfigsForCountry(issued, code);
	if (configs.length === 0) return 'unknown';
	const states = configs.map(premiumIssuedConfigFreshness);
	if (states.includes('stale')) return 'stale';
	if (states.includes('unknown')) return 'unknown';
	return 'fresh';
}

// === Карточка подписки и метка строки страны ===

/**
 * Флаг страны из двухбуквенного кода: пара региональных индикаторов.
 *
 * Всё, что не пара латинских букв, даёт ПУСТУЮ строку: половина флага и
 * «квадратик с кодом» в списке стран читаются как поломка вёрстки, а не как
 * отсутствие данных.
 */
export function premiumCountryFlag(code: string): string {
	const cc = premiumCountryCode(code);
	if (!/^[a-z]{2}$/.test(cc)) return '';
	const base = 0x1f1e6 - 'a'.charCodeAt(0);
	return String.fromCodePoint(...[...cc].map((ch) => base + ch.charCodeAt(0)));
}

/** Порог «истекает» — 30 дней, как в клиенте Amnezia (`apiUtils.h`, `withinDays = 30`). */
const PREMIUM_EXPIRING_DAYS = 30;
const DAY_MS = 86_400_000;

/**
 * Состояние подписки считается ПО ДАТЕ, а не по статусу портала, — так делает
 * клиент Amnezia (`apiAccountInfoModel.cpp`, `isSubscriptionExpired`).
 *
 * `unknown` — даты нет или она непарсима. Это отдельное состояние: свести его
 * к `active` значило бы выдать неизвестное за норму, а к `expired` — запретить
 * выдачу подписке, про срок которой портал просто промолчал.
 */
export type PremiumSubscriptionState = 'active' | 'expiring' | 'expired' | 'unknown';

/** Сколько осталось до конца подписки, мс; null — считать не от чего. */
function premiumMsLeft(endDate: string | undefined, nowMs: number): number | null {
	const left = Date.parse(endDate?.trim() ?? '') - nowMs;
	return Number.isFinite(left) ? left : null;
}

/** Время — аргументом: с `Date.now()` внутри проверка зависела бы от часов машины. */
export function premiumSubscriptionState(
	endDate: string | undefined,
	nowMs: number
): PremiumSubscriptionState {
	const left = premiumMsLeft(endDate, nowMs);
	if (left === null) return 'unknown';
	if (left <= 0) return 'expired';
	return left <= PREMIUM_EXPIRING_DAYS * DAY_MS ? 'expiring' : 'active';
}

/**
 * Дней до конца подписки, вверх (начатый день ещё идёт); null — даты нет.
 * У истёкшей подписки значение нулевое или отрицательное.
 */
export function premiumSubscriptionDaysLeft(
	endDate: string | undefined,
	nowMs: number
): number | null {
	const left = premiumMsLeft(endDate, nowMs);
	return left === null ? null : Math.ceil(left / DAY_MS);
}

/**
 * Можно ли забирать конфигурацию при таком состоянии подписки.
 *
 * Запрещает ТОЛЬКО истёкшая. «Истекает» — предупреждение (жёлтая карточка), а
 * не запрет; «срок неизвестен» тоже не запрет: портал мог не прислать дату, и
 * выключать по этому фичу — значит ломать работающую подписку.
 */
export function isPremiumIssueAllowed(state: PremiumSubscriptionState): boolean {
	return state !== 'expired';
}

/**
 * Дата С ГОДОМ; на мусоре — пустая строка.
 *
 * `formatDate` из `utils/format.ts` не подходит дважды: она печатает без года
 * (а «действует до 03.10» без года бессмысленно) и отдаёт на непарсимом входе
 * `'—'`, то есть рисует прочерк там, где строку показывать вообще не следует.
 */
export function formatPremiumDate(value: string | undefined): string {
	const ms = Date.parse(value?.trim() ?? '');
	if (!Number.isFinite(ms)) return '';
	return new Date(ms).toLocaleDateString('ru-RU', {
		day: '2-digit',
		month: '2-digit',
		year: 'numeric',
	});
}

export type PremiumCountryLabelKind = 'stale' | 'tunnel' | 'issued' | 'external';

export interface PremiumCountryLabel {
	kind: PremiumCountryLabelKind;
	text: string;
}

/**
 * Метка строки страны — ОДНА функция: приоритет задаётся здесь, а не порядком
 * `{:else if}` в разметке.
 *
 * Порядок: устарела → туннель → уже выдавалась → получено вне AWG-M.
 * «Конфиг устарел» обязан идти первым: у страны с туннелем конфигурация
 * устаревает ровно так же, а лесенка, где туннель проверяется раньше, эту
 * метку не покажет никогда — то есть спрячет её у единственных, кому она
 * нужна (пришедших обновить конфигурацию).
 *
 * «Получено вне AWG-M» — последняя: она говорит лишь, что страна УЖЕ занимает
 * слот подписки, хотя конфигурацию по ней выдавали не мы.
 *
 * Она уступает `stale` и `issued` потому, что те сообщают то же самое и притом
 * конкретнее. С `tunnel` причина ДРУГАЯ и слабее: туннель с `amneziaCountry`
 * НЕ доказывает, что страна занимает слот (слот могли отозвать, а туннель
 * остался), и наоборот — «туннель N» не говорит про цену ничего. Туннель
 * выигрывает потому, что он про ЭТОТ роутер и полезнее в строке списка, а не
 * потому, что он информативнее про слоты. Защиту от случайной траты слота
 * несёт не метка, а подтверждение выдачи (AmneziaPremiumWizard.requestConfig),
 * и оно смотрит на записи подписки напрямую, мимо приоритета меток.
 *
 * Формулировка именно такая, потому что это всё, что нам известно: портал
 * помечает такую запись `gateway_account`, а что на том конце — приложение
 * Amnezia, другой роутер или чужая панель — он не сообщает. «Устройство
 * приложения» звучало бы конкретнее, чем есть на самом деле.
 *
 * Почему метка вообще нужна (F284, поймано на живой подписке): страна с такой
 * записью выглядела совершенно свободной — ни одна ветка её не описывала, — и
 * выдача создавала ВТОРУЮ запись по той же стране, занимая второй слот.
 * Выдачу метка не запрещает: делать это законно, пользователь просто обязан
 * видеть цену.
 */
export function premiumCountryLabel<T extends { name: string; amneziaCountry?: string }>(
	code: string,
	issued: AmneziaPremiumIssuedConfig[],
	tunnels: readonly T[]
): PremiumCountryLabel | null {
	if (premiumCountryConfigFreshness(issued, code) === 'stale') {
		return { kind: 'stale', text: 'конфиг устарел' };
	}
	const tunnel = findPremiumCountryTunnel(tunnels, code);
	if (tunnel) return { kind: 'tunnel', text: `туннель ${tunnel.name}` };
	if (isPremiumCountryIssued(issued, code)) {
		return { kind: 'issued', text: 'конфиг уже выдавался' };
	}
	if (premiumActiveDevicesForCountry(issued, code).length > 0) {
		return { kind: 'external', text: 'получено вне AWG-M' };
	}
	return null;
}
