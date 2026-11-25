<!-- Вкладка «Журнал и проверки»: логирование и цели проверки пинга. -->
<script lang="ts">
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { usageLevel } from '$lib/stores/settings';
	import type { Settings } from '$lib/types';
	import { ScrollText } from 'lucide-svelte';
	import LoggingSettings from './LoggingSettings.svelte';
	import PingTargetsCard from './PingTargetsCard.svelte';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';
	import { backendErrorOr, type SettingsEditor } from './settingsEditor.svelte';

	interface Props {
		editor: SettingsEditor;
	}

	let { editor }: Props = $props();

	function toggleLogging(enabled: boolean) {
		return editor.save(
			() => api.updateSettings({ logging: { enabled } }),
			enabled ? m.settings_page_logging_enabled() : m.settings_page_logging_disabled(),
			m.settings_page_save_error(),
		);
	}

	function saveLogging(logging: Settings['logging']) {
		return editor.save(
			() => api.updateSettings({ logging }),
			m.settings_page_logging_saved(),
			m.settings_page_save_error(),
		);
	}

	function savePingTargets(target: string, connectivityCheckUrl: string) {
		return editor.save(
			() => api.updateSettings({ pingCheck: { defaults: { target } }, connectivityCheckUrl }),
			m.settings_page_ping_targets_saved(),
			backendErrorOr(m.settings_page_ping_targets_save_failed()),
		);
	}
</script>

<div class="settings-columns">
	<div class="settings-column">
		<div class="settings-block">
			<div class="card">
				<SettingsSectionLabel label={m.settings_page_logging_title()} icon={ScrollText} tone="slate" header />
				<LoggingSettings
					settings={editor.settings}
					saving={editor.saving}
					onToggle={toggleLogging}
					onSave={saveLogging}
				/>
			</div>
		</div>
	</div>
	{#if $usageLevel === 'expert'}
		<div class="settings-column">
			<PingTargetsCard
				target={editor.settings.pingCheck.defaults.target}
				url={editor.settings.connectivityCheckUrl}
				saving={editor.saving}
				onsave={savePingTargets}
			/>
		</div>
	{/if}
</div>
