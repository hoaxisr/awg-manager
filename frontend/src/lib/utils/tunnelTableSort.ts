export const AWG_TUNNEL_SORT_DEFAULTS = {
	name: true,
	status: false,
	endpoint: true,
	traffic: false,
	handshake: false,
} as const;

export const SINGBOX_TUNNEL_SORT_DEFAULTS = {
	delay: true,
	name: true,
	protocol: true,
	server: true,
	running: false,
	traffic: false,
	ping: true,
} as const;

export const SUBSCRIPTION_SORT_DEFAULTS = {
	delay: true,
	label: true,
	mode: true,
	active: true,
	traffic: false,
	updated: false,
	ping: true,
} as const;

export const SUBSCRIPTION_MEMBERS_SORT_DEFAULTS = {
	delay: true,
	server: true,
	protocol: true,
	ping: true,
	tag: true,
	status: false,
} as const;

export function compareString(a: string | null | undefined, b: string | null | undefined): number {
	return String(a ?? '').localeCompare(String(b ?? ''), 'ru', { sensitivity: 'base', numeric: true });
}

export function compareNumber(a: number, b: number): number {
	return a - b;
}

export function compareNullableNumber(
	a: number | null | undefined,
	b: number | null | undefined,
	nullsLast = true,
): number {
	const aNull = a === null || a === undefined || Number.isNaN(a);
	const bNull = b === null || b === undefined || Number.isNaN(b);
	if (aNull && bNull) return 0;
	if (aNull) return nullsLast ? 1 : -1;
	if (bNull) return nullsLast ? -1 : 1;
	return a - b;
}

export function compareBool(a: boolean | null | undefined, b: boolean | null | undefined): number {
	return Number(Boolean(a)) - Number(Boolean(b));
}

export function applyDirection(result: number, asc: boolean): number {
	return asc ? result : -result;
}

export function ariaSort(activeSortKey: string | null, key: string, asc: boolean): 'ascending' | 'descending' | 'none' {
	if (activeSortKey !== key) return 'none';
	return asc ? 'ascending' : 'descending';
}

function delayRank(value: number | null | undefined): number {
	if (value === null || value === undefined || Number.isNaN(value) || value <= 0) return 1;
	return 0;
}

export function compareDelayLike(a: number | null | undefined, b: number | null | undefined, asc: boolean): number {
	const rankDiff = delayRank(a) - delayRank(b);
	if (rankDiff !== 0) return rankDiff;
	return applyDirection(compareNullableNumber(a, b, true), asc);
}
