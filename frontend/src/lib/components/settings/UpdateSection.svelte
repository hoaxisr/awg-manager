<script lang="ts">
	import { m } from '$lib/i18n';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Modal, Button, Toggle } from '$lib/components/ui';
	import ChangelogModal from './ChangelogModal.svelte';
	import DownloadErrorNotice from '$lib/components/downloads/DownloadErrorNotice.svelte';
	import { downloadErrorToText } from '$lib/utils/downloadError';
	import { formatDate } from '$lib/utils/format';
	import { setSettings as setGlobalSettings } from '$lib/stores/settings';
	import { isAutoInstallSettingsVisible } from '$lib/types/usageLevel';
	import type { Settings, UpdateInfo } from '$lib/types';

	interface Props {
		updateInfo: UpdateInfo | null;
		settings: Settings;
	}

	let { updateInfo = $bindable(), settings = $bindable() }: Props = $props();

	let checking = $state(false);
	let upgrading = $state(false);
	let showConfirm = $state(false);
	let showChangelog = $state(false);
	let savingAutoInstall = $state(false);
	let savingStats = $state(false);

	let localIntervalDays = $state(settings.updates.autoInstallIntervalDays || 7);
	let localTime = $state(settings.updates.autoInstallTime || '05:00');

	let savedIntervalDays = $derived(settings.updates.autoInstallIntervalDays || 7);
	let savedTime = $derived(settings.updates.autoInstallTime || '05:00');

	let autoInstallScheduleChanged = $derived(
		localIntervalDays !== savedIntervalDays || localTime !== savedTime
	);

	$effect(() => {
		localIntervalDays = savedIntervalDays;
	});

	$effect(() => {
		localTime = savedTime;
	});

	const manualCheckTitle = $derived(
		updateInfo?.available ? m.settings_update_check_newer_title() : m.settings_update_check_title()
	);
	const manualCheckLabel = $derived(
		checking ? m.settings_update_checking() : updateInfo?.available ? m.settings_update_check_again() : m.settings_update_check()
	);

	async function checkForUpdates() {
		if (checking) return;
		checking = true;
		try {
			updateInfo = await api.checkUpdate(true);
			if (updateInfo.error) {
				notifications.error(m.settings_update_check_failed({ error: downloadErrorToText(updateInfo.error) }));
			} else if (updateInfo.available) {
				notifications.success(m.settings_update_version_available({ version: updateInfo.latestVersion ?? '' }));
			} else {
				notifications.info(m.settings_update_none_toast());
			}
			if (updateInfo.warning) {
				notifications.info(updateInfo.warning);
			}
		} catch (e) {
			notifications.error(m.settings_update_check_failed({ error: downloadErrorToText(e) }));
		} finally {
			checking = false;
		}
	}

	function confirmUpgrade() {
		if (checking || !updateInfo?.available) return;
		showConfirm = true;
	}

	async function applyUpgrade() {
		if (checking || !updateInfo?.available) return;
		showConfirm = false;
		upgrading = true;

		// Capture instanceId before upgrade to detect restart
		let previousInstanceId = '';
		try {
			const status = await api.getBootStatus();
			previousInstanceId = status.instanceId;
		} catch { /* proceed anyway */ }

		try {
			await api.applyUpdate();
		} catch (e) {
			notifications.error(m.settings_update_start_failed({ error: downloadErrorToText(e) }));
			upgrading = false;
			return;
		}

		// Poll boot-status (public endpoint — no auth, no connection-lost callbacks).
		// Detect restart via instanceId change, then reload to pick up new frontend.
		const maxAttempts = 30;

		for (let i = 0; i < maxAttempts; i++) {
			await new Promise(r => setTimeout(r, 2000));
			try {
				const status = await api.getBootStatus();
				if (status.instanceId !== previousInstanceId && !status.initializing) {
					window.location.reload();
					return;
				}
			} catch {
				// Server still down — expected during upgrade
			}
		}

		notifications.error(m.settings_update_no_response());
		upgrading = false;
	}

	async function toggleStats(enabled: boolean) {
		savingStats = true;
		try {
			settings = await api.updateSettings({
				...settings,
				updates: { ...settings.updates, statsEnabled: enabled },
			});
			setGlobalSettings(settings);
		} catch (e) {
			notifications.error(m.settings_update_stats_failed({ error: downloadErrorToText(e) }));
		} finally {
			savingStats = false;
		}
	}

	async function toggleAutoInstall(enabled: boolean) {
		savingAutoInstall = true;
		try {
			settings = await api.updateSettings({
				...settings,
				updates: { ...settings.updates, autoInstallEnabled: enabled },
			});
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
				...settings,
				updates: {
					...settings.updates,
					autoInstallIntervalDays: localIntervalDays,
					autoInstallTime: localTime,
				},
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

<div class="setting-row update-row">
	<div class="flex flex-col gap-1 update-info">
		{#if upgrading}
			<span class="setting-description update-status">
				{m.settings_update_in_progress()}
			</span>
		{:else if updateInfo?.available}
			<span class="setting-description update-available">
				{m.settings_update_version_available({ version: updateInfo.latestVersion ?? '' })}
			</span>
		{:else if updateInfo?.error}
			<div class="update-error-notice">
				<DownloadErrorNotice error={updateInfo.error} hideSettingsLink />
			</div>
		{:else}
			<span class="setting-description">
				{m.settings_update_latest()}
			</span>
		{/if}
		{#if updateInfo?.warning}
			<span class="setting-description update-warning">
				{updateInfo.warning}
			</span>
		{/if}
	</div>
	<div class="update-actions">
		{#if upgrading}
			<div class="update-spinner"></div>
		{:else}
			{#if updateInfo?.currentVersion}
				<Button
					variant="secondary"
					size="sm"
					onclick={() => (showChangelog = true)}
				>
					{m.settings_update_whats_new()}
				</Button>
			{/if}
			<!-- Manual check must stay available even when an update is already cached:
				repo may publish a newer build after the cached result was fetched. -->
			<Button
				variant="secondary"
				size="sm"
				onclick={checkForUpdates}
				loading={checking}
				title={manualCheckTitle}
			>
				{manualCheckLabel}
			</Button>
			{#if updateInfo?.available}
				<Button
					variant="primary"
					size="sm"
					onclick={confirmUpgrade}
					disabled={checking}
				>
					{m.settings_update_apply()}
				</Button>
			{/if}
		{/if}
	</div>
</div>

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
			onchange={toggleAutoInstall}
			disabled={savingAutoInstall}
		/>
	</div>

	{#if settings.updates.autoInstallEnabled}
		<div class="settings-panel">
			<div class="inline-form">
				<div class="input-with-suffix">
					<input
						type="number"
						id="autoInstallIntervalDays"
						bind:value={localIntervalDays}
						min="1"
						max="30"
						disabled={savingAutoInstall}
					/>
					<span class="input-suffix">{m.settings_update_every_n_days()}</span>
				</div>
				<input
					type="time"
					id="autoInstallTime"
					bind:value={localTime}
					disabled={savingAutoInstall}
				/>
				{#if autoInstallScheduleChanged}
					<Button
						variant="primary"
						size="sm"
						onclick={saveAutoInstallSchedule}
						loading={savingAutoInstall}
					>
						{savingAutoInstall ? m.settings_update_saving() : m.settings_update_save()}
					</Button>
				{/if}
			</div>
			{#if updateInfo?.nextAutoInstallAt}
				<p class="auto-install-status">{m.settings_update_next_window({ date: formatDate(updateInfo.nextAutoInstallAt) })}</p>
			{/if}
			{#if updateInfo?.lastAutoInstallAt}
				<p class="auto-install-status">{m.settings_update_last_auto({ date: formatDate(updateInfo.lastAutoInstallAt) })}</p>
			{/if}
		</div>
	{/if}
{/if}

<div class="setting-row toggle-inline-row">
	<div class="flex flex-col gap-1">
		<span class="font-medium">{m.settings_update_stats_label()}</span>
		<span class="setting-description">
			{m.settings_update_stats_description()}
		</span>
	</div>
	<Toggle
		checked={settings.updates.statsEnabled}
		onchange={toggleStats}
		disabled={savingStats}
	/>
</div>

<Modal
	open={showConfirm}
	title={m.settings_update_confirm_title()}
	onclose={() => showConfirm = false}
>
	<p class="modal-text">
		{m.settings_update_confirm_text({ version: updateInfo?.latestVersion ?? '' })}
	</p>

	{#snippet actions()}
		<Button variant="secondary" size="md" onclick={() => showConfirm = false}>{m.common_cancel()}</Button>
		<Button variant="primary" size="md" onclick={applyUpgrade}>{m.settings_update_apply()}</Button>
	{/snippet}
</Modal>

{#if updateInfo?.currentVersion}
	<ChangelogModal
		open={showChangelog}
		pendingUpdate={Boolean(updateInfo.available && updateInfo.latestVersion)}
		fromVersion={updateInfo.available && updateInfo.latestVersion ? updateInfo.currentVersion : ''}
		toVersion={updateInfo.available && updateInfo.latestVersion ? updateInfo.latestVersion : updateInfo.currentVersion}
		oncheckUpdates={() => {
			showChangelog = false;
			void checkForUpdates();
		}}
		onclose={() => (showChangelog = false)}
	/>
{/if}

<style>
	.update-row.setting-row {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto;
		align-items: center;
		gap: 0.75rem;
	}

	.update-info {
		min-width: 0;
	}

	.update-actions {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-shrink: 0;
		flex-wrap: wrap;
		justify-content: flex-end;
	}

	@media (max-width: 860px) {
		.update-row.setting-row {
			grid-template-columns: 1fr;
			align-items: start;
		}

		.update-actions {
			justify-content: stretch;
			width: 100%;
			display: grid;
			grid-template-columns: repeat(2, minmax(0, 1fr));
			gap: 0.5rem;
		}

		.update-actions :global(button) {
			width: 100%;
		}
	}

	/* Keep the update card readable in the narrow settings column:
		status takes its own row, actions are arranged below. */
	.update-row.setting-row {
		grid-template-columns: minmax(0, 1fr);
		align-items: start;
	}

	.update-actions {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		justify-content: stretch;
		width: 100%;
		flex-shrink: 1;
	}

	.update-actions :global(button) {
		width: 100%;
		min-width: 0;
	}

	.update-actions :global(button:first-child:nth-last-child(3)),
	.update-actions :global(button:first-child:last-child) {
		grid-column: 1 / -1;
	}

	.update-spinner {
		grid-column: 1 / -1;
		justify-self: end;
	}

	@media (min-width: 641px) {
		.update-actions {
			justify-self: end;
			max-width: 28rem;
		}
	}

	.update-available {
		color: var(--success, #22c55e) !important;
		font-weight: 500;
	}

	.update-error-notice {
		min-width: 0;
	}

	.update-warning {
		color: var(--warning, #eab308) !important;
	}

	.update-status {
		color: var(--accent) !important;
	}
	.update-spinner {
		width: 20px;
		height: 20px;
		border: 2px solid var(--border);
		border-top-color: var(--accent);
		border-radius: 50%;
		animation: spin 0.8s linear infinite;
	}

	@keyframes spin {
		to { transform: rotate(360deg); }
	}

	.modal-text {
		color: var(--text-secondary);
		margin: 0;
	}

	.inline-form {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
	}

	.input-with-suffix {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		min-width: 0;
	}

	.input-suffix {
		font-size: 0.8125rem;
		color: var(--text-secondary);
	}

	.inline-form input[type="number"] {
		width: 4.75rem;
	}

	.inline-form input[type="time"] {
		width: 8rem;
	}

	.auto-install-status {
		margin: 0.5rem 0 0;
		font-size: 0.75rem;
		color: var(--text-secondary);
	}

	@media (max-width: 640px) {
		.inline-form {
			flex-direction: column;
			align-items: stretch;
		}

		.input-with-suffix {
			display: grid;
			grid-template-columns: minmax(0, 1fr) auto;
			width: 100%;
		}

		.inline-form input[type="number"],
		.inline-form input[type="time"] {
			width: 100%;
			box-sizing: border-box;
		}

		.inline-form :global(.btn) {
			width: 100%;
		}
	}
</style>
