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

	let localMode = $state(settings.dnsRoute.refreshMode || 'interval');
	let localInterval = $state(settings.dnsRoute.refreshIntervalHours || 6);
	let localDailyTime = $state(settings.dnsRoute.refreshDailyTime || '03:00');

	let savedMode = $derived(settings.dnsRoute.refreshMode || 'interval');
	let savedInterval = $derived(settings.dnsRoute.refreshIntervalHours);
	let savedDailyTime = $derived(settings.dnsRoute.refreshDailyTime || '03:00');

	let settingsChanged = $derived(
		localMode !== savedMode ||
		(localMode === 'interval' && localInterval !== savedInterval) ||
		(localMode === 'daily' && localDailyTime !== savedDailyTime)
	);

	$effect(() => {
		if (savedInterval > 0) {
			localInterval = savedInterval;
		}
	});

	$effect(() => {
		localMode = savedMode;
	});

	$effect(() => {
		localDailyTime = savedDailyTime;
	});

	function handleSave() {
		settings.dnsRoute.refreshMode = localMode;
		if (localMode === 'interval') {
			settings.dnsRoute.refreshIntervalHours = localInterval;
		} else {
			settings.dnsRoute.refreshDailyTime = localDailyTime;
		}
		onSave();
	}
</script>

<div class="setting-row">
	<div class="flex flex-col gap-1">
		<span class="font-medium">Автообновление подписок DNS</span>
		<span class="setting-description">
			Периодически обновлять списки доменов из подписок DNS-маршрутизации
		</span>
	</div>
	<Toggle checked={settings.dnsRoute.autoRefreshEnabled} onchange={() => onToggle()} disabled={saving} />
</div>

{#if settings.dnsRoute.autoRefreshEnabled}
	<div style="padding: 0 0 0.875rem 0;">
		<div class="settings-panel">
			<!-- svelte-ignore a11y_label_has_associated_control -->
			<label class="form-label">Режим обновления:</label>
			<div class="mode-options">
				<label class="mode-option">
					<input type="radio" value="interval" bind:group={localMode} disabled={saving} />
					<span>каждые N часов</span>
				</label>
				<label class="mode-option">
					<input type="radio" value="daily" bind:group={localMode} disabled={saving} />
					<span>ежедневно</span>
				</label>
			</div>

			{#if localMode === 'interval'}
				<div class="inline-form">
					<input
						type="number"
						id="dnsRefreshInterval"
						bind:value={localInterval}
						min="1"
						max="48"
						disabled={saving}
					/>
					<span class="input-suffix">ч.</span>
					{#if settingsChanged}
						<button
							class="btn btn-primary btn-sm"
							onclick={handleSave}
							disabled={saving}
						>
							{saving ? 'Сохранение...' : 'Сохранить'}
						</button>
					{/if}
				</div>
				<p class="form-hint">Рекомендуется от 6 до 24 часов</p>
			{/if}

			{#if localMode === 'daily'}
				<div class="inline-form">
					<input
						type="time"
						id="dnsRefreshTime"
						bind:value={localDailyTime}
						disabled={saving}
					/>
					{#if settingsChanged}
						<button
							class="btn btn-primary btn-sm"
							onclick={handleSave}
							disabled={saving}
						>
							{saving ? 'Сохранение...' : 'Сохранить'}
						</button>
					{/if}
				</div>
				<p class="form-hint">Локальное время роутера</p>
			{/if}
		</div>
	</div>
{/if}

<style>
	.form-label {
		display: block;
		font-size: 0.8125rem;
		font-weight: 500;
		color: var(--text-secondary);
		margin-bottom: 0.375rem;
	}

	.mode-options {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem 1rem;
		margin-bottom: 0.75rem;
	}

	.mode-option {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		font-size: 0.8125rem;
		color: var(--text-primary);
		cursor: pointer;
		white-space: nowrap;
	}

	.mode-option input[type="radio"] {
		accent-color: var(--accent);
	}

	.inline-form {
		display: flex;
		align-items: center;
		gap: 0.75rem;
	}

	.input-suffix {
		font-size: 0.8125rem;
		color: var(--text-secondary);
		margin-left: -0.5rem;
	}

	.inline-form input[type="number"] {
		width: 80px;
		padding: 0.5rem 0.75rem;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		color: var(--text-primary);
		font-size: 0.875rem;
	}

	.inline-form input[type="time"] {
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

	@media (max-width: 480px) {
		.mode-options {
			flex-direction: column;
			gap: 0.5rem;
			align-items: flex-start;
		}
	}
</style>
