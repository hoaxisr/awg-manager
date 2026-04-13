<script lang="ts">
	import type { ManagedPeer } from '$lib/types';
	import { Modal, FormToggle } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';

	interface Props {
		open: boolean;
		peer: ManagedPeer;
		routerIP?: string;
		onclose: () => void;
		onUpdated: () => void;
	}

	let { open = $bindable(false), peer, routerIP = '', onclose, onUpdated }: Props = $props();

	let description = $state('');
	let tunnelIP = $state('');
	let dns = $state('');
	let useRouterDNS = $state(false);
	let saving = $state(false);
	let wasOpen = $state(false);

	$effect(() => {
		if (open && !wasOpen) {
			description = peer.description;
			tunnelIP = peer.tunnelIP;
			dns = peer.dns || '';
			useRouterDNS = routerIP !== '' && dns === routerIP;
		}
		wasOpen = open;
	});

	async function handleSave() {
		saving = true;
		try {
			await api.updateManagedPeer(peer.publicKey, { description, tunnelIP, dns: dns || undefined });
			notifications.success('Клиент обновлён');
			onclose();
			onUpdated();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка сохранения');
		} finally {
			saving = false;
		}
	}
</script>

<Modal {open} title="Редактировать клиента" size="sm" {onclose}>
	<div class="form-fields">
		<div class="form-group">
			<label class="label" for="emp-desc">Имя / описание</label>
			<input type="text" id="emp-desc" class="input" bind:value={description} />
		</div>
		<div class="form-group">
			<label class="label" for="emp-ip">Tunnel IP (CIDR)</label>
			<input type="text" id="emp-ip" class="input" bind:value={tunnelIP} />
		</div>
		<div class="form-group">
			<label class="label" for="emp-dns">DNS серверы</label>
			<input type="text" id="emp-dns" class="input" bind:value={dns} placeholder="1.1.1.1, 8.8.8.8" disabled={useRouterDNS} />
			{#if routerIP}
				<div class="toggle-row">
					<span class="toggle-label">DNS роутера ({routerIP})</span>
					<FormToggle bind:checked={useRouterDNS} onchange={(val) => { dns = val ? routerIP : ''; }} size="sm" />
				</div>
			{/if}
			<span class="field-hint">Используется в конфиге клиента. По умолчанию: 1.1.1.1, 8.8.8.8</span>
		</div>
	</div>

	{#snippet actions()}
		<button class="btn btn-ghost" onclick={onclose}>Отмена</button>
		<button class="btn btn-primary" onclick={handleSave} disabled={saving}>
			{saving ? 'Сохранение...' : 'Сохранить'}
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

	.field-hint {
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
		font-size: 0.75rem;
		color: var(--text-secondary);
	}
</style>
