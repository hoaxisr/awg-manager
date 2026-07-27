<!--
  Карточка «Действия»: перезапуск демона AWG Manager (с ожиданием нового
  instanceId бэкенда) и управление процессами sing-box / HydraRoute.
-->
<script lang="ts">
	import { Power } from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { singboxStatus } from '$lib/stores/singbox';
	import { hydrarouteStatus } from '$lib/stores/hydraroute';
	import { Modal, Button } from '$lib/components/ui';
	import { waitForBackendRestart } from '$lib/restartRecovery';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';

	const singboxStatusValue = $derived($singboxStatus.data ?? null);
	const singboxInstalled = $derived(singboxStatusValue?.installed ?? false);
	const singboxRunning = $derived(singboxStatusValue?.running ?? false);
	const hydraStatusValue = $derived($hydrarouteStatus.data ?? null);
	const hydraInstalled = $derived(hydraStatusValue?.installed ?? false);
	const hydraRunning = $derived(hydraStatusValue?.running ?? false);

	let restarting = $state(false);
	let restartConfirmOpen = $state(false);
	let singboxBusy = $state(false);
	let hydraBusy = $state(false);

	async function controlSingbox(action: 'start' | 'stop' | 'restart') {
		singboxBusy = true;
		try {
			const fresh = await api.singboxControl(action);
			singboxStatus.applyMutationResponse(fresh);
			notifications.success(
				action === 'restart' ? 'Sing-box перезапущен' :
				action === 'stop' ? 'Sing-box остановлен' : 'Sing-box запущен',
			);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Не удалось управлять sing-box');
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
				action === 'restart' ? 'HydraRoute перезапущен' :
				action === 'stop' ? 'HydraRoute остановлен' : 'HydraRoute запущен',
			);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Не удалось управлять HydraRoute');
		} finally {
			hydraBusy = false;
		}
	}

	async function restartDaemon() {
		restartConfirmOpen = false;
		restarting = true;
		const before = await readBackendInstanceId().catch(() => null);
		try {
			const result = await requestDaemonRestart();
			if (result === 'accepted') {
				notifications.success("AWG Manager перезапускается...");
			} else {
				notifications.warning("Соединение оборвалось, проверяю перезапуск AWG Manager...");
			}
			const waitResult = await waitForDaemonRestart(before);
			if (waitResult === 'timeout') {
				restarting = false;
				notifications.warning('Не удалось подтвердить перезапуск AWG Manager. Обновите страницу вручную.');
				return;
			}
			location.reload();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : "Не удалось перезапустить");
			restarting = false;
		}
	}

	async function requestDaemonRestart(): Promise<'accepted' | 'network-drop'> {
		try {
			const response = await fetch('/api/system/restart', {
				method: 'POST',
				credentials: 'same-origin',
				cache: 'no-store',
				headers: { 'Content-Type': 'application/json' },
			});

			if (response.status === 401) {
				throw new Error('Сессия истекла');
			}

			if (!response.ok) {
				const text = await response.text().catch(() => '');
				throw new Error(`Не удалось перезапустить AWG Manager (${response.status}): ${text.substring(0, 120)}`);
			}

			return 'accepted';
		} catch (e) {
			if (e instanceof TypeError) {
				return 'network-drop';
			}
			throw e;
		}
	}

	async function readBackendInstanceId(): Promise<string | null> {
		const res = await fetch('/api/health', {
			method: 'GET',
			cache: 'no-store',
			credentials: 'same-origin',
		});
		if (!res.ok) {
			return null;
		}
		const body = await res.json().catch(() => null);
		const id = body?.data?.instanceId;
		return typeof id === 'string' && id.length > 0 ? id : null;
	}

	function sleep(ms: number) {
		return new Promise<void>((resolve) => setTimeout(resolve, ms));
	}

	async function waitForDaemonRestart(previousInstanceId: string | null) {
		return waitForBackendRestart({
			previousInstanceId,
			readInstanceId: readBackendInstanceId,
			sleep,
			now: () => Date.now(),
			timeoutMs: 45_000,
			pollMs: 750,
			stableOnlineMs: 3_000,
		});
	}
</script>

<div class="settings-block" id="settings-actions">
	<div class="card actions-card">
	<SettingsSectionLabel label="Действия" icon={Power} tone="red" header />
	<div class="setting-row">
		<div class="flex flex-col gap-1">
			<span class="font-medium">Перезапуск AWGM</span>
			<span class="setting-description">Туннели продолжат работать</span>
		</div>
		<Button
			variant="secondary"
			size="sm"
			onclick={() => (restartConfirmOpen = true)}
			loading={restarting}
		>
			{restarting ? "Перезапуск..." : "Перезапустить"}
		</Button>
	</div>

	{#if singboxInstalled}
		<div class="setting-row">
			<div class="flex flex-col gap-1">
				<span class="font-medium">Sing-box</span>
				<span class="setting-description">
					{singboxRunning ? "Процесс работает" : "Процесс остановлен"}
				</span>
			</div>
			<div class="action-buttons">
				{#if singboxRunning}
					<span title={singboxStatusValue?.updateAvailable ? `Сначала обновите sing-box до ${singboxStatusValue.requiredVersion}` : ''}>
						<Button
							variant="secondary"
							size="sm"
							onclick={() => controlSingbox('restart')}
							loading={singboxBusy}
							disabled={singboxStatusValue?.updateAvailable ?? false}
						>
							Перезапустить
						</Button>
					</span>
					<Button variant="danger" size="sm" onclick={() => controlSingbox('stop')} loading={singboxBusy}>Остановить</Button>
				{:else}
					<Button variant="success" size="sm" onclick={() => controlSingbox('start')} loading={singboxBusy}>Запустить</Button>
				{/if}
			</div>
		</div>
	{/if}

	{#if hydraInstalled}
		<div class="setting-row">
			<div class="flex flex-col gap-1">
				<span class="font-medium">HydraRoute Neo</span>
				<span class="setting-description">
					{hydraRunning ? "Демон работает" : "Демон остановлен"}
				</span>
			</div>
			<div class="action-buttons">
				{#if hydraRunning}
					<Button variant="secondary" size="sm" onclick={() => controlHydra('restart')} loading={hydraBusy}>Перезапустить</Button>
					<Button variant="danger" size="sm" onclick={() => controlHydra('stop')} loading={hydraBusy}>Остановить</Button>
				{:else}
					<Button variant="success" size="sm" onclick={() => controlHydra('start')} loading={hydraBusy}>Запустить</Button>
				{/if}
			</div>
		</div>
	{/if}
	</div>
</div>

<Modal
	open={restartConfirmOpen}
	title="Перезапуск AWG Manager"
	size="sm"
	onclose={() => (restartConfirmOpen = false)}
>
	<p class="modal-text">
		Перезапустить процесс AWG Manager? Туннели продолжат работать.
	</p>
	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={() => (restartConfirmOpen = false)}>Отмена</Button>
		<Button variant="primary" size="md" onclick={restartDaemon}>Перезапустить</Button>
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
