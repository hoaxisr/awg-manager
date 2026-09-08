<script lang="ts">
	import type { ManagedServer } from '$lib/types';
	import { Modal, FormToggle, Button, FieldHint } from '$lib/components/ui';
	import { routerDnsHint } from './routerDnsHint';
	import { validateTunnelIP, validateDNSList } from '$lib/utils/peerForm';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { suggestNextPeerIP } from '$lib/utils/serverPeerOptions';

	interface Props {
		open: boolean;
		serverId: string;
		server: ManagedServer;
		routerIP?: string;
		onclose: () => void;
		onAdded: () => void;
	}

	let { open = $bindable(false), serverId, server, routerIP = '', onclose, onAdded }: Props = $props();

	let description = $state('');
	let tunnelIP = $state('');
	let dns = $state('');
	let useRouterDNS = $state(false);
	let adding = $state(false);
	let wasOpen = $state(false);

	// Track initial state for this modal opening
	let initialDescription = $state('');
	let initialTunnelIP = $state('');
	let initialDns = $state('');
	let initialUseRouterDNS = $state(false);

	$effect(() => {
		if (open && !wasOpen) {
			description = '';
			initialDescription = '';
			tunnelIP = suggestNextIP();
			initialTunnelIP = tunnelIP;
			dns = '';
			initialDns = '';
			useRouterDNS = false;
			initialUseRouterDNS = false;
		}
		wasOpen = open;
	});

	const ipError = $derived(validateTunnelIP(tunnelIP));
	const dnsError = $derived(validateDNSList(dns));

	const isDirty = $derived(
		description !== initialDescription ||
		tunnelIP !== initialTunnelIP ||
		dns !== initialDns ||
		useRouterDNS !== initialUseRouterDNS
	);

	function suggestNextIP(): string {
		return suggestNextPeerIP(
			server.address,
			(server.peers ?? []).map((p) => p.tunnelIP.replace(/\/\d+$/, ''))
		);
	}

	async function handleAdd() {
		adding = true;
		try {
			await api.addManagedPeer(serverId, { description, tunnelIP, dns: dns || undefined });
			notifications.success('Клиент добавлен');
			onclose();
			onAdded();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка добавления');
		} finally {
			adding = false;
		}
	}
</script>

<Modal {open} title="Добавить клиента" size="sm" {onclose} hasUnsavedChanges={() => isDirty}>
	<div class="form-fields">
		<div class="form-group">
			<label class="label" for="amp-desc">Имя / описание</label>
			<input type="text" id="amp-desc" class="input" bind:value={description} placeholder="Телефон, ноутбук..." />
		</div>
		<div class="form-group">
			<label class="label" for="amp-ip">Tunnel IP (CIDR)</label>
			<input type="text" id="amp-ip" class="input" bind:value={tunnelIP} placeholder="10.0.0.2/32" />
			{#if ipError}
				<span class="field-hint is-error">{ipError}</span>
			{:else}
				<span class="hint-text">Адрес клиента в VPN-сети</span>
			{/if}
		</div>
		<div class="form-group">
			<label class="label" for="amp-dns">DNS серверы</label>
			<input type="text" id="amp-dns" class="input" bind:value={dns} placeholder="1.1.1.1, 8.8.8.8" disabled={useRouterDNS} />
			{#if routerIP}
				<div class="toggle-row">
					<span class="toggle-label">DNS роутера ({routerIP})<FieldHint text={routerDnsHint} ariaLabel="Подсказка: DNS роутера" /></span>
					<FormToggle bind:checked={useRouterDNS} onchange={(val) => { dns = val ? routerIP : ''; }} size="sm" />
				</div>
			{/if}
			{#if dnsError}
				<span class="field-hint is-error">{dnsError}</span>
			{:else}
				<span class="hint-text">По умолчанию: 1.1.1.1, 8.8.8.8</span>
			{/if}
		</div>
	</div>

	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={onclose}>Отмена</Button>
		<Button variant="primary" size="md" onclick={handleAdd} disabled={adding || !!ipError || !!dnsError} loading={adding}>
			Добавить
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

	.hint-text {
		font-size: 0.6875rem;
		color: var(--text-muted);
	}

	.toggle-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
	}

	.toggle-label {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
		font-size: 0.75rem;
		color: var(--text-secondary);
	}
</style>
