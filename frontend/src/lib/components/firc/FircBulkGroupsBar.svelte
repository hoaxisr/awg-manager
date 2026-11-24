<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button } from '$lib/components/ui';
	import type { FircProxyGroup } from '$lib/types/firc';
	import { Check, Copy, Monitor, Power, PowerOff, Trash2 } from 'lucide-svelte';

	interface Props {
		selectedCount: number;
		visibleCount: number;
		interfaces: { value: string; label: string }[];
		proxyGroups: FircProxyGroup[];
		onSelectAllVisible: () => void;
		onDeselectAll: () => void;
		onDone: () => void;
		onSetInterface: (iface: string | string[], proxyGroupId?: string) => void;
		onOpenDevices: () => void;
		onEnable: () => void;
		onDisable: () => void;
		onCopyDomains: () => void;
		onDelete: () => void;
	}

	let {
		selectedCount,
		visibleCount,
		interfaces,
		proxyGroups,
		onSelectAllVisible,
		onDeselectAll,
		onDone,
		onSetInterface,
		onOpenDevices,
		onEnable,
		onDisable,
		onCopyDomains,
		onDelete,
	}: Props = $props();

	let actionsDisabled = $derived(selectedCount === 0);
	let uniqueProxyGroups = $derived.by(() => {
		const seen = new Set<string>();
		return proxyGroups.filter((pg) => {
			if (!pg.id || seen.has(pg.id)) return false;
			seen.add(pg.id);
			return true;
		});
	});
	let uniqueInterfaces = $derived.by(() => {
		const seen = new Set<string>();
		return interfaces.filter((opt) => {
			if (!opt.value || seen.has(opt.value)) return false;
			seen.add(opt.value);
			return true;
		});
	});

	function handleIfaceChange(e: Event & { currentTarget: HTMLSelectElement }) {
		const raw = e.currentTarget.value;
		e.currentTarget.value = '';
		if (!raw) return;
		if (raw.startsWith('pg:')) {
			const pgId = raw.slice('pg:'.length);
			const pg = proxyGroups.find((p) => p.id === pgId);
			if (pg && pg.interfaces.length > 0) {
				onSetInterface([...pg.interfaces], pg.id);
			}
			return;
		}
		if (raw.startsWith('iface:')) {
			onSetInterface(raw.slice('iface:'.length));
		}
	}
</script>

<div class="fb-bar" data-testid="firc-bulk-groups-bar">
	<div class="fb-left">
		<span class="fb-count">{m.firc_bulk_groups_selected({ count: selectedCount })}</span>
		{#if visibleCount > 0 && selectedCount < visibleCount}
			<button type="button" class="fb-link" onclick={onSelectAllVisible}>
				{m.firc_bulk_select_all({ count: visibleCount })}
			</button>
		{/if}
		{#if selectedCount > 0}
			<button type="button" class="fb-link" onclick={onDeselectAll}>
				{m.firc_bulk_deselect_all()}
			</button>
		{/if}
		<Button variant="outline-primary" size="sm" onclick={onDone}>
			{#snippet iconBefore()}<Check size={13} />{/snippet}
			{m.firc_bulk_select_groups_done()}
		</Button>
	</div>

	<div class="fb-actions">
		<select
			class="fb-select"
			aria-label={m.firc_bulk_groups_change_iface()}
			disabled={actionsDisabled}
			value=""
			onchange={handleIfaceChange}
		>
			<option value="" disabled>{m.firc_bulk_groups_change_iface()}</option>
			{#if uniqueProxyGroups.length > 0}
				<optgroup label={m.firc_bulk_groups_proxy_groups_opt()}>
					{#each uniqueProxyGroups as pg (pg.id)}
						<option value={`pg:${pg.id}`}>{pg.name}</option>
					{/each}
				</optgroup>
			{/if}
			<optgroup label={m.firc_bulk_groups_interfaces_opt()}>
				{#each uniqueInterfaces as opt (opt.value)}
					<option value={`iface:${opt.value}`}>{opt.label}</option>
				{/each}
			</optgroup>
		</select>

		<Button variant="secondary" size="sm" disabled={actionsDisabled} onclick={onOpenDevices}>
			{#snippet iconBefore()}<Monitor size={13} />{/snippet}
			{m.firc_bulk_groups_devices_btn()}
		</Button>

		<Button variant="secondary" size="sm" disabled={actionsDisabled} onclick={onEnable}>
			{#snippet iconBefore()}<Power size={13} />{/snippet}
			{m.firc_bulk_enable()}
		</Button>

		<Button variant="secondary" size="sm" disabled={actionsDisabled} onclick={onDisable}>
			{#snippet iconBefore()}<PowerOff size={13} />{/snippet}
			{m.firc_bulk_disable()}
		</Button>

		<Button variant="secondary" size="sm" disabled={actionsDisabled} onclick={onCopyDomains}>
			{#snippet iconBefore()}<Copy size={13} />{/snippet}
			{m.firc_bulk_groups_copy_domains()}
		</Button>

		<Button variant="outline-danger" size="sm" disabled={actionsDisabled} onclick={onDelete}>
			{#snippet iconBefore()}<Trash2 size={13} />{/snippet}
			{m.firc_bulk_delete()}
		</Button>
	</div>
</div>

<style>
	.fb-bar {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: 8px 12px;
		padding: 8px 12px;
		border-radius: var(--radius);
		background: color-mix(in srgb, var(--color-accent-tint) 45%, var(--color-bg-secondary));
		border: 1px solid var(--color-accent-border);
	}

	.fb-left {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 8px;
	}

	.fb-count {
		font-size: 12.5px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.fb-link {
		border: 0;
		background: transparent;
		color: var(--color-accent);
		font-size: 12px;
		font-weight: 500;
		cursor: pointer;
		padding: 1px 4px;
		border-radius: 4px;
	}

	.fb-link:hover {
		text-decoration: underline;
	}

	.fb-actions {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 6px;
	}



	.fb-select:disabled {
		opacity: 0.45;
		cursor: not-allowed;
	}




	.fb-select {
		height: 28px;
		padding: 0 8px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
		font-size: 12px;
		font-family: inherit;
		cursor: pointer;
		max-width: 220px;
	}

	.fb-select:focus {
		outline: none;
		border-color: var(--color-accent);
	}
</style>
