<!--
  Карточка «Загрузки и обновления»: автопроверка обновлений, канал обновлений
  (со шлюзом develop-квиза), автообновление DNS-подписок (OS5) и маршрут,
  через который качаются файлы.
-->
<script lang="ts">
	import { get } from 'svelte/store';
	import { CloudDownload } from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Toggle, SegmentedControl } from '$lib/components/ui';
	import { setSettings as setGlobalSettings } from '$lib/stores/settings';
	import {
		downloadOutbounds,
		downloadOutboundsLoading,
		downloadOutboundsError,
		ensureDownloadOutboundsLoaded,
	} from '$lib/stores/downloadRoute';
	import { downloadErrorToText } from '$lib/utils/downloadError';
	import { hasDevelopChannelQuizPassed } from '$lib/utils/developChannelGate';
	import { pluralize, AVAILABLE_WORDS, TUNNEL_WORDS } from '$lib/utils/pluralize';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';
	import DnsRouteSettings from './DnsRouteSettings.svelte';
	import DownloadSettings from './DownloadSettings.svelte';
	import DevelopChannelGateModal from './DevelopChannelGateModal.svelte';
	import type { Settings, UpdateInfo } from '$lib/types';

	interface Props {
		settings: Settings;
		saving: boolean;
		updateInfo: UpdateInfo | null;
		isOS5: boolean;
		highlightDownloads: boolean;
	}

	let {
		settings = $bindable(),
		saving = $bindable(),
		updateInfo = $bindable(),
		isOS5,
		highlightDownloads,
	}: Props = $props();

	let developGateOpen = $state(false);

	$effect(() => {
		void ensureDownloadOutboundsLoaded();
	});

	async function toggleUpdateCheck(enabled: boolean) {
		saving = true;
		try {
			settings = await api.updateSettings({
				...settings,
				updates: { ...settings.updates, checkEnabled: enabled },
			});
			setGlobalSettings(settings);
			notifications.success(enabled ? "Автопроверка обновлений включена" : "Автопроверка обновлений отключена");
		} catch {
			notifications.error("Ошибка сохранения настроек");
		} finally {
			saving = false;
		}
	}

	async function toggleDnsAutoRefresh(enabled: boolean) {
		saving = true;
		try {
			settings = await api.updateSettings({
				...settings,
				dnsRoute: {
					...settings.dnsRoute,
					autoRefreshEnabled: enabled,
					refreshIntervalHours:
						enabled && settings.dnsRoute.refreshIntervalHours === 0
							? 6
							: settings.dnsRoute.refreshIntervalHours,
					refreshMode: settings.dnsRoute.refreshMode || "interval",
				},
			});
			setGlobalSettings(settings);
			notifications.success(enabled ? "Автообновление подписок включено" : "Автообновление подписок отключено");
		} catch {
			notifications.error("Ошибка сохранения настроек");
		} finally {
			saving = false;
		}
	}

	async function saveDnsRouteSettings() {
		saving = true;
		try {
			settings = await api.updateSettings(settings);
			setGlobalSettings(settings);
			notifications.success("Настройки автообновления сохранены");
		} catch {
			notifications.error("Ошибка сохранения настроек");
		} finally {
			saving = false;
		}
	}

	function requestChannel(channel: 'stable' | 'develop') {
		if (settings.updates.channel === channel) return;
		if (channel === 'develop' && !hasDevelopChannelQuizPassed()) {
			developGateOpen = true;
			return;
		}
		void selectChannel(channel);
	}

	async function selectChannel(channel: 'stable' | 'develop') {
		if (settings.updates.channel === channel) return;
		saving = true;
		try {
			settings = await api.updateSettings({
				...settings,
				updates: { ...settings.updates, channel },
			});
			setGlobalSettings(settings);
			// Кэш проверки относится к прежнему каналу — перепроверяем.
			updateInfo = await api.checkUpdate(true);
			notifications.success(
				channel === 'develop'
					? 'Канал обновлений: develop (нестабильный)'
					: 'Канал обновлений изменён на стабильный',
			);
		} catch (e) {
			notifications.error(`Смена канала обновлений: ${downloadErrorToText(e)}`);
		} finally {
			saving = false;
		}
	}

	async function confirmDevelopChannel() {
		developGateOpen = false;
		await selectChannel('develop');
	}

	async function refreshDownloadOutbounds(showNotification = true) {
		await ensureDownloadOutboundsLoaded(true);
		if (!showNotification) return;
		const err = get(downloadOutboundsError);
		if (err) {
			notifications.error(`Маршруты загрузок: ${downloadErrorToText(err)}`);
			return;
		}
		const list = get(downloadOutbounds);
		const tunnelCount = list.filter((ob) => ob.tag !== 'direct').length;
		const availableTunnelCount = list.filter((ob) => ob.tag !== 'direct' && ob.available).length;
		notifications.success(
			tunnelCount > 0
				? `Маршруты обновлены: найдено ${pluralize(tunnelCount, TUNNEL_WORDS)} (${pluralize(availableTunnelCount, AVAILABLE_WORDS)})`
				: 'Маршруты обновлены: туннели не найдены (доступен только Direct)'
		);
	}

	async function selectDownloadRoute(routeTag: string, routeKind?: 'direct' | 'awg' | 'singbox' | 'subscription') {
		saving = true;
		try {
			const normalizedTag = routeTag.trim() || 'direct';
			const normalizedKind = normalizedTag === 'direct' ? 'direct' : routeKind;
			settings = await api.updateSettings({
				download: { routeTag: normalizedTag, routeKind: normalizedKind },
			});
			setGlobalSettings(settings);
			notifications.success('Маршрут загрузок сохранён');
		} catch {
			notifications.error('Не удалось сохранить маршрут загрузок');
		} finally {
			saving = false;
		}
	}
</script>

<div class="settings-block">
	<div class="card">
	<SettingsSectionLabel label="Загрузки и обновления" icon={CloudDownload} tone="orange" header />
	<div class="setting-row toggle-inline-row">
		<div class="flex flex-col gap-1">
			<span class="font-medium">Автопроверка обновлений</span>
			<span class="setting-description">Проверять наличие новых версий раз в сутки.</span>
		</div>
		<Toggle
			checked={settings.updates.checkEnabled}
			onchange={toggleUpdateCheck}
			disabled={saving}
		/>
	</div>
	{#if isOS5}
		<DnsRouteSettings
			bind:settings
			{saving}
			onToggle={toggleDnsAutoRefresh}
			onSave={saveDnsRouteSettings}
		/>
	{/if}
	<div class="setting-row">
		<div class="flex flex-col gap-1">
			<span class="font-medium">Канал обновлений</span>
			<span class="setting-description">
				Ветка develop — свежие, потенциально нестабильные сборки из ветки разработки.
			</span>
		</div>
		<SegmentedControl
			value={settings.updates.channel}
			options={[
				{ value: 'stable', label: 'Стабильный' },
				{ value: 'develop', label: 'Разработка' },
			] satisfies Array<{ value: 'stable' | 'develop'; label: string }>}
			ariaLabel="Канал обновлений"
			disabled={saving}
			onchange={(channel) => requestChannel(channel)}
		/>
	</div>
	<div class="settings-highlight-target" class:highlighted={highlightDownloads}>
		<DownloadSettings
			bind:settings
			{saving}
			outbounds={$downloadOutbounds}
			loading={$downloadOutboundsLoading}
			error={$downloadOutboundsError}
			onRefresh={refreshDownloadOutbounds}
			onSelectRoute={selectDownloadRoute}
		/>
	</div>
	</div>
</div>

<DevelopChannelGateModal
	open={developGateOpen}
	busy={saving}
	onclose={() => (developGateOpen = false)}
	onpassed={confirmDevelopChannel}
/>
