<script lang="ts">
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Modal, Button, Dropdown } from '$lib/components/ui';
	import type { AWGTunnel, ConnectivityCheckConfig } from '$lib/types';

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
			notifications.error('Не удалось загрузить настройки');
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
			notifications.success('Настройки проверки сохранены');
			onSaved();
		} catch (e) {
			notifications.error(`Ошибка: ${(e as Error).message}`);
		} finally {
			saving = false;
		}
	}
</script>

<Modal {open} title="Проверка связности" size="sm" {onclose}>
	{#if loading}
		<div class="loading-state">Загрузка...</div>
	{:else}
		<div class="form-fields">
			<div class="field">
				<Dropdown
					id="cc-method"
					label="Метод проверки"
					bind:value={method}
					options={[
						{ value: 'http', label: 'HTTP 204 (интернет)' },
						{ value: 'ping', label: 'Ping IP' },
						{ value: 'disabled', label: 'Выключено' },
					]}
					fullWidth
				/>
			</div>

			{#if method === 'ping'}
				<div class="field">
					<label class="field-label" for="cc-target">IP для ping</label>
					<input id="cc-target" type="text" class="field-input" bind:value={pingTarget} placeholder="10.0.0.1" />
					<span class="hint-text">По умолчанию: gateway (.1) из адреса туннеля</span>
				</div>
			{/if}

			{#if method === 'http'}
				<p class="hint-text">Проверка через HTTP запрос к connectivitycheck.gstatic.com. Требует выход в интернет через туннель.</p>
			{:else if method === 'disabled'}
				<p class="hint-text">Индикатор связности будет скрыт на карточке туннеля.</p>
			{/if}
		</div>
	{/if}

	{#snippet actions()}
		<Button variant="ghost" onclick={onclose}>Отмена</Button>
		<Button variant="primary" onclick={handleSave} disabled={loading} loading={saving}>
			Сохранить
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
