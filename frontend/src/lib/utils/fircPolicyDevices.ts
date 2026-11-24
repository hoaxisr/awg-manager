import type { FircHost, FircPolicy } from '$lib/types/firc';

export type FircDevicesMode = 'all' | 'allow';

const POLICY_PREFIX = 'policy:';

/**
 * Политика, на которую указывает запись селектора; null — запись не про политику.
 * Как internal_name в fircd: имя сверяется с описаниями по порядку списка, поэтому
 * описание, общее для двух политик, принадлежит первой.
 */
export function policyOfEntry(entry: string, policies: FircPolicy[]): string | null {
	if (!entry.startsWith(POLICY_PREFIX)) return null;
	const name = entry.slice(POLICY_PREFIX.length);
	return policies.find((p) => (p.description?.trim() || p.name) === name)?.name ?? name;
}

function entriesOfPolicy(entries: Iterable<string>, name: string, policies: FircPolicy[]): string[] {
	return Array.from(entries).filter((e) => policyOfEntry(e, policies) === name);
}

export function hasPolicyEntry(entries: Iterable<string>, name: string, policies: FircPolicy[]): boolean {
	return entriesOfPolicy(entries, name, policies).length > 0;
}

/**
 * Набор с политикой или без неё. Вернувшаяся политика пишется так, как была записана
 * изначально: бэкенд снимает с deny каноническую запись политики включённого сервера,
 * и замена описания на имя теряла бы исключение при сохранении.
 */
export function withPolicyEntry(
	entries: Set<string>,
	name: string,
	policies: FircPolicy[],
	present: boolean,
	original: Set<string>,
): Set<string> {
	const next = new Set(entries);
	for (const e of entriesOfPolicy(entries, name, policies)) next.delete(e);
	if (present) {
		const restored = entriesOfPolicy(original, name, policies);
		for (const e of restored.length > 0 ? restored : [`${POLICY_PREFIX}${name}`]) next.add(e);
	}
	return next;
}

/**
 * Попадает ли устройство в маршрутизацию группы и решает ли это его политика.
 * Политика не из загруженного списка не блокирует: переключить её было бы негде.
 */
export function hostDeviceState(
	host: FircHost,
	key: string,
	mode: FircDevicesMode,
	entries: Set<string>,
	policies: FircPolicy[],
): { enabled: boolean; locked: boolean } {
	const name = host.policy?.trim();
	const locked = policies.some((p) => p.name === name) && hasPolicyEntry(entries, name ?? '', policies);
	const listed = locked || entries.has(key);
	return { enabled: mode === 'all' ? !listed : listed, locked };
}

/** Название политики для показа: описание, если оно есть. */
export function policyLabel(name: string, policies: FircPolicy[]): string {
	return policies.find((p) => p.name === name)?.description || name;
}

/** Устройства по адресам IPv4 и IPv6; один адрес у двух устройств — выигрывает активное. */
export function hostsByIp(hosts: FircHost[]): Map<string, FircHost> {
	const map = new Map<string, FircHost>();
	for (const h of hosts) {
		for (const ip of [h.ip, ...(h.ip6 ?? [])]) {
			if (!ip || ip === '0.0.0.0') continue;
			if (!map.get(ip)?.active) map.set(ip, h);
		}
	}
	return map;
}
