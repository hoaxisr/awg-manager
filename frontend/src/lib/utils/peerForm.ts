// ---------------------------------------------------------------------------
// Валидация полей модалок клиента встроенного сервера (F156).
//
// Проверки только фронтовые (UX) — бэкенд-проверки не заменяют и не
// дублируют логику сервера, лишь блокируют явно невалидный ввод раньше.
// ---------------------------------------------------------------------------

import { isIPv4, isIPv6, isCIDR } from './cidr';

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
