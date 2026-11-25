<!-- Вкладка «Обновления»: сама панель, служебные загрузки, списки DNS-маршрутизации. -->
<script lang="ts">
	import { onMount } from 'svelte';
	import { get } from 'svelte/store';
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { SegmentedControl, Toggle } from '$lib/components/ui';
	import {
		downloadOutbounds,
		downloadOutboundsError,
		downloadOutboundsLoading,
		ensureDownloadOutboundsLoaded,
	} from '$lib/stores/downloadRoute';
	import { notifications } from '$lib/stores/notifications';
	import { setSettings as setGlobalSettings, usageLevel } from '$lib/stores/settings';
	import { systemInfo } from '$lib/stores/system';
	import type { DownloadOutbound, Settings, UpdateInfo } from '$lib/types';
	import {
		areDownloadRouteDetailsVisible,
		isRoutingSubTabVisible,
		isUpdateChannelSwitchVisible,
	} from '$lib/types/usageLevel';
	import { hasDevelopChannelQuizPassed } from '$lib/utils/developChannelGate';
	import { downloadErrorToText } from '$lib/utils/downloadError';
	import { CircleArrowDown, CloudDownload } from 'lucide-svelte';
	import DevelopChannelGateModal from './DevelopChannelGateModal.svelte';
	import DnsRouteSettings from './DnsRouteSettings.svelte';
	import DownloadSettings from './DownloadSettings.svelte';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';
	import UpdateSection from './UpdateSection.svelte';
	import { pageState } from './pageState.svelte';
	import type { SettingsEditor } from './settingsEditor.svelte';

	interface Props {
		editor: SettingsEditor;
	}

	let { editor }: Props = $props();

	const update = pageState<{ info: UpdateInfo | null }>('update-info', () => ({ info: null }));
	const showDownloadRoute = $derived(areDownloadRouteDetailsVisible($usageLevel));
	const showDnsRoute = $derived(Boolean($systemInfo.data?.isOS5) && isRoutingSubTabVisible($usageLevel, 'dnsRoutes'));
	let developGateOpen = $state(false);

	onMount(() => {
		if (update.info) return;
		// Некритично: при сбое строка статуса просто останется пустой.
		api.checkUpdate().then(
			(info) => (update.info = info),
			() => {},
		);
	});

	$effect(() => {
		if (showDownloadRoute) void ensureDownloadOutboundsLoaded();
	});

	function toggleUpdateCheck(enabled: boolean) {
		return editor.save(
			() => api.updateSettings({ updates: { checkEnabled: enabled } }),
			enabled ? m.settings_page_update_check_enabled() : m.settings_page_update_check_disabled(),
			m.settings_page_save_error(),
		);
	}

	function requestChannel(channel: 'stable' | 'develop') {
		if (editor.settings.updates.channel === channel) return;
		if (channel === 'develop' && !hasDevelopChannelQuizPassed()) {
			developGateOpen = true;
			return;
		}
		void selectChannel(channel);
	}

	async function selectChannel(channel: 'stable' | 'develop') {
		if (editor.settings.updates.channel === channel) return;
		editor.saving = true;
		try {
			editor.settings = await api.updateSettings({ updates: { channel } });
			setGlobalSettings(editor.settings);
			// Кэш проверки относится к прежнему каналу — перепроверяем.
			update.info = await api.checkUpdate(true);
			notifications.success(
				channel === 'develop' ? m.settings_page_channel_develop() : m.settings_page_channel_stable(),
			);
		} catch (e) {
			notifications.error(m.settings_page_channel_failed({ error: downloadErrorToText(e) }));
		} finally {
			editor.saving = false;
		}
	}

	async function confirmDevelopChannel() {
		developGateOpen = false;
		await selectChannel('develop');
	}

	async function refreshDownloadOutbounds() {
		await ensureDownloadOutboundsLoaded(true);
		const err = get(downloadOutboundsError);
		if (err) {
			notifications.error(m.settings_page_download_routes_error({ error: downloadErrorToText(err) }));
			return;
		}
		const tunnels = get(downloadOutbounds).filter((ob) => ob.tag !== 'direct');
		const available = tunnels.filter((ob) => ob.available).length;
		notifications.success(
			tunnels.length > 0
				? m.settings_page_routes_updated_found({
						tunnels: m.settings_page_tunnels_count({ count: tunnels.length }),
						available: m.settings_page_available_count({ count: available }),
					})
				: m.settings_page_routes_updated_none(),
		);
	}

	function selectDownloadRoute(routeTag: string, routeKind?: DownloadOutbound['kind']) {
		const tag = routeTag.trim() || 'direct';
		return editor.save(
			() => api.updateSettings({ download: { routeTag: tag, routeKind: tag === 'direct' ? 'direct' : routeKind } }),
			m.settings_page_download_route_saved(),
			m.settings_page_download_route_save_failed(),
		);
	}

	function toggleDnsAutoRefresh(enabled: boolean) {
		const current = editor.settings.dnsRoute;
		return editor.save(
			() =>
				api.updateSettings({
					dnsRoute: {
						autoRefreshEnabled: enabled,
						// Дефолты дописываем, только если их нет: остальное блока не трогаем.
						...(enabled && current.refreshIntervalHours === 0 ? { refreshIntervalHours: 6 } : {}),
						...(current.refreshMode ? {} : { refreshMode: 'interval' as const }),
					},
				}),
			enabled ? m.settings_page_dns_auto_enabled() : m.settings_page_dns_auto_disabled(),
			m.settings_page_save_error(),
		);
	}

	function saveDnsRouteSettings(dnsRoute: Settings['dnsRoute']) {
		return editor.save(
			() => api.updateSettings({ dnsRoute }),
			m.settings_page_dns_auto_saved(),
			m.settings_page_save_error(),
		);
	}
</script>

<div class="settings-columns">
	<div class="settings-column">
		<div id="awgm-update" class="settings-block">
			<div class="card">
				<SettingsSectionLabel label={m.settings_page_update_section_title()} icon={CircleArrowDown} tone="green" header />
				<UpdateSection bind:updateInfo={update.info} bind:settings={editor.settings} />
			</div>
		</div>
	</div>
	<div class="settings-column">
		<div class="settings-block">
			<div class="card">
				<SettingsSectionLabel label={m.settings_page_downloads_title()} icon={CloudDownload} tone="orange" header />
				<div class="setting-row toggle-inline-row">
					<div class="flex flex-col gap-1">
						<span class="font-medium">{m.settings_page_update_check_label()}</span>
						<span class="setting-description">{m.settings_page_update_check_description()}</span>
					</div>
					<Toggle
						checked={editor.settings.updates.checkEnabled}
						controlled
						onchange={toggleUpdateCheck}
						disabled={editor.saving}
					/>
				</div>
				{#if showDnsRoute}
					<DnsRouteSettings
						settings={editor.settings}
						saving={editor.saving}
						onToggle={toggleDnsAutoRefresh}
						onSave={saveDnsRouteSettings}
					/>
				{/if}
				{#if isUpdateChannelSwitchVisible(editor.settings.usageLevel)}
					<div class="setting-row">
						<div class="flex flex-col gap-1">
							<span class="font-medium">{m.settings_page_channel_label()}</span>
							<span class="setting-description">{m.settings_page_channel_description()}</span>
						</div>
						<SegmentedControl
							value={editor.settings.updates.channel}
							options={[
								{ value: 'stable', label: m.settings_page_channel_option_stable() },
								{ value: 'develop', label: m.settings_page_channel_option_develop() },
							] satisfies Array<{ value: 'stable' | 'develop'; label: string }>}
							ariaLabel={m.settings_page_channel_label()}
							disabled={editor.saving}
							onchange={requestChannel}
						/>
					</div>
				{/if}
				{#if showDownloadRoute}
					<DownloadSettings
						settings={editor.settings}
						saving={editor.saving}
						outbounds={$downloadOutbounds}
						loading={$downloadOutboundsLoading}
						error={$downloadOutboundsError}
						onRefresh={refreshDownloadOutbounds}
						onSelectRoute={selectDownloadRoute}
					/>
				{/if}
			</div>
		</div>
	</div>
</div>

<DevelopChannelGateModal
	open={developGateOpen}
	busy={editor.saving}
	onclose={() => (developGateOpen = false)}
	onpassed={confirmDevelopChannel}
/>
