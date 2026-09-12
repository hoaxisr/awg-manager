import { classifyVpnLink, isVpnLink } from '$lib/utils/vpnlink';

// Обвязка поля «вставить ссылку»: распознавание клиентских vpn://-ссылок и
// ничего больше. Решения списка стран подписки живут в
// amneziaPremiumCatalog.ts — у них другие данные и другой вызывающий.

export function shouldShowPremiumChrome(raw: string): boolean {
	const trimmed = raw.trim();
	if (!trimmed || !isVpnLink(trimmed)) return false;
	return classifyVpnLink(trimmed) !== 'regular';
}
