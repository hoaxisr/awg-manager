// Скрыт ли источник правила динамическим NAT сегмента (метка в «Маршрутах»).
import { ipInCIDR } from '$lib/utils/cidr';
import type { PolicyTunNATSegmentInfo, SingboxRouterRule } from '$lib/types';

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
