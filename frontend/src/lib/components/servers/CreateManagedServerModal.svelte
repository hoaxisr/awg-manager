<script lang="ts">
	import { m } from '$lib/i18n';
	import { Modal, Button, Toggle } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { isValidEndpointHost } from '$lib/utils/endpoint';

	interface Props {
		open: boolean;
		onclose: () => void;
		onCreated: (serverId: string) => void;
		existingListenPorts?: number[];
	}

	const DEFAULT_LISTEN_PORT = 51820;
	const MAX_LISTEN_PORT = 65535;

	function suggestListenPort(occupied: number[], start = DEFAULT_LISTEN_PORT): number {
		const used = new Set(
			occupied
				.map((p) => Number(p))
				.filter((p) => Number.isInteger(p) && p > 0 && p <= MAX_LISTEN_PORT),
		);
		for (let port = start; port <= MAX_LISTEN_PORT; port++) {
			if (!used.has(port)) return port;
		}
		return start;
	}

	let {
		open = $bindable(false),
		onclose,
		onCreated,
		existingListenPorts = [],
	}: Props = $props();

	let address = $state('10.0.0.1');
	let mask = $state('24');
	let addressDirty = $state(false);
	let maskDirty = $state(false);
	let listenPort = $state(DEFAULT_LISTEN_PORT);
	let listenPortDirty = $state(false);
	let description = $state('');
	let endpoint = $state('');
	let mtu = $state(1376);
	let generateAsc = $state(false);
	let creating = $state(false);
	let wanIP = $state('');
	let loadingWanIP = $state(false);
	let wasOpen = $state(false);
	let showEndpointHint = $state(false);
	let showAscHint = $state(false);

	// Track initial state for this modal opening to determine isDirty correctly.
	// Updated synchronously on modal open, and again when API returns suggested values.
	let initialAddress = $state('10.0.0.1');
	let initialMask = $state('24');
	let initialListenPort = $state(DEFAULT_LISTEN_PORT);
	let initialDescription = $state('');
	let initialEndpoint = $state('');
	let initialMtu = $state(1376);
	let initialGenerateAsc = $state(false);

	$effect(() => {
		if (open && !wasOpen) {
			const suggestedPort = suggestListenPort(existingListenPorts);
			// Reset to defaults on modal open
			address = '10.0.0.1';
			mask = '24';
			addressDirty = false;
			maskDirty = false;
			listenPort = suggestedPort;
			listenPortDirty = false;
			description = '';
			endpoint = '';
			mtu = 1376;
			generateAsc = false;
			initialAddress = '10.0.0.1';
			initialMask = '24';
			initialListenPort = suggestedPort;
			initialDescription = '';
			initialEndpoint = '';
			initialMtu = 1376;
			initialGenerateAsc = false;
			showAscHint = false;

			wanIP = '';
			loadingWanIP = true;
			api.getWANIP().then(ip => {
				wanIP = ip;
				// Update initialEndpoint in lockstep so the async-resolved
				// suggestion is not mistaken for user input by the dirty
				// check (which compares endpoint to initialEndpoint).
				if (!endpoint) {
					endpoint = ip;
					initialEndpoint = ip;
				}
			}).catch(() => wanIP = '').finally(() => loadingWanIP = false);

			api.suggestManagedServerAddress().then(s => {
				if (!addressDirty) {
					address = s.address;
					initialAddress = s.address;
				}
				if (!maskDirty) {
					mask = s.mask;
					initialMask = s.mask;
				}
			}).catch(() => { /* keep defaults */ });
		}
		wasOpen = open;
	});

	$effect(() => {
		if (!open || listenPortDirty) return;
		const suggestedPort = suggestListenPort(existingListenPorts);
		if (listenPort !== suggestedPort) {
			listenPort = suggestedPort;
			initialListenPort = suggestedPort;
		}
	});

	const isDirty = $derived(
		address !== initialAddress ||
		mask !== initialMask ||
		listenPort !== initialListenPort ||
		description !== initialDescription ||
		endpoint !== initialEndpoint ||
		mtu !== initialMtu ||
		generateAsc !== initialGenerateAsc
	);


	async function handleCreate() {
		if (!isValidEndpointHost(endpoint)) {
			notifications.error(m.servers_edit_endpoint_invalid());
			return;
		}
		creating = true;
		try {
			const created = await api.createManagedServer({
				address,
				mask,
				listenPort,
				description: description || undefined,
				endpoint: endpoint || undefined,
				mtu,
				generateAsc,
			});
			onclose();
			onCreated(created.interfaceName);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_create_failed());
		} finally {
			creating = false;
		}
	}
</script>

<Modal {open} title={m.servers_create_title()} size="sm" {onclose} hasUnsavedChanges={() => isDirty}>
	<div class="form-fields">
		<div class="wan-info">
			<span class="wan-label">{m.servers_create_wan_label()}</span>
			{#if loadingWanIP}
				<span class="wan-value wan-loading">{m.servers_create_wan_detecting()}</span>
			{:else if wanIP}
				<span class="wan-value mono">{wanIP}</span>
			{:else}
				<span class="wan-value wan-error">{m.servers_create_wan_failed()}</span>
			{/if}
			<span class="wan-hint">{m.servers_create_wan_hint({ host: endpoint || wanIP || '...', port: listenPort })}</span>
		</div>

		<div class="form-group">
			<label class="label" for="ms-description">{m.servers_edit_label_name()}</label>
			<input
				type="text"
				id="ms-description"
				class="input"
				bind:value={description}
				placeholder="AWGM WG Server"
				maxlength="64"
			/>
		</div>
		<div class="form-group">
			<label class="label" for="ms-address">{m.servers_edit_label_ip()}</label>
			<input type="text" id="ms-address" class="input" bind:value={address} oninput={() => addressDirty = true} placeholder="10.10.0.1" />
		</div>
		<div class="form-group">
			<label class="label" for="ms-mask">{m.servers_edit_label_mask()}</label>
			<input type="text" id="ms-mask" class="input" bind:value={mask} oninput={() => maskDirty = true} placeholder="24" />
		</div>
		<div class="form-group">
			<label class="label" for="ms-port">{m.tunnels_card_port()}</label>
			<input
				type="number"
				id="ms-port"
				class="input"
				bind:value={listenPort}
				min={1}
				max={65535}
				oninput={() => listenPortDirty = true}
			/>
		</div>

		<div class="separator"></div>

		<div class="form-group">
			<div class="label-row">
				<label class="label" for="ms-endpoint">Endpoint</label>
				<button type="button" class="hint-toggle" onclick={() => showEndpointHint = !showEndpointHint}>?</button>
			</div>
			{#if showEndpointHint}
				<p class="hint-text">{m.servers_edit_endpoint_hint()}</p>
			{/if}
			<input
				type="text"
				id="ms-endpoint"
				class="input"
				bind:value={endpoint}
				placeholder={loadingWanIP ? m.servers_edit_wan_detecting() : (wanIP || 'WAN IP')}
			/>
		</div>
		<div class="form-group">
			<label class="label" for="ms-mtu">MTU</label>
			<input type="number" id="ms-mtu" class="input" bind:value={mtu} min={1280} max={1500} />
			<span class="field-hint">{m.servers_edit_mtu_hint()}</span>
		</div>

		<div class="form-group">
			<div class="label-row asc-label-row">
				<span class="label">{m.servers_create_gen_asc()}</span>
				<button
					type="button"
					class="hint-toggle"
					onclick={() => showAscHint = !showAscHint}
					aria-label={m.servers_create_asc_hint_aria()}
					aria-expanded={showAscHint}
				>
					?
				</button>
				<Toggle
					checked={generateAsc}
					onchange={(checked) => generateAsc = checked}
					size="sm"
				/>
			</div>
			{#if showAscHint}
				<p class="hint-text">
					{m.servers_create_asc_hint()}
				</p>
			{/if}
		</div>
	</div>

	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={onclose}>{m.common_cancel()}</Button>
		<Button variant="primary" size="md" onclick={handleCreate} disabled={!address || !mask} loading={creating}>
			{m.tunnels_create()}
		</Button>
	{/snippet}
</Modal>

<style>
	.form-fields {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.field-hint {
		font-size: 0.6875rem;
		color: var(--text-muted);
	}

	.form-group {
		display: flex;
		flex-direction: column;
		gap: 0.375rem;
	}

	.label {
		font-size: 0.8125rem;
		font-weight: 500;
		color: var(--text-secondary);
	}

	.input {
		padding: 8px 12px;
		font-size: 13px;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: 6px;
		color: var(--text-primary);
	}

	.input:focus {
		outline: none;
		border-color: var(--accent);
	}

	.input[type="number"] {
		-moz-appearance: textfield;
		appearance: textfield;
	}

	.separator {
		border-top: 1px solid var(--border);
		margin: 0.25rem 0;
	}

	.label-row {
		display: flex;
		align-items: center;
		gap: 0.375rem;
	}

	.hint-toggle {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 16px;
		height: 16px;
		padding: 0;
		font-size: 0.6875rem;
		font-weight: 600;
		color: var(--text-muted);
		background: none;
		border: 1px solid var(--border);
		border-radius: 50%;
		cursor: pointer;
	}

	.hint-text {
		font-size: 0.75rem;
		color: var(--text-muted);
		line-height: 1.4;
		margin: 0;
		padding: 0.5rem 0.625rem;
		background: var(--bg-tertiary, rgba(255, 255, 255, 0.03));
		border: 1px solid var(--border);
		border-radius: 6px;
	}

	.wan-info {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		padding: 0.75rem;
		background: var(--bg-tertiary, rgba(255, 255, 255, 0.03));
		border: 1px solid var(--border);
		border-radius: 6px;
	}

	.wan-label {
		font-size: 0.75rem;
		font-weight: 500;
		color: var(--text-muted);
		text-transform: uppercase;
		letter-spacing: 0.03em;
	}

	.wan-value {
		font-size: 0.9375rem;
		font-weight: 600;
		color: var(--text-primary);
	}

	.wan-loading {
		color: var(--text-muted);
		font-weight: 400;
	}

	.wan-error {
		color: var(--warning, #f59e0b);
		font-weight: 400;
		font-size: 0.8125rem;
	}

	.wan-hint {
		font-size: 0.6875rem;
		color: var(--text-muted);
	}

	.mono {
		font-family: var(--font-mono, monospace);
	}

	.asc-label-row {
		justify-content: space-between;
	}

	.asc-label-row .label {
		margin-right: auto;
	}

	.asc-label-row :global(.toggle-container) {
		flex-shrink: 0;
	}
</style>
