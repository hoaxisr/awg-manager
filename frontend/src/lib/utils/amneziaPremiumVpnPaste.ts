import { classifyVpnLink, isVpnLink } from '$lib/utils/vpnlink';

// Вид кнопки и обвязки поля «вставить ссылку»: распознавание клиентских
// vpn://-ссылок и ничего больше. Решения списка стран подписки живут в
// amneziaPremiumCatalog.ts — у них другие данные и другой вызывающий.

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
