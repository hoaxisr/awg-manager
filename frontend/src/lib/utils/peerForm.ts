// ---------------------------------------------------------------------------
// Валидация полей модалок клиента встроенного сервера (F156).
//
// Проверки только фронтовые (UX) — бэкенд-проверки не заменяют и не
// дублируют логику сервера, лишь блокируют явно невалидный ввод раньше.
// ---------------------------------------------------------------------------

import { m } from '$lib/i18n';
import { isIPv4, isIPv6, isCIDR, cidrOverlaps } from './cidr';

/**
 * Tunnel IP клиента встроенного сервера: обязан быть IPv4 CIDR с префиксом.
 * Возвращает null, если значение корректно, иначе текст ошибки по-русски.
 */
export function validateTunnelIP(v: string): string | null {
	const value = v.trim();
	if (value === '') return m.peer_form_need_address();
	if (!value.includes('/')) return m.peer_form_need_prefix();
	if (!isCIDR(value)) return m.peer_form_bad_ipv4_prefix();
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
			return m.peer_form_bad_dns({ entry });
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

/** Элементы «AllowedIPs клиента»: через запятую и/или с новой строки; пустые пропускаются. */
function splitClientAllowedIPs(text: string): string[] {
	return text
		.split(/[\n,]+/)
		.map((s) => s.trim())
		.filter((s) => s !== '');
}

/** Формат хранения и API: CIDR через «, ». */
export function normalizeClientAllowedIPs(text: string): string {
	return splitClientAllowedIPs(text).join(', ');
}

/** Для textarea: перенос строки после каждой запятой — длинный пресет читаем. */
export function formatClientAllowedIPs(text: string): string {
	return splitClientAllowedIPs(text).join(',\n');
}

/** «AllowedIPs клиента»: CIDR IPv4/IPv6; пусто — весь трафик. */
export function validateClientAllowedIPs(v: string): string | null {
	for (const entry of splitClientAllowedIPs(v)) {
		if (!isAnyCIDR(entry)) return m.peer_form_bad_cidr({ entry });
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
		if (!isCIDR(entry)) return m.peer_form_bad_ipv4_net({ entry });
		if (entry.endsWith('/0')) return m.peer_form_default_route_forbidden();
		for (let j = 0; j < i; j++) {
			if (cidrOverlaps(list[j], entry)) return m.peer_form_nets_overlap({ first: list[j], second: entry });
		}
	}
	return null;
}

export function validatePeerNetworks(allowed: string, subnets: string): string | null {
	return validateClientAllowedIPs(allowed) ?? validateRemoteSubnets(subnets);
}
