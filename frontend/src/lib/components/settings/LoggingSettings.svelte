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

	let localMaxAge = $state(settings.logging.maxAge);
	let localLogLevel = $state(settings.logging.logLevel || 'info');

	$effect(() => {
		localMaxAge = settings.logging.maxAge;
		localLogLevel = settings.logging.logLevel || 'info';
	});

	function handleSave() {
		settings.logging.maxAge = localMaxAge;
		settings.logging.logLevel = localLogLevel;
		onSave();
	}

	function handleLogLevelChange(e: Event) {
		localLogLevel = (e.currentTarget as HTMLSelectElement).value;
		handleSave();
	}
</script>

<div class="setting-row">
	<div class="flex flex-col gap-1">
		<span class="font-medium">Логирование</span>
		<span class="setting-description">
			Запись событий приложения в память для отладки и аудита.
		</span>
	</div>
	<div class="setting-controls">
		{#if settings.logging.enabled}
			<select
				class="hours-select"
				value={localMaxAge}
				onchange={(e) => { localMaxAge = Number(e.currentTarget.value); handleSave(); }}
				disabled={saving}
			>
				<option value={1}>1 ч</option>
				<option value={2}>2 ч</option>
				<option value={4}>4 ч</option>
				<option value={8}>8 ч</option>
				<option value={12}>12 ч</option>
				<option value={24}>24 ч</option>
			</select>
		{/if}
		<Toggle checked={settings.logging.enabled} onchange={() => onToggle()} disabled={saving} />
	</div>
</div>

{#if settings.logging.enabled}
	<div class="setting-row">
		<div class="flex flex-col gap-1">
			<span class="font-medium">Уровень логирования</span>
			<span class="setting-description">INFO — результаты операций. FULL — промежуточные шаги. DEBUG — полная информация.</span>
		</div>
		<select class="hours-select" value={localLogLevel} onchange={handleLogLevelChange} disabled={saving}>
			<option value="info">INFO</option>
			<option value="full">FULL</option>
			<option value="debug">DEBUG</option>
		</select>
	</div>
{/if}

<style>
	.setting-controls {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		flex-shrink: 0;
	}

	.hours-select {
		padding: 0.25rem 0.5rem;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		color: var(--text-primary);
		font-size: 0.8125rem;
		cursor: pointer;
	}

	.hours-select:focus {
		outline: none;
		border-color: var(--accent);
	}
</style>
