// Pure logic for the FakeIP «Устройства» chip: per-device «назначение»
// (targeting) resolution and find-the-rule-for-a-device.
//
// Kept framework-free + unit-tested (deviceTargeting.test.ts). The Svelte chip
// (DevicesTab.svelte) feeds it live store snapshots and renders the result.
//
// «назначение» (mockup page-devices):
//   персональный — route-правило по source_ip_cidr содержит IP устройства
//                  (badge + имя привязанного outbound).
//   прямой       — иначе (нет персональной привязки).
import { ipInCIDR } from '$lib/utils/cidr';
import type { PolicyTunNATSegmentInfo, SingboxRouterRule } from '$lib/types';

export type DeviceMode = 'personal' | 'direct';

export interface DeviceTargeting {
	mode: DeviceMode;
	/** Index of the personal route rule (for update/delete); null otherwise. */
	ruleIndex: number | null;
	/** The bound outbound tag (mode === 'personal'); null otherwise. */
	outbound: string | null;
}

/**
 * Normalize one `source_ip_cidr` entry to a slashed CIDR so `ipInCIDR` (which
 * requires a prefix) accepts it. A bare IPv4 («192.168.0.70») becomes «/32».
 */
function asCidr(entry: string): string {
	return entry.includes('/') ? entry : `${entry}/32`;
}

/** True if any `source_ip_cidr` entry of the rule covers `ip`. */
function ruleMatchesDevice(rule: SingboxRouterRule, ip: string): boolean {
	const list = rule.source_ip_cidr;
	if (!list || list.length === 0) return false;
	return list.some((entry) => {
		const cidr = asCidr(entry.trim());
		return ipInCIDR(ip, cidr);
	});
}

/**
 * First route rule whose `source_ip_cidr` covers `ip`. first-match mirrors the
 * router's own ordering; the editor binds a device to exactly one outbound, so
 * the first hit is the binding. Returns its index (for update/delete) or null.
 */
export function findRuleIndexForDevice(rules: SingboxRouterRule[], ip: string): number | null {
	if (!ip) return null;
	for (let i = 0; i < rules.length; i++) {
		if (ruleMatchesDevice(rules[i], ip)) return i;
	}
	return null;
}

/**
 * Resolve a device's «назначение»: personal (an explicit per-device route
 * binds it to an outbound) or direct (no binding).
 */
export function resolveDeviceTargeting(
	ip: string,
	rules: SingboxRouterRule[],
): DeviceTargeting {
	const ruleIndex = findRuleIndexForDevice(rules, ip);
	if (ruleIndex !== null) {
		return {
			mode: 'personal',
			ruleIndex,
			outbound: rules[ruleIndex].outbound ?? null,
		};
	}
	return { mode: 'direct', ruleIndex: null, outbound: null };
}

/**
 * Адрес стоит за динамическим NAT сегмента: маскарад срабатывает и по пути в
 * tun, поэтому sing-box видит адрес tun, а source_ip_cidr не совпадает (#1160).
 *
 * Сегмент с обеими строками (`ip nat` + `ip static`) предпоказ отдаёт как
 * static, хотя маскарад у него жив: здесь он не определяется, предупреждения
 * не будет.
 */
export function sourceIsMasked(ip: string, segments: PolicyTunNATSegmentInfo[]): boolean {
	return segments.some(
		(seg) => seg.mode === 'dynamic' && !!seg.subnet && ipInCIDR(ip, seg.subnet),
	);
}

/** Хотя бы один источник правила стоит за динамическим NAT. */
export function ruleSourceMasked(
	rule: SingboxRouterRule,
	segments: PolicyTunNATSegmentInfo[],
): boolean {
	return (rule.source_ip_cidr ?? []).some((entry) =>
		sourceIsMasked(entry.trim().split('/')[0], segments),
	);
}
