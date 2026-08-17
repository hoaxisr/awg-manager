import type { SubscriptionHeader } from '$lib/types';

export function parseHeadersText(text: string): SubscriptionHeader[] {
	const lines = text.split('\n');
	const out: SubscriptionHeader[] = [];
	for (const raw of lines) {
		const line = raw.trim();
		if (!line || line.startsWith('#')) continue;
		const idx = line.indexOf(':');
		if (idx <= 0) continue;
		const name = line.slice(0, idx).trim();
		const value = line.slice(idx + 1).trim();
		if (name && value) out.push({ name, value });
	}
	return out;
}

export function serializeHeaders(headers: SubscriptionHeader[]): string {
	return headers.map((h) => `${h.name}: ${h.value}`).join('\n');
}

function randomHex(len: number): string {
	const chars = '0123456789abcdef';
	let res = '';
	for (let i = 0; i < len; i++) {
		res += chars[Math.floor(Math.random() * chars.length)];
	}
	return res;
}

export function generateHappPreset(): string {
	const models = [
		'iPhone 15 Pro',
		'iPhone 15 Pro Max',
		'iPhone 16 Pro',
		'iPhone 16 Pro Max',
		'iPhone 17 Pro',
		'iPhone 17 Pro Max',
	];
	const model = models[Math.floor(Math.random() * models.length)];
	const hwid = randomHex(16);
	const buildTime = Date.now().toString() + Math.floor(Math.random() * 1000).toString();
	const minorVer = Math.floor(Math.random() * 5);

	return `User-Agent: Happ/4.6.${minorVer}/ios/${buildTime}
X-Device-OS: iOS
X-HWID: ${hwid}
X-Device-Locale: ru
X-Ver-OS: 18.${minorVer + 1}
X-App-Version: 4.6.${minorVer}
X-Device-Model: ${model}`;
}

export function generateMihomoPreset(): string {
	const versions = ['v1.18.10', 'v1.19.0', 'v1.19.2', 'v1.20.0'];
	const v = versions[Math.floor(Math.random() * versions.length)];
	return `User-Agent: mihomo/${v} (Clash.Meta)`;
}

export function generateSingboxPreset(): string {
	const versions = ['v1.10.0', 'v1.10.7', 'v1.11.0', 'v1.11.2', 'v1.14.20'];
	const v = versions[Math.floor(Math.random() * versions.length)];
	return `User-Agent: sing-box/${v}`;
}

export function generateV2rayNPreset(): string {
	const versions = ['6.39', '6.40', '6.42', '6.45'];
	const v = versions[Math.floor(Math.random() * versions.length)];
	return `User-Agent: v2rayN/${v} (Windows NT 10.0; Win64; x64)`;
}

export function detectHeaderProfileForUrl(rawUrl: string): HeaderProfileKind {
	const lower = rawUrl.trim().toLowerCase();
	if (!lower) return 'happ';

	// Clash / Mihomo indicators
	if (
		lower.startsWith('clash://') ||
		lower.startsWith('clashmeta://') ||
		lower.includes('clash') ||
		lower.includes('mihomo') ||
		lower.includes('meta') ||
		lower.includes('.yaml') ||
		lower.includes('.yml') ||
		lower.includes('format=clash')
	) {
		return 'mihomo';
	}

	// V2Ray / Xray raw indicators
	if (
		lower.startsWith('v2ray://') ||
		lower.includes('v2ray') ||
		lower.includes('v2rayn') ||
		lower.includes('format=v2ray')
	) {
		return 'v2rayn';
	}

	// Sing-box explicit indicators
	if (
		lower.startsWith('singbox://') ||
		lower.startsWith('sing-box://') ||
		lower.includes('format=singbox') ||
		lower.includes('format=sb')
	) {
		return 'singbox';
	}

	// Default to dynamic HAPP (universal support for Remnawave, Marzban, 3X-UI, VOX, Infomir, CloVPN)
	return 'happ';
}

export function generateHeadersForUrl(rawUrl: string): string {
	const kind = detectHeaderProfileForUrl(rawUrl);
	switch (kind) {
		case 'mihomo':
			return generateMihomoPreset();
		case 'singbox':
			return generateSingboxPreset();
		case 'v2rayn':
			return generateV2rayNPreset();
		default:
			return generateHappPreset();
	}
}

export const SINGBOX_PRESET = `User-Agent: sing-box/v1.14.20`;
export const DEFAULT_PRESET = SINGBOX_PRESET;
export const HAPP_PRESET = generateHappPreset();
export const MIHOMO_PRESET = generateMihomoPreset();

export const ALL_HEADERS_PRESET = `# Заполните только нужные строки. Пустые игнорируются при сохранении.
User-Agent:
Accept-Encoding:
X-HWID:
X-Device-OS:
X-Device-Locale:
X-Device-Model:
X-Ver-OS:
X-App-Version:
X-Real-IP:
X-Forwarded-For:`;
