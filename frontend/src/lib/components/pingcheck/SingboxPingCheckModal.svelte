<script lang="ts">
	import { Modal } from '$lib/components/ui';
	import { configureSingboxPingCheck, removeSingboxPingCheck } from '$lib/stores/pingcheck';
	import { notifications } from '$lib/stores/notifications';

	interface Props {
		open: boolean;
		tag: string;
		tunnelName: string;
		currentInterval?: number;
		currentThreshold?: number;
		currentEnabled?: boolean;
		onclose: () => void;
		onSaved: () => void; // для инвалидации стора
		onRemoved?: () => void; // опционально
	}

	let {
		open = $bindable(false),
		tag,
		tunnelName,
		currentInterval = 30,
		currentThreshold = 3,
		currentEnabled = false,
		onclose,
		onSaved,
		onRemoved
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

	async function handleRemove(): Promise<void> {
		saving = true;
		try {
			await removeSingboxPingCheck(tag);
			notifications.success(`Мониторинг для ${tunnelName} удален`);
			onSaved();
			onRemoved?.();
			open = false;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
			notifications.error(error);
		} finally {
			saving = false;
		}
	}
</script>

<Modal bind:open title="Настройки мониторинга Sing-box" size="md" onclose={onclose}>
	<div class="form">
		<div class="form-group">
			<span class="label">Статус</span>
			<div class="toggle-row">
				<span class="toggle-label">{enabled ? 'Включен' : 'Отключен'}</span>
				<button
					class="btn btn-sm {enabled ? 'btn-primary' : 'btn-ghost'}"
					onclick={() => enabled = !enabled}
				>
					{enabled ? 'Выключить' : 'Включить'}
				</button>
			</div>
		</div>

		{#if enabled}
			<div class="form-group">
				<label for="interval" class="label">Интервал (сек)</label>
				<input
					id="interval"
					type="number"
					class="input"
					bind:value={interval}
					min="10"
					max="3600"
				/>
			</div>
			<div class="form-group">
				<label for="threshold" class="label">Порог ошибок</label>
				<input
					id="threshold"
					type="number"
					class="input"
					bind:value={failThreshold}
					min="1"
					max="100"
				/>
			</div>
		{/if}

		{#if error}
			<div class="error">{error}</div>
		{/if}
	</div>

	{#snippet actions()}
		<button class="btn btn-ghost" onclick={onclose}>Отмена</button>
		<button class="btn btn-primary" onclick={handleSave} disabled={saving}>
			{saving ? 'Сохранение...' : 'Сохранить'}
		</button>
	{/snippet}
</Modal>

<style>
	.form {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}
	.form-group {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.label {
		font-size: 13px;
		font-weight: 500;
		color: var(--text-secondary);
	}
	.input {
		padding: 6px 10px;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: 6px;
		color: var(--text-primary);
		font-size: 14px;
	}
	.toggle-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
	}
	.toggle-label {
		font-size: 14px;
	}
	.error {
		color: var(--error);
		font-size: 13px;
	}
</style>