<script lang="ts">
	import { Toggle, Dropdown, type DropdownOption } from '$lib/components/ui';
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

	const MIN_ENTRIES = 100;
	const MAX_ENTRIES = 100000;

	let localMaxAge = $state(settings.logging.maxAge);
	let localLogLevel = $state<'info' | 'full' | 'debug'>(
		(settings.logging.logLevel as 'info' | 'full' | 'debug') || 'info',
	);
	let localAppMaxEntries = $state(settings.logging.appMaxEntries || 5000);
	let localSingboxMaxEntries = $state(settings.logging.singboxMaxEntries || 5000);

	$effect(() => {
		localMaxAge = settings.logging.maxAge;
		localLogLevel = (settings.logging.logLevel as 'info' | 'full' | 'debug') || 'info';
		localAppMaxEntries = settings.logging.appMaxEntries || 5000;
		localSingboxMaxEntries = settings.logging.singboxMaxEntries || 5000;
	});

	function clampEntries(n: number): number {
		if (!Number.isFinite(n)) return 5000;
		return Math.min(MAX_ENTRIES, Math.max(MIN_ENTRIES, Math.round(n)));
	}

	function handleSave() {
		settings.logging.maxAge = localMaxAge;
		settings.logging.logLevel = localLogLevel;
		settings.logging.appMaxEntries = clampEntries(localAppMaxEntries);
		settings.logging.singboxMaxEntries = clampEntries(localSingboxMaxEntries);
		onSave();
	}

	const hoursOptions: DropdownOption[] = [
		{ value: '1', label: '1 ч' },
		{ value: '2', label: '2 ч' },
		{ value: '4', label: '4 ч' },
		{ value: '8', label: '8 ч' },
		{ value: '12', label: '12 ч' },
		{ value: '24', label: '24 ч' },
	];

	const levelOptions: DropdownOption<'info' | 'full' | 'debug'>[] = [
		{ value: 'info', label: 'INFO' },
		{ value: 'full', label: 'FULL' },
		{ value: 'debug', label: 'DEBUG' },
	];

	function handleHoursChange(v: string) {
		localMaxAge = Number(v);
		handleSave();
	}

	function handleLevelChange(v: 'info' | 'full' | 'debug') {
		localLogLevel = v;
		handleSave();
	}

	function handleAppCommit() {
		localAppMaxEntries = clampEntries(localAppMaxEntries);
		handleSave();
	}

	function handleSingboxCommit() {
		localSingboxMaxEntries = clampEntries(localSingboxMaxEntries);
		handleSave();
	}
</script>

<div id="logging" class="setting-row logging-main-row">
	<div class="flex flex-col gap-1">
		<span class="font-medium">Логирование</span>
		<span class="setting-description">
			Запись событий приложения в память для отладки и аудита.
		</span>
	</div>
	<div class="setting-controls">
		{#if settings.logging.enabled}
			<div class="hours-select">
				<Dropdown
					value={String(localMaxAge)}
					options={hoursOptions}
					onchange={handleHoursChange}
					disabled={saving}
					fullWidth
				/>
			</div>
		{/if}
		<Toggle checked={settings.logging.enabled} onchange={onToggle} disabled={saving} />
	</div>
</div>

{#if settings.logging.enabled}
	<div class="setting-row logging-level-row">
		<div class="flex flex-col gap-1">
			<span class="font-medium">Уровень логирования</span>
			<span class="setting-description">INFO — результаты операций. FULL — промежуточные шаги. DEBUG — полная информация.</span>
		</div>
		<div class="hours-select">
			<Dropdown
				value={localLogLevel}
				options={levelOptions}
				onchange={handleLevelChange}
				disabled={saving}
				fullWidth
			/>
		</div>
	</div>

	<div class="setting-row logging-buffer-row">
		<div class="flex flex-col gap-1">
			<span class="font-medium">Размер буфера приложения</span>
			<span class="setting-description">Сколько записей удерживать в журнале приложения (туннели, маршрутизация, серверы, система). По умолчанию 5000.</span>
		</div>
		<div class="num-input">
			<input
				type="number"
				bind:value={localAppMaxEntries}
				onblur={handleAppCommit}
				min={MIN_ENTRIES}
				max={MAX_ENTRIES}
				step="500"
				disabled={saving}
			/>
		</div>
	</div>

	<div class="setting-row logging-buffer-row">
		<div class="flex flex-col gap-1">
			<span class="font-medium">Размер буфера sing-box</span>
			<span class="setting-description">Sing-box форвардер шумный — отдельный буфер, чтобы не вытеснять записи приложения. По умолчанию 5000.</span>
		</div>
		<div class="num-input">
			<input
				type="number"
				bind:value={localSingboxMaxEntries}
				onblur={handleSingboxCommit}
				min={MIN_ENTRIES}
				max={MAX_ENTRIES}
				step="500"
				disabled={saving}
			/>
		</div>
	</div>
{/if}

<style>
	.setting-controls {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		flex-shrink: 0;
		flex-wrap: wrap;
		justify-content: flex-end;
	}

	.hours-select {
		width: 132px;
		min-width: 132px;
	}

	.num-input {
		width: 180px;
		min-width: 180px;
	}

	.num-input input {
		width: 100%;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		color: var(--color-text-primary);
		font: inherit;
		font-size: 13px;
		padding: 0.375rem 0.5rem;
		text-align: right;
		font-variant-numeric: tabular-nums;
	}
	.num-input input:focus {
		outline: none;
		border-color: var(--color-accent);
	}
	.num-input input:disabled {
		opacity: 0.6;
	}

	.logging-buffer-row {
		align-items: center;
	}

	.logging-level-row {
		align-items: center;
	}

	@media (max-width: 900px) {
		.logging-main-row {
			display: grid;
			grid-template-columns: minmax(0, 1fr) auto;
			align-items: center;
			gap: 0.75rem;
			flex-wrap: nowrap;
		}

		.setting-controls {
			flex-wrap: nowrap;
		}

		.logging-level-row,
		.logging-buffer-row {
			flex-direction: column;
			align-items: stretch;
			gap: 0.5rem;
		}

		.hours-select,
		.num-input {
			width: 100%;
			min-width: 0;
		}

		.num-input input {
			width: 100%;
			max-width: 100%;
			display: block;
		}
	}
</style>
