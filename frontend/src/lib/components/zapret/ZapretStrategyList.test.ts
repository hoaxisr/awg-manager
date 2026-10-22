import '@testing-library/jest-dom/vitest';
import { expect, it, vi } from 'vitest';
import { fireEvent, render } from '@testing-library/svelte';
import type { ZapretStrategy } from '$lib/types';
import ZapretStrategyList from './ZapretStrategyList.svelte';

const items: ZapretStrategy[] = [
	{ index: 0, name: 'First', raw: '--filter-tcp=443', filterTcp: '443' },
	{ index: 1, name: 'Second', raw: '--filter-udp=443', filterUdp: '443' }
];

it('uses the same persistent reorder callback for drag and keyboard controls', async () => {
	const onmove = vi.fn();
	const { getAllByRole, getAllByTestId } = render(ZapretStrategyList, {
		props: { items, busy: false, conflict: false, onmove }
	});
	const cards = getAllByTestId('strategy-drag-item');
	const dataTransfer = {
		effectAllowed: '',
		dropEffect: '',
		setData: vi.fn(),
		getData: vi.fn().mockReturnValue('0')
	} as unknown as DataTransfer;

	await fireEvent.dragStart(cards[0], { dataTransfer });
	await fireEvent.dragOver(cards[1], { dataTransfer });
	await fireEvent.drop(cards[1], { dataTransfer });
	expect(onmove).toHaveBeenLastCalledWith(0, 1);
	expect(cards[0].textContent).toContain('First');
	expect(cards[1].textContent).toContain('Second');

	const keyboardButtons = getAllByRole('button');
	await fireEvent.click(keyboardButtons[2]);
	expect(onmove).toHaveBeenLastCalledWith(1, 0);
});

it('disables both reorder paths during conflict', async () => {
	const onmove = vi.fn();
	const { getAllByRole, getAllByTestId } = render(ZapretStrategyList, {
		props: { items, busy: false, conflict: true, onmove }
	});
	expect(getAllByTestId('strategy-drag-item')[0]).toHaveAttribute('draggable', 'false');
	for (const button of getAllByRole('button')) expect(button).toBeDisabled();
});
