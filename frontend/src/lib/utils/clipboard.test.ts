import { afterEach, describe, expect, it, vi } from 'vitest';
import { copyToClipboard } from './clipboard';

describe('copyToClipboard: запасной путь без HTTPS', () => {
	afterEach(() => {
		vi.unstubAllGlobals();
		document.body.innerHTML = '';
	});

	it.each([
		['успех', () => true, true],
		['отказ', () => false, false],
		['исключение', () => { throw new Error('x'); }, false],
	])('%s: textarea не остаётся в DOM', async (_name, exec, want) => {
		vi.stubGlobal('navigator', {});
		document.execCommand = vi.fn(exec);
		expect(await copyToClipboard('текст')).toBe(want);
		expect(document.execCommand).toHaveBeenCalledWith('copy');
		expect(document.querySelector('textarea')).toBeNull();
	});
});
