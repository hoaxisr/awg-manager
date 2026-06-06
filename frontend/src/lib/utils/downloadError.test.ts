import { describe, expect, it } from 'vitest';
import {
	DOWNLOAD_ERROR_CODES,
	downloadErrorToText,
	downloadRouteError,
	humanizeDownloadError,
} from './downloadError';

describe('humanizeDownloadError', () => {
	it('classifies singbox-off from backend code', () => {
		const h = humanizeDownloadError({
			message: 'outbound "DE" is unavailable: sing-box is not running',
			code: DOWNLOAD_ERROR_CODES.SINGBOX_NOT_RUNNING,
		});
		expect(h.kind).toBe('singbox-off');
		expect(h.title).toMatch(/sing-box/i);
	});

	it('classifies singbox-off when local proxy is not ready', () => {
		const h = humanizeDownloadError({
			message: 'outbound "DE" is unavailable: sing-box proxy port not listening',
			code: DOWNLOAD_ERROR_CODES.SINGBOX_NOT_READY,
		});
		expect(h.kind).toBe('singbox-off');
		expect(h.title).toMatch(/запускается/i);
	});

	it('classifies singbox-route from egress failure code', () => {
		const h = humanizeDownloadError({
			message:
				'download via RelayCH (singbox): request failed: Get "https://github.com/foo/VERSION": EOF',
			code: DOWNLOAD_ERROR_CODES.SINGBOX_EGRESS_FAILED,
		});
		expect(h.kind).toBe('singbox-route');
		expect(h.detail).toMatch(/туннель/i);
		expect(h.detail).not.toMatch(/запускается/i);
	});

	it('classifies awg-down from backend code', () => {
		const h = humanizeDownloadError({
			message: 'outbound "awg-de" interface "awg0" is not present',
			code: DOWNLOAD_ERROR_CODES.AWG_DOWN,
		});
		expect(h.kind).toBe('awg-down');
	});

	it('classifies timeout from backend code', () => {
		const h = humanizeDownloadError({
			message: 'download timed out',
			code: DOWNLOAD_ERROR_CODES.TIMEOUT,
		});
		expect(h.kind).toBe('timeout');
	});

	it('classifies network from backend code', () => {
		const h = humanizeDownloadError({
			message: 'connection reset by peer',
			code: DOWNLOAD_ERROR_CODES.NETWORK,
		});
		expect(h.kind).toBe('network');
	});

	it('falls back to string patterns when code is missing (legacy)', () => {
		const h = humanizeDownloadError(
			new Error('outbound "sub-14ddb10d" is unavailable: sing-box is not running'),
		);
		expect(h.kind).toBe('singbox-off');
	});

	it('legacy EOF on singbox route without code maps to singbox-route', () => {
		const h = humanizeDownloadError(
			'download via RelayCH (singbox): request failed: Get "https://github.com/foo/VERSION": EOF',
		);
		expect(h.kind).toBe('singbox-route');
		expect(h.detail).toMatch(/перезапустите sing-box/i);
	});

	it('legacy connection refused on singbox route maps to singbox-off (starting)', () => {
		const h = humanizeDownloadError(
			'download via RelayCH (singbox): request failed: dial tcp 127.0.0.1:1080: connect: connection refused',
		);
		expect(h.kind).toBe('singbox-off');
		expect(h.title).toMatch(/запускается/i);
	});

	it('legacy EOF without code stays network (not singbox-route)', () => {
		expect(humanizeDownloadError('http get: EOF').kind).toBe('network');
	});

	it('reads message + code from API client error body', () => {
		const err = Object.assign(new Error('top'), {
			body: { code: DOWNLOAD_ERROR_CODES.SINGBOX_NOT_RUNNING, message: 'sing-box is not running' },
		});
		const h = humanizeDownloadError(err);
		expect(h.kind).toBe('singbox-off');
		expect(h.code).toBe(DOWNLOAD_ERROR_CODES.SINGBOX_NOT_RUNNING);
	});

	it('reads errorCode from UpdateInfo-shaped objects', () => {
		const h = humanizeDownloadError(
			downloadRouteError('download via RelayCH (singbox): EOF', DOWNLOAD_ERROR_CODES.SINGBOX_EGRESS_FAILED),
		);
		expect(h.kind).toBe('singbox-route');
	});

	it('falls back to raw message for unknown errors', () => {
		const h = humanizeDownloadError('repository quota exceeded');
		expect(h.kind).toBe('generic');
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
		const text = downloadErrorToText(
			downloadRouteError('sing-box is not running', DOWNLOAD_ERROR_CODES.SINGBOX_NOT_RUNNING),
		);
		expect(text).toMatch(/sing-box/i);
		expect(text).toMatch(/Direct|AWG/);
	});

	it('accepts an already-humanized error', () => {
		const h = humanizeDownloadError({
			message: 'download timed out',
			code: DOWNLOAD_ERROR_CODES.TIMEOUT,
		});
		expect(downloadErrorToText(h)).toBe(downloadErrorToText(h));
	});
});
