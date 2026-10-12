<script lang="ts">
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Modal, Button, Dropdown } from '$lib/components/ui';
	import type { AWGTunnel, ConnectivityCheckConfig } from '$lib/types';
	import { m } from '$lib/i18n';

	interface Props {
		open: boolean;
		tunnelId: string;
		tunnelAddress?: string;
		onclose: () => void;
		onSaved: () => void;
	}

	let { open = $bindable(false), tunnelId, tunnelAddress, onclose, onSaved }: Props = $props();

	let loading = $state(false);
	let saving = $state(false);
	let tunnel: AWGTunnel | null = $state(null);

	let method = $state<ConnectivityCheckConfig['method']>('http');
	let pingTarget = $state('');
	let wasOpen = $state(false);

	$effect(() => {
		if (open && !wasOpen) {
			loadSettings();
		}
		wasOpen = open;
	});

	function computeDefaultGateway(address?: string): string {
		if (!address) return '';
		const ip = address.split('/')[0].split(',')[0].trim();
		const parts = ip.split('.');
		if (parts.length !== 4) return '';
		parts[3] = '1';
		return parts.join('.');
	}

	async function loadSettings() {
		loading = true;
		try {
			tunnel = await api.getTunnel(tunnelId);
			const cfg = tunnel.connectivityCheck;
			method = (cfg?.method !== undefined && cfg?.method !== null) ? cfg.method : 'http';
			pingTarget = cfg?.pingTarget || computeDefaultGateway(tunnel.interface?.address || tunnelAddress);
		} catch (e) {
			notifications.error(m.tunnels_connectivity_load_failed());
		} finally {
			loading = false;
		}
	}

	async function handleSave() {
		if (!tunnel) return;
		saving = true;
		try {
			tunnel.connectivityCheck = {
				method,
				pingTarget: method === 'ping' ? pingTarget : undefined,
			};
			await api.updateTunnel(tunnelId, tunnel);
			notifications.success(m.tunnels_connectivity_saved());
			onSaved();
		} catch (e) {
			notifications.error(m.tunnels_error_with_message({ message: (e as Error).message }));
		} finally {
			saving = false;
		}
	}
</script>

<Modal {open} title={m.tunnels_connectivity_title()} size="sm" {onclose}>
	{#if loading}
		<div class="loading-state">{m.tunnels_loading()}</div>
	{:else}
		<div class="form-fields">
			<div class="field">
				<Dropdown
					id="cc-method"
					label={m.tunnels_connectivity_method()}
					bind:value={method}
					options={[
						{ value: 'http', label: m.tunnels_connectivity_method_http() },
						{ value: 'ping', label: m.tunnels_connectivity_method_ping() },
						{ value: 'disabled', label: m.tunnels_connectivity_method_disabled() },
					]}
					fullWidth
				/>
			</div>

			{#if method === 'ping'}
				<div class="field">
					<label class="field-label" for="cc-target">{m.tunnels_connectivity_ping_ip()}</label>
					<input id="cc-target" type="text" class="field-input" bind:value={pingTarget} placeholder="10.0.0.1" />
					<span class="hint-text">{m.tunnels_connectivity_ping_default()}</span>
				</div>
			{/if}

			{#if method === 'http'}
				<p class="hint-text">{m.tunnels_connectivity_http_hint()}</p>
			{:else if method === 'disabled'}
				<p class="hint-text">{m.tunnels_connectivity_disabled_hint()}</p>
			{/if}
		</div>
	{/if}

	{#snippet actions()}
		<Button variant="secondary" onclick={onclose}>{m.common_cancel()}</Button>
		<Button variant="primary" onclick={handleSave} disabled={loading} loading={saving}>
			{m.common_save()}
		</Button>
	{/snippet}
</Modal>

<style>
	.form-fields {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
	}

	.field {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}

	.field-label {
		font-size: 0.6875rem;
		text-transform: uppercase;
		color: var(--color-text-muted);
	}

	.hint-text {
		font-size: 0.6875rem;
		color: var(--color-text-muted);
	}

	.loading-state {
		text-align: center;
		padding: 2rem;
		color: var(--color-text-muted);
	}
</style>
