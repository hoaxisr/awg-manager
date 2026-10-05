<script lang="ts">
	import { m } from '$lib/i18n';
	import type { ManagedServer, UpdateManagedServerRequest } from '$lib/types';
	import { Modal, Button } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { isValidEndpointHost } from '$lib/utils/endpoint';
	import { servers } from '$lib/stores/servers';

	interface Props {
		open: boolean;
		serverId: string;
		server: ManagedServer;
		onclose: () => void;
		onUpdated: () => void;
	}

	let { open = $bindable(false), serverId, server, onclose, onUpdated }: Props = $props();

	let address = $state('');
	let mask = $state('');
	let listenPort = $state(0);
	let description = $state('');
	let endpoint = $state('');
	let mtu = $state(1376);
	let saving = $state(false);
	let wanIP = $state('');
	let loadingWanIP = $state(false);
	let wasOpen = $state(false);
	let showEndpointHint = $state(false);

	$effect(() => {
		if (open && !wasOpen) {
			address = server.address;
			// Convert dotted mask back to CIDR for editing
			mask = dotToPrefix(server.mask);
			listenPort = server.listenPort;
			description = server.description ?? '';
			endpoint = server.endpoint ?? '';
			mtu = server.mtu || 1376;

			// Fetch WAN IP as placeholder for endpoint
			wanIP = '';
			loadingWanIP = true;
			api.getWANIP().then(ip => wanIP = ip).catch(() => wanIP = '').finally(() => loadingWanIP = false);
		}
		wasOpen = open;
	});

	const isDirty = $derived(
		address !== server.address ||
		mask !== dotToPrefix(server.mask) ||
		listenPort !== server.listenPort ||
		description !== (server.description ?? '') ||
		endpoint !== (server.endpoint ?? '') ||
		mtu !== (server.mtu || 1376)
	);

	function dotToPrefix(mask: string): string {
		if (/^\d+$/.test(mask)) return mask;
		const parts = mask.split('.').map(Number);
		let bits = 0;
		for (const p of parts) {
			bits += (p >>> 0).toString(2).split('1').length - 1;
		}
		return String(bits);
	}


	async function handleSave() {
		if (!isValidEndpointHost(endpoint)) {
			notifications.error(m.servers_edit_endpoint_invalid());
			return;
		}
		saving = true;
		try {
			// Build conditional payload — only include optional fields when the
			// user actually changed them. Backend semantics: nil pointer =
			// preserve, non-nil pointer = set (empty string CLEARS). Mapping
			// from TS: omit (undefined) = preserve, present = set.
			const payload: UpdateManagedServerRequest = { address, mask, listenPort };
			if (description !== (server.description ?? '')) {
				payload.description = description;
			}
			if (endpoint !== (server.endpoint ?? '')) {
				payload.endpoint = endpoint;
			}
			// MTU всегда включается в payload: backend применяет его к интерфейсу
			// роутера идемпотентно, и это догоняет legacy-серверы, созданные до
			// того, как MTU начал ставиться на интерфейс.
			payload.mtu = mtu;
			const fresh = await api.updateManagedServer(serverId, payload);
			servers.applyMutationResponse(fresh);
			notifications.success(m.servers_edit_updated());
			onclose();
			onUpdated();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.system_tunnels_save_failed());
		} finally {
			saving = false;
		}
	}
</script>

<Modal {open} title={m.servers_edit_title()} size="sm" {onclose} hasUnsavedChanges={() => isDirty}>
	<div class="form-fields">
		<div class="form-group">
			<label class="label" for="ems-description">{m.servers_edit_label_name()}</label>
			<input
				type="text"
				id="ems-description"
				class="input"
				bind:value={description}
				placeholder="AWGM WG Server"
				maxlength="64"
			/>
		</div>
		<div class="form-group">
			<label class="label" for="ems-address">{m.servers_edit_label_ip()}</label>
			<input type="text" id="ems-address" class="input" bind:value={address} />
		</div>
		<div class="form-group">
			<label class="label" for="ems-mask">{m.servers_edit_label_mask()}</label>
			<input type="text" id="ems-mask" class="input" bind:value={mask} />
		</div>
		<div class="form-group">
			<label class="label" for="ems-port">{m.tunnels_card_port()}</label>
			<input type="number" id="ems-port" class="input" bind:value={listenPort} min={1} max={65535} />
		</div>

		<div class="separator"></div>

		<div class="form-group">
			<div class="label-row">
				<label class="label" for="ems-endpoint">Endpoint</label>
				<button type="button" class="hint-toggle" onclick={() => showEndpointHint = !showEndpointHint}>?</button>
			</div>
			{#if showEndpointHint}
				<p class="hint-text">{m.servers_edit_endpoint_hint()}</p>
			{/if}
			<input
				type="text"
				id="ems-endpoint"
				class="input"
				bind:value={endpoint}
				placeholder={loadingWanIP ? m.servers_edit_wan_detecting() : (wanIP || 'WAN IP')}
			/>
			{#if wanIP && !endpoint}
				<span class="field-hint">{m.servers_edit_wan_will_be_used({ ip: wanIP })}</span>
			{/if}
		</div>
		<div class="form-group">
			<label class="label" for="ems-mtu">MTU</label>
			<input type="number" id="ems-mtu" class="input" bind:value={mtu} min={1280} max={1500} />
			<span class="field-hint">{m.servers_edit_mtu_hint()}</span>
		</div>
	</div>

	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={onclose}>{m.common_cancel()}</Button>
		<Button variant="primary" size="md" onclick={handleSave} loading={saving}>
			{m.common_save()}
		</Button>
	{/snippet}
</Modal>

<style>
	.form-fields {
		display: flex;
		flex-direction: column;
		gap: 1rem;
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

	.field-hint {
		font-size: 0.6875rem;
		color: var(--text-muted);
	}
</style>
