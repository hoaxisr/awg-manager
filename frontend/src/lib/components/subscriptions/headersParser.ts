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

// Значение по умолчанию для поля заголовков, пока пресеты (их отдаёт
// /singbox/subscriptions/header-profiles) не загружены.
export const DEFAULT_PRESET = `User-Agent: sing-box/v1.14.20`;

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
