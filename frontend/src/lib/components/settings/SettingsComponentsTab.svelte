<!-- Вкладка «Компоненты»: то, что панель ставит и запускает на роутере. -->
<script lang="ts">
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { notifications } from '$lib/stores/notifications';
	import { setSettings as setGlobalSettings, usageLevel } from '$lib/stores/settings';
	import { isSectionVisible } from '$lib/types/usageLevel';
	import FircIntegrationSection from './FircIntegrationSection.svelte';
	import IntegrationsSection from './IntegrationsSection.svelte';
	import ObfuscatorRelayCard from './ObfuscatorRelayCard.svelte';
	import { backendErrorOr, type SettingsEditor } from './settingsEditor.svelte';

	interface Props {
		editor: SettingsEditor;
	}

	let { editor }: Props = $props();

	const showSingbox = $derived(isSectionVisible($usageLevel, 'singboxTunnels'));

	let savingBootstrapDNS = $state(false);

	// Bootstrap-DNS применяется бэкендом сразу: он переписывает адрес в
	// 00-base.json и перечитывает конфиг sing-box без перезапуска.
	async function saveBootstrapDNS(value: string) {
		savingBootstrapDNS = true;
		try {
			editor.settings = await api.updateSettings({ singboxBootstrapDNS: value });
			setGlobalSettings(editor.settings);
			notifications.success(value ? m.settings_page_bootstrap_set({ value }) : m.settings_page_bootstrap_cleared());
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_page_bootstrap_save_failed());
		} finally {
			savingBootstrapDNS = false;
		}
	}

	let savingClashPort = $state(false);
	let clashPortError = $state<string | null>(null);

	// Порт Clash API применяется бэкендом сразу: он переписывает
	// external_controller в 00-base.json, перечитывает конфиг sing-box и
	// переставляет собственного клиента. Отказ по занятости порта приходит
	// текстом ошибки и показывается прямо под полем.
	async function saveClashPort(value: number) {
		savingClashPort = true;
		clashPortError = null;
		try {
			editor.settings = await api.updateSettings({ singboxClashPort: value });
			setGlobalSettings(editor.settings);
			notifications.success(m.settings_page_clash_port_saved({ value }));
		} catch (e) {
			const msg = e instanceof Error ? e.message : m.settings_page_clash_port_save_failed();
			clashPortError = msg;
			notifications.error(msg);
		} finally {
			savingClashPort = false;
		}
	}

	function toggleObfuscatorRelay(process: boolean) {
		return editor.save(
			() => api.setObfuscatorRelay(process),
			process ? m.settings_page_phobos_userspace() : m.settings_page_phobos_kernel(),
			backendErrorOr(m.settings_page_save_error()),
		);
	}
</script>

<div class="settings-columns">
	<div class="settings-column">
		<IntegrationsSection
			{showSingbox}
			bootstrapDNS={editor.settings.singboxBootstrapDNS ?? ''}
			bootstrapSaving={savingBootstrapDNS}
			onsaveBootstrapDNS={saveBootstrapDNS}
			clashPort={editor.settings.singboxClashPort ?? 0}
			clashPortSaving={savingClashPort}
			{clashPortError}
			onsaveClashPort={saveClashPort}
		/>
	</div>
	<div class="settings-column">
		<FircIntegrationSection />
		{#if $usageLevel === 'expert'}
			<ObfuscatorRelayCard
				process={editor.settings.obfuscatorRelayProcess ?? false}
				tripped={editor.settings.obfuscatorKmodTripped ?? ''}
				saving={editor.saving}
				ontoggle={toggleObfuscatorRelay}
			/>
		{/if}
	</div>
</div>
