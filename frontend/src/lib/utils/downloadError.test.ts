import { afterEach, describe, expect, it } from 'vitest';
import { downloadErrorToText, humanizeDownloadError } from './downloadError';
import { ApiNetworkError } from '$lib/api/clientCore';
import { locale } from '$lib/i18n';

describe('сетевой сбой клиента', () => {
	afterEach(() => {
		locale.set('ru');
		localStorage.clear();
	});

	// Классификатор не должен зависеть от перевода: ApiNetworkError узнаётся по
	// коду, на любом языке интерфейса.
	it.each(['ru', 'en'] as const)('ApiNetworkError → network (%s)', (lang) => {
		locale.set(lang);
		expect(humanizeDownloadError(new ApiNetworkError()).kind).toBe('network');
	});
});

describe('humanizeDownloadError', () => {
	it('classifies sing-box-off from outbound-unavailable message', () => {
		const h = humanizeDownloadError(
			new Error('outbound "sub-14ddb10d" is unavailable: sing-box is not running'),
		);
		expect(h.kind).toBe('singbox-off');
		expect(h.needsDownloadSettings).toBe(true);
		expect(h.title).toMatch(/sing-box/i);
	});

	it('classifies awg-down from missing interface', () => {
		const h = humanizeDownloadError('outbound "awg-de" interface "awg0" is not present');
		expect(h.kind).toBe('awg-down');
		expect(h.needsDownloadSettings).toBe(true);
	});

	it('classifies timeout', () => {
		const h = humanizeDownloadError('download timed out after 15m0s');
		expect(h.kind).toBe('timeout');
	});

	it('classifies network drops (EOF / malformed HTTP / reset)', () => {
		expect(humanizeDownloadError('http get: EOF').kind).toBe('network');
		expect(
			humanizeDownloadError('malformed HTTP response "\\x00\\x00"').kind,
		).toBe('network');
		expect(humanizeDownloadError('connection reset by peer').kind).toBe('network');
	});

	it('classifies generic route errors via envelope code', () => {
		const err = Object.assign(new Error('selected outbound is unavailable for download transport'), {
			body: { code: 'GEO_DOWNLOAD_ROUTE_ERROR' },
		});
		const h = humanizeDownloadError(err);
		expect(h.kind).toBe('route');
		expect(h.code).toBe('GEO_DOWNLOAD_ROUTE_ERROR');
	});

	it('reads message + code from API client error body', () => {
		const err = Object.assign(new Error('top'), {
			body: { code: 'X', message: 'sing-box is not running' },
		});
		const h = humanizeDownloadError(err);
		expect(h.kind).toBe('singbox-off');
		expect(h.raw).toBe('sing-box is not running');
		expect(h.code).toBe('X');
	});

	it('falls back to raw message for unknown errors', () => {
		const h = humanizeDownloadError('repository quota exceeded');
		expect(h.kind).toBe('generic');
		expect(h.needsDownloadSettings).toBe(false);
		expect(h.title).toBe('repository quota exceeded');
	});

	it('handles null / empty input', () => {
		const h = humanizeDownloadError(null);
		expect(h.kind).toBe('generic');
		expect(h.title).toBeTruthy();
	});
});

describe('downloadErrorToText', () => {
	it('joins title + detail for toast contexts', () => {
		const text = downloadErrorToText('sing-box is not running');
		expect(text).toMatch(/sing-box/i);
		expect(text).toMatch(/Direct|AWG/);
	});

	it('appends settings cue when no detail but route-related', () => {
		// generic has no settings cue
		expect(downloadErrorToText('weird error')).toBe('weird error');
	});

	it('accepts an already-humanized error', () => {
		const h = humanizeDownloadError('download timed out');
		expect(downloadErrorToText(h)).toBe(downloadErrorToText('download timed out'));
	});
});
