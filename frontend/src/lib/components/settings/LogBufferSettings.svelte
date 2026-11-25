<!-- Строки «Размер буфера»: сколько записей журналов держать в памяти. -->
<script lang="ts">
	import { m } from '$lib/i18n';
	import type { Settings } from '$lib/types';
	import { usageLevel } from '$lib/stores/settings';

	interface Props {
		settings: Settings;
		saving: boolean;
		onSave: (logging: Partial<Settings['logging']>) => void | Promise<void>;
	}

	let { settings, saving, onSave }: Props = $props();

	const MIN_ENTRIES = 100;
	const MAX_ENTRIES = 100000;

	const isBasic = $derived($usageLevel === 'basic');

	// svelte-ignore state_referenced_locally
	let appMaxEntries = $state(settings.logging.appMaxEntries || 5000);
	// svelte-ignore state_referenced_locally
	let singboxMaxEntries = $state(settings.logging.singboxMaxEntries || 5000);

	function syncFromSettings() {
		appMaxEntries = settings.logging.appMaxEntries || 5000;
		singboxMaxEntries = settings.logging.singboxMaxEntries || 5000;
	}
	$effect(syncFromSettings);

	function clampEntries(n: number): number {
		if (!Number.isFinite(n)) return 5000;
		return Math.min(MAX_ENTRIES, Math.max(MIN_ENTRIES, Math.round(n)));
	}

	// Шлём только свои поля: уровни правит соседняя карточка, и снимок целиком
	// откатил бы её запись. Сохраняется по уходу фокуса — и по щелчку на другую вкладку тоже. После
	// отказа поля возвращаются к сохранённому, а не остаются на отвергнутом.
	async function handleCommit() {
		await onSave({
			appMaxEntries: clampEntries(appMaxEntries),
			singboxMaxEntries: clampEntries(singboxMaxEntries),
		});
		syncFromSettings();
	}
</script>

<div class="setting-row logging-buffer-row">
	<div class="flex flex-col gap-1">
		<span class="font-medium">{m.settings_logging_app_buffer_label()}</span>
		<span class="setting-description">{m.settings_logging_app_buffer_description()}</span>
	</div>
	<div class="settings-select num-input">
		<input
			type="number"
			bind:value={appMaxEntries}
			onblur={handleCommit}
			min={MIN_ENTRIES}
			max={MAX_ENTRIES}
			step="500"
			disabled={saving}
		/>
	</div>
</div>

{#if !isBasic}
	<div class="setting-row logging-buffer-row">
		<div class="flex flex-col gap-1">
			<span class="font-medium">{m.settings_logging_singbox_buffer_label()}</span>
			<span class="setting-description">{m.settings_logging_singbox_buffer_description()}</span>
		</div>
		<div class="settings-select num-input">
			<input
				type="number"
				bind:value={singboxMaxEntries}
				onblur={handleCommit}
				min={MIN_ENTRIES}
				max={MAX_ENTRIES}
				step="500"
				disabled={saving}
			/>
		</div>
	</div>
{/if}
