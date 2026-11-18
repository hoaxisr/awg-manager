<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { singboxRouter as singboxRouterStore } from '$lib/stores/singboxRouter';
	import { fircStatus, fircGroups, fircTunnels, fircTunnelsState, reloadFircAll } from '$lib/stores/firc';
	import { Button, StatusDot, Badge, SideDrawer, ConfirmModal } from '$lib/components/ui';
	import StatStrip, { type StatCellData } from '$lib/components/sb-router/StatStrip.svelte';
	import FircGroupsPanel from '$lib/components/firc/FircGroupsPanel.svelte';
	import FircSettingsPanel from '$lib/components/firc/FircSettingsPanel.svelte';
	import FircMonitorPanel from '$lib/components/firc/FircMonitorPanel.svelte';
	import { Settings, RotateCw, Power, AlertTriangle } from 'lucide-svelte';

	let settingsDrawerOpen = $state(false);
	let settingsHasUnsaved = $state(false);
	let confirmCloseSettingsOpen = $state(false);
	let activeView = $state<'rules' | 'monitor'>('rules');
	let busy = $state(false);

	function requestCloseSettingsDrawer() {
		if (settingsHasUnsaved) {
			confirmCloseSettingsOpen = true;
			return;
		}
		settingsDrawerOpen = false;
	}

	onMount(() => {
		void reloadFircAll().catch((e) => {
			notifications.error(`Не удалось загрузить статус FIRC: ${(e as Error).message}`);
		});
	});

	async function handleSelectFircEngine() {
		if (busy) return;
		busy = true;
		try {
			await api.fircUpdateSettings({ routingEngine: 'firc' });
			await Promise.all([reloadFircAll(), singboxRouterStore.reloadSettings()]);
			notifications.success('Активное ядро маршрутизации переключено на FIRC');
		} catch (e) {
			notifications.error(`Ошибка переключения на FIRC: ${(e as Error).message}`);
		} finally {
			busy = false;
		}
	}

	async function handleToggleFircDaemon() {
		if (busy) return;
		busy = true;
		try {
			if ($fircStatus?.running) {
				await api.fircStop();
				notifications.success('Демон fircd остановлен');
			} else {
				await api.fircStart();
				notifications.success('Демон fircd запущен');
			}
			await reloadFircAll();
		} catch (e) {
			notifications.error((e as Error).message || 'Ошибка управления fircd');
		} finally {
			busy = false;
		}
	}

	async function handleRestartFircDaemon() {
		if (busy) return;
		busy = true;
		try {
			if ($fircStatus?.recoveryRequired) {
				await api.fircReconcileRecovery('firc');
				await reloadFircAll();
				notifications.success('FIRC успешно восстановлен и синхронизирован');
			} else {
				await api.fircRestart();
				await reloadFircAll();
				notifications.success('Конфигурация fircd перезагружена');
			}
		} catch (e) {
			notifications.error((e as Error).message || 'Ошибка перезапуска fircd');
		} finally {
			busy = false;
		}
	}

	async function handleReconcileRecovery() {
		if (busy) return;
		busy = true;
		try {
			await api.fircReconcileRecovery('firc');
			await reloadFircAll();
			notifications.success('FIRC успешно восстановлен: цепочки и правила синхронизированы');
		} catch (e) {
			notifications.error((e as Error).message || 'Ошибка восстановления FIRC');
		} finally {
			busy = false;
		}
	}

	let totalGroups = $derived($fircGroups.length);
	let enabledGroups = $derived($fircGroups.filter((g) => g.enable !== false).length);
	let totalRules = $derived(
		$fircGroups.reduce((acc, g) => acc + (g.rules?.length ?? 0) + (g.list?.rulesTotal ?? 0), 0),
	);
	let totalTunnels = $derived($fircTunnels.length);
	let runningTunnels = $derived(
		$fircTunnelsState.filter((s) => s.status === 'up').length ||
			$fircTunnels.filter((t) => t.enable !== false).length,
	);

	let statCells = $derived<StatCellData[]>([
		{
			label: 'Движок FIRC',
			value: $fircStatus?.recoveryRequired ? 'Авария (Recovery)' : ($fircStatus?.running ? 'Активен' : 'Остановлен'),
			tone: $fircStatus?.recoveryRequired ? 'error' : ($fircStatus?.running ? 'success' : 'error'),
			helpTitle: 'Состояние ядра fircd',
			helpText: $fircStatus?.recoveryRequired
				? 'Включена fail-closed защита. Требуется восстановление FIRC.'
				: 'Высокопроизводительный C-демон маршрутизации с FakeIP DNS и ipset/iptables.',
		},
		{
			label: 'Групп правил',
			value: `${enabledGroups}/${totalGroups}`,
			tone: enabledGroups > 0 ? 'default' : 'muted',
			helpTitle: 'Группы маршрутизации',
			helpText: 'Активные группы правил из общего числа настроенных групп.',
		},
		{
			label: 'Доменов и правил',
			value: String(totalRules),
			tone: totalRules > 0 ? 'default' : 'muted',
			helpTitle: 'Суммарная база правил',
			helpText: 'Общее количество доменов, масок, подсетей и записей из удалённых списков.',
		},
		{
			label: 'Туннелей VLESS',
			value: `${runningTunnels}/${totalTunnels}`,
			tone: runningTunnels > 0 ? 'success' : 'muted',
			helpTitle: 'Туннели tunvless',
			helpText: 'Активные интерфейсы tunvless для выхода трафика групп.',
		},
		{
			label: 'DNS / Прокси',
			value: `:${$fircStatus?.dnsPort || 3553} · :${$fircStatus?.inboundPort || 1099}`,
			compact: true,
			tone: 'default',
			helpTitle: 'Локальные порты FIRC',
			helpText: 'Порт DNS FakeIP (198.18.0.0/15) и локальный входной порт SOCKS5/HTTP.',
		},
	]);
</script>

<div class="sb-shell">
	<header class="sb-header">
		<div class="title-block">
			<div class="title-row">
				<h1 class="title">Маршрутизация · FIRC</h1>
				<span
					class="status-pill"
					class:is-running={$fircStatus?.running && !$fircStatus?.recoveryRequired}
					class:is-recovery={$fircStatus?.recoveryRequired}
				>
					<StatusDot variant={$fircStatus?.recoveryRequired ? 'error' : ($fircStatus?.running ? 'success' : 'muted')} size="sm" />
					<span>{$fircStatus?.recoveryRequired ? 'Требуется восстановление' : ($fircStatus?.running ? 'fircd активен' : 'fircd остановлен')}</span>
				</span>
				{#if $fircStatus?.selected}
					<Badge variant="accent" size="xs">Активное ядро</Badge>
				{/if}
			</div>
			<div class="subtitle">
				Групповая маршрутизация доменов, удалённых списков и подсетей через FakeIP и туннели
			</div>
		</div>

		<div class="header-tools">
			{#if !$fircStatus?.selected}
				<Button variant="primary" size="sm" onclick={handleSelectFircEngine} disabled={busy}>
					Сделать активным ядром
				</Button>
			{/if}

			<button
				type="button"
				class="icon-btn"
				onclick={handleRestartFircDaemon}
				disabled={busy}
				title={$fircStatus?.recoveryRequired ? 'Восстановить и перезагрузить fircd' : 'Перезагрузить fircd'}
				aria-label={$fircStatus?.recoveryRequired ? 'Восстановить и перезагрузить fircd' : 'Перезагрузить fircd'}
			>
				<RotateCw size={15} />
			</button>

			<button
				type="button"
				class="icon-btn"
				onclick={handleToggleFircDaemon}
				disabled={busy}
				title={$fircStatus?.running ? 'Остановить fircd' : 'Запустить fircd'}
				aria-label={$fircStatus?.running ? 'Остановить fircd' : 'Запустить fircd'}
			>
				<Power size={15} />
			</button>

			<button
				type="button"
				class="params-btn"
				onclick={() => (settingsDrawerOpen = true)}
				aria-label="Параметры FIRC"
			>
				<Settings size={16} aria-hidden="true" />
				<span class="params-text">Параметры FIRC</span>
			</button>
		</div>
	</header>

	<StatStrip cells={statCells} />

	{#if $fircStatus?.recoveryRequired}
		<div class="firc-recovery-banner" role="alert">
			<div class="recovery-content">
				<div class="recovery-title">
					<AlertTriangle size={16} />
					<span>Требуется восстановление FIRC (аварийный режим защиты)</span>
				</div>
				<div class="recovery-text">
					{$fircStatus.recoveryReason || 'Операции маршрутизации заблокированы после сбоя netfilter. Нажмите «Восстановить FIRC» для автоматической сверки конфигурации и воссоздания цепочек.'}
				</div>
			</div>
			<div class="recovery-actions">
				<Button variant="danger" size="sm" onclick={handleReconcileRecovery} disabled={busy}>
					Восстановить FIRC
				</Button>
			</div>
		</div>
	{:else if $fircStatus?.lastError}
		<div class="firc-error-banner">
			{$fircStatus.lastError}
		</div>
	{/if}

	<nav class="firc-views" aria-label="Разделы FIRC">
		<button type="button" class:active={activeView === 'rules'} aria-pressed={activeView === 'rules'} onclick={() => (activeView = 'rules')}>Правила</button>
		<button type="button" class:active={activeView === 'monitor'} aria-pressed={activeView === 'monitor'} onclick={() => (activeView = 'monitor')}>Мониторинг</button>
	</nav>
	{#if activeView === 'monitor'}
		<FircMonitorPanel />
	{/if}

	<!-- Keep the editor mounted: changing the view must not discard unsaved rules. -->
	<div class="sb-body" hidden={activeView !== 'rules'}>
		<FircGroupsPanel />
	</div>
</div>

<SideDrawer
	open={settingsDrawerOpen}
	title="Параметры FIRC"
	width={460}
	onClose={requestCloseSettingsDrawer}
>
	<FircSettingsPanel bind:hasUnsaved={settingsHasUnsaved} />
</SideDrawer>

<ConfirmModal
	open={confirmCloseSettingsOpen}
	title="Несохранённые параметры FIRC"
	message="В параметрах FIRC есть несохранённые изменения. Если закрыть панель сейчас, изменения не будут применены."
	confirmLabel="Закрыть без сохранения"
	cancelLabel="Остаться и сохранить"
	variant="danger"
	onConfirm={() => {
		confirmCloseSettingsOpen = false;
		settingsHasUnsaved = false;
		settingsDrawerOpen = false;
	}}
	onClose={() => {
		confirmCloseSettingsOpen = false;
	}}
/>

<style>
	.firc-views { display: flex; gap: .5rem; margin: .75rem 0; }
	.firc-views button { padding: .45rem 1rem; background: var(--bg-secondary); color: var(--text-secondary); border: 1px solid var(--border); border-radius: 6px; font: inherit; cursor: pointer; }
	.firc-views button.active { border-color: var(--accent); color: var(--text-primary); background: var(--color-accent-tint, var(--bg-tertiary)); }
	.firc-views button:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
	.sb-shell {
		display: flex;
		flex-direction: column;
		margin-top: 0.25rem;
		min-width: 0;
		max-width: 100%;
	}

	.sb-header {
		display: flex;
		align-items: center;
		gap: var(--sp-4, 1rem);
		padding-bottom: var(--sp-3, 0.75rem);
		border-bottom: 1px solid var(--border);
		flex-wrap: wrap;
	}

	.title-block {
		flex: 1 1 auto;
		min-width: 0;
	}

	.title-row {
		display: flex;
		align-items: center;
		gap: 0.625rem;
		flex-wrap: wrap;
	}

	.title {
		margin: 0;
		font-size: var(--fs-h3, 1.125rem);
		font-weight: 600;
		color: var(--text-primary);
		line-height: 1.25;
	}

	.status-pill {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		padding: 0.15rem 0.55rem;
		border-radius: 999px;
		font-family: var(--font-mono);
		font-size: 0.72rem;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		color: var(--text-muted);
	}

	.status-pill.is-running {
		color: var(--color-success, #9ece6a);
		border-color: color-mix(in srgb, var(--color-success, #9ece6a) 35%, var(--border));
		background: color-mix(in srgb, var(--color-success, #9ece6a) 10%, var(--bg-secondary));
	}

	.status-pill.is-recovery {
		color: var(--danger, #f7768e);
		border-color: color-mix(in srgb, var(--danger, #f7768e) 40%, var(--border));
		background: color-mix(in srgb, var(--danger, #f7768e) 12%, var(--bg-secondary));
	}

	.subtitle {
		margin-top: 3px;
		font-size: var(--fs-sm, 0.8125rem);
		color: var(--text-muted);
	}

	.header-tools {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
		min-width: 0;
		max-width: 100%;
	}

	.params-btn,
	.icon-btn {
		height: 32px;
		box-sizing: border-box;
	}

	.params-btn {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		padding: 0 12px;
		border-radius: var(--radius-sm, 6px);
		background: color-mix(in srgb, var(--accent) 12%, var(--bg-secondary));
		border: 1px solid color-mix(in srgb, var(--accent) 38%, var(--border));
		color: var(--text-primary);
		font-size: 12px;
		font-weight: 600;
		font-family: inherit;
		cursor: pointer;
		white-space: nowrap;
		flex-shrink: 0;
		transition:
			background 0.15s ease,
			border-color 0.15s ease;
	}

	.params-btn:hover {
		background: color-mix(in srgb, var(--accent) 20%, var(--bg-tertiary));
		border-color: var(--accent);
	}

	.icon-btn {
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		color: var(--text-secondary);
		padding: 0 8px;
		border-radius: var(--radius-sm, 6px);
		cursor: pointer;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		flex-shrink: 0;
		transition:
			color 0.15s ease,
			border-color 0.15s ease,
			background 0.15s ease;
	}

	.icon-btn:hover:not(:disabled) {
		color: var(--text-primary);
		background: var(--bg-tertiary);
		border-color: var(--border-hover, var(--accent));
	}

	.icon-btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.firc-error-banner {
		padding: 0.65rem 0.9rem;
		margin-bottom: 0.75rem;
		border-radius: 8px;
		background: rgba(247, 118, 142, 0.12);
		border: 1px solid rgba(247, 118, 142, 0.35);
		color: var(--danger, #f7768e);
		font-size: 0.8125rem;
	}

	.firc-recovery-banner {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 1rem;
		padding: 0.75rem 1rem;
		margin-bottom: 0.75rem;
		border-radius: 8px;
		background: rgba(247, 118, 142, 0.12);
		border: 1px solid rgba(247, 118, 142, 0.4);
		color: var(--text-primary);
		font-size: 0.8125rem;
	}

	.firc-recovery-banner .recovery-content {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		min-width: 0;
	}

	.firc-recovery-banner .recovery-title {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		font-weight: 600;
		color: var(--danger, #f7768e);
	}

	.firc-recovery-banner .recovery-text {
		color: var(--text-secondary);
		font-size: 0.75rem;
		line-height: 1.35;
		word-break: break-word;
	}

	.firc-recovery-banner .recovery-actions {
		flex-shrink: 0;
	}

	.sb-body {
		width: 100%;
	}
</style>
