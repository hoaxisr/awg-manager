<!--
  Карточка «Действия»: перезапуск демона и управление sing-box, HydraRoute
  Neo, zapret. Общих настроек не меняет — статусы служб берёт из их сторов,
  запросы делает сама. Видимость строк sing-box/Hydra (уровень интерфейса)
  решает страница.
-->
<script lang="ts">
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { Button, Modal } from '$lib/components/ui';
	import { runZapretControl } from '$lib/components/zapret';
	import { notifications } from '$lib/stores/notifications';
	import { singboxStatus } from '$lib/stores/singbox';
	import { hydrarouteStatus } from '$lib/stores/hydraroute';
	import { zapretStatusIfExpert } from '$lib/stores/zapret';
	import { readBackendInstanceId, requestDaemonRestart, waitForDaemonRestart } from '$lib/utils/daemonRestart';
	import type { ZapretControlAction } from '$lib/types';
	import { Power } from 'lucide-svelte';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';

	interface Props {
		showSingbox: boolean;
		showHydra: boolean;
	}

	let { showSingbox, showHydra }: Props = $props();

	let restarting = $state(false);
	let restartConfirmOpen = $state(false);
	let singboxBusy = $state(false);
	let hydraBusy = $state(false);
	let zapretBusy = $state(false);

	const singboxStatusValue = $derived($singboxStatus.data ?? null);
	const singboxInstalled = $derived(singboxStatusValue?.installed ?? false);
	const singboxRunning = $derived(singboxStatusValue?.running ?? false);
	const hydraInstalled = $derived($hydrarouteStatus.data?.installed ?? false);
	const hydraRunning = $derived($hydrarouteStatus.data?.running ?? false);
	const zapretStatusValue = $derived($zapretStatusIfExpert?.data ?? null);
	const zapretInstalled = $derived(zapretStatusValue?.installed ?? false);
	const zapretRunning = $derived(zapretStatusValue?.running ?? false);

	async function controlSingbox(action: 'start' | 'stop' | 'restart') {
		singboxBusy = true;
		try {
			const fresh = await api.singboxControl(action);
			singboxStatus.applyMutationResponse(fresh);
			notifications.success(
				action === 'restart' ? m.settings_page_singbox_restarted() :
				action === 'stop' ? m.settings_page_singbox_stopped() : m.settings_page_singbox_started(),
			);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_page_singbox_control_failed());
		} finally {
			singboxBusy = false;
		}
	}

	async function controlHydra(action: 'start' | 'stop' | 'restart') {
		hydraBusy = true;
		try {
			const fresh = await api.controlHydraRoute(action);
			hydrarouteStatus.applyMutationResponse(fresh);
			notifications.success(
				action === 'restart' ? m.settings_page_hydra_restarted() :
				action === 'stop' ? m.settings_page_hydra_stopped() : m.settings_page_hydra_started(),
			);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_page_hydra_control_failed());
		} finally {
			hydraBusy = false;
		}
	}

	async function controlZapret(action: ZapretControlAction) {
		zapretBusy = true;
		try {
			await runZapretControl(action, zapretStatusValue?.base ?? '');
		} finally {
			zapretBusy = false;
		}
	}

	async function restartDaemon() {
		restartConfirmOpen = false;
		restarting = true;
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
				restarting = false;
				notifications.warning(m.settings_page_restart_unconfirmed());
				return;
			}
			location.reload();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_page_restart_failed());
			restarting = false;
		}
	}
</script>

<div class="settings-block" id="settings-actions">
	<div class="card actions-card">
	<SettingsSectionLabel label={m.settings_page_actions_title()} icon={Power} tone="red" header />
	<div class="setting-row">
		<div class="flex flex-col gap-1">
			<span class="font-medium">{m.settings_page_restart_label()}</span>
			<span class="setting-description">{m.settings_page_restart_description()}</span>
		</div>
		<Button
			variant="secondary"
			size="sm"
			onclick={() => (restartConfirmOpen = true)}
			loading={restarting}
		>
			{restarting ? m.settings_page_restarting_button() : m.common_restart()}
		</Button>
	</div>

	{#if singboxInstalled && showSingbox}
		<div class="setting-row">
			<div class="flex flex-col gap-1">
				<span class="font-medium">Sing-box</span>
				<span class="setting-description">
					{singboxRunning ? m.settings_page_process_running() : m.settings_page_process_stopped()}
				</span>
			</div>
			<div class="action-buttons">
				{#if singboxRunning}
					<span title={singboxStatusValue?.updateAvailable ? m.settings_page_singbox_update_first({ version: singboxStatusValue.requiredVersion }) : ''}>
						<Button
							variant="secondary"
							size="sm"
							onclick={() => controlSingbox('restart')}
							loading={singboxBusy}
							disabled={singboxStatusValue?.updateAvailable ?? false}
						>
							{m.common_restart()}
						</Button>
					</span>
					<Button variant="danger" size="sm" onclick={() => controlSingbox('stop')} loading={singboxBusy}>{m.common_stop()}</Button>
				{:else}
					<Button variant="success" size="sm" onclick={() => controlSingbox('start')} loading={singboxBusy}>{m.common_start()}</Button>
				{/if}
			</div>
		</div>
	{/if}

	{#if hydraInstalled && showHydra}
		<div class="setting-row">
			<div class="flex flex-col gap-1">
				<span class="font-medium">HydraRoute Neo</span>
				<span class="setting-description">
					{hydraRunning ? m.settings_page_daemon_running() : m.settings_page_daemon_stopped()}
				</span>
			</div>
			<div class="action-buttons">
				{#if hydraRunning}
					<Button variant="secondary" size="sm" onclick={() => controlHydra('restart')} loading={hydraBusy}>{m.common_restart()}</Button>
					<Button variant="danger" size="sm" onclick={() => controlHydra('stop')} loading={hydraBusy}>{m.common_stop()}</Button>
				{:else}
					<Button variant="success" size="sm" onclick={() => controlHydra('start')} loading={hydraBusy}>{m.common_start()}</Button>
				{/if}
			</div>
		</div>
	{/if}

	{#if zapretInstalled}
		<div class="setting-row">
			<div class="flex flex-col gap-1">
				<span class="font-medium">Zapret</span>
				<span class="setting-description">
					{zapretRunning ? m.settings_page_daemon_running() : m.settings_page_daemon_stopped()}
				</span>
			</div>
			<div class="action-buttons">
				{#if zapretRunning}
					<Button variant="secondary" size="sm" onclick={() => controlZapret('restart')} loading={zapretBusy}>{m.common_restart()}</Button>
					<Button variant="danger" size="sm" onclick={() => controlZapret('stop')} loading={zapretBusy}>{m.common_stop()}</Button>
				{:else}
					<Button variant="success" size="sm" onclick={() => controlZapret('start')} loading={zapretBusy}>{m.common_start()}</Button>
				{/if}
			</div>
		</div>
	{/if}
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

	.actions-card > .setting-row {
		align-items: center;
	}

	.action-buttons {
		display: inline-flex;
		gap: 0.375rem;
		flex-shrink: 0;
		align-items: center;
		justify-content: flex-end;
	}

	@media (min-width: 641px) {
		.actions-card > .setting-row > :global(.btn),
		.action-buttons :global(.btn) {
			width: 7.5rem;
			min-width: 7.5rem;
		}

		.action-buttons > span {
			display: inline-flex;
		}

		.action-buttons > span :global(.btn) {
			width: 7.5rem;
			min-width: 7.5rem;
		}
	}

	@media (max-width: 640px) {
		.actions-card > .setting-row:has(.action-buttons) {
			flex-direction: column;
			align-items: stretch;
			flex-wrap: nowrap;
			gap: 0.625rem;
		}

		.actions-card > .setting-row:has(.action-buttons) > *:first-child {
			flex: initial;
			width: 100%;
		}

		.actions-card > .setting-row {
			flex-direction: row;
			align-items: center;
			flex-wrap: nowrap;
			gap: 0.75rem;
		}

		.actions-card > .setting-row > *:first-child {
			flex: 1 1 auto;
			min-width: 0;
		}

		.action-buttons {
			display: grid;
			grid-template-columns: repeat(2, minmax(0, 1fr));
			justify-content: stretch;
			flex-wrap: nowrap;
			width: 100%;
			gap: 0.5rem;
		}

		.actions-card > .setting-row > :global(.btn) {
			width: min(50%, 10rem);
			min-width: 0;
			margin-left: auto;
		}

		.action-buttons > span {
			display: block;
			width: 100%;
			min-width: 0;
		}

		.action-buttons > span :global(.btn),
		.action-buttons :global(.btn) {
			width: 100%;
			min-width: 0;
		}
	}
</style>
