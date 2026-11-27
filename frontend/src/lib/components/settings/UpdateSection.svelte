<script lang="ts">
	import { m } from '$lib/i18n';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Modal, Button } from '$lib/components/ui';
	import ChangelogModal from './ChangelogModal.svelte';
	import DownloadErrorNotice from '$lib/components/downloads/DownloadErrorNotice.svelte';
	import { downloadErrorToText } from '$lib/utils/downloadError';
	import { formatDate } from '$lib/utils/format';
	import type { UpdateInfo } from '$lib/types';
	import { pageState } from './pageState.svelte';

	interface Props {
		updateInfo: UpdateInfo | null;
	}

	let { updateInfo = $bindable() }: Props = $props();

	// Обновление идёт минуту и дольше — признак переживает уход со вкладки.
	const op = pageState('update-op', () => ({ checking: false, upgrading: false }));
	let showConfirm = $state(false);
	let showChangelog = $state(false);

	const failureHint = $derived(
		updateInfo?.upgradeErrorHint === 'dns'
			? m.settings_update_failed_hint_dns()
			: updateInfo?.upgradeErrorHint === 'space'
				? m.settings_update_failed_hint_space()
				: updateInfo?.upgradeErrorHint === 'lock'
					? m.settings_update_failed_hint_lock()
					: ''
	);

	const manualCheckTitle = $derived(
		updateInfo?.available ? m.settings_update_check_newer_title() : m.settings_update_check_title()
	);
	const manualCheckLabel = $derived(
		op.checking ? m.settings_update_checking() : updateInfo?.available ? m.settings_update_check_again() : m.common_check()
	);

	async function checkForUpdates() {
		if (op.checking) return;
		op.checking = true;
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
			op.checking = false;
		}
	}

	function confirmUpgrade() {
		if (op.checking || !updateInfo?.available) return;
		showConfirm = true;
	}

	async function applyUpgrade() {
		if (op.checking || !updateInfo?.available) return;
		showConfirm = false;
		op.upgrading = true;

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
			op.upgrading = false;
			return;
		}

		// Poll boot-status (public endpoint — no auth, no connection-lost callbacks).
		// Detect restart via instanceId change, then reload to pick up new frontend.
		// opkg update перед установкой удлиняет её — ждём до двух минут.
		const maxAttempts = 60;

		for (let i = 0; i < maxAttempts; i++) {
			await new Promise(r => setTimeout(r, 2000));
			try {
				const status = await api.getBootStatus();
				if (status.instanceId !== previousInstanceId && !status.initializing) {
					window.location.reload();
					return;
				}
				if (status.instanceId === previousInstanceId) {
					// Демон прежний: opkg мог отказать до остановки старой версии.
					// Прошлую причину сервер сбросил при запуске, так что любая — эта.
					const info = await api.checkUpdate(false);
					if (info.upgradeError) {
						updateInfo = info;
						notifications.error(m.settings_update_failed());
						op.upgrading = false;
						return;
					}
				}
			} catch {
				// Server still down — expected during upgrade
			}
		}

		notifications.error(m.settings_update_no_response());
		op.upgrading = false;
	}
</script>

<div class="setting-row">
	<div class="flex flex-col gap-1">
		{#if op.upgrading}
			<span class="font-medium update-status">
				{m.settings_update_in_progress()}
			</span>
		{:else if updateInfo?.available}
			<span class="font-medium update-available">
				{m.settings_update_version_available({ version: updateInfo.latestVersion ?? '' })}
			</span>
		{:else if updateInfo?.error}
			<DownloadErrorNotice error={updateInfo.error} hideSettingsLink />
		{:else}
			<span class="font-medium" class:update-available={updateInfo}>
				{m.settings_update_latest()}
			</span>
		{/if}
		{#if updateInfo && !op.upgrading}
			<span class="setting-description">
				{m.settings_update_checked({ version: updateInfo.currentVersion, date: formatDate(updateInfo.checkedAt) })}
			</span>
		{/if}
		{#if updateInfo?.warning}
			<span class="setting-description update-warning">
				{updateInfo.warning}
			</span>
		{/if}
		{#if updateInfo?.upgradeError && !op.upgrading}
			<span class="font-medium update-failed">{m.settings_update_failed()}</span>
			{#if failureHint}
				<span class="setting-description">{failureHint}</span>
			{/if}
			<pre class="update-failed-log">{updateInfo.upgradeError}</pre>
		{/if}
	</div>
	<div class="setting-actions">
		{#if op.upgrading}
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
				loading={op.checking}
				title={manualCheckTitle}
			>
				{manualCheckLabel}
			</Button>
			{#if updateInfo?.available}
				<Button
					variant="primary"
					size="sm"
					onclick={confirmUpgrade}
					disabled={op.checking}
				>
					{m.common_update()}
				</Button>
			{/if}
		{/if}
	</div>
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
		<Button variant="primary" size="md" onclick={applyUpgrade}>{m.common_update()}</Button>
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
	.update-available {
		color: var(--color-success);
	}

	.update-warning {
		color: var(--color-warning);
	}

	.update-status {
		color: var(--color-accent);
	}

	.update-failed {
		color: var(--color-error);
	}

	.update-failed-log {
		margin: 0;
		font-size: 12px;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
		color: var(--text-secondary);
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
</style>
