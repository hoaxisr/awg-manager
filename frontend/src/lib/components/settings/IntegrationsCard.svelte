<script lang="ts">
	import type { SingboxStatus, HydraRouteStatus } from '$lib/types';
	import { Button, ConfirmModal, Modal, StatusDot } from '$lib/components/ui';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { singboxInstallProgress } from '$lib/stores/singboxInstall';
	import { formatBytes } from '$lib/utils/format';
	import { stripAnsi } from '$lib/utils/ansi';
	import { Blocks } from 'lucide-svelte';

	interface Props {
		singboxStatus: SingboxStatus | null;
		singboxStatusLoading?: boolean;
		hydraStatus: HydraRouteStatus | null;
		hydraStatusLoading?: boolean;
		hydraStatusError?: string | null;
		singboxInstalling: boolean;
		singboxUpdating?: boolean;
		singboxInstallError: string | null;
		singboxUpdateError?: string | null;
		oninstallSingbox: () => void;
		onupdateSingbox?: () => void;
		onuninstallSingbox?: () => void;
		singboxUninstalling?: boolean;
		showSingbox?: boolean;
		showHydra?: boolean;
	}

	let {
		singboxStatus,
		singboxStatusLoading = false,
		hydraStatus,
		hydraStatusLoading = false,
		hydraStatusError = null,
		singboxInstalling,
		singboxUpdating = false,
		singboxInstallError,
		singboxUpdateError = null,
		oninstallSingbox,
		onupdateSingbox,
		onuninstallSingbox,
		singboxUninstalling = false,
		showSingbox = true,
		showHydra = true,
	}: Props = $props();

	let confirmUninstall = $state(false);

	const singboxInstalled = $derived(singboxStatus?.installed ?? false);
	const singboxRunning = $derived(singboxStatus?.running ?? false);
	const singboxNeedsUpdate = $derived(singboxStatus?.updateAvailable ?? false);
	const hydraInstalled = $derived(hydraStatus?.installed ?? false);
	const hydraRunning = $derived(hydraStatus?.running ?? false);
	const hydraProcessState = $derived(
		hydraStatus?.processState ?? (hydraStatus?.running ? 'running' : hydraStatus?.installed ? 'stopped' : 'not_installed')
	);
	const singboxFatalLines = $derived.by(() => {
		const raw = stripAnsi(singboxStatus?.lastError ?? '').trim();
		if (!raw) return '';
		// Match backend stderrLineIndicatesSingBoxFatal: real sing-box text
		// fatals start with "+TZO YYYY-MM-DD …" or contain "FATAL[" — avoid
		// JSON keys like "type":"fatal" polluting the settings card.
		const fatal = raw.split('\n').filter((l) => {
			const u = l.toUpperCase();
			if (!u.includes('FATAL')) return false;
			if (u.includes('FATAL[')) return true;
			return /^\s*\+[0-9]{1,4}\s+\d{4}-\d{2}-\d{2}\b/.test(l);
		});
		return fatal.join('\n');
	});

	const installProgress = $derived($singboxInstallProgress);
	const installPhaseLabel = $derived.by(() => {
		const p = installProgress;
		if (!p) return '';
		switch (p.phase) {
			case 'download':
				if (p.total > 0) {
					const pct = Math.min(100, Math.round((p.downloaded / p.total) * 100));
					return `Скачивание ${pct}% (${formatBytes(p.downloaded)} / ${formatBytes(p.total)})`;
				}
				return `Скачивание (${formatBytes(p.downloaded)})`;
			case 'activate':
				return 'Установка…';
			case 'stop':
				return 'Остановка sing-box…';
			case 'start':
				return 'Запуск sing-box…';
			case 'done':
				return 'Готово';
			case 'error':
				return p.error ? `Ошибка: ${p.error}` : 'Ошибка';
			default:
				return '';
		}
	});
	const installProgressPct = $derived.by(() => {
		const p = installProgress;
		if (!p || p.phase !== 'download' || p.total <= 0) return null;
		return Math.min(100, Math.round((p.downloaded / p.total) * 100));
	});
	const errorModalTitle = $derived(singboxUpdateError ? 'Не удалось обновить sing-box' : 'Не удалось установить sing-box');

	let errorModalOpen = $state(false);

	function showErrorDetails() {
		errorModalOpen = true;
	}

	async function copyError() {
		const err = singboxInstallError ?? singboxUpdateError;
		if (err) {
			await copyToClipboard(err);
		}
	}

	// Auto-close modal when the upstream error is cleared (e.g. successful retry).
	$effect(() => {
		if (singboxInstallError === null && singboxUpdateError === null) {
			errorModalOpen = false;
		}
	});
</script>

{#if showSingbox || showHydra}
	<div class="settings-block">
		<div class="card">
		<SettingsSectionLabel label="Интеграции" icon={Blocks} tone="purple" header />
		{#if showSingbox}
			<div class="setting-row">
				<div class="integration-item">
					<StatusDot
						variant={singboxStatusLoading ? 'muted' : (singboxInstalled && singboxRunning ? 'success' : 'muted')}
						size="md"
						ariaLabel={
							singboxStatusLoading
								? 'Sing-box: получение данных'
								: singboxInstalled && singboxRunning
									? 'Sing-box работает'
									: 'Sing-box остановлен'
						}
					/>
					<div class="integration-meta">
						<span class="font-medium">Sing-box</span>
						{#if singboxStatusLoading}
							<span class="integration-sub">получаю данные…</span>
						{:else if singboxInstalled && singboxStatus}
							<span class="integration-sub">
								v{singboxStatus.version ?? singboxStatus.currentVersion ?? '?'}
								{#if singboxRunning && singboxStatus.pid}· pid {singboxStatus.pid}{:else if !singboxRunning}· остановлен{/if}
							</span>
							{#if singboxNeedsUpdate}
								<span class="setting-description warning">
									Требуется обновление: {singboxStatus.currentVersion ?? '—'} → {singboxStatus.requiredVersion}
								</span>
							{/if}
							{#if singboxFatalLines}
								<span class="setting-description warning" title={singboxFatalLines}>{singboxFatalLines}</span>
							{/if}
							{#if singboxUpdateError}
								<span class="install-error-row">
									<span class="install-error-label">Не удалось обновить</span>
									<Button variant="ghost" size="sm" onclick={showErrorDetails}>
										Подробнее
									</Button>
								</span>
							{/if}
						{:else}
							<span class="setting-description">
								Поддержка VLESS/Reality, Hysteria2, NaiveProxy. Требует Entware на внешнем носителе.
							</span>
							{#if singboxInstallError}
								<span class="install-error-row">
									<span class="install-error-label">Не удалось установить</span>
									<Button variant="ghost" size="sm" onclick={showErrorDetails}>
										Подробнее
									</Button>
								</span>
							{/if}
						{/if}
					</div>
				</div>
				{#if installProgress}
					<div class="progress-widget" class:progress-error={installProgress.phase === 'error'} class:progress-done={installProgress.phase === 'done'}>
						<div class="progress-label">{installPhaseLabel}</div>
						<div class="progress-bar" class:indeterminate={installProgressPct === null && installProgress.phase !== 'done' && installProgress.phase !== 'error'}>
							<div
								class="progress-fill"
								style:width={installProgressPct !== null ? `${installProgressPct}%` : '100%'}
							></div>
						</div>
					</div>
				{:else if singboxInstalled}
					<div class="integration-actions">
						{#if singboxNeedsUpdate && onupdateSingbox}
							<Button variant="primary" size="sm" onclick={onupdateSingbox} loading={singboxUpdating}>
								{singboxUpdating ? 'Обновление...' : 'Обновить'}
							</Button>
						{:else}
							<Button variant="secondary" size="sm" href="/?tab=singbox">Открыть</Button>
						{/if}
						{#if onuninstallSingbox}
							<Button
								variant="outline-danger"
								size="sm"
								loading={singboxUninstalling}
								onclick={() => (confirmUninstall = true)}
							>
								{singboxUninstalling ? 'Удаление...' : 'Удалить'}
							</Button>
						{/if}
					</div>
				{:else if singboxStatusLoading}
					<Button variant="secondary" size="sm" disabled>Ожидание…</Button>
				{:else}
					<Button variant="primary" size="sm" onclick={oninstallSingbox} loading={singboxInstalling}>
						{singboxInstalling ? 'Установка...' : 'Установить'}
					</Button>
				{/if}
			</div>
		{/if}

		{#if showHydra}
			<div class="setting-row">
				<div class="integration-item">
					<StatusDot
						variant={hydraStatusLoading ? 'muted' : (hydraInstalled && hydraRunning ? 'success' : 'muted')}
						size="md"
						ariaLabel={
							hydraStatusLoading
								? 'HydraRoute: получение данных'
								: hydraProcessState === 'dead'
									? 'HydraRoute: stale pid'
									: hydraInstalled && hydraRunning
									? 'HydraRoute работает'
									: 'HydraRoute остановлен'
						}
					/>
					<div class="integration-meta">
						<span class="font-medium">HydraRoute Neo</span>
						{#if hydraStatusLoading}
							<span class="integration-sub">получаю данные…</span>
						{:else if hydraInstalled}
							<span class="integration-sub">
								v{hydraStatus?.version ?? '?'}
								{#if hydraRunning && hydraStatus?.pid}
									· pid {hydraStatus.pid}
								{:else if hydraProcessState === 'dead' && hydraStatus?.stalePid}
									· dead pid {hydraStatus.stalePid}
								{:else}
									· остановлен
								{/if}
							</span>
						{:else}
							<span class="integration-sub">не установлен</span>
						{/if}
						{#if !hydraRunning && hydraStatus?.lastError}
							<span class="setting-description warning" title={hydraStatus.lastError}>{hydraStatus.lastError}</span>
						{/if}
						{#if !hydraStatusLoading && !hydraStatus && hydraStatusError}
							<span class="setting-description warning">нет ответа: {hydraStatusError}</span>
						{/if}
					</div>
				</div>
				{#if hydraInstalled}
					<Button variant="secondary" size="sm" href="/routing?tab=hrneo">Открыть</Button>
				{:else if hydraStatusLoading}
					<Button variant="secondary" size="sm" disabled>Ожидание…</Button>
				{:else}
					<Button
						variant="outline-primary"
						size="sm"
						href="https://github.com/Ground-Zerro/HydraRoute"
						target="_blank"
						rel="noopener noreferrer"
					>
						Установить
					</Button>
				{/if}
			</div>
		{/if}
		</div>
	</div>
{/if}

<Modal
	open={errorModalOpen}
	title={errorModalTitle}
	size="lg"
	onclose={() => (errorModalOpen = false)}
>
	<pre class="error-pre">{singboxInstallError ?? singboxUpdateError ?? ''}</pre>
	{#snippet actions()}
		<Button variant="ghost" size="sm" onclick={copyError}>Скопировать</Button>
		<Button variant="primary" size="sm" onclick={() => (errorModalOpen = false)}>
			Закрыть
		</Button>
	{/snippet}
</Modal>

{#if confirmUninstall}
	<ConfirmModal
		open={confirmUninstall}
		title="Удалить sing-box?"
		message="Движок будет остановлен, а его файлы удалены: бинарь, конфигурация config.d, кэш FakeIP и журналы."
		secondary="Подписки, правила маршрутизации и настройки прокси сохранятся — после повторной установки они снова заработают."
		confirmLabel="Удалить"
		variant="danger"
		busy={singboxUninstalling}
		onConfirm={() => {
			confirmUninstall = false;
			onuninstallSingbox?.();
		}}
		onClose={() => (confirmUninstall = false)}
	/>
{/if}

<style>
	.integration-actions {
		display: flex;
		gap: 0.35rem;
		align-items: center;
	}

	.card {
		container-type: inline-size;
	}

	.setting-row {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto;
		align-items: start;
		gap: 0.75rem;
	}

	.integration-item {
		display: flex;
		align-items: center;
		gap: 0.625rem;
		min-width: 0;
		flex: 1;
	}

	.integration-meta {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
		min-width: 0;
	}

	.integration-meta .setting-description {
		min-width: 0;
	}

	.integration-meta .setting-description.warning {
		white-space: pre-wrap;
	}

	.integration-sub {
		font-size: 0.6875rem;
		font-family: var(--font-mono);
		color: var(--color-text-muted);
	}
	.warning {
		color: var(--color-warning);
	}
	.install-error-row {
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
	}
	.install-error-label {
		color: var(--color-error);
		font-size: 0.8125rem;
	}
	.error-pre {
		margin: 0;
		padding: 0.75rem;
		background: var(--color-settings-control-bg);
		border-radius: var(--radius-sm);
		font-family: var(--font-mono);
		font-size: 0.75rem;
		white-space: pre-wrap;
		word-break: break-word;
		max-height: 50vh;
		overflow: auto;
	}

	.progress-widget {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
		min-width: 0;
		grid-column: 1 / -1;
	}

	/* Same action-button floor as settings actions-card (fits «Обновление…»). */
	@media (min-width: 641px) {
		.setting-row > :global(.btn) {
			justify-self: end;
			align-self: center;
			min-width: 7.5rem;
		}
	}

	@media (min-width: 901px) {
		.setting-row {
			grid-template-columns: minmax(0, 1fr) auto;
			align-items: start;
			gap: 0.75rem;
		}

		.integration-item {
			display: grid;
			grid-template-columns: 8px minmax(0, 1fr);
			align-items: flex-start;
			column-gap: 0.625rem;
		}

		.integration-item :global(.dot) {
			margin-top: 0.42rem;
		}

		.integration-meta {
			min-width: 0;
		}
	}

	@media (max-width: 640px) {
		.integration-item {
			display: grid;
			grid-template-columns: 8px minmax(0, 1fr);
			align-items: start;
			column-gap: 0.625rem;
		}

		.integration-item :global(.dot) {
			margin-top: 0.42rem;
		}

		@container (max-width: 420px) {
			.setting-row {
				grid-template-columns: minmax(0, 1fr) auto;
				align-items: center;
				gap: 0.625rem;
			}

			.setting-row > :global(.btn) {
				justify-self: end;
				align-self: center;
				min-width: 7.5rem;
			}
		}
	}
	.progress-label {
		font-size: 0.78rem;
		color: var(--color-text-primary);
		font-variant-numeric: tabular-nums;
	}
	.progress-bar {
		position: relative;
		height: 6px;
		background: var(--color-settings-control-bg, rgba(0, 0, 0, 0.08));
		border-radius: 3px;
		overflow: hidden;
	}
	.progress-fill {
		position: absolute;
		left: 0;
		top: 0;
		bottom: 0;
		background: var(--color-primary, #3b82f6);
		transition: width 120ms ease-out;
	}
	.progress-bar.indeterminate .progress-fill {
		background: linear-gradient(
			90deg,
			transparent 0%,
			var(--color-primary, #3b82f6) 50%,
			transparent 100%
		);
		background-size: 200% 100%;
		animation: indeterminate-slide 1.2s linear infinite;
		width: 100% !important;
	}
	.progress-widget.progress-error .progress-fill {
		background: var(--color-error, #ef4444);
	}
	.progress-widget.progress-done .progress-fill {
		background: var(--color-success, #10b981);
	}
	@keyframes indeterminate-slide {
		0% { background-position: 200% 0; }
		100% { background-position: -100% 0; }
	}
</style>
