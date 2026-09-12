import type { AmneziaPremiumIssuedConfig } from '$lib/types';
import { classifyVpnLink, isVpnLink } from '$lib/utils/vpnlink';

export type VpnPastePresentation =
	| { kind: 'neutral'; label: string }
	| { kind: 'regular'; label: string }
	| { kind: 'premium'; label: string };

export function getVpnPastePresentation(raw: string): VpnPastePresentation {
	const trimmed = raw.trim();
	if (!trimmed || !isVpnLink(trimmed)) {
		return { kind: 'neutral', label: 'Вставить ссылку' };
	}
	if (classifyVpnLink(trimmed) === 'regular') {
		return { kind: 'regular', label: 'Вставить ссылку' };
	}
	return { kind: 'premium', label: 'Amnezia Premium' };
}

export function shouldShowPremiumChrome(raw: string): boolean {
	const trimmed = raw.trim();
	if (!trimmed || !isVpnLink(trimmed)) return false;
	return classifyVpnLink(trimmed) !== 'regular';
}

export function premiumIssuedConfigSourceType(ic: AmneziaPremiumIssuedConfig): string {
	return String(ic.sourceType ?? '').trim().toLowerCase();
}

export function isPremiumIssuedConfigActiveDevice(ic: AmneziaPremiumIssuedConfig): boolean {
	return premiumIssuedConfigSourceType(ic) === 'gateway_account';
}

export function isPremiumIssuedConfigReissuable(ic: AmneziaPremiumIssuedConfig): boolean {
	return !isPremiumIssuedConfigActiveDevice(ic);
}

function premiumCountryCode(value: unknown): string {
	return String(value ?? '').trim().toLowerCase();
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

/** portalUpdatedAt позже lastIssuedAt — адрес на сервере меняли после последней выдачи. */
export function isPremiumCountryConfigStale(issued: AmneziaPremiumIssuedConfig[], code: string): boolean {
	return premiumIssuedConfigsForCountry(issued, code).some((ic) => {
		const workerRaw = ic.portalUpdatedAt?.trim();
		const downloadedRaw = ic.lastIssuedAt?.trim();
		if (!workerRaw || !downloadedRaw) return false;
		const workerMs = Date.parse(workerRaw);
		const downloadedMs = Date.parse(downloadedRaw);
		if (!Number.isFinite(workerMs) || !Number.isFinite(downloadedMs)) return false;
		return workerMs > downloadedMs;
	});
}
