<script lang="ts">
	import type { WireguardServer } from '$lib/types';
	import { Modal, Button } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { servers } from '$lib/stores/servers';
	import { suggestNextPeerIP } from '$lib/utils/serverPeerOptions';
	import { FieldHint, FormToggle } from '$lib/components/ui';
	import { routerDnsHint } from './routerDnsHint';

	interface Props {
		open: boolean;
		serverId: string;
		server: WireguardServer;
		/** LAN-адрес роутера для тумблера «DNS роутера»; пусто — тумблера нет. */
		routerIP?: string;
		onclose: () => void;
		onAdded: () => void;
	}

	let { open = $bindable(false), serverId, server, routerIP = '', onclose, onAdded }: Props = $props();

	let description = $state('');
	let tunnelIP = $state('');
	// Резолвер пира (#933): пусто — бэкенд подставит LAN-адрес роутера. Раньше
	// на его месте стоял зашитый 1.1.1.1, и абонент резолвил мимо роутера.
	let dns = $state('');
	let useRouterDNS = $state(false);
	let adding = $state(false);
	let wasOpen = $state(false);

	function peerHostIP(p: { allowedIPs?: string[] }): string {
		const raw = p.allowedIPs?.find((ip) => ip.includes('/32')) || p.allowedIPs?.[0] || '';
		return raw.replace(/\/(32|128)$/, '');
	}

	function suggestNextIP(): string {
		return suggestNextPeerIP(server.address, (server.peers ?? []).map(peerHostIP));
	}

	$effect(() => {
		if (open && !wasOpen) {
			description = '';
			tunnelIP = suggestNextIP();
			dns = '';
			useRouterDNS = false;
		}
		wasOpen = open;
	});

	async function handleAdd() {
		adding = true;
		try {
			const fresh = await api.addSystemServerPeer(serverId, { description, tunnelIP, dns });
			servers.applyMutationResponse(fresh);
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

<Modal {open} title="Добавить клиента" size="sm" {onclose}>
	<div class="form-fields">
		<div class="form-group">
			<label class="label" for="ssp-desc">Имя / описание</label>
			<input type="text" id="ssp-desc" class="input" bind:value={description} placeholder="Телефон" />
		</div>
		<div class="form-group">
			<label class="label" for="ssp-ip">Tunnel IP (CIDR)</label>
			<input type="text" id="ssp-ip" class="input" bind:value={tunnelIP} placeholder="10.0.0.2/32" />
		</div>
		<div class="form-group">
			<label class="label" for="ssp-dns">DNS серверы</label>
			<input
				type="text"
				id="ssp-dns"
				class="input"
				bind:value={dns}
				placeholder="192.168.1.1"
				disabled={useRouterDNS}
			/>
			{#if routerIP}
				<div class="toggle-row">
					<span class="toggle-label">
						DNS роутера ({routerIP})<FieldHint text={routerDnsHint} ariaLabel="Подсказка: DNS роутера" />
					</span>
					<FormToggle
						bind:checked={useRouterDNS}
						onchange={(val) => {
							dns = val ? routerIP : '';
						}}
						size="sm"
					/>
				</div>
			{/if}
			<span class="hint-text">Пусто — DNS роутера</span>
		</div>
	</div>

	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={onclose}>Отмена</Button>
		<Button variant="primary" size="md" onclick={handleAdd} loading={adding} disabled={!tunnelIP}>
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

	.toggle-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
	}

	.toggle-label {
		font-size: 0.8125rem;
		color: var(--text-secondary);
	}

	.hint-text {
		font-size: 0.75rem;
		color: var(--text-muted, #94a3b8);
	}

	.input:focus {
		outline: none;
		border-color: var(--accent);
	}
</style>
