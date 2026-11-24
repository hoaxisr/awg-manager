import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import '@testing-library/jest-dom/vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/svelte';
import { m } from '$lib/i18n';
import type { FircRule } from '$lib/types/firc';
import FircBulkGroupsBar from './FircBulkGroupsBar.svelte';
import FircBulkRulesBar from './FircBulkRulesBar.svelte';
import FircBulkRulesEditorModal from './FircBulkRulesEditorModal.svelte';

describe('FircBulkRulesEditorModal', () => {
	afterEach(() => {
		cleanup();
	});

	it('populates serialized rules, updates live stats on edit, and emits parsed rules preserving IDs', async () => {
		const initialRules: FircRule[] = [
			{ id: 'rule-keep', type: 'namespace', rule: 'youtube.com', enable: true },
			{ id: 'rule-drop', type: 'wildcard', rule: '*.googlevideo.com', enable: true },
		];
		const onapply = vi.fn<(nextRules: FircRule[]) => void>();

		render(FircBulkRulesEditorModal, {
			props: {
				open: true,
				groupName: 'YouTube',
				rules: initialRules,
				makeId: () => 'generated-1',
				onclose: vi.fn(),
				onapply,
			},
		});

		const textarea = screen.getByRole('textbox', { name: m.firc_bulk_rules_edit_list_btn() });
		expect(textarea).toHaveValue('youtube.com\n*.googlevideo.com');
		expect(screen.getByTestId('firc-bulk-rules-editor-stats')).toHaveTextContent(
			m.firc_bulk_rules_editor_stats({ total: 2, added: 0, removed: 0, disabled: 0 }),
		);

		await fireEvent.input(textarea, {
			target: { value: 'youtube.com\n[off] 149.154.160.0/20 tcp:443 # TG' },
		});

		expect(screen.getByTestId('firc-bulk-rules-editor-stats')).toHaveTextContent(
			m.firc_bulk_rules_editor_stats({ total: 2, added: 1, removed: 1, disabled: 1 }),
		);

		await fireEvent.click(screen.getByRole('button', { name: m.common_apply() }));
		expect(onapply).toHaveBeenCalledTimes(1);
		expect(onapply.mock.calls[0][0]).toEqual([
			{ id: 'rule-keep', type: 'namespace', rule: 'youtube.com', enable: true },
			{
				id: 'generated-1',
				type: 'subnet',
				rule: '149.154.160.0/20',
				enable: false,
				proto: 'tcp',
				ports: '443',
				comment: 'TG',
			},
		]);
	});

	it('collapses subdomains to root domains with undo before applying', async () => {
		const initialRules: FircRule[] = [
			{ id: 'r1', type: 'namespace', rule: 'cdn.example.com', enable: true },
			{ id: 'r2', type: 'namespace', rule: 'api.example.com', enable: true },
			{ id: 'r3', type: 'wildcard', rule: '*.assets.vendor.co.uk', enable: true },
		];
		const onapply = vi.fn<(nextRules: FircRule[]) => void>();

		render(FircBulkRulesEditorModal, {
			props: {
				open: true,
				groupName: 'Work',
				rules: initialRules,
				makeId: () => 'new-1',
				onclose: vi.fn(),
				onapply,
			},
		});

		const textarea = screen.getByRole('textbox', { name: m.firc_bulk_rules_edit_list_btn() });
		const collapseBtn = screen.getByRole('button', { name: m.firc_bulk_collapse_root_btn() });
		expect(collapseBtn).toBeEnabled();

		await fireEvent.click(collapseBtn);
		expect(textarea).toHaveValue('example.com\n*.vendor.co.uk');

		const undoBtn = screen.getByRole('button', { name: m.firc_bulk_collapse_undo_btn() });
		await fireEvent.click(undoBtn);
		expect(textarea).toHaveValue('cdn.example.com\napi.example.com\n*.assets.vendor.co.uk');

		await fireEvent.click(collapseBtn);
		await fireEvent.click(screen.getByRole('button', { name: m.common_apply() }));
		expect(onapply).toHaveBeenCalledTimes(1);
		expect(onapply.mock.calls[0][0]).toEqual([
			{ id: 'new-1', type: 'namespace', rule: 'example.com', enable: true },
			{ id: 'new-1', type: 'wildcard', rule: '*.vendor.co.uk', enable: true },
		]);
	});
});

describe('FircBulkRulesBar', () => {
	beforeEach(() => {
		vi.clearAllMocks();
	});

	afterEach(() => {
		cleanup();
	});

	it('triggers enable, disable, type change, move to group, selection, and delete callbacks', async () => {
		const onSelectAll = vi.fn();
		const onDeselectAll = vi.fn();
		const onEnable = vi.fn();
		const onDisable = vi.fn();
		const onChangeType = vi.fn();
		const onMoveToGroup = vi.fn();
		const onDelete = vi.fn();

		render(FircBulkRulesBar, {
			props: {
				selectedCount: 2,
				totalCount: 5,
				otherGroups: [{ id: 'g2', name: 'Gaming' }],
				onSelectAll,
				onDeselectAll,
				onEnable,
				onDisable,
				onChangeType,
				onMoveToGroup,
				onDelete,
			},
		});

		expect(screen.getByTestId('firc-bulk-rules-bar')).toHaveTextContent(
			m.firc_bulk_rules_selected({ count: 2 }),
		);

		await fireEvent.click(screen.getByRole('button', { name: m.firc_bulk_select_all({ count: 5 }) }));
		await fireEvent.click(screen.getByRole('button', { name: m.firc_bulk_deselect_all() }));
		await fireEvent.click(screen.getByRole('button', { name: m.firc_bulk_enable() }));
		await fireEvent.click(screen.getByRole('button', { name: m.firc_bulk_disable() }));
		await fireEvent.click(screen.getByRole('button', { name: m.firc_bulk_delete() }));

		expect(onSelectAll).toHaveBeenCalledTimes(1);
		expect(onDeselectAll).toHaveBeenCalledTimes(1);
		expect(onEnable).toHaveBeenCalledTimes(1);
		expect(onDisable).toHaveBeenCalledTimes(1);
		expect(onDelete).toHaveBeenCalledTimes(1);

		const typeSelect = screen.getByRole('combobox', { name: m.firc_bulk_rules_change_type() });
		await fireEvent.change(typeSelect, { target: { value: 'domain' } });
		expect(onChangeType).toHaveBeenCalledWith('domain');
		expect(typeSelect).toHaveValue('');

		const moveSelect = screen.getByRole('combobox', { name: m.firc_bulk_rules_move_to() });
		await fireEvent.change(moveSelect, { target: { value: 'g2' } });
		expect(onMoveToGroup).toHaveBeenCalledWith('g2');
		expect(moveSelect).toHaveValue('');
	});
});

describe('FircBulkGroupsBar', () => {
	afterEach(() => {
		cleanup();
	});

	it('disables batch actions when zero groups are selected and applies proxy group or single interface otherwise', async () => {
		const onSetInterface = vi.fn();
		const onOpenDevices = vi.fn();
		const onEnable = vi.fn();
		const onDisable = vi.fn();
		const onCopyDomains = vi.fn();
		const onDelete = vi.fn();
		const onDone = vi.fn();

		const view = render(FircBulkGroupsBar, {
			props: {
				selectedCount: 0,
				visibleCount: 3,
				interfaces: [{ value: 'tunvless0', label: 'Tunnel 0' }],
				proxyGroups: [{ id: 'pg1', name: 'Failover Main', interfaces: ['tunvless0', 'tunvless1'] }],
				onSelectAllVisible: vi.fn(),
				onDeselectAll: vi.fn(),
				onDone,
				onSetInterface,
				onOpenDevices,
				onEnable,
				onDisable,
				onCopyDomains,
				onDelete,
			},
		});

		expect(screen.getByRole('button', { name: m.firc_bulk_groups_devices_btn() })).toBeDisabled();
		expect(screen.getByRole('combobox', { name: m.firc_bulk_groups_change_iface() })).toBeDisabled();

		await view.rerender({
			selectedCount: 2,
			visibleCount: 3,
			interfaces: [{ value: 'tunvless0', label: 'Tunnel 0' }],
			proxyGroups: [{ id: 'pg1', name: 'Failover Main', interfaces: ['tunvless0', 'tunvless1'] }],
			onSelectAllVisible: vi.fn(),
			onDeselectAll: vi.fn(),
			onDone,
			onSetInterface,
			onOpenDevices,
			onEnable,
			onDisable,
			onCopyDomains,
			onDelete,
		});

		const ifaceSelect = screen.getByRole('combobox', { name: m.firc_bulk_groups_change_iface() });
		expect(ifaceSelect).toBeEnabled();

		await fireEvent.change(ifaceSelect, { target: { value: 'pg:pg1' } });
		expect(onSetInterface).toHaveBeenLastCalledWith(['tunvless0', 'tunvless1'], 'pg1');

		await fireEvent.change(ifaceSelect, { target: { value: 'iface:tunvless0' } });
		expect(onSetInterface).toHaveBeenLastCalledWith('tunvless0');

		await fireEvent.click(screen.getByRole('button', { name: m.firc_bulk_groups_devices_btn() }));
		await fireEvent.click(screen.getByRole('button', { name: m.firc_bulk_enable() }));
		await fireEvent.click(screen.getByRole('button', { name: m.firc_bulk_disable() }));
		await fireEvent.click(screen.getByRole('button', { name: m.firc_bulk_groups_copy_domains() }));
		await fireEvent.click(screen.getByRole('button', { name: m.firc_bulk_delete() }));
		await fireEvent.click(screen.getByRole('button', { name: m.firc_bulk_select_groups_done() }));

		expect(onOpenDevices).toHaveBeenCalledTimes(1);
		expect(onEnable).toHaveBeenCalledTimes(1);
		expect(onDisable).toHaveBeenCalledTimes(1);
		expect(onCopyDomains).toHaveBeenCalledTimes(1);
		expect(onDelete).toHaveBeenCalledTimes(1);
		expect(onDone).toHaveBeenCalledTimes(1);
	});

	it('deduplicates duplicate interface and proxy group keys without throwing each_key_duplicate', () => {
		render(FircBulkGroupsBar, {
			props: {
				selectedCount: 1,
				visibleCount: 2,
				interfaces: [
					{ value: 'blackhole', label: 'blackhole' },
					{ value: 'block', label: 'block' },
					{ value: 'tunvless4', label: 'VOX' },
					{ value: 'blackhole', label: 'Блокировать (Blackhole)' },
					{ value: 'block', label: 'Блокировать по DNS' },
				],
				proxyGroups: [
					{ id: 'pg1', name: 'Failover', interfaces: ['tunvless4'] },
					{ id: 'pg1', name: 'Failover Dup', interfaces: ['tunvless4'] },
				],
				onSelectAllVisible: vi.fn(),
				onDeselectAll: vi.fn(),
				onDone: vi.fn(),
				onSetInterface: vi.fn(),
				onOpenDevices: vi.fn(),
				onEnable: vi.fn(),
				onDisable: vi.fn(),
				onCopyDomains: vi.fn(),
				onDelete: vi.fn(),
			},
		});

		const options = screen.getAllByRole('option');
		const values = options.map((opt) => (opt as HTMLOptionElement).value);
		expect(values.filter((v) => v === 'iface:blackhole')).toHaveLength(1);
		expect(values.filter((v) => v === 'iface:block')).toHaveLength(1);
		expect(values.filter((v) => v === 'pg:pg1')).toHaveLength(1);
	});
});
