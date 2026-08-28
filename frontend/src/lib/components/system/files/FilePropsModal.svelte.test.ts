import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import FilePropsModal from './FilePropsModal.svelte';
import type { SystemFileEntry } from '$lib/types/systemTools';

const entry: SystemFileEntry = {
	name: 'dns_rewrites.json',
	path: '/opt/etc/awg-manager/dns_rewrites.json',
	isDir: false,
	size: 112,
	mode: '0644',
	modTime: '2026-08-15T10:10:54Z',
};

describe('FilePropsModal', () => {
	// Родитель обнуляет entry в onClose; entry — реактивный проп, поэтому
	// читать его после onClose() уже поздно — прилетал null.
	it('передаёт entry в onEdit, хотя onClose его обнуляет', async () => {
		const onEdit = vi.fn();
		const props = $state<{
			open: boolean;
			entry: SystemFileEntry | null;
			onEdit: (e: SystemFileEntry) => void;
			onClose: () => void;
		}>({
			open: true,
			entry,
			onEdit,
			onClose: () => {
				props.entry = null;
				props.open = false;
			},
		});
		render(FilePropsModal, { props });

		await fireEvent.click(screen.getByText('Редактировать'));

		expect(onEdit).toHaveBeenCalledWith(entry);
	});
});
