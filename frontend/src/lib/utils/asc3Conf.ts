import type { ASCParams } from '$lib/types';

// Параметры AWG 3.x из ASC интерфейса в форме NDMS (5.02.A.11+): kebab-ключи,
// пары *-start/*-end, флаги числами. Строки .conf — той же грамматики, что
// бэкендовый signature.writeASC3Conf: только заданное, "N" или "N-M", флаги "on".
const RANGES: [string, string][] = [
	['ContentPaddingAddition', 'content-padding-addition'],
	['RekeyAfterTime', 'rekey-after-time'],
	['RekeyTimeout', 'rekey-timeout'],
	['RejectAfterTime', 'reject-after-time'],
	['KeepaliveTimeout', 'keepalive-timeout'],
	['MaxHandshakeAttempts', 'max-handshake-attempts'],
];

function field(asc: ASCParams | null, key: string): unknown {
	return (asc as unknown as Record<string, unknown> | null)?.[key];
}

/** Ключ защиты заголовков; непустой — сервер AWG 3.x. */
export function headerProtectionKey(asc: ASCParams | null): string {
	return String(field(asc, 'header-protection-key') ?? '');
}

export function asc3ConfLines(asc: ASCParams | null): string[] {
	const lines: string[] = [];
	const hp = headerProtectionKey(asc);
	if (hp) lines.push(`HeaderProtectionKey = ${hp}`);
	for (const [name, key] of RANGES) {
		const lo = Number(field(asc, `${key}-start`) ?? 0);
		const hi = Number(field(asc, `${key}-end`) ?? 0);
		if (lo || hi) lines.push(`${name} = ${lo === hi ? lo : `${lo}-${hi}`}`);
	}
	if (Number(field(asc, 'random-trailers'))) lines.push('RandomTrailers = on');
	if (Number(field(asc, 'disable-cookies'))) lines.push('DisableCookies = on');
	return lines;
}
