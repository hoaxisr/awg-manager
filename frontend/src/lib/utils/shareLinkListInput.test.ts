import { describe, expect, it } from 'vitest';
import {
	appendImportedFileText,
	mergePastedShareList,
	normalizeSpaceSeparatedShareLinks,
} from './shareLinkListInput';

describe('normalizeSpaceSeparatedShareLinks', () => {
	it('splits space-separated share links into lines', () => {
		expect(normalizeSpaceSeparatedShareLinks('vless://a vless://b')).toBe(
			'vless://a\nvless://b',
		);
	});

	it('splits space-separated tt:// links into lines', () => {
		expect(normalizeSpaceSeparatedShareLinks('tt://?A tt://?B')).toBe('tt://?A\ntt://?B');
	});

	it('does not touch a TrustTunnel TOML body (no scheme after the space)', () => {
		const toml = 'hostname = "vpn.example.com"\naddresses = ["1.2.3.4:443"]\n';
		expect(normalizeSpaceSeparatedShareLinks(toml)).toBe(toml);
	});

	it('does not touch multiline JSON (no share schemes inside)', () => {
		const json = '{\n  "profiles": [ { "profileName": "default" } ]\n}';
		expect(normalizeSpaceSeparatedShareLinks(json)).toBe(json);
	});
});

describe('mergePastedShareList', () => {
	it('inserts normalized paste at caret', () => {
		const { next, caret } = mergePastedShareList('x', 1, 1, ' vless://a');
		expect(next).toBe('x\nvless://a');
		expect(caret).toBe(next.length);
	});
});

describe('appendImportedFileText', () => {
	it('replaces empty textarea with file content as-is', () => {
		const json = '{\n  "profiles": []\n}';
		expect(appendImportedFileText('', json)).toEqual({ text: json, error: '' });
		expect(appendImportedFileText('  \n', json)).toEqual({ text: json, error: '' });
	});

	it('appends share links to non-empty textarea on a new line', () => {
		expect(appendImportedFileText('vless://a', 'vless://b')).toEqual({
			text: 'vless://a\nvless://b',
			error: '',
		});
	});

	it('does not duplicate trailing newline of current text', () => {
		expect(appendImportedFileText('vless://a\n', 'vless://b')).toEqual({
			text: 'vless://a\nvless://b',
			error: '',
		});
	});

	it('strips leading BOM from file content', () => {
		expect(appendImportedFileText('', '﻿{"profiles":[]}')).toEqual({
			text: '{"profiles":[]}',
			error: '',
		});
	});

	it('keeps file content untrimmed otherwise (multiline JSON survives)', () => {
		const json = '{\n  "profiles": [\n    { "profileName": "default" }\n  ]\n}\n';
		expect(appendImportedFileText('', json)).toEqual({ text: json, error: '' });
	});

	// Автодетект формата на бэкенде работает по ЦЕЛОМУ телу: JSON после ссылок
	// молча выпал бы при построчном разборе. Склейка запрещена с ошибкой.
	it('rejects mixing a JSON config file into a non-empty textarea', () => {
		const res = appendImportedFileText('vless://a', '{"profiles":[]}');
		expect(res.text).toBe('vless://a');
		expect(res.error).toContain('целиком');
	});

	it('rejects mixing a Clash YAML file into a non-empty textarea', () => {
		const res = appendImportedFileText('vless://a', 'proxies:\n  - name: x\n');
		expect(res.error).toContain('целиком');
	});

	it('rejects appending links when textarea already holds a JSON config', () => {
		const res = appendImportedFileText('{"outbounds":[]}', 'vless://b');
		expect(res.text).toBe('{"outbounds":[]}');
		expect(res.error).toContain('целиком');
	});
});
