<script lang="ts">
	import type { ManagedServer } from '$lib/types';
	import { Modal } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';

	interface Props {
		open: boolean;
		server: ManagedServer;
		onclose: () => void;
		onAdded: () => void;
	}

	let { open = $bindable(false), server, onclose, onAdded }: Props = $props();

	let description = $state('');
	let tunnelIP = $state('');
	let dns = $state('');
	let adding = $state(false);
	let wasOpen = $state(false);

	$effect(() => {
		if (open && !wasOpen) {
			description = '';
			tunnelIP = suggestNextIP();
			dns = '';
		}
		wasOpen = open;
	});

	function suggestNextIP(): string {
		const parts = server.address.split('.');
		if (parts.length !== 4) return '';
		const base = parts.slice(0, 3).join('.');
		const usedIPs = new Set([
			server.address,
			...(server.peers ?? []).map(p => p.tunnelIP.replace(/\/\d+$/, ''))
		]);
		for (let i = 2; i < 255; i++) {
			const candidate = `${base}.${i}`;
			if (!usedIPs.has(candidate)) return `${candidate}/32`;
		}
		return '';
	}

	async function handleAdd() {
		adding = true;
		try {
			await api.addManagedPeer({ description, tunnelIP, dns: dns || undefined });
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
			<label class="label" for="amp-desc">Имя / описание</label>
			<input type="text" id="amp-desc" class="input" bind:value={description} placeholder="Телефон, ноутбук..." />
		</div>
		<div class="form-group">
			<label class="label" for="amp-ip">Tunnel IP (CIDR)</label>
			<input type="text" id="amp-ip" class="input" bind:value={tunnelIP} placeholder="10.0.0.2/32" />
			<span class="hint-text">Адрес клиента в VPN-сети</span>
		</div>
		<div class="form-group">
			<label class="label" for="amp-dns">DNS серверы</label>
			<input type="text" id="amp-dns" class="input" bind:value={dns} placeholder="1.1.1.1, 8.8.8.8" />
			<span class="hint-text">По умолчанию: 1.1.1.1, 8.8.8.8</span>
		</div>
	</div>

	{#snippet actions()}
		<button class="btn btn-ghost" onclick={onclose}>Отмена</button>
		<button class="btn btn-primary" onclick={handleAdd} disabled={adding || !tunnelIP}>
			{adding ? 'Добавление...' : 'Добавить'}
		</button>
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
</style>
