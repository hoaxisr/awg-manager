<script lang="ts">
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Modal } from '$lib/components/ui';
	import type { UpdateInfo } from '$lib/types';

	interface Props {
		updateInfo: UpdateInfo | null;
	}

	let { updateInfo = $bindable() }: Props = $props();

	let checking = $state(false);
	let upgrading = $state(false);
	let showConfirm = $state(false);

	async function checkForUpdates() {
		checking = true;
		try {
			updateInfo = await api.checkUpdate(true);
			if (updateInfo.error) {
				notifications.error(`Ошибка проверки: ${updateInfo.error}`);
			} else if (updateInfo.available) {
				notifications.success(`Доступна версия ${updateInfo.latestVersion}`);
			} else {
				notifications.info('Обновлений нет');
			}
		} catch (e) {
			notifications.error('Ошибка проверки обновлений');
		} finally {
			checking = false;
		}
	}

	function confirmUpgrade() {
		showConfirm = true;
	}

	async function applyUpgrade() {
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
			notifications.error('Ошибка запуска обновления');
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

<div class="setting-row">
	<div class="flex flex-col gap-1">
		<span class="font-medium">Обновление</span>
		{#if upgrading}
			<span class="setting-description update-status">
				Обновление... не закрывайте страницу
			</span>
		{:else if updateInfo?.available}
			<span class="setting-description update-available">
				Доступна версия {updateInfo.latestVersion}
			</span>
		{:else if updateInfo?.error}
			<span class="setting-description update-error">
				{updateInfo.error}
			</span>
		{:else}
			<span class="setting-description">
				Установлена последняя версия
			</span>
		{/if}
	</div>
	<div class="update-actions">
		{#if upgrading}
			<div class="update-spinner"></div>
		{:else}
			{#if updateInfo?.available}
				<button
					class="btn btn-primary btn-sm"
					onclick={confirmUpgrade}
				>
					Обновить
				</button>
			{/if}
			<button
				class="btn btn-secondary btn-sm"
				onclick={checkForUpdates}
				disabled={checking}
			>
				{checking ? 'Проверка...' : 'Проверить'}
			</button>
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
		<button class="btn btn-secondary" onclick={() => showConfirm = false}>Отмена</button>
		<button class="btn btn-primary" onclick={applyUpgrade}>Обновить</button>
	{/snippet}
</Modal>

<style>
	.update-actions {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-shrink: 0;
	}

	.update-available {
		color: var(--success, #22c55e) !important;
		font-weight: 500;
	}

	.update-error {
		color: var(--error, #ef4444) !important;
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
