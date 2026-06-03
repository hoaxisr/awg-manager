import { describe, it, expect, vi } from 'vitest';
import { render, fireEvent, screen } from '@testing-library/svelte';
import RuleSetAddModal from './RuleSetAddModal.svelte';

vi.mock('$lib/api/client', () => ({
	api: {
		getGeoFiles: vi.fn().mockResolvedValue([
			{ type: 'geosite', path: '/geo/geosite.dat', url: '', size: 1, tagCount: 1, updated: '' },
		]),
		getGeoTags: vi.fn().mockResolvedValue([
			{ name: 'GOOGLE', count: 42 },
			{ name: 'YOUTUBE', count: 24 },
		]),
		expandGeoTag: vi.fn(),
		singboxRouterDatRuleSetURL: vi.fn((kind: 'geosite' | 'geoip', tags: string[]) => {
			const q = new URLSearchParams({ kind });
			for (const t of tags) q.append('tag', t);
			q.set('token', 'test');
			return Promise.resolve({
				url: `http://127.0.0.1:2222/api/singbox/router/rulesets/dat-srs?${q.toString()}`,
			});
		}),
	},
}));

describe('RuleSetAddModal', () => {
	it('allows editing an existing rule_set tag and submits the new tag', async () => {
		const onSave = vi.fn().mockResolvedValue(undefined);
		render(RuleSetAddModal, {
			props: {
				ruleSet: {
					tag: 'old-set',
					type: 'remote',
					format: 'binary',
					url: 'https://example.com/old.srs',
					update_interval: '24h',
				},
				outboundOptions: [],
				onClose: vi.fn(),
				onSave,
			},
		});

		const tagInput = screen.getByPlaceholderText('geosite-example') as HTMLInputElement;
		expect(tagInput.disabled).toBe(false);

		await fireEvent.input(tagInput, { target: { value: 'new-set' } });
		await fireEvent.click(screen.getByRole('button', { name: /сохранить/i }));

		expect(onSave).toHaveBeenCalledWith(expect.objectContaining({ tag: 'new-set' }));
	});

	it('creates geosite selection as remote binary dat-srs rule_set', async () => {
		const onSave = vi.fn().mockResolvedValue(undefined);
		render(RuleSetAddModal, {
			props: {
				outboundOptions: [],
				onClose: vi.fn(),
				onSave,
			},
		});

		await fireEvent.click(screen.getByRole('button', { name: 'Geosite' }));
		await fireEvent.click(await screen.findByRole('button', { name: /GOOGLE/ }));
		await fireEvent.click(screen.getByRole('button', { name: /сохранить/i }));

		expect(onSave).toHaveBeenCalledWith(expect.objectContaining({
			tag: 'geosite-google',
			type: 'remote',
			format: 'binary',
			update_interval: '24h',
			download_detour: undefined,
			url: expect.stringContaining('/api/singbox/router/rulesets/dat-srs?'),
		}));
	});

	it('opens existing dat-srs remote rule_set in geosite edit mode', async () => {
		render(RuleSetAddModal, {
			props: {
				ruleSet: {
					tag: 'geosite-GOOGLE',
					type: 'remote',
					format: 'binary',
					url: 'http://127.0.0.1:2222/api/singbox/router/rulesets/dat-srs?kind=geosite&tag=GOOGLE&token=test',
					update_interval: '24h',
				},
				outboundOptions: [],
				onClose: vi.fn(),
				onSave: vi.fn(),
			},
		});

		const geositeButton = screen.getByRole('button', { name: 'Geosite' });
		const remoteButton = screen.getByRole('button', { name: 'Remote' });

		expect(geositeButton.className).toContain('active');
		expect(remoteButton.className).not.toContain('active');
		expect(screen.getByText('geosite:GOOGLE')).toBeTruthy();
		expect(screen.queryByText('URL к файлу')).toBeNull();
	});

	it('keeps geosite tag list open and creates one rule_set from multiple selected tags', async () => {
		const onSave = vi.fn().mockResolvedValue(undefined);
		render(RuleSetAddModal, {
			props: {
				outboundOptions: [],
				onClose: vi.fn(),
				onSave,
			},
		});

		await fireEvent.click(screen.getByRole('button', { name: 'Geosite' }));

		expect(screen.queryByRole('button', { name: 'Выбрать' })).toBeNull();
		expect(screen.queryByRole('button', { name: 'Изменить' })).toBeNull();

		await fireEvent.click(await screen.findByRole('button', { name: /GOOGLE/ }));
		await fireEvent.click(await screen.findByRole('button', { name: /YOUTUBE/ }));
		await fireEvent.click(screen.getByRole('button', { name: /сохранить/i }));

		expect(onSave).toHaveBeenCalledWith(expect.objectContaining({
			tag: 'geosite-google-youtube',
			type: 'remote',
			format: 'binary',
			update_interval: '24h',
			url: expect.stringContaining('tag=GOOGLE'),
		}));
		expect(onSave.mock.calls[0][0].url).toContain('tag=YOUTUBE');
	});

	it('keeps a custom existing dat rule_set tag after picking another dat tag', async () => {
		const onSave = vi.fn().mockResolvedValue(undefined);
		render(RuleSetAddModal, {
			props: {
				ruleSet: {
					tag: 'custom-name',
					type: 'remote',
					format: 'binary',
					url: 'http://127.0.0.1:2222/api/singbox/router/rulesets/dat-srs?kind=geosite&tag=OLD&token=test',
					update_interval: '24h',
				},
				outboundOptions: [],
				onClose: vi.fn(),
				onSave,
			},
		});

		await fireEvent.click(await screen.findByRole('button', { name: /GOOGLE/ }));
		await fireEvent.click(screen.getByRole('button', { name: /сохранить/i }));

		expect(onSave).toHaveBeenCalledWith(expect.objectContaining({
			tag: 'custom-name',
			type: 'remote',
			format: 'binary',
			update_interval: '24h',
			url: expect.stringContaining('/api/singbox/router/rulesets/dat-srs?'),
		}));
	});

	it('updates an auto-generated dat rule_set tag to standard lowercase multi-tag name after picking another dat tag', async () => {
		const onSave = vi.fn().mockResolvedValue(undefined);
		render(RuleSetAddModal, {
			props: {
				ruleSet: {
					tag: 'geosite-old',
					type: 'remote',
					format: 'binary',
					url: 'http://127.0.0.1:2222/api/singbox/router/rulesets/dat-srs?kind=geosite&tag=OLD&token=test',
					update_interval: '24h',
				},
				outboundOptions: [],
				onClose: vi.fn(),
				onSave,
			},
		});

		await fireEvent.click(await screen.findByRole('button', { name: /GOOGLE/ }));
		await fireEvent.click(screen.getByRole('button', { name: /сохранить/i }));

		expect(onSave).toHaveBeenCalledWith(expect.objectContaining({
			tag: 'geosite-old-google',
			type: 'remote',
			format: 'binary',
			update_interval: '24h',
			url: expect.stringContaining('/api/singbox/router/rulesets/dat-srs?'),
		}));
	});
});
