<script lang="ts">
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Modal, FormToggle } from '$lib/components/ui';
	import type { NativePingCheckConfig, NativePingCheckStatus } from '$lib/types';

	interface Props {
		open: boolean;
		tunnelId: string;
		tunnelName: string;
		status: NativePingCheckStatus | null;
		onclose: () => void;
		onSaved: () => void;
		onRemoved: () => void;
	}

	let { open = $bindable(false), tunnelId, tunnelName, status, onclose, onSaved, onRemoved }: Props = $props();

	let saving = $state(false);
	let removing = $state(false);

	// Form fields
	let host = $state('8.8.8.8');
	let mode = $state<'icmp' | 'connect' | 'tls'>('icmp');
	let updateInterval = $state(10);
	let maxFails = $state(3);
	let minSuccess = $state(1);
	let timeout = $state(5);
	let port = $state(443);
	let restart = $state(true);

	let needsPort = $derived(mode === 'connect' || mode === 'tls');

	const presets = [
		{ label: 'ICMP 8.8.8.8', host: '8.8.8.8', mode: 'icmp' as const },
		{ label: 'ICMP 1.1.1.1', host: '1.1.1.1', mode: 'icmp' as const },
		{ label: 'TCP 8.8.8.8:53', host: '8.8.8.8', mode: 'connect' as const, port: 53 },
		{ label: 'TLS 1.1.1.1:443', host: '1.1.1.1', mode: 'tls' as const, port: 443 },
	];

	function applyPreset(p: typeof presets[0]) {
		host = p.host;
		mode = p.mode;
		if (p.port) port = p.port;
	}

	function syncFromStatus() {
		// Backend overlays storage into status.* when the NDMS profile is
		// disabled, so a single read path works for all three cases:
		// active profile (from NDMS), disabled profile with stored settings
		// (from storage), and brand-new tunnel (all empty → hardcoded
		// defaults via `||`/`??`).
		host = status?.host || '8.8.8.8';
		const m = status?.mode;
		mode = (m === 'icmp' || m === 'connect' || m === 'tls') ? m : 'icmp';
		updateInterval = status?.interval || 10;
		maxFails = status?.maxFails || 3;
		minSuccess = status?.minSuccess || 1;
		timeout = status?.timeout || 5;
		port = status?.port || 443;
		restart = status?.restart ?? true;
	}

	// Sync form from status when modal opens
	let wasOpen = $state(false);
	$effect(() => {
		if (open && !wasOpen) {
			syncFromStatus();
		}
		wasOpen = open;
	});

	async function handleSave() {
		saving = true;
		try {
			const config: NativePingCheckConfig = {
				host,
				mode,
				updateInterval,
				maxFails,
				minSuccess,
				timeout,
				restart,
			};
			if (needsPort) config.port = port;
			await api.configureNativePingCheck(tunnelId, config);
			notifications.success('Настройки мониторинга сохранены');
			onSaved();
		} catch (e) {
			notifications.error(`Ошибка: ${(e as Error).message}`);
		} finally {
			saving = false;
		}
	}

	async function handleRemove() {
		removing = true;
		try {
			await api.removeNativePingCheck(tunnelId);
			notifications.success('Мониторинг отключён');
			onRemoved();
		} catch (e) {
			notifications.error(`Ошибка: ${(e as Error).message}`);
		} finally {
			removing = false;
		}
	}

	let busy = $derived(saving || removing);
</script>

<Modal {open} title="Мониторинг Native" size="sm" {onclose}>
	<div class="modal-subtitle">{tunnelName}</div>
	<div class="presets">
		{#each presets as p}
			<button class="preset-btn" onclick={() => applyPreset(p)} disabled={busy}>{p.label}</button>
		{/each}
	</div>

	<div class="form-grid">
		<div class="field">
			<label class="field-label" for="npc-host">Хост</label>
			<input id="npc-host" type="text" class="input" bind:value={host} />
		</div>

		<div class="field">
			<label class="field-label" for="npc-mode">Метод</label>
			<select id="npc-mode" class="input" bind:value={mode}>
				<option value="icmp">ICMP</option>
				<option value="connect">TCP Connect</option>
				<option value="tls">TLS</option>
			</select>
		</div>

		{#if needsPort}
			<div class="field">
				<label class="field-label" for="npc-port">Порт</label>
				<input id="npc-port" type="number" class="input" bind:value={port} min="1" max="65535" />
			</div>
		{/if}

		<div class="field">
			<label class="field-label" for="npc-interval">Интервал (сек)</label>
			<input id="npc-interval" type="number" class="input" bind:value={updateInterval} min="3" max="3600" />
			<span class="field-hint">3–3600</span>
		</div>

		<div class="field">
			<label class="field-label" for="npc-maxfails">Максимум сбоев</label>
			<input id="npc-maxfails" type="number" class="input" bind:value={maxFails} min="1" max="10" />
			<span class="field-hint">1–10</span>
		</div>

		<div class="field">
			<label class="field-label" for="npc-minsuccess">Минимум успехов</label>
			<input id="npc-minsuccess" type="number" class="input" bind:value={minSuccess} min="1" max="10" />
			<span class="field-hint">1–10</span>
		</div>

		<div class="field">
			<label class="field-label" for="npc-timeout">Таймаут (сек)</label>
			<input id="npc-timeout" type="number" class="input" bind:value={timeout} min="1" max="10" />
			<span class="field-hint">1–10</span>
		</div>
	</div>
	<p class="limits-note">Пределы заданы компонентом ping-check Keenetic NDMS.</p>

	<div class="restart-row">
		<div class="restart-info">
			<span class="restart-label">Перезапуск при dead</span>
			<span class="restart-hint">Автоматически перезапускать туннель при потере связи</span>
		</div>
		<FormToggle bind:checked={restart} size="sm" />
	</div>

	{#snippet actions()}
		{#if status?.exists}
			<button class="btn btn-danger" onclick={handleRemove} disabled={busy}>
				{removing ? 'Отключение...' : 'Отключить'}
			</button>
		{/if}
		<div class="actions-spacer"></div>
		<button class="btn btn-ghost" onclick={onclose}>Отмена</button>
		<button class="btn btn-primary" onclick={handleSave} disabled={busy}>
			{saving ? 'Сохранение...' : status?.exists ? 'Обновить' : 'Включить'}
		</button>
	{/snippet}
</Modal>

<style>
	.presets {
		display: flex;
		flex-wrap: wrap;
		gap: 0.375rem;
		margin-bottom: 0.5rem;
	}

	.preset-btn {
		padding: 0.125rem 0.5rem;
		font-size: 0.75rem;
		font-family: var(--font-mono, monospace);
		border-radius: 10px;
		border: 1px solid var(--border);
		background: var(--bg-primary);
		color: var(--text-muted);
		cursor: pointer;
		transition: all 0.15s ease;
	}

	.preset-btn:hover:not(:disabled) {
		background: var(--accent);
		border-color: var(--accent);
		color: white;
	}

	.preset-btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

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

	.field-hint {
		font-size: 0.6875rem;
		color: var(--text-muted);
		opacity: 0.75;
		font-variant-numeric: tabular-nums;
	}

	.limits-note {
		margin: 0.625rem 0 0;
		font-size: 0.6875rem;
		color: var(--text-muted);
		opacity: 0.7;
	}

	.restart-row {
		display: flex;
		justify-content: space-between;
		align-items: center;
		padding: 0.625rem 0.75rem;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: 6px;
		margin-top: 0.5rem;
	}

	.restart-info {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
	}

	.restart-label {
		font-size: 0.8125rem;
		font-weight: 500;
	}

	.restart-hint {
		font-size: 0.6875rem;
		color: var(--text-muted);
	}

	.modal-subtitle {
		font-size: 0.875rem;
		color: var(--text-muted);
		margin-bottom: 1rem;
		font-weight: 500;
	}

	.actions-spacer {
		flex: 1;
	}

	@media (max-width: 640px) {
		.form-grid {
			grid-template-columns: repeat(2, 1fr);
		}
	}
</style>
