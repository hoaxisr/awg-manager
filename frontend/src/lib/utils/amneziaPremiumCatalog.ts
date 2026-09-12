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
