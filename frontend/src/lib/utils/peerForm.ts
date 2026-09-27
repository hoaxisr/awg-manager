// ---------------------------------------------------------------------------
// Валидация полей модалок клиента встроенного сервера (F156).
//
// Проверки только фронтовые (UX) — бэкенд-проверки не заменяют и не
// дублируют логику сервера, лишь блокируют явно невалидный ввод раньше.
// ---------------------------------------------------------------------------

import { isIPv4, isIPv6, isCIDR, cidrOverlaps } from './cidr';

/**
 * Tunnel IP клиента встроенного сервера: обязан быть IPv4 CIDR с префиксом.
 * Возвращает null, если значение корректно, иначе текст ошибки по-русски.
 */
export function validateTunnelIP(v: string): string | null {
	const value = v.trim();
	if (value === '') return 'укажите адрес';
	if (!value.includes('/')) return 'укажите префикс, например /32';
	if (!isCIDR(value)) return 'некорректный IPv4-адрес с префиксом';
	return null;
}

/**
 * Список DNS-серверов через запятую: IPv4 или IPv6.
 * Пустая строка допустима (используются значения по умолчанию).
 */
export function validateDNSList(v: string): string | null {
	const value = v.trim();
	if (value === '') return null;
	const entries = value.split(',').map((s) => s.trim());
	for (const entry of entries) {
		if (entry === '' || !(isIPv4(entry) || isIPv6(entry))) {
			return `некорректный DNS-адрес: ${entry}`;
		}
	}
	return null;
}

function isAnyCIDR(v: string): boolean {
	const slash = v.lastIndexOf('/');
	if (slash === -1) return false;
	const ip = v.slice(0, slash);
	const prefixStr = v.slice(slash + 1);
	const prefix = Number(prefixStr);
	if (prefixStr === '' || !Number.isInteger(prefix) || prefix < 0) return false;
	if (isIPv4(ip)) return prefix <= 32;
	if (isIPv6(ip)) return prefix <= 128;
	return false;
}

/** «AllowedIPs клиента»: CIDR через запятую, IPv4/IPv6; пусто — весь трафик. */
export function validateClientAllowedIPs(v: string): string | null {
	const value = v.trim();
	if (value === '') return null;
	for (const entry of value.split(',').map((s) => s.trim())) {
		if (entry === '' || !isAnyCIDR(entry)) return `некорректный CIDR: ${entry}`;
	}
	return null;
}

/** «Сети за клиентом» из textarea: по одной в строке или через запятую. */
export function parseRemoteSubnets(text: string): string[] {
	return text
		.split(/[\n,]+/)
		.map((s) => s.trim())
		.filter((s) => s !== '');
}

/** IPv4 CIDR, не 0.0.0.0/0, без пересечений внутри. Пересечения с занятым знает только бэкенд. */
export function validateRemoteSubnets(text: string): string | null {
	const list = parseRemoteSubnets(text);
	for (let i = 0; i < list.length; i++) {
		const entry = list[i];
		if (!isCIDR(entry)) return `некорректная IPv4-сеть: ${entry}`;
		if (entry.endsWith('/0')) return '0.0.0.0/0 задать нельзя';
		for (let j = 0; j < i; j++) {
			if (cidrOverlaps(list[j], entry)) return `сети пересекаются: ${list[j]} и ${entry}`;
		}
	}
	return null;
}

export function validatePeerNetworks(allowed: string, subnets: string): string | null {
	return validateClientAllowedIPs(allowed) ?? validateRemoteSubnets(subnets);
}
