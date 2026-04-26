<script lang="ts">
	import { Modal } from '$lib/components/ui';
	import { configureSingboxPingCheck, removeSingboxPingCheck } from '$lib/stores/pingcheck';
	import { notifications } from '$lib/stores/notifications';

	interface Props {
		open: boolean;
		tag: string;
		tunnelName: string;
		currentEnabled?: boolean;
		currentInterval?: number;
		currentThreshold?: number;
		onclose: () => void;
		onSaved: () => void;
	}

	let {
		open = $bindable(false),
		tag,
		tunnelName,
		currentEnabled = false,
		currentThreshold = 3,
		currentInterval = 30,
		onclose,
		onSaved
	}: Props = $props();

	let enabled = $state(false);
	let interval = $state(30);
	let failThreshold = $state(3);
	let saving = $state(false);
	let error = $state('');

	// Синхронизируем данные при открытии модалки (Svelte 5 way)
	$effect(() => {
		if (open) {
			enabled = currentEnabled;
			interval = currentInterval;
			failThreshold = currentThreshold;
		}
	});

	async function handleSave(): Promise<void> {
		saving = true;
		error = '';
		try {
			if (enabled) {
				await configureSingboxPingCheck(tag, {
					enabled: true,
					intervalSec: interval,
					failThreshold
				});
				notifications.success(`Мониторинг для ${tunnelName} включен`);
			} else {
				await removeSingboxPingCheck(tag);
				notifications.success(`Мониторинг для ${tunnelName} отключен`);
			}
			onSaved();
			open = false;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
			notifications.error(error);
		} finally {
			saving = false;
		}
	}


</script>

<Modal bind:open title="Мониторинг Sing-box" size="sm" onclose={onclose}>
	<div class="modal-subtitle">{tunnelName}</div>
	<div class="status-line">
		<span class="status-text">Статус: {enabled ? 'Включён' : 'Отключён'}</span>
	</div>

	<div class="form-grid">
		<div class="field">
			<label class="field-label" for="interval">Интервал (сек)</label>
			<input id="interval" type="number" class="input" bind:value={interval} min="10" max="3600" />
		</div>
		<div class="field">
			<label class="field-label" for="threshold">Порог ошибок</label>
			<input id="threshold" type="number" class="input" bind:value={failThreshold} min="1" max="100" />
		</div>
	</div>

	{#if error}
		<div class="error">{error}</div>
	{/if}

	{#snippet actions()}
		<div class="actions-row">
			<button
				class="btn btn-sm {enabled ? 'btn-danger' : 'btn-primary'}"
				onclick={() => enabled = !enabled}
			>
				{enabled ? 'Выключить' : 'Включить'}
			</button>
			<div class="actions-right">
				<button class="btn btn-ghost" onclick={onclose}>Отмена</button>
				<button class="btn btn-primary" onclick={handleSave} disabled={saving}>
					{saving ? 'Сохранение...' : 'Сохранить'}
				</button>
			</div>
		</div>
	{/snippet}
</Modal>

<style>
	.status-line {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
	}
	.status-text {
		font-size: 0.875rem;
		color: var(--text-primary);
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
	.input {
		padding: 6px 10px;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: 6px;
		color: var(--text-primary);
		font-size: 14px;
	}
	.error {
		color: var(--error);
		font-size: 13px;
	}

	.actions-row {
		display: flex;
		justify-content: space-between;
		width: 100%;
	}

	.actions-right {
		display: flex;
		gap: 0.5rem;
	}

	.modal-subtitle {
		font-size: 0.875rem;
		color: var(--text-muted);
		margin-bottom: 1rem;
		font-weight: 500;
	}
</style>