import { describe, expect, it } from 'vitest';
import { resolveIconSlug, isPresetIconResolvable } from './resolve-icon-slug';

// Names that MUST resolve to a brandIcons slug (so removing their inline
// duplicate in service-icons.ts is behavior-neutral).
const BRAND_PARITY: [name: string, slug: string][] = [
	['Telegram', 'telegram'],
	['YouTube', 'youtube'],
	['Google', 'google'],
	['WhatsApp', 'whatsapp'],
	['Facebook', 'facebook'],
	['Steam', 'steam'],
	['Discord', 'discord'],
	['GitHub', 'github'],
	['Samsung', 'samsung'],
	['Microsoft', 'microsoft'],
	['Spotify', 'spotify'],
	['Netflix', 'netflix'],
	['TikTok', 'tiktok'],
	['Twitch', 'twitch'],
	['Cloudflare', 'cloudflare'],
	['Roblox', 'roblox'],
	['Apple', 'apple'],
	['Twitter', 'x'],
	['Instagram', 'instagram'],
	// Alternate brand names that need an explicit alias (no compact match):
	['ChatGPT', 'openai'],
	['OpenAI', 'openai'],
	['x.com', 'x'],
];

describe('resolveIconSlug brand parity', () => {
	for (const [name, slug] of BRAND_PARITY) {
		it(`${name} → ${slug}`, () => {
			expect(resolveIconSlug(name)).toBe(slug);
			expect(isPresetIconResolvable(slug)).toBe(true);
		});
	}
});

describe('resolveIconSlug keeps custom (non-brandIcons) names unresolved', () => {
	// These have no brandIcons equivalent and must stay on the inline
	// keyword path (resolveIconSlug returns undefined).
	for (const name of ['VK', 'Mail', 'Почта', 'TMDB', 'Amazon', 'LinkedIn']) {
		it(`${name} → undefined`, () => {
			expect(resolveIconSlug(name)).toBeUndefined();
		});
	}
});
