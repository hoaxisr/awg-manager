<!-- Карточка «Действия»: перезапуск демона панели. -->
<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button, Modal } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import { readBackendInstanceId, requestDaemonRestart, waitForDaemonRestart } from '$lib/utils/daemonRestart';
	import { Power } from 'lucide-svelte';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';
	import { pageState } from './pageState.svelte';

	// Перезапуск ждём минуту и дольше — признак переживает уход со вкладки.
	const op = pageState('daemon-restart', () => ({ restarting: false }));
	let restartConfirmOpen = $state(false);

	async function restartDaemon() {
		restartConfirmOpen = false;
		op.restarting = true;
		const before = await readBackendInstanceId().catch(() => null);
		try {
			const result = await requestDaemonRestart();
			if (result === 'accepted') {
				notifications.success(m.settings_page_restarting());
			} else {
				notifications.warning(m.settings_page_connection_dropped());
			}
			const waitResult = await waitForDaemonRestart(before);
			if (waitResult === 'timeout') {
				op.restarting = false;
				notifications.warning(m.settings_page_restart_unconfirmed());
				return;
			}
			location.reload();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_page_restart_failed());
			op.restarting = false;
		}
	}
</script>

<div class="settings-block" id="settings-actions">
	<div class="card">
	<SettingsSectionLabel label={m.settings_page_actions_title()} icon={Power} tone="red" header />
	<div class="setting-row toggle-inline-row">
		<div class="flex flex-col gap-1">
			<span class="font-medium">{m.settings_page_restart_label()}</span>
			<span class="setting-description">{m.settings_page_restart_description()}</span>
		</div>
		<Button
			variant="secondary"
			size="sm"
			onclick={() => (restartConfirmOpen = true)}
			loading={op.restarting}
		>
			{op.restarting ? m.settings_page_restarting_button() : m.common_restart()}
		</Button>
	</div>
	</div>
</div>

<Modal
	open={restartConfirmOpen}
	title={m.settings_page_restart_modal_title()}
	size="sm"
	onclose={() => (restartConfirmOpen = false)}
>
	<p class="modal-text">
		{m.settings_page_restart_modal_text()}
	</p>
	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={() => (restartConfirmOpen = false)}>{m.common_cancel()}</Button>
		<Button variant="primary" size="md" onclick={restartDaemon}>{m.common_restart()}</Button>
	{/snippet}
</Modal>

<style>
	.modal-text {
		color: var(--color-text-secondary);
		font-size: 0.875rem;
		margin: 0;
	}
</style>
