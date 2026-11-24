import { describe, expect, it } from 'vitest';
import type { FircGroup, FircProxyGroup } from '$lib/types/firc';
import {
	buildGroupCheckFromProxyGroup,
	groupFormFrom,
	groupFromForm,
	inferDraftProxyGroupCheck,
	matchingProxyGroup,
	normalizeGroup,
	normalizeProxyGroupForSave,
	proxyGroupsDraftError,
} from './fircGroups';

describe('normalizeGroup', () => {
	const base: FircGroup = { id: 'g1', name: 'CF', interface: 'nwg0', enable: true, rules: [] };

	it('keeps the cloudflare flag the daemon sent', () => {
		expect(normalizeGroup({ ...base, cloudflare: true }).cloudflare).toBe(true);
		expect('cloudflare' in normalizeGroup(base)).toBe(false);
	});
});

describe('proxy group check helpers', () => {
	it('prefers proxy group with non-null check when duplicate interface chains exist', () => {
		const groups: FircProxyGroup[] = [
			{ id: 'pg_legacy', name: 'Legacy', interfaces: ['qmi_br0', 'tunvless2'] },
			{
				id: 'pg_active',
				name: 'Configured',
				interfaces: ['qmi_br0', 'tunvless2'],
				check: { mode: 'url', url: 'https://example.test/204' },
			},
		];
		const matched = matchingProxyGroup(groups, { interface: ['qmi_br0', 'tunvless2'] });
		expect(matched?.id).toBe('pg_active');
	});

	it('rejects duplicate ordered interface chains in proxyGroupsDraftError', () => {
		const err = proxyGroupsDraftError([
			{ id: 'pg1', name: 'First', interfaces: ['qmi_br0', 'tunvless2'] },
			{ id: 'pg2', name: 'Second', interfaces: ['qmi_br0', 'tunvless2'] },
		]);
		expect(err).toContain('First');
		expect(err).toContain('Second');
	});

	it('infers, builds, and normalizes proxy group checks cleanly', () => {
		const inferred = inferDraftProxyGroupCheck(
			{ id: 'pg1', name: 'Main', interfaces: ['eth3', 'tunvless0'] },
			[
				{
					id: 'g1',
					name: 'Group1',
					interface: ['eth3', 'tunvless0'],
					enable: true,
					check: { mode: 'url', url: 'https://probe.example/204' },
					rules: [],
				},
			],
		);
		expect(inferred).toEqual({ mode: 'url', url: 'https://probe.example/204' });

		expect(
			buildGroupCheckFromProxyGroup({
				id: 'pg2',
				name: 'KD',
				interfaces: ['eth3', 'tunvless0'],
				check: { mode: 'keendns' },
			}),
		).toEqual({ mode: 'keendns', url: '', timeoutMs: 5000 });

		expect(
			normalizeProxyGroupForSave({
				id: 'pg3',
				name: ' Single ',
				interfaces: ['eth3'],
				check: { mode: 'url', url: 'https://ignored.example' },
			}),
		).toEqual({
			id: 'pg3',
			name: 'Single',
			interfaces: ['eth3'],
			check: { mode: 'none' },
		});
	});

	it('keeps manual multi-iface chain check editable and delegates PG-backed chain check in groupFromForm', () => {
		const manualCur: FircGroup = {
			id: 'g_manual',
			name: 'Manual',
			interface: ['tunvless0', 'tunvless1'],
			enable: true,
			check: { mode: 'url', url: 'https://old.example/204' },
			rules: [],
		};
		const form = groupFormFrom(manualCur);
		form.checkMode = 'url';
		form.checkUrl = 'https://custom.example/204';

		const savedManual = groupFromForm(form, manualCur, 1, [
			{ id: 'pg_other', name: 'Other', interfaces: ['eth3', 'tunvless2'], check: { mode: 'none' } },
		]);
		expect(savedManual.check).toEqual({ mode: 'url', url: 'https://custom.example/204' });

		const savedManaged = groupFromForm(form, manualCur, 1, [
			{
				id: 'pg_managed',
				name: 'Managed',
				interfaces: ['tunvless0', 'tunvless1'],
				check: { mode: 'keendns' },
			},
		]);
		expect(savedManaged.check).toEqual({ mode: 'keendns', url: '', timeoutMs: 5000 });
	});
});
