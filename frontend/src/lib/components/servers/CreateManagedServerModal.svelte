<script lang="ts">
	import { Modal } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';

	interface Props {
		open: boolean;
		onclose: () => void;
		onCreated: () => void;
	}

	let { open = $bindable(false), onclose, onCreated }: Props = $props();

	let address = $state('10.0.0.1');
	let mask = $state('24');
	let addressDirty = $state(false);
	let maskDirty = $state(false);
	let listenPort = $state(51820);
	let endpoint = $state('');
	let mtu = $state(1376);
	let creating = $state(false);
	let wanIP = $state('');
	let loadingWanIP = $state(false);
	let wasOpen = $state(false);
	let showEndpointHint = $state(false);

	$effect(() => {
		if (open && !wasOpen) {
			wanIP = '';
			loadingWanIP = true;
			api.getWANIP().then(ip => {
				wanIP = ip;
				if (!endpoint) endpoint = ip;
			}).catch(() => wanIP = '').finally(() => loadingWanIP = false);

			api.suggestManagedServerAddress().then(s => {
				if (!addressDirty) address = s.address;
				if (!maskDirty) mask = s.mask;
			}).catch(() => { /* keep defaults */ });
		}
		wasOpen = open;
	});

	function isValidEndpoint(val: string): boolean {
		if (!val) return true;
		if (/^(\d{1,3}\.){3}\d{1,3}$/.test(val)) return true;
		if (/^([a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$/.test(val)) return true;
		return false;
	}

	async function handleCreate() {
		if (!isValidEndpoint(endpoint)) {
			notifications.error('Endpoint должен быть IP-адресом или доменным именем');
			return;
		}
		creating = true;
		try {
			await api.createManagedServer({ address, mask, listenPort, endpoint: endpoint || undefined, mtu });
			notifications.success('Сервер создан');
			onclose();
			onCreated();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка создания');
		} finally {
			creating = false;
		}
	}
</script>

<Modal {open} title="Создать WireGuard сервер" size="sm" {onclose}>
	<div class="form-fields">
		<div class="wan-info">
			<span class="wan-label">Внешний IP (WAN)</span>
			{#if loadingWanIP}
				<span class="wan-value wan-loading">Определение...</span>
			{:else if wanIP}
				<span class="wan-value mono">{wanIP}</span>
			{:else}
				<span class="wan-value wan-error">Не удалось определить</span>
			{/if}
			<span class="wan-hint">Клиенты будут подключаться к {endpoint || wanIP || '...'}:{listenPort}</span>
		</div>

		<div class="form-group">
			<label class="label" for="ms-address">IP адрес</label>
			<input type="text" id="ms-address" class="input" bind:value={address} oninput={() => addressDirty = true} placeholder="10.10.0.1" />
		</div>
		<div class="form-group">
			<label class="label" for="ms-mask">Маска (CIDR)</label>
			<input type="text" id="ms-mask" class="input" bind:value={mask} oninput={() => maskDirty = true} placeholder="24" />
		</div>
		<div class="form-group">
			<label class="label" for="ms-port">Порт</label>
			<input type="number" id="ms-port" class="input" bind:value={listenPort} min={1} max={65535} />
		</div>

		<div class="separator"></div>

		<div class="form-group">
			<div class="label-row">
				<label class="label" for="ms-endpoint">Endpoint</label>
				<button type="button" class="hint-toggle" onclick={() => showEndpointHint = !showEndpointHint}>?</button>
			</div>
			{#if showEndpointHint}
				<p class="hint-text">IP-адрес или доменное имя, по которому клиенты будут подключаться к серверу. Если не указан — используется внешний IP роутера (WAN)</p>
			{/if}
			<input
				type="text"
				id="ms-endpoint"
				class="input"
				bind:value={endpoint}
				placeholder={loadingWanIP ? 'Определение WAN IP...' : (wanIP || 'WAN IP')}
			/>
		</div>
		<div class="form-group">
			<label class="label" for="ms-mtu">MTU</label>
			<input type="number" id="ms-mtu" class="input" bind:value={mtu} min={1280} max={1500} />
		</div>
	</div>

	{#snippet actions()}
		<button class="btn btn-ghost" onclick={onclose}>Отмена</button>
		<button class="btn btn-primary" onclick={handleCreate} disabled={creating || !address || !mask}>
			{creating ? 'Создание...' : 'Создать'}
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
</style>
