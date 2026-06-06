<script lang="ts">
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Modal, Button } from '$lib/components/ui';
	import ChangelogModal from './ChangelogModal.svelte';
	import DownloadErrorNotice from '$lib/components/downloads/DownloadErrorNotice.svelte';
	import { downloadErrorToText, downloadRouteError } from '$lib/utils/downloadError';
	import type { UpdateInfo } from '$lib/types';

	interface Props {
		updateInfo: UpdateInfo | null;
	}

	let { updateInfo = $bindable() }: Props = $props();

	let checking = $state(false);
	let upgrading = $state(false);
	let showConfirm = $state(false);
	let showChangelog = $state(false);

	const manualCheckTitle = $derived(
		updateInfo?.available ? 'Проверить наличие более новой версии' : 'Проверить обновления'
	);
	const manualCheckLabel = $derived(
		checking ? 'Проверка...' : updateInfo?.available ? 'Проверить ещё' : 'Проверить'
	);

	async function checkForUpdates() {
		if (checking) return;
		checking = true;
		try {
			updateInfo = await api.checkUpdate(true);
			if (updateInfo.error) {
				notifications.error(
					`Проверка обновлений: ${downloadErrorToText(downloadRouteError(updateInfo.error, updateInfo.errorCode))}`,
				);
			} else if (updateInfo.available) {
				notifications.success(`Доступна версия ${updateInfo.latestVersion}`);
			} else {
				notifications.info('Обновлений нет');
			}
			if (updateInfo.warning) {
				notifications.info(updateInfo.warning);
			}
		} catch (e) {
			notifications.error(`Проверка обновлений: ${downloadErrorToText(e)}`);
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
			notifications.error(`Запуск обновления: ${downloadErrorToText(e)}`);
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

		notifications.error('Сервер не ответил после обновления');
		upgrading = false;
	}
</script>

<div class="setting-row update-row">
	<div class="flex flex-col gap-1 update-info">
		{#if upgrading}
			<span class="setting-description update-status">
				Обновление... не закрывайте страницу
			</span>
		{:else if updateInfo?.available}
			<span class="setting-description update-available">
				Доступна версия {updateInfo.latestVersion}
			</span>
		{:else if updateInfo?.error}
			<div class="update-error-notice">
				<DownloadErrorNotice
					error={downloadRouteError(updateInfo.error, updateInfo.errorCode)}
					hideSettingsLink
				/>
			</div>
		{:else}
			<span class="setting-description">
				Установлена последняя версия
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
					Что нового
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
					Обновить
				</Button>
			{/if}
		{/if}
	</div>
</div>

<Modal
	open={showConfirm}
	title="Обновление"
	onclose={() => showConfirm = false}
>
	<p class="modal-text">
		Обновить до версии {updateInfo?.latestVersion}? Сервис будет перезапущен.
	</p>

	{#snippet actions()}
		<Button variant="secondary" size="md" onclick={() => showConfirm = false}>Отмена</Button>
		<Button variant="primary" size="md" onclick={applyUpgrade}>Обновить</Button>
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
</style>
