<script lang="ts">
	import type { Snippet } from 'svelte';
	import { m, uiText, type UiText } from '$lib/i18n';
	import type { SingboxStatus } from '$lib/types';
	import { Button, ConfirmModal, Input, Modal } from '$lib/components/ui';
	import ComponentRow from './ComponentRow.svelte';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';
	import { pageDraft } from './pageState.svelte';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { singboxInstallProgress } from '$lib/stores/singboxInstall';
	import { formatBytes } from '$lib/utils/format';
	import { stripAnsi } from '$lib/utils/ansi';
	import { Blocks } from 'lucide-svelte';
	import { isIPv4, isIPv6 } from '$lib/utils/cidr';

	interface Props {
		singboxStatus: SingboxStatus | null;
		singboxStatusLoading?: boolean;
		singboxInstalling: boolean;
		singboxUpdating?: boolean;
		singboxInstallError: UiText | null;
		singboxUpdateError?: UiText | null;
		oninstallSingbox: () => void;
		onupdateSingbox?: () => void;
		onuninstallSingbox?: () => void;
		singboxUninstalling?: boolean;
		/** Адрес bootstrap-резолвера sing-box; пусто = адрес не навязывается. */
		bootstrapDNS?: string;
		onsaveBootstrapDNS?: (value: string) => void;
		bootstrapSaving?: boolean;
		/** Порт Clash API sing-box; 0 или пусто = порт по умолчанию. */
		clashPort?: number;
		onsaveClashPort?: (value: number) => void;
		clashPortSaving?: boolean;
		clashPortError?: string | null;
		/** Управление процессом; без обработчика строки нет. */
		oncontrol?: (action: 'start' | 'stop' | 'restart') => void;
		controlBusy?: boolean;
		/** Строки под настройками движка (NDMS Proxy). */
		children?: Snippet;
	}

	let {
		singboxStatus,
		singboxStatusLoading = false,
		singboxInstalling,
		singboxUpdating = false,
		singboxInstallError,
		singboxUpdateError = null,
		oninstallSingbox,
		onupdateSingbox,
		onuninstallSingbox,
		singboxUninstalling = false,
		bootstrapDNS = '',
		onsaveBootstrapDNS,
		bootstrapSaving = false,
		clashPort = 0,
		onsaveClashPort,
		clashPortSaving = false,
		clashPortError = null,
		oncontrol,
		controlBusy = false,
		children,
	}: Props = $props();

	// Копии Go-констант: singbox.DefaultClashPort и api.minClashPort. Фронт не
	// импортирует Go, а бэкенд остаётся авторитетом — невалидное он отвергнет
	// и без нас. Здесь они только чтобы подсказка и placeholder не врали, так
	// что при смене первоисточника поправить нужно и тут.
	const DEFAULT_CLASH_PORT = 9099;
	const MIN_CLASH_PORT = 1024;
	const MAX_CLASH_PORT = 65535;

	const bootstrapDraft = pageDraft('bootstrap-dns', () => bootstrapDNS);

	// Bootstrap отвечает раньше любого другого DNS, поэтому домен здесь
	// неработоспособен — принимаем только литеральный IP.
	const bootstrapValid = $derived.by(() => {
		const v = bootstrapDraft.value.trim();
		return v === '' || isIPv4(v) || isIPv6(v);
	});
	const bootstrapDirty = $derived(bootstrapDraft.value.trim() !== bootstrapDNS);

	const clashPortDraft = pageDraft('clash-port', () => String(clashPort || DEFAULT_CLASH_PORT));
	const clashPortValue = $derived(Number(clashPortDraft.value.trim()));
	const clashPortValid = $derived(
		/^\d+$/.test(clashPortDraft.value.trim()) &&
			clashPortValue >= MIN_CLASH_PORT &&
			clashPortValue <= MAX_CLASH_PORT
	);
	const clashPortDirty = $derived(clashPortValue !== (clashPort || DEFAULT_CLASH_PORT));

	let confirmUninstall = $state(false);

	const singboxInstalled = $derived(singboxStatus?.installed ?? false);
	const singboxRunning = $derived(singboxStatus?.running ?? false);
	const singboxNeedsUpdate = $derived(singboxStatus?.updateAvailable ?? false);
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
					return m.settings_integrations_phase_downloading_pct({ pct, downloaded: formatBytes(p.downloaded), total: formatBytes(p.total) });
				}
				return m.settings_integrations_phase_downloading({ downloaded: formatBytes(p.downloaded) });
			case 'activate':
				return m.settings_integrations_phase_installing();
			case 'stop':
				return m.settings_integrations_phase_stopping();
			case 'start':
				return m.settings_integrations_phase_starting();
			case 'done':
				return m.common_done();
			case 'error':
				return p.error ? m.settings_integrations_phase_error_detail({ error: p.error }) : m.common_error();
			default:
				return '';
		}
	});
	const installProgressPct = $derived.by(() => {
		const p = installProgress;
		if (!p || p.phase !== 'download' || p.total <= 0) return null;
		return Math.min(100, Math.round((p.downloaded / p.total) * 100));
	});
	const errorModalTitle = $derived(singboxUpdateError ? m.settings_integrations_error_title_update() : m.settings_integrations_error_title_install());

	let errorModalOpen = $state(false);

	function showErrorDetails() {
		errorModalOpen = true;
	}

	async function copyError() {
		const err = singboxInstallError ?? singboxUpdateError;
		if (err) {
			await copyToClipboard(uiText(err));
		}
	}

	// Auto-close modal when the upstream error is cleared (e.g. successful retry).
	$effect(() => {
		if (singboxInstallError === null && singboxUpdateError === null) {
			errorModalOpen = false;
		}
	});
</script>

<div class="settings-block">
	<div class="card">
		<SettingsSectionLabel label="Sing-box" icon={Blocks} tone="purple" header />
		<ComponentRow
			label="Sing-box"
			running={!singboxStatusLoading && singboxInstalled && singboxRunning}
			ariaLabel={singboxStatusLoading
				? m.settings_integrations_singbox_loading_aria()
				: singboxInstalled && singboxRunning
					? m.settings_integrations_singbox_running_aria()
					: m.settings_integrations_singbox_stopped_aria()}
			actions={installProgress ? undefined : engineActions}
			progress={installProgress ? installProgressBar : undefined}
		>
			{#if singboxStatusLoading}
				<span class="component-sub">{m.settings_integrations_loading()}</span>
			{:else if singboxInstalled && singboxStatus}
				<span class="component-sub">
					v{singboxStatus.version ?? singboxStatus.currentVersion ?? '?'}
					{#if singboxRunning && singboxStatus.pid}· pid {singboxStatus.pid}{:else if !singboxRunning}· {m.settings_integrations_stopped()}{/if}
				</span>
				{#if singboxNeedsUpdate}
					<span class="setting-description warning">
						{m.settings_integrations_needs_update({ current: singboxStatus.currentVersion ?? '—', required: singboxStatus.requiredVersion ?? '' })}
					</span>
				{/if}
				{#if singboxFatalLines}
					<span class="setting-description warning" title={singboxFatalLines}>{singboxFatalLines}</span>
				{/if}
				{#if singboxUpdateError}
					<span class="install-error-row">
						<span class="install-error-label">{m.settings_integrations_update_failed()}</span>
						<Button variant="ghost" size="sm" onclick={showErrorDetails}>
							{m.settings_integrations_details()}
						</Button>
					</span>
				{/if}
			{:else}
				<span class="setting-description">
					{m.settings_integrations_singbox_description()}
				</span>
				{#if singboxInstallError}
					<span class="install-error-row">
						<span class="install-error-label">{m.settings_integrations_install_failed()}</span>
						<Button variant="ghost" size="sm" onclick={showErrorDetails}>
							{m.settings_integrations_details()}
						</Button>
					</span>
				{/if}
			{/if}
		</ComponentRow>
		{#if singboxInstalled && oncontrol}
			<ComponentRow label={m.settings_page_process_label()}>
				<span class="component-sub">
					{singboxRunning ? m.settings_page_process_running() : m.settings_page_process_stopped()}
				</span>
				{#snippet actions()}
					{#if singboxRunning}
						<span title={singboxNeedsUpdate ? m.settings_page_singbox_update_first({ version: singboxStatus?.requiredVersion ?? '' }) : ''}>
							<Button
								variant="secondary"
								size="sm"
								onclick={() => oncontrol('restart')}
								loading={controlBusy}
								disabled={singboxNeedsUpdate}
							>
								{m.common_restart()}
							</Button>
						</span>
						<Button variant="danger" size="sm" onclick={() => oncontrol('stop')} loading={controlBusy}>{m.common_stop()}</Button>
					{:else}
						<Button variant="success" size="sm" onclick={() => oncontrol('start')} loading={controlBusy}>{m.common_start()}</Button>
					{/if}
				{/snippet}
			</ComponentRow>
		{/if}
		{#if singboxInstalled && onsaveBootstrapDNS}
			<div class="setting-row settings-stacked-row component-indent">
				<div class="flex flex-col gap-1">
					<span class="font-medium">Bootstrap-DNS</span>
					<span class="setting-description">
						{m.settings_integrations_bootstrap_description_1()}
					</span>
					<span class="setting-description">
						{m.settings_integrations_bootstrap_description_2()}
					</span>
				</div>
				<div class="field-save">
					<Input
						type="text"
						bind:value={bootstrapDraft.value}
						placeholder="1.1.1.1"
						disabled={bootstrapSaving}
						error={bootstrapValid ? undefined : m.settings_integrations_bootstrap_invalid()}
						fullWidth
					/>
					<Button
						variant="secondary"
						size="sm"
						loading={bootstrapSaving}
						disabled={!bootstrapValid || !bootstrapDirty || bootstrapSaving}
						onclick={() => onsaveBootstrapDNS?.(bootstrapDraft.value.trim())}
					>
						{m.common_save()}
					</Button>
				</div>
			</div>
		{/if}
		{#if singboxInstalled && onsaveClashPort}
			<div class="setting-row settings-stacked-row component-indent">
				<div class="flex flex-col gap-1">
					<span class="font-medium">{m.settings_integrations_clash_label()}</span>
					<span class="setting-description">
						{m.settings_integrations_clash_description_1()}
					</span>
					<span class="setting-description">
						{m.settings_integrations_clash_description_2({ port: DEFAULT_CLASH_PORT })}
					</span>
				</div>
				<div class="field-save">
					<Input
						type="text"
						bind:value={clashPortDraft.value}
						placeholder={String(DEFAULT_CLASH_PORT)}
						disabled={clashPortSaving}
						error={clashPortValid ? (clashPortError ?? undefined) : m.settings_integrations_clash_invalid({ min: MIN_CLASH_PORT, max: MAX_CLASH_PORT })}
						fullWidth
					/>
					<Button
						variant="secondary"
						size="sm"
						loading={clashPortSaving}
						disabled={!clashPortValid || !clashPortDirty || clashPortSaving}
						onclick={() => onsaveClashPort?.(clashPortValue)}
					>
						{m.common_save()}
					</Button>
				</div>
			</div>
		{/if}
		{#if singboxInstalled}
			{@render children?.()}
		{/if}
	</div>
</div>

{#snippet engineActions()}
	{#if singboxInstalled}
		{#if singboxNeedsUpdate && onupdateSingbox}
			<Button variant="primary" size="sm" onclick={onupdateSingbox} loading={singboxUpdating}>
				{singboxUpdating ? m.settings_integrations_updating() : m.common_update()}
			</Button>
		{:else}
			<Button variant="secondary" size="sm" href="/?tab=singbox">{m.settings_integrations_open()}</Button>
		{/if}
		{#if onuninstallSingbox}
			<Button
				variant="outline-danger"
				size="sm"
				loading={singboxUninstalling}
				onclick={() => (confirmUninstall = true)}
			>
				{singboxUninstalling ? m.settings_integrations_uninstalling() : m.common_delete()}
			</Button>
		{/if}
	{:else if singboxStatusLoading}
		<Button variant="secondary" size="sm" disabled>{m.settings_integrations_waiting()}</Button>
	{:else}
		<Button variant="primary" size="sm" onclick={oninstallSingbox} loading={singboxInstalling}>
			{singboxInstalling ? m.settings_integrations_installing() : m.settings_integrations_install()}
		</Button>
	{/if}
{/snippet}

{#snippet installProgressBar()}
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
	{/if}
{/snippet}

<Modal
	open={errorModalOpen}
	title={errorModalTitle}
	size="lg"
	onclose={() => (errorModalOpen = false)}
>
	<pre class="error-pre">{uiText(singboxInstallError ?? singboxUpdateError)}</pre>
	{#snippet actions()}
		<Button variant="ghost" size="sm" onclick={copyError}>{m.settings_integrations_copy()}</Button>
		<Button variant="primary" size="sm" onclick={() => (errorModalOpen = false)}>
			{m.common_close()}
		</Button>
	{/snippet}
</Modal>

{#if confirmUninstall}
	<ConfirmModal
		open={confirmUninstall}
		title={m.settings_integrations_remove_singbox_title()}
		message={m.settings_integrations_remove_singbox_message()}
		secondary={m.settings_integrations_remove_singbox_secondary()}
		confirmLabel={m.common_delete()}
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
	.field-save {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.field-save :global(.field) {
		flex: 1;
		min-width: 0;
		max-width: 16rem;
	}

	.setting-description.warning {
		white-space: pre-wrap;
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
