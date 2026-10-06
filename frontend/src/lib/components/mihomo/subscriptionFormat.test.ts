import { describe, expect, it } from 'vitest';
import { normalizeMihomoSubscriptionFormat } from './subscriptionFormat';

describe('normalizeMihomoSubscriptionFormat', () => {
	it('maps a legacy URL subscription to a Mihomo provider', () => {
		expect(normalizeMihomoSubscriptionFormat('auto', {
			url: 'https://example.test/provider.yaml',
		})).toBe('mihomo-provider');
	});

	it('maps a legacy inline subscription to share links', () => {
		expect(normalizeMihomoSubscriptionFormat('auto', {
			inline: 'vless://example',
		})).toBe('share-links');
	});

	it.each(['mihomo-provider', 'share-links'] as const)('keeps concrete format %s', (format) => {
		expect(normalizeMihomoSubscriptionFormat(format, {
			url: 'https://example.test/provider.yaml',
			inline: 'vless://example',
		})).toBe(format);
	});
});
