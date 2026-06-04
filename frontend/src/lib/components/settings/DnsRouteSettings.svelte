<script lang="ts">
	import { Toggle, Button } from '$lib/components/ui';
	import type { Settings } from '$lib/types';

	interface Props {
		settings: Settings;
		saving: boolean;
		onToggle: (enabled: boolean) => void;
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

<div class="setting-row dns-header-row">
	<div class="flex flex-col gap-1">
		<span class="font-medium">Автообновление подписок DNS</span>
		<span class="setting-description">
			Периодически обновлять списки доменов из подписок DNS-маршрутизации.
		</span>
	</div>
	<Toggle checked={settings.dnsRoute.autoRefreshEnabled} onchange={onToggle} disabled={saving} />
</div>

{#if settings.dnsRoute.autoRefreshEnabled}
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
				<div class="input-with-suffix">
					<input
						type="number"
						id="dnsRefreshInterval"
						bind:value={localInterval}
						min="1"
						max="48"
						disabled={saving}
					/>
					<span class="input-suffix">ч.</span>
				</div>
				{#if settingsChanged}
					<Button
						variant="primary"
						size="sm"
						onclick={handleSave}
						loading={saving}
					>
						{saving ? 'Сохранение...' : 'Сохранить'}
					</Button>
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
					<Button
						variant="primary"
						size="sm"
						onclick={handleSave}
						loading={saving}
					>
						{saving ? 'Сохранение...' : 'Сохранить'}
					</Button>
				{/if}
			</div>
			<p class="form-hint">Локальное время роутера</p>
		{/if}
	</div>
{/if}

<style>
	.settings-panel {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto;
		grid-template-areas:
			'label label'
			'modes form'
			'hint hint';
		align-items: center;
		gap: 0.55rem 0.75rem;
		min-width: 0;
		margin-top: 0.35rem;
		padding: 0.75rem 0.875rem;
		border: 1px solid color-mix(in srgb, var(--border) 70%, transparent);
		border-radius: var(--radius-sm);
		background: color-mix(in srgb, var(--bg-tertiary) 72%, transparent);
	}

	.form-label {
		grid-area: label;
		display: block;
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--text-secondary);
		margin: 0;
	}

	.mode-options {
		grid-area: modes;
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.45rem 0.9rem;
		min-width: 0;
		margin: 0;
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
		grid-area: form;
		display: flex;
		align-items: center;
		justify-content: flex-end;
		gap: 0.5rem;
		flex-wrap: nowrap;
		min-width: 0;
	}

	.form-hint {
		grid-area: hint;
		margin: 0;
		font-size: 0.75rem;
		line-height: 1.35;
		color: var(--text-secondary);
	}

	.input-with-suffix {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		min-width: 0;
	}

	.input-suffix {
		font-size: 0.8125rem;
		color: var(--text-secondary);
	}

	.inline-form input[type="number"] {
		width: 4.75rem;
		padding: 0.5rem 0.75rem;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		color: var(--text-primary);
		font-size: 0.875rem;
	}

	.inline-form input[type="time"] {
		width: 8rem;
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

	@media (max-width: 640px) {
		.dns-header-row {
			display: grid;
			grid-template-columns: minmax(0, 1fr) auto;
			align-items: center;
			gap: 0.75rem;
		}

		.settings-panel {
			grid-template-columns: minmax(0, 1fr);
			grid-template-areas:
				'label'
				'modes'
				'form'
				'hint';
			align-items: stretch;
			gap: 0.6rem;
			padding: 0.75rem;
		}

		.mode-options {
			display: grid;
			grid-template-columns: repeat(2, minmax(0, 1fr));
			gap: 0.5rem;
			align-items: stretch;
		}

		.mode-option {
			white-space: normal;
		}

		.inline-form {
			flex-direction: column;
			align-items: stretch;
			justify-content: stretch;
			gap: 0.5rem;
		}

		.input-with-suffix {
			display: grid;
			grid-template-columns: minmax(0, 1fr) auto;
			width: 100%;
		}

		.inline-form input[type="number"],
		.inline-form input[type="time"] {
			width: 100%;
			box-sizing: border-box;
		}

		.inline-form :global(.btn) {
			width: 100%;
		}

		.input-with-suffix .input-suffix {
			flex: 0 0 auto;
		}
	}
</style>
