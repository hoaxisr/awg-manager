<script lang="ts">
	import { m } from '$lib/i18n';
	import { Toggle, Dropdown, type DropdownOption } from '$lib/components/ui';
	import type { Settings } from '$lib/types';

	interface Props {
		settings: Settings;
		saving: boolean;
		onToggle: (enabled: boolean) => void;
		onSave: (logging: Partial<Settings['logging']>) => void | Promise<void>;
	}

	let {
		settings,
		saving,
		onToggle,
		onSave,
	}: Props = $props();

	type AwgmLogLevel = 'info' | 'full' | 'debug';
	type SingboxLogLevel = 'trace' | 'debug' | 'info' | 'warn' | 'error' | 'fatal' | 'panic';

	// Поля формы — локальная копия сохранённых значений: начальное значение
	// берётся один раз, дальше их держит в синхроне syncFromSettings().
	// svelte-ignore state_referenced_locally
	let localMaxAge = $state(settings.logging.maxAge);
	// svelte-ignore state_referenced_locally
	let localLogLevel = $state<AwgmLogLevel>(
		(settings.logging.logLevel as AwgmLogLevel) || 'info',
	);
	// svelte-ignore state_referenced_locally
	let localSingboxLogLevel = $state<SingboxLogLevel>(
		(settings.logging.singboxLogLevel as SingboxLogLevel) || 'trace',
	);

	function syncFromSettings() {
		localMaxAge = settings.logging.maxAge;
		localLogLevel = (settings.logging.logLevel as AwgmLogLevel) || 'info';
		localSingboxLogLevel = (settings.logging.singboxLogLevel as SingboxLogLevel) || 'trace';
	}
	$effect(syncFromSettings);

	// `settings` is not touched until the page saves it: after a failed save the
	// controls fall back to the last saved values instead of the rejected ones.
	async function handleSave() {
		await onSave({
			maxAge: localMaxAge,
			logLevel: localLogLevel,
			singboxLogLevel: localSingboxLogLevel,
		});
		syncFromSettings();
	}

	const hoursOptions: DropdownOption[] = $derived([
		{ value: '1', label: m.settings_logging_hours_option({ hours: 1 }) },
		{ value: '2', label: m.settings_logging_hours_option({ hours: 2 }) },
		{ value: '4', label: m.settings_logging_hours_option({ hours: 4 }) },
		{ value: '8', label: m.settings_logging_hours_option({ hours: 8 }) },
		{ value: '12', label: m.settings_logging_hours_option({ hours: 12 }) },
		{ value: '24', label: m.settings_logging_hours_option({ hours: 24 }) },
	]);

	const levelOptions: DropdownOption<AwgmLogLevel>[] = [
		{ value: 'info', label: 'INFO' },
		{ value: 'full', label: 'FULL' },
		{ value: 'debug', label: 'DEBUG' },
	];
	const singboxLevelOptions: DropdownOption<SingboxLogLevel>[] = [
		{ value: 'trace', label: 'TRACE' },
		{ value: 'debug', label: 'DEBUG' },
		{ value: 'info', label: 'INFO' },
		{ value: 'warn', label: 'WARN' },
		{ value: 'error', label: 'ERROR' },
	];

	function handleHoursChange(v: string) {
		localMaxAge = Number(v);
		handleSave();
	}

	function handleLevelChange(v: AwgmLogLevel) {
		localLogLevel = v;
		handleSave();
	}
	function handleSingboxLevelChange(v: SingboxLogLevel) {
		localSingboxLogLevel = v;
		handleSave();
	}
</script>

<div id="logging" class="setting-row toggle-inline-row">
	<div class="flex flex-col gap-1">
		<span class="font-medium">{m.settings_logging_label()}</span>
		<span class="setting-description">
			{m.settings_logging_description()}
		</span>
	</div>
	<Toggle checked={settings.logging.enabled} controlled onchange={onToggle} disabled={saving} />
</div>

{#if settings.logging.enabled}
	<div class="setting-row">
		<div class="flex flex-col gap-1">
			<span class="font-medium">{m.settings_logging_retention_label()}</span>
			<span class="setting-description">{m.settings_logging_retention_description()}</span>
		</div>
		<div class="settings-select">
			<Dropdown
				value={String(localMaxAge)}
				options={hoursOptions}
				onchange={handleHoursChange}
				disabled={saving}
				fullWidth
			/>
		</div>
	</div>
	<div class="setting-row">
		<div class="flex flex-col gap-1">
			<span class="font-medium">{m.settings_logging_awgm_level_label()}</span>
			<span class="setting-description">{m.settings_logging_awgm_level_description()}</span>
		</div>
		<div class="settings-select">
			<Dropdown
				value={localLogLevel}
				options={levelOptions}
				onchange={handleLevelChange}
				disabled={saving}
				fullWidth
			/>
		</div>
	</div>
	<div class="setting-row">
		<div class="flex flex-col gap-1">
			<span class="font-medium">{m.settings_logging_singbox_level_label()}</span>
			<span class="setting-description">{m.settings_logging_singbox_level_description()}</span>
		</div>
		<div class="settings-select">
			<Dropdown
				value={localSingboxLogLevel}
				options={singboxLevelOptions}
				onchange={handleSingboxLevelChange}
				disabled={saving}
				fullWidth
			/>
		</div>
	</div>
{/if}
