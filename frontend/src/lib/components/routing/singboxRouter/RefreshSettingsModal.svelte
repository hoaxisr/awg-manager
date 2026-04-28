<script lang="ts">
	import { api } from '$lib/api/client';
	import Modal from '$lib/components/ui/Modal.svelte';
	import type { SingboxRouterSettings } from '$lib/types';

	interface Props {
		settings: SingboxRouterSettings;
		onClose: () => void;
		onSaved: () => Promise<void> | void;
	}
	let { settings, onClose, onSaved }: Props = $props();

	// svelte-ignore state_referenced_locally
	let refreshMode: 'interval' | 'daily' = $state((settings.refreshMode ?? 'interval') as 'interval' | 'daily');
	// svelte-ignore state_referenced_locally
	let refreshIntervalHours = $state(settings.refreshIntervalHours ?? 24);
	// svelte-ignore state_referenced_locally
	let refreshDailyTime = $state(settings.refreshDailyTime ?? '03:00');
	let busy = $state(false);
	let error = $state('');

	async function save(): Promise<void> {
		busy = true;
		error = '';
		try {
			await api.singboxRouterPutSettings({
				...settings,
				refreshMode,
				refreshIntervalHours,
				refreshDailyTime,
			});
			await onSaved();
			onClose();
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = false;
		}
	}
</script>

<Modal open onclose={onClose} title="Настройки автообновления">
	<div class="form">
		<label class="field">
			<div class="label">Режим</div>
			<select bind:value={refreshMode}>
				<option value="interval">Каждые N часов</option>
				<option value="daily">Ежедневно в заданное время</option>
			</select>
		</label>

		{#if refreshMode === 'interval'}
			<label class="field">
				<div class="label">Интервал (часов)</div>
				<input type="number" min="1" max="168" bind:value={refreshIntervalHours} />
			</label>
		{:else}
			<label class="field">
				<div class="label">Время (HH:MM)</div>
				<input type="time" bind:value={refreshDailyTime} />
			</label>
		{/if}

		{#if error}<div class="error">{error}</div>{/if}

		<div class="actions">
			<button class="btn btn-secondary" onclick={onClose}>Отмена</button>
			<button class="btn btn-primary" onclick={save} disabled={busy}>Сохранить</button>
		</div>
	</div>
</Modal>

<style>
	.form {
		display: grid;
		gap: 0.75rem;
		min-width: 320px;
	}
	.field {
		display: grid;
		gap: 0.25rem;
	}
	.label {
		font-size: 0.75rem;
		color: var(--muted-text);
	}
	.field select,
	.field input {
		background: var(--bg);
		border: 1px solid var(--border);
		padding: 0.45rem 0.6rem;
		border-radius: 4px;
		color: var(--text);
	}
	.error {
		color: var(--danger, #dc2626);
		font-size: 0.85rem;
	}
	.actions {
		display: flex;
		justify-content: flex-end;
		gap: 0.5rem;
	}
</style>
