import { describe, it, expect, vi } from 'vitest';
import { render, fireEvent, screen } from '@testing-library/svelte';
import RuleSetAddModal from './RuleSetAddModal.svelte';

vi.mock('$lib/api/client', () => ({
	api: {
		getGeoFiles: vi.fn().mockResolvedValue([
			{ type: 'geosite', path: '/geo/geosite.dat', url: '', size: 1, tagCount: 1, updated: '' },
		]),
		getGeoTags: vi.fn().mockResolvedValue([{ name: 'GOOGLE', count: 42 }]),
		expandGeoTag: vi.fn(),
		singboxRouterDatRuleSetURL: vi.fn().mockResolvedValue({
			url: 'http://127.0.0.1:2222/api/singbox/router/rulesets/dat-srs?kind=geosite&tag=GOOGLE&token=test',
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
		await fireEvent.click(screen.getByRole('button', { name: 'Выбрать' }));
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

		await fireEvent.click(screen.getByRole('button', { name: 'Изменить' }));
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

	it('updates an auto-generated dat rule_set tag to standard lowercase name after picking another dat tag', async () => {
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

		await fireEvent.click(screen.getByRole('button', { name: 'Изменить' }));
		await fireEvent.click(await screen.findByRole('button', { name: /GOOGLE/ }));
		await fireEvent.click(screen.getByRole('button', { name: /сохранить/i }));

		expect(onSave).toHaveBeenCalledWith(expect.objectContaining({
			tag: 'geosite-google',
			type: 'remote',
			format: 'binary',
			update_interval: '24h',
			url: expect.stringContaining('/api/singbox/router/rulesets/dat-srs?'),
		}));
	});
});
