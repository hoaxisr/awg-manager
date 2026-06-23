import { describe, it, expect, vi } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import SubscriptionImportPreview from './SubscriptionImportPreview.svelte';
import type { SubscriptionPreviewMember } from '$lib/types';

const members: SubscriptionPreviewMember[] = [
	{ key: 'k-alpha', label: 'Alpha NL', protocol: 'vless', server: 'nl.example.com', port: 443, security: 'reality' },
	{ key: 'k-bravo', label: 'Bravo DE', protocol: 'trojan', server: 'de.example.com', port: 8443, security: 'tls' },
	{ key: 'k-charlie', label: '', protocol: 'shadowsocks', server: 'us.example.com', port: 9000 },
];

function checkboxes(container: HTMLElement): HTMLInputElement[] {
	return Array.from(container.querySelectorAll('.list input[type="checkbox"]')) as HTMLInputElement[];
}

describe('SubscriptionImportPreview', () => {
	it('default empty excludedKeys → every row checkbox is checked (checked = keep)', () => {
		const { container } = render(SubscriptionImportPreview, {
			props: {
				members,
				excludedKeys: new Set<string>(),
				ontoggle: vi.fn(),
				onselectAll: vi.fn(),
				onselectNone: vi.fn(),
			},
		});
		const boxes = checkboxes(container);
		expect(boxes).toHaveLength(3);
		expect(boxes.every((b) => b.checked)).toBe(true);
	});

	it('clicking a row checkbox calls ontoggle with that member key', async () => {
		const ontoggle = vi.fn();
		const { container } = render(SubscriptionImportPreview, {
			props: {
				members,
				excludedKeys: new Set<string>(),
				ontoggle,
				onselectAll: vi.fn(),
				onselectNone: vi.fn(),
			},
		});
		// first row corresponds to members[0]
		await fireEvent.click(checkboxes(container)[0]);
		expect(ontoggle).toHaveBeenCalledTimes(1);
		expect(ontoggle).toHaveBeenCalledWith('k-alpha');
	});

	it('a member in excludedKeys renders unchecked with dropped/strikethrough style', () => {
		const { container } = render(SubscriptionImportPreview, {
			props: {
				members,
				excludedKeys: new Set<string>(['k-bravo']),
				ontoggle: vi.fn(),
				onselectAll: vi.fn(),
				onselectNone: vi.fn(),
			},
		});
		const boxes = checkboxes(container);
		// order matches members[]: alpha checked, bravo unchecked, charlie checked
		expect(boxes[0].checked).toBe(true);
		expect(boxes[1].checked).toBe(false);
		expect(boxes[2].checked).toBe(true);

		const rows = Array.from(container.querySelectorAll('.row')) as HTMLElement[];
		expect(rows[1].classList.contains('dropped')).toBe(true);
		expect(rows[0].classList.contains('dropped')).toBe(false);
	});

	it('filter narrows visible rows by label/server, case-insensitively', async () => {
		const { container } = render(SubscriptionImportPreview, {
			props: {
				members,
				excludedKeys: new Set<string>(),
				ontoggle: vi.fn(),
				onselectAll: vi.fn(),
				onselectNone: vi.fn(),
			},
		});
		const input = container.querySelector('input.filter') as HTMLInputElement;

		// label match, case-insensitive
		await fireEvent.input(input, { target: { value: 'alpha' } });
		expect(checkboxes(container)).toHaveLength(1);
		expect((container.querySelector('.row .name') as HTMLElement).textContent).toContain('Alpha NL');

		// server match, case-insensitive
		await fireEvent.input(input, { target: { value: 'DE.EXAMPLE' } });
		const rows = Array.from(container.querySelectorAll('.row .addr')) as HTMLElement[];
		expect(rows).toHaveLength(1);
		expect(rows[0].textContent).toContain('de.example.com');

		// no match → empty-list placeholder
		await fireEvent.input(input, { target: { value: 'zzz-nomatch' } });
		expect(checkboxes(container)).toHaveLength(0);
		expect(container.querySelector('.empty-list')).toBeTruthy();
	});

	it('live counter reflects kept vs excluded as excludedKeys changes', async () => {
		const { container, rerender } = render(SubscriptionImportPreview, {
			props: {
				members,
				excludedKeys: new Set<string>(),
				ontoggle: vi.fn(),
				onselectAll: vi.fn(),
				onselectNone: vi.fn(),
			},
		});
		expect((container.querySelector('.counter .kept') as HTMLElement).textContent).toContain('3 оставить');
		expect((container.querySelector('.counter .excluded') as HTMLElement).textContent).toContain('0 исключить');

		await rerender({
			members,
			excludedKeys: new Set<string>(['k-alpha', 'k-charlie']),
			ontoggle: vi.fn(),
			onselectAll: vi.fn(),
			onselectNone: vi.fn(),
		});
		expect((container.querySelector('.counter .kept') as HTMLElement).textContent).toContain('1 оставить');
		expect((container.querySelector('.counter .excluded') as HTMLElement).textContent).toContain('2 исключить');
	});

	it('«Выбрать все» / «Снять все» call onselectAll / onselectNone', async () => {
		const onselectAll = vi.fn();
		const onselectNone = vi.fn();
		const { getByText } = render(SubscriptionImportPreview, {
			props: {
				members,
				excludedKeys: new Set<string>(),
				ontoggle: vi.fn(),
				onselectAll,
				onselectNone,
			},
		});
		await fireEvent.click(getByText('Выбрать все'));
		await fireEvent.click(getByText('Снять все'));
		expect(onselectAll).toHaveBeenCalledTimes(1);
		expect(onselectNone).toHaveBeenCalledTimes(1);
	});

	it('name falls back to server:port when label is empty', () => {
		const { container } = render(SubscriptionImportPreview, {
			props: {
				members: [members[2]], // charlie, empty label
				excludedKeys: new Set<string>(),
				ontoggle: vi.fn(),
				onselectAll: vi.fn(),
				onselectNone: vi.fn(),
			},
		});
		const name = container.querySelector('.row .name') as HTMLElement;
		expect(name.textContent?.trim()).toBe('us.example.com:9000');
		expect(name.classList.contains('empty')).toBe(true);
	});
});
