<script lang="ts">
	import type { ManagedServer } from '$lib/types';
	import { Modal } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';

	interface Props {
		open: boolean;
		server: ManagedServer;
		onclose: () => void;
		onUpdated: () => void;
	}

	let { open = $bindable(false), server, onclose, onUpdated }: Props = $props();

	let address = $state('');
	let mask = $state('');
	let listenPort = $state(0);
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
			endpoint = server.endpoint || '';
			mtu = server.mtu || 1376;

			// Fetch WAN IP as placeholder for endpoint
			wanIP = '';
			loadingWanIP = true;
			api.getWANIP().then(ip => wanIP = ip).catch(() => wanIP = '').finally(() => loadingWanIP = false);
		}
		wasOpen = open;
	});

	function dotToPrefix(m: string): string {
		if (/^\d+$/.test(m)) return m;
		const parts = m.split('.').map(Number);
		let bits = 0;
		for (const p of parts) {
			bits += (p >>> 0).toString(2).split('1').length - 1;
		}
		return String(bits);
	}

	function isValidEndpoint(val: string): boolean {
		if (!val) return true; // empty = use WAN IP
		// IP address
		if (/^(\d{1,3}\.){3}\d{1,3}$/.test(val)) return true;
		// Domain name
		if (/^([a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$/.test(val)) return true;
		return false;
	}

	async function handleSave() {
		if (!isValidEndpoint(endpoint)) {
			notifications.error('Endpoint должен быть IP-адресом или доменным именем');
			return;
		}
		saving = true;
		try {
			await api.updateManagedServer({ address, mask, listenPort, endpoint: endpoint || undefined, mtu });
			notifications.success('Сервер обновлён');
			onclose();
			onUpdated();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка сохранения');
		} finally {
			saving = false;
		}
	}
</script>

<Modal {open} title="Настройки сервера" size="sm" {onclose}>
	<div class="form-fields">
		<div class="form-group">
			<label class="label" for="ems-address">IP адрес</label>
			<input type="text" id="ems-address" class="input" bind:value={address} />
		</div>
		<div class="form-group">
			<label class="label" for="ems-mask">Маска (CIDR)</label>
			<input type="text" id="ems-mask" class="input" bind:value={mask} />
		</div>
		<div class="form-group">
			<label class="label" for="ems-port">Порт</label>
			<input type="number" id="ems-port" class="input" bind:value={listenPort} min={1} max={65535} />
		</div>

		<div class="separator"></div>

		<div class="form-group">
			<div class="label-row">
				<label class="label" for="ems-endpoint">Endpoint</label>
				<button type="button" class="hint-toggle" onclick={() => showEndpointHint = !showEndpointHint}>?</button>
			</div>
			{#if showEndpointHint}
				<p class="hint-text">IP-адрес или доменное имя, по которому клиенты будут подключаться к серверу. Если не указан — используется внешний IP роутера (WAN)</p>
			{/if}
			<input
				type="text"
				id="ems-endpoint"
				class="input"
				bind:value={endpoint}
				placeholder={loadingWanIP ? 'Определение WAN IP...' : (wanIP || 'WAN IP')}
			/>
			{#if wanIP && !endpoint}
				<span class="field-hint">Будет использован WAN IP: {wanIP}</span>
			{/if}
		</div>
		<div class="form-group">
			<label class="label" for="ems-mtu">MTU</label>
			<input type="number" id="ems-mtu" class="input" bind:value={mtu} min={1280} max={1500} />
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
