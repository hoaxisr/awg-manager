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

	let localMaxAge = $state(settings.logging.maxAge);
	let localLogLevel = $state<'info' | 'full' | 'debug'>(
		(settings.logging.logLevel as 'info' | 'full' | 'debug') || 'info',
	);

	$effect(() => {
		localMaxAge = settings.logging.maxAge;
		localLogLevel = (settings.logging.logLevel as 'info' | 'full' | 'debug') || 'info';
	});

	function handleSave() {
		settings.logging.maxAge = localMaxAge;
		settings.logging.logLevel = localLogLevel;
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
	<div class="setting-row">
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
{/if}

<style>
	.setting-controls {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		flex-shrink: 0;
	}

	.hours-select {
		min-width: 110px;
	}
</style>
