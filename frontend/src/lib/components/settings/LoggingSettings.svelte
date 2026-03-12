<script lang="ts">
	import { Toggle } from '$lib/components/ui';
	import type { Settings } from '$lib/types';

	interface Props {
		settings: Settings;
		saving: boolean;
		onToggle: () => void;
		onSave: () => void;
	}

	let {
		settings = $bindable(),
		saving,
		onToggle,
		onSave,
	}: Props = $props();

	let savedMaxAge = $derived(settings.logging.maxAge);
	let localMaxAge = $state(settings.logging.maxAge);
	let maxAgeChanged = $derived(localMaxAge !== savedMaxAge);

	$effect(() => {
		localMaxAge = savedMaxAge;
	});

	function handleSave() {
		settings.logging.maxAge = localMaxAge;
		onSave();
	}
</script>

<div class="setting-row">
	<div class="flex flex-col gap-1">
		<span class="font-medium">Логирование</span>
		<span class="setting-description">
			Запись событий приложения в память для отладки и аудита.
		</span>
	</div>
	<Toggle checked={settings.logging.enabled} onchange={() => onToggle()} disabled={saving} />
</div>

{#if settings.logging.enabled}
	<div style="padding: 0 0 0.875rem 0;">
		<div class="settings-panel">
			<label for="loggingMaxAge">Время хранения (часы)</label>
			<div class="inline-form">
				<input
					type="number"
					id="loggingMaxAge"
					bind:value={localMaxAge}
					min="1"
					max="24"
					disabled={saving}
				/>
				{#if maxAgeChanged}
					<button
						class="btn btn-primary btn-sm"
						onclick={handleSave}
						disabled={saving}
					>
						{saving ? 'Сохранение...' : 'Сохранить'}
					</button>
				{/if}
			</div>
			<p class="form-hint">Записи старше этого времени будут удалены автоматически</p>
		</div>
	</div>
{/if}

<style>
	.settings-panel label {
		display: block;
		font-size: 0.875rem;
		font-weight: 500;
		color: var(--text-secondary);
		margin-bottom: 0.5rem;
	}

	.inline-form {
		display: flex;
		align-items: center;
		gap: 0.75rem;
	}

	.inline-form input {
		width: 80px;
		padding: 0.5rem 0.75rem;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		color: var(--text-primary);
		font-size: 0.875rem;
	}

	.inline-form input:focus {
		outline: none;
		border-color: var(--accent);
	}
</style>
