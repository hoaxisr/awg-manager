import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import DevelopChannelGateModal from './DevelopChannelGateModal.svelte';
import { m } from '$lib/i18n';

describe('DevelopChannelGateModal', () => {
	it('пункты дисклеймера показаны текстом сообщений', () => {
		render(DevelopChannelGateModal, {
			props: { open: true, onclose: vi.fn(), onpassed: vi.fn() },
		});
		const items = screen.getAllByRole('listitem').map((li) => li.textContent?.trim());
		expect(items).toEqual([
			m.settings_develop_gate_item_1(),
			m.settings_develop_gate_item_2(),
			m.settings_develop_gate_item_3(),
			m.settings_develop_gate_item_4(),
			m.settings_develop_gate_item_5(),
			m.settings_develop_gate_item_6(),
		]);
	});
});
