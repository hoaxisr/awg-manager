<!-- Вкладка «Общие»: уровень использования, внешний вид, действия с панелью. -->
<script lang="ts">
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { experimentalSettingsUnlocked } from '$lib/stores/experimentalSettingsUnlocked';
	import { usageLevel } from '$lib/stores/settings';
	import { isAppearanceSettingsVisible, usageLevelLabel, type UsageLevel } from '$lib/types/usageLevel';
	import ExperimentalSettingsCard from './ExperimentalSettingsCard.svelte';
	import ServiceActionsCard from './ServiceActionsCard.svelte';
	import ThemeSchemeCard from './ThemeSchemeCard.svelte';
	import UsageLevelCard from './UsageLevelCard.svelte';
	import type { SettingsEditor } from './settingsEditor.svelte';

	interface Props {
		editor: SettingsEditor;
		/** Глубокая ссылка `?mode` — сразу раскрыть выбор уровня. */
		expandLevel?: boolean;
	}

	let { editor, expandLevel = false }: Props = $props();

	function selectUsageLevel(level: UsageLevel) {
		return editor.save(
			() => api.updateSettings({ usageLevel: level }),
			m.settings_level_saved({ level: usageLevelLabel(level) }),
			m.settings_page_level_save_failed(),
		);
	}
</script>

<div class="settings-columns">
	<div class="settings-column">
		<UsageLevelCard
			value={editor.settings.usageLevel}
			saving={editor.saving}
			onSelect={selectUsageLevel}
			initialExpanded={expandLevel}
		/>
		<ServiceActionsCard />
		{#if $usageLevel === 'expert' && $experimentalSettingsUnlocked}
			<ExperimentalSettingsCard />
		{/if}
	</div>
	{#if isAppearanceSettingsVisible(editor.settings.usageLevel)}
		<div class="settings-column">
			<ThemeSchemeCard />
		</div>
	{/if}
</div>
