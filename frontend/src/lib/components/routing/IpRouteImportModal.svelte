<script lang="ts">
	import { m } from '$lib/i18n';
	import { Modal, Button, Dropdown, type DropdownOption } from '$lib/components/ui';
	import { parseStaticRouteImport, type PortableStaticRoute } from '$lib/utils/staticroute-export';
	import type { RoutingTunnel } from '$lib/types';
	import { routingTunnelLabel } from '$lib/utils/routingTunnelOptions';
	import RoutingImportDropZone from './RoutingImportDropZone.svelte';

	interface Props {
		open: boolean;
		existingNames: string[];
		tunnels: RoutingTunnel[];
		onclose: () => void;
		onimport: (routes: (PortableStaticRoute & { tunnelID: string })[]) => void;
	}

	let {
		open = $bindable(false),
		existingNames,
		tunnels,
		onclose,
		onimport,
	}: Props = $props();

	let parsed = $state<PortableStaticRoute[] | null>(null);
	let selectedFlags = $state<boolean[]>([]);
	let parseError = $state('');
	let parseFailure = $state<'' | 'empty' | 'read'>('');
	let parseErrorText = $derived(
		parseError ||
			(parseFailure === 'empty'
				? m.routing_import_no_routes()
				: parseFailure === 'read'
					? m.routing_import_read_error()
					: ''),
	);
	let importing = $state(false);
	let wasOpen = $state(false);
	let defaultTunnelId = $state('');
	let tunnelOverrides = $state<Record<number, string>>({});
	let editingTunnelIdx = $state<number | null>(null);

	// Reset on open
	$effect(() => {
		if (open && !wasOpen) {
			parsed = null;
			selectedFlags = [];
			parseError = '';
			parseFailure = '';
			importing = false;
			defaultTunnelId = tunnels.find(t => t.available)?.id ?? '';
			tunnelOverrides = {};
			editingTunnelIdx = null;
		}
		wasOpen = open;
	});

	let selectedCount = $derived(selectedFlags.filter(Boolean).length);
	let existingLower = $derived(existingNames.map(n => n.toLowerCase()));
	let userTunnels = $derived(tunnels.filter(t => t.type === 'managed' && t.available));
	let systemTunnels = $derived(tunnels.filter(t => t.type === 'system' && t.available));
	let noTunnels = $derived(tunnels.filter(t => t.available).length === 0);
	let tunnelOpts = $derived<DropdownOption[]>([
		...userTunnels.map((t) => ({ value: t.id, label: t.name, group: m.routing_group_user() })),
		...systemTunnels.map((t) => ({ value: t.id, label: routingTunnelLabel(t), group: m.routing_group_system() })),
	]);

	function isDuplicate(name: string): boolean {
		return existingLower.includes(name.toLowerCase());
	}

	function effectiveTunnel(index: number): string {
		return tunnelOverrides[index] ?? defaultTunnelId;
	}

	function tunnelName(tunnelId: string): string {
		return tunnels.find(t => t.id === tunnelId)?.name ?? tunnelId;
	}

	async function processFile(file: File) {
		try {
			const text = await file.text();
			const routes = parseStaticRouteImport(text);
			if (routes.length === 0) {
				parseError = '';
				parseFailure = 'empty';
				return;
			}
			parsed = routes;
			selectedFlags = routes.map(r => !isDuplicate(r.name));
			tunnelOverrides = {};
			editingTunnelIdx = null;
		} catch (e) {
			parseError = e instanceof Error ? e.message : '';
			parseFailure = e instanceof Error ? '' : 'read';
		}
	}

	function handleImport() {
		if (!parsed) return;
		const selected = parsed
			.map((r, i) => ({ ...r, tunnelID: effectiveTunnel(i), _selected: selectedFlags[i] }))
			.filter(r => r._selected)
			.map(({ _selected, ...r }) => r);
		importing = true;
		onimport(selected);
	}
</script>

<Modal {open} title={m.routing_import_title()} size="lg" {onclose}>
	{#if !parsed}
		<RoutingImportDropZone
			subject={m.routing_import_subject()}
			parseError={parseErrorText}
			onfile={processFile}
		/>
	{:else}
		<div class="import-preview">
		<div class="tunnel-default-bar">
			<span class="tunnel-default-label">{m.routing_import_tunnel_for_all()}</span>
			<div class="tunnel-select">
				<Dropdown
					bind:value={defaultTunnelId}
					options={tunnelOpts}
					disabled={importing}
					fullWidth
				/>
			</div>
		</div>

		{#if noTunnels}
			<p class="import-error">{m.routing_import_no_tunnels()}</p>
		{/if}

		<p class="import-hint">{m.routing_import_found_routes({ count: parsed.length })}</p>
		<div class="import-list">
			{#each parsed as route, i}
				<label class="import-item" class:duplicate={isDuplicate(route.name)} class:overridden={tunnelOverrides[i] != null}>
					<input type="checkbox" bind:checked={selectedFlags[i]} disabled={importing} />
					<div class="import-item-info">
						<span class="import-name">{route.name}</span>
						<span class="import-meta">{m.routing_subnets_count({ count: route.subnets.length })}</span>
					</div>
					{#if isDuplicate(route.name)}
						<span class="import-dup">{m.routing_import_duplicate()}</span>
					{/if}
					{#if editingTunnelIdx === i}
						<div class="tunnel-select-inline">
							<Dropdown
								value={effectiveTunnel(i)}
								options={tunnelOpts}
								onchange={(val) => {
									if (val === defaultTunnelId) {
										const next = { ...tunnelOverrides };
										delete next[i];
										tunnelOverrides = next;
									} else {
										tunnelOverrides = { ...tunnelOverrides, [i]: val };
									}
									editingTunnelIdx = null;
								}}
								fullWidth
							/>
						</div>
					{:else}
						<button
							class="tunnel-name-btn"
							class:overridden={tunnelOverrides[i] != null}
							onclick={(e) => { e.stopPropagation(); editingTunnelIdx = i; }}
							disabled={importing}
						>
							{tunnelName(effectiveTunnel(i))}
						</button>
					{/if}
				</label>
			{/each}
		</div>
		</div>
	{/if}

	{#snippet actions()}
		<Button variant="ghost" onclick={onclose} disabled={importing}>{m.common_cancel()}</Button>
		{#if parsed}
			<Button variant="primary" onclick={handleImport} disabled={selectedCount === 0 || noTunnels} loading={importing}>
				{m.routing_import_submit({ count: selectedCount })}
			</Button>
		{/if}
	{/snippet}
</Modal>
