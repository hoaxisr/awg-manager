<!-- Вкладка «Общие»: уровень использования, внешний вид, действия с панелью. -->
<script lang="ts">
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { experimentalSettingsUnlocked } from '$lib/stores/experimentalSettingsUnlocked';
	import { usageLevel } from '$lib/stores/settings';
	import { isAppearanceSettingsVisible, usageLevelLabel, type UsageLevel } from '$lib/types/usageLevel';
	import ExperimentalSettingsCard from './ExperimentalSettingsCard.svelte';
	import LanguageCard from './LanguageCard.svelte';
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

	const showAppearance = $derived(isAppearanceSettingsVisible(editor.settings.usageLevel));

	function selectUsageLevel(level: UsageLevel) {
		return editor.save(
			() => api.updateSettings({ usageLevel: level }),
			m.settings_level_saved({ level: usageLevelLabel(level) }),
			m.settings_page_level_save_failed(),
		);
	}
</script>

<!-- Без «Внешнего вида» (уровень «Базовый») вторую колонку занимает язык. -->
<div class="settings-columns">
	<div class="settings-column">
		<UsageLevelCard
			value={editor.settings.usageLevel}
			saving={editor.saving}
			onSelect={selectUsageLevel}
			initialExpanded={expandLevel}
		/>
		{#if showAppearance}
			<LanguageCard />
		{/if}
		<ServiceActionsCard />
		{#if $usageLevel === 'expert' && $experimentalSettingsUnlocked}
			<ExperimentalSettingsCard />
		{/if}
	</div>
	<div class="settings-column">
		{#if showAppearance}
			<ThemeSchemeCard />
		{:else}
			<LanguageCard />
		{/if}
	</div>
</div>
