import { afterEach, describe, expect, it, vi } from 'vitest';
import '@testing-library/jest-dom/vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/svelte';
import type { FircTunnel } from '$lib/types/firc';
import FircTunnelEditSourcesTab from './FircTunnelEditSourcesTab.svelte';
import { m } from '$lib/i18n';

afterEach(cleanup);

function draftWith(headers?: Record<string, string>, interval?: number): FircTunnel {
	return {
		sources: [{ id: 's1', kind: 'subscription', name: 'sub', url: 'https://x/sub', interval, headers }],
	} as unknown as FircTunnel;
}

function renderTab(draft: FircTunnel) {
	render(FircTunnelEditSourcesTab, {
		draft,
		liveState: undefined,
		happKeysCount: 0,
		includeWarning: '',
		excludeWarning: '',
		onaddsource: vi.fn(),
		onhappkeys: vi.fn(),
		onremovesource: vi.fn(),
	});
}

describe('FircTunnelEditSourcesTab: заголовки подписки', () => {
	it('бейдж открывает правку, ввод меняет заголовки в черновике', async () => {
		const draft = draftWith({ 'User-Agent': 'Happ/3.6.1' });
		renderTab(draft);
		await fireEvent.click(screen.getByRole('button', { name: m.firc_tunnel_edit_headers_badge({ count: 1 }) }));
		const area = screen.getByLabelText<HTMLTextAreaElement>(m.firc_tunnel_edit_headers_label());
		expect(area.value).toBe('User-Agent: Happ/3.6.1');
		await fireEvent.input(area, { target: { value: 'User-Agent: Happ/3.7.0\nX-HWID: abc' } });
		expect(draft.sources[0].headers).toEqual({ 'User-Agent': 'Happ/3.7.0', 'X-HWID': 'abc' });
	});

	it('подписке без заголовков их можно добавить', async () => {
		const draft = draftWith(undefined);
		renderTab(draft);
		await fireEvent.click(screen.getByRole('button', { name: m.firc_tunnel_edit_headers_badge({ count: 0 }) }));
		const area = screen.getByLabelText(m.firc_tunnel_edit_headers_label());
		await fireEvent.input(area, { target: { value: 'X-HWID: abc' } });
		expect(draft.sources[0].headers).toEqual({ 'X-HWID': 'abc' });
	});
});

describe('FircTunnelEditSourcesTab: интервал обновления подписки', () => {
	it('отображает и сохраняет режим «Вручную» (interval = 0)', async () => {
		const draft = draftWith(undefined, 0);
		renderTab(draft);
		const select = screen.getByRole<HTMLSelectElement>('combobox');
		expect(select.value).toBe('0');
	});

	it('переключение с 6 часов на «Вручную» записывает 0 в черновик', async () => {
		const draft = draftWith(undefined, 21600);
		renderTab(draft);
		const select = screen.getByRole<HTMLSelectElement>('combobox');
		expect(select.value).toBe('21600');
		await fireEvent.change(select, { target: { value: '0' } });
		expect(draft.sources[0].interval).toBe(0);
	});

	it('отсутствующий interval (undefined) отображает 6 часов и не мутирует черновик в 0 при монтировании', async () => {
		const draft = draftWith(undefined, undefined);
		renderTab(draft);
		const select = screen.getByRole<HTMLSelectElement>('combobox');
		expect(select.value).toBe('21600');
		expect(draft.sources[0].interval).toBeUndefined();
		await fireEvent.change(select, { target: { value: '0' } });
		expect(draft.sources[0].interval).toBe(0);
	});
});
