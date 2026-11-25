<!--
  «Установка обновлений»: расписание автоустановки, снимок настроек перед ней и
  анонимная статистика. `updateInfo` — только чтение (даты автоустановки).
-->
<script lang="ts">
	import { m } from '$lib/i18n';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Button, Toggle } from '$lib/components/ui';
	import { downloadErrorToText } from '$lib/utils/downloadError';
	import { formatDate } from '$lib/utils/format';
	import { setSettings as setGlobalSettings } from '$lib/stores/settings';
	import { isAutoInstallSettingsVisible } from '$lib/types/usageLevel';
	import type { Settings, UpdateInfo } from '$lib/types';
	import { pageDraft } from './pageState.svelte';

	interface Props {
		updateInfo: UpdateInfo | null;
		settings: Settings;
	}

	let { updateInfo, settings = $bindable() }: Props = $props();

	let savingAutoInstall = $state(false);
	let savingStats = $state(false);
	let savingSnapshot = $state(false);

	const intervalDays = pageDraft('update-interval-days', () => settings.updates.autoInstallIntervalDays || 7);
	const installTime = pageDraft('update-install-time', () => settings.updates.autoInstallTime || '05:00');

	async function toggleStats(enabled: boolean) {
		savingStats = true;
		try {
			settings = await api.updateSettings({ updates: { statsEnabled: enabled } });
			setGlobalSettings(settings);
		} catch (e) {
			notifications.error(m.settings_update_stats_failed({ error: downloadErrorToText(e) }));
		} finally {
			savingStats = false;
		}
	}

	async function toggleSnapshot(enabled: boolean) {
		savingSnapshot = true;
		try {
			settings = await api.updateSettings({ updates: { snapshotDisabled: !enabled } });
			setGlobalSettings(settings);
		} catch (e) {
			notifications.error(m.settings_update_snapshot_failed({ error: downloadErrorToText(e) }));
		} finally {
			savingSnapshot = false;
		}
	}

	async function toggleAutoInstall(enabled: boolean) {
		savingAutoInstall = true;
		try {
			settings = await api.updateSettings({ updates: { autoInstallEnabled: enabled } });
			setGlobalSettings(settings);
			notifications.success(
				enabled ? m.settings_update_auto_enabled() : m.settings_update_auto_disabled()
			);
		} catch (e) {
			notifications.error(m.settings_update_auto_failed({ error: downloadErrorToText(e) }));
		} finally {
			savingAutoInstall = false;
		}
	}

	async function saveAutoInstallSchedule() {
		savingAutoInstall = true;
		try {
			settings = await api.updateSettings({
				updates: { autoInstallIntervalDays: intervalDays.value, autoInstallTime: installTime.value },
			});
			setGlobalSettings(settings);
			notifications.success(m.settings_update_schedule_saved());
		} catch (e) {
			notifications.error(m.settings_update_schedule_failed({ error: downloadErrorToText(e) }));
		} finally {
			savingAutoInstall = false;
		}
	}
</script>

{#if isAutoInstallSettingsVisible(settings.usageLevel)}
	<div class="setting-row toggle-inline-row">
		<div class="flex flex-col gap-1">
			<span class="font-medium">{m.settings_update_auto_label()}</span>
			<span class="setting-description">
				{m.settings_update_auto_description()}
			</span>
		</div>
		<Toggle
			checked={settings.updates.autoInstallEnabled}
			controlled
			onchange={toggleAutoInstall}
			disabled={savingAutoInstall}
		/>
	</div>

	{#if settings.updates.autoInstallEnabled}
		<div class="setting-sub">
			<div class="setting-row">
				<div class="flex flex-col gap-1">
					{#if updateInfo?.nextAutoInstallAt}
						<span class="setting-description">{m.settings_update_next_window({ date: formatDate(updateInfo.nextAutoInstallAt) })}</span>
					{/if}
					{#if updateInfo?.lastAutoInstallAt}
						<span class="setting-description">{m.settings_update_last_auto({ date: formatDate(updateInfo.lastAutoInstallAt) })}</span>
					{/if}
				</div>
				<div class="inline-form">
					<div class="input-with-suffix">
						<input
							type="number"
							id="autoInstallIntervalDays"
							bind:value={intervalDays.value}
							min="1"
							max="30"
							disabled={savingAutoInstall}
						/>
						<span class="input-suffix">{m.settings_update_every_n_days()}</span>
					</div>
					<input
						type="time"
						id="autoInstallTime"
						bind:value={installTime.value}
						disabled={savingAutoInstall}
					/>
					{#if intervalDays.dirty || installTime.dirty}
						<Button
							variant="primary"
							size="sm"
							onclick={saveAutoInstallSchedule}
							loading={savingAutoInstall}
						>
							{savingAutoInstall ? m.common_saving() : m.common_save()}
						</Button>
					{/if}
				</div>
			</div>
		</div>
	{/if}
{/if}

<div class="setting-row toggle-inline-row">
	<div class="flex flex-col gap-1">
		<span class="font-medium">{m.settings_update_snapshot_label()}</span>
		<span class="setting-description">
			{m.settings_update_snapshot_description()}
		</span>
	</div>
	<Toggle
		checked={!settings.updates.snapshotDisabled}
		ariaLabel={m.settings_update_snapshot_label()}
		onchange={toggleSnapshot}
		disabled={savingSnapshot}
	/>
</div>

<div class="setting-row toggle-inline-row">
	<div class="flex flex-col gap-1">
		<span class="font-medium">{m.settings_update_stats_label()}</span>
		<span class="setting-description">
			{m.settings_update_stats_description()}
		</span>
	</div>
	<Toggle
		checked={settings.updates.statsEnabled}
		controlled
		onchange={toggleStats}
		disabled={savingStats}
	/>
</div>
