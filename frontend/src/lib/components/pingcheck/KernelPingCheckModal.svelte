<script lang="ts">
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Modal } from '$lib/components/ui';
	import type { AWGTunnel } from '$lib/types';

	interface Props {
		open: boolean;
		tunnelId: string;
		tunnelName: string;
		onclose: () => void;
		onSaved: () => void;
	}

	let { open = $bindable(false), tunnelId, tunnelName, onclose, onSaved }: Props = $props();

	let loading = $state(false);
	let saving = $state(false);
	let tunnel: AWGTunnel | null = $state(null);

	// Form fields
	let method = $state('http');
	let target = $state('8.8.8.8');
	let interval = $state(45);
	let deadInterval = $state(120);
	let failThreshold = $state(3);

	let wasOpen = $state(false);
	let prevMethod = $state('');

	$effect(() => {
		if (open && !wasOpen) {
			loadSettings();
		}
		wasOpen = open;
	});

	// Auto-set default target when method changes
	$effect(() => {
		if (prevMethod && method !== prevMethod) {
			if (method === 'http') target = 'cp.cloudflare.com';
			else if (method === 'icmp') target = '8.8.8.8';
		}
		prevMethod = method;
	});

	async function loadSettings() {
		loading = true;
		try {
			tunnel = await api.getTunnel(tunnelId);
			if (tunnel.pingCheck) {
				method = tunnel.pingCheck.method || 'http';
				target = tunnel.pingCheck.target || '8.8.8.8';
				interval = tunnel.pingCheck.interval || 45;
				deadInterval = tunnel.pingCheck.deadInterval || 120;
				failThreshold = tunnel.pingCheck.failThreshold || 3;
			}
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
			tunnel.pingCheck = {
				...tunnel.pingCheck!,
				method,
				target,
				interval,
				deadInterval,
				failThreshold
			};
			await api.updateTunnel(tunnelId, tunnel);
			notifications.success('Настройки мониторинга сохранены');
			onSaved();
		} catch (e) {
			notifications.error(`Ошибка: ${(e as Error).message}`);
		} finally {
			saving = false;
		}
	}
</script>

<Modal {open} title="Настройки мониторинга: {tunnelName}" size="sm" {onclose}>
	{#if loading}
		<div class="loading-state">Загрузка...</div>
	{:else}
		<div class="form-grid">
			<div class="field">
				<label class="field-label" for="kpc-method">Метод</label>
				<select id="kpc-method" class="input" bind:value={method}>
					<option value="http">HTTP 204</option>
					<option value="icmp">ICMP</option>
				</select>
			</div>

			<div class="field">
				<label class="field-label" for="kpc-target">Цель</label>
				<input id="kpc-target" type="text" class="input" bind:value={target} placeholder="8.8.8.8" />
			</div>

			<div class="field">
				<label class="field-label" for="kpc-interval">Интервал (сек)</label>
				<input id="kpc-interval" type="number" class="input" bind:value={interval} min="10" max="600" />
			</div>

			<div class="field">
				<label class="field-label" for="kpc-dead">Интервал при dead (сек)</label>
				<input id="kpc-dead" type="number" class="input" bind:value={deadInterval} min="30" max="600" />
			</div>

			<div class="field">
				<label class="field-label" for="kpc-threshold">Порог ошибок</label>
				<input id="kpc-threshold" type="number" class="input" bind:value={failThreshold} min="1" max="20" />
			</div>
		</div>
	{/if}

	{#snippet actions()}
		<button class="btn btn-ghost" onclick={onclose}>Отмена</button>
		<button class="btn btn-primary" onclick={handleSave} disabled={saving || loading}>
			{saving ? 'Сохранение...' : 'Сохранить'}
		</button>
	{/snippet}
</Modal>

<style>
	.form-grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(140px, 1fr));
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
		color: var(--text-muted);
	}

	.loading-state {
		text-align: center;
		padding: 2rem;
		color: var(--text-muted);
	}
</style>
