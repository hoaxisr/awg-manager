import { describe, expect, it } from 'vitest';
import { hasServiceIconKeywordMatch } from './service-icons';

describe('hasServiceIconKeywordMatch', () => {
	it('matches keywords for kept (non-brandIcons) inline icons', () => {
		// These have no brandIcons equivalent, so they stay on the keyword path.
		expect(hasServiceIconKeywordMatch('My torrent list')).toBe(true);
		expect(hasServiceIconKeywordMatch('Amazon')).toBe(true);
		expect(hasServiceIconKeywordMatch('LinkedIn')).toBe(true);
		expect(hasServiceIconKeywordMatch('VK feed')).toBe(true);
	});

	it('no longer keyword-matches brands moved to brandIcons', () => {
		// Removed inline duplicates — these resolve via resolveIconSlug→brandIcons
		// (usePreset), not the keyword path, so hasServiceIconKeywordMatch is false.
		expect(hasServiceIconKeywordMatch('YouTube DISABLED')).toBe(false);
		expect(hasServiceIconKeywordMatch('Cloudflare IPs')).toBe(false);
		expect(hasServiceIconKeywordMatch('Facebook')).toBe(false);
	});

	it('does not match unrelated names', () => {
		expect(hasServiceIconKeywordMatch('My custom list')).toBe(false);
		expect(hasServiceIconKeywordMatch('')).toBe(false);
	});
});
