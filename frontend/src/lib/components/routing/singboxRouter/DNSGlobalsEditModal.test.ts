import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/svelte';
import DNSGlobalsEditModal from './DNSGlobalsEditModal.svelte';
import type { SingboxRouterDNSServer } from '$lib/types';

class ResizeObserverStub {
	observe(): void {}
	disconnect(): void {}
}
vi.stubGlobal('ResizeObserver', ResizeObserverStub);

const servers: SingboxRouterDNSServer[] = [{ tag: 'a', type: 'udp', server: '1.1.1.1' }];

/** Открывает Dropdown по видимому тексту его триггера. */
async function openDropdown(triggerText: string): Promise<void> {
	await fireEvent.click(screen.getByText(triggerText));
}

async function pickOption(match: RegExp): Promise<void> {
	const option = screen
		.getAllByRole('option')
		.find((el) => match.test((el.textContent ?? '').trim()));
	if (!option) throw new Error(`option ${match} not found`);
	await fireEvent.click(option);
}

describe('DNSGlobalsEditModal', () => {
	it('передаёт timeout в onSave', async () => {
		const onSave = vi.fn(async () => {});
		render(DNSGlobalsEditModal, {
			props: { servers, final: 'a', strategy: '', timeout: '', onClose: () => {}, onSave },
		});

		await openDropdown('— по умолчанию (10 с) —');
		await pickOption(/^5 с$/);
		await fireEvent.click(screen.getByRole('button', { name: 'Сохранить' }));

		expect(onSave).toHaveBeenCalledWith({ final: 'a', strategy: '', timeout: '5s' });
	});
});
