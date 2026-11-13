import type { AutolistConfig } from '$lib/types';

/** WAN-URL по умолчанию 0.9.3 (awgmautolist/internal/config). */
export const AUTOLIST_DEFAULT_WAN_URL = 'https://ya.ru/robots.txt';

/**
 * Бюджет добавлений 0.9.3: по умолчанию 50 в скользящий час, допустимо
 * 1..500 (awgmautolist/internal/config: DefaultAddsPerHour, MaxAddsPerHour).
 */
export const AUTOLIST_DEFAULT_ADDS_PER_HOUR = 50;
export const AUTOLIST_MAX_ADDS_PER_HOUR = 500;

/** Нормализация значения поля addsPerHour: строка поля ввода, мусор, вне 1..500 — к дефолту. */
export function normalizeAutolistAddsPerHour(raw: unknown): number {
	const parsed = typeof raw === 'number' ? raw : typeof raw === 'string' && raw.trim() !== '' ? Number(raw) : NaN;
	if (!Number.isInteger(parsed) || parsed < 1 || parsed > AUTOLIST_MAX_ADDS_PER_HOUR) {
		return AUTOLIST_DEFAULT_ADDS_PER_HOUR;
	}
	return parsed;
}

/**
 * Whitelist ровно настроек 0.9.3: лишние поля черновика (legacy, локальные
 * флаги) в JSON-тело не попадают. Строки сохраняются пустыми — они значимы
 * ('' targetInterface = первый доступный туннель, '' resolveVia = dns.final).
 */
export function buildAutolistConfigPayload(cfg: AutolistConfig): AutolistConfig {
	return {
		autoAdd: cfg.autoAdd,
		targetInterface: cfg.targetInterface?.trim() ?? '',
		resolveVia: cfg.resolveVia ?? '',
		wanCheckUrl: cfg.wanCheckUrl?.trim() || AUTOLIST_DEFAULT_WAN_URL,
		wanMonitorEnabled: cfg.wanMonitorEnabled ?? false,
		tunnelProbe: cfg.tunnelProbe ?? false,
		excludeDomains: cfg.excludeDomains ?? [],
		excludeRuleSets: cfg.excludeRuleSets ?? [],
		udpEnabled: cfg.udpEnabled ?? false,
		verbose: cfg.verbose ?? false,
		addsPerHour: normalizeAutolistAddsPerHour(cfg.addsPerHour),
	};
}

/**
 * Черновик из прочитанного конфига: отсутствующие поля — значения по
 * умолчанию. Никакого stale default draft: draft живёт только после
 * успешного чтения (cfgLoadSucceeded в компоненте).
 */
export function autolistConfigDraft(cfg: Partial<AutolistConfig>): AutolistConfig {
	return {
		autoAdd: cfg.autoAdd === true,
		targetInterface: cfg.targetInterface ?? '',
		resolveVia: cfg.resolveVia ?? '',
		wanCheckUrl: cfg.wanCheckUrl?.trim() || AUTOLIST_DEFAULT_WAN_URL,
		wanMonitorEnabled: cfg.wanMonitorEnabled === true,
		tunnelProbe: cfg.tunnelProbe === true,
		excludeDomains: cfg.excludeDomains ?? [],
		excludeRuleSets: cfg.excludeRuleSets ?? [],
		udpEnabled: cfg.udpEnabled === true,
		verbose: cfg.verbose === true,
		addsPerHour: normalizeAutolistAddsPerHour(cfg.addsPerHour),
	};
}

/**
 * Черновик по умолчанию для модалки настроек до успешного чтения: форма
 * видна сразу, но сохранение и autoAdd заблокированы до успешного load.
 */
export function defaultAutolistConfig(): AutolistConfig {
	return autolistConfigDraft({ autoAdd: false });
}

/**
 * Канонизация домена так же, как это делает бэкенд
 * (awgmautolist/internal/model target.go NormalizeDomain): нижний регистр,
 * без завершающей точки. IP-литерал доменом не является.
 */
export function canonicalAutolistDomain(raw: string): string | null {
	const name = raw.trim().toLowerCase().replace(/\.$/, '');
	if (name === '') return null;
	if (isAutolistIPLiteral(name)) return null;
	if (!/^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$/.test(name)) return null;
	return name;
}

/** IP-литерал (v4/v6): excludeDomains бэкенда принимает только домены. */
export function isAutolistIPLiteral(value: string): boolean {
	return value.includes(':') || /^\d{1,3}(\.\d{1,3}){3}$/.test(value);
}

/**
 * Слияние доменов выбранных кандидатов в текущий список исключений.
 * Возвращает null, если хоть одно значение — не домен (IP-литерал или
 * мусор): конфиг бэкенда отклонит такой excludeDomains целиком, поэтому
 * частичное обновление недопустимо. Вызывающий обязан предварительно
 * отфильтровать IP-кандидатов и показать отдельную ошибку.
 */
export function mergeAutolistExclusions(current: string[] | undefined, domains: string[]): string[] | null {
	const merged = new Set((current ?? []).map((d) => d.trim().toLowerCase().replace(/\.$/, '')).filter((d) => d !== ''));
	for (const d of domains) {
		const name = canonicalAutolistDomain(d);
		if (!name) return null;
		merged.add(name);
	}
	return [...merged];
}
