<script lang="ts">
	import { m } from '$lib/i18n';
	import type { SingboxStatus, HydraRouteStatus } from '$lib/types';
	import type { OperationalState } from '$lib/types/adaptiveRouting';
	import type { ProxySubsystem } from '$lib/api/proxyInstances';
	import { Button, ConfirmModal, Input, Modal, StatusDot } from '$lib/components/ui';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { singboxInstallProgress } from '$lib/stores/singboxInstall';
	import { formatBytes } from '$lib/utils/format';
	import { stripAnsi } from '$lib/utils/ansi';
	import { Blocks } from 'lucide-svelte';
	import { isIPv4, isIPv6 } from '$lib/utils/cidr';

	/** Строка подсистемы прокси: её бинари ставятся и снимаются целиком. */
	export interface ProxyBinaryRow {
		key: ProxySubsystem;
		label: string;
		/** Бинари подсистемы лежат на диске. */
		present: boolean;
		installAvailable: boolean;
		updateAvailable: boolean;
		installedVersion?: string;
		installVersion?: string;
		busy: boolean;
		/** Инстансы подсистемы, включая выключенные: гейт удаления. */
		instances: number;
		oninstall: () => void;
		onuninstall: () => void;
	}

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
		/** Susanin (Адаптивная маршрутизация) */
		susaninStatus?: OperationalState | null;
		susaninStatusLoading?: boolean;
		susaninInstalling?: boolean;
		susaninRestarting?: boolean;
		susaninUninstalling?: boolean;
		oninstallSusanin?: () => void;
		onrestartSusanin?: () => void;
		onuninstallSusanin?: () => void;
		showSusanin?: boolean;
		/** Адрес bootstrap-резолвера sing-box; пусто = адрес не навязывается. */
		bootstrapDNS?: string;
		onsaveBootstrapDNS?: (value: string) => void;
		bootstrapSaving?: boolean;
		/** Порт Clash API sing-box; 0 или пусто = порт по умолчанию. */
		clashPort?: number;
		onsaveClashPort?: (value: number) => void;
		clashPortSaving?: boolean;
		clashPortError?: string | null;
		/** Подсистемы прокси (WDTT, FreeTurn); пусто — блок не рисуется. */
		proxyBinaries?: ProxyBinaryRow[];
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
		susaninStatus = null,
		susaninStatusLoading = false,
		susaninInstalling = false,
		susaninRestarting = false,
		susaninUninstalling = false,
		oninstallSusanin,
		onrestartSusanin,
		onuninstallSusanin,
		showSusanin = true,
		bootstrapDNS = '',
		onsaveBootstrapDNS,
		bootstrapSaving = false,
		clashPort = 0,
		onsaveClashPort,
		clashPortSaving = false,
		clashPortError = null,
		proxyBinaries = [],
	}: Props = $props();

	// Подсистема, ожидающая подтверждения удаления.
	let confirmProxy = $state<ProxyBinaryRow | null>(null);
	let confirmUninstallSusanin = $state(false);

	const susaninInstalled = $derived(susaninStatus?.installed ?? false);
	const susaninRunning = $derived(susaninStatus?.status === 'running' || susaninStatus?.status === 'learning');

	// Копии Go-констант: singbox.DefaultClashPort и api.minClashPort. Фронт не
	// импортирует Go, а бэкенд остаётся авторитетом — невалидное он отвергнет
	// и без нас. Здесь они только чтобы подсказка и placeholder не врали, так
	// что при смене первоисточника поправить нужно и тут.
	const DEFAULT_CLASH_PORT = 9099;
	const MIN_CLASH_PORT = 1024;
	const MAX_CLASH_PORT = 65535;

	// Черновик поля bootstrap-DNS. Настройки на странице грузятся один раз,
	// внешних обновлений у этого props нет — ресинк не нужен, начальное
	// значение снимается однократно.
	// svelte-ignore state_referenced_locally
	let bootstrapDraft = $state(bootstrapDNS);

	// Bootstrap отвечает раньше любого другого DNS, поэтому домен здесь
	// неработоспособен — принимаем только литеральный IP.
	const bootstrapValid = $derived.by(() => {
		const v = bootstrapDraft.trim();
		return v === '' || isIPv4(v) || isIPv6(v);
	});
	const bootstrapDirty = $derived(bootstrapDraft.trim() !== bootstrapDNS);

	// Черновик порта Clash API — та же схема, что у bootstrap-DNS: настройки
	// грузятся один раз, внешних обновлений props нет, ресинк не нужен.
	// svelte-ignore state_referenced_locally
	let clashPortDraft = $state(String(clashPort || DEFAULT_CLASH_PORT));
	const clashPortValue = $derived(Number(clashPortDraft.trim()));
	const clashPortValid = $derived(
		/^\d+$/.test(clashPortDraft.trim()) &&
			clashPortValue >= MIN_CLASH_PORT &&
			clashPortValue <= MAX_CLASH_PORT
	);
	const clashPortDirty = $derived(clashPortValue !== (clashPort || DEFAULT_CLASH_PORT));

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

{#if showSingbox || showHydra || proxyBinaries.length > 0}
	<div class="settings-block">
		<div class="card">
		<SettingsSectionLabel label={m.settings_integrations_title()} icon={Blocks} tone="purple" header />
		{#if showSingbox}
			<div class="setting-row">
				<div class="integration-item">
					<StatusDot
						variant={singboxStatusLoading ? 'muted' : (singboxInstalled && singboxRunning ? 'success' : 'muted')}
						size="md"
						ariaLabel={
							singboxStatusLoading
								? m.settings_integrations_singbox_loading_aria()
								: singboxInstalled && singboxRunning
									? m.settings_integrations_singbox_running_aria()
									: m.settings_integrations_singbox_stopped_aria()
						}
					/>
					<div class="integration-meta">
						<span class="font-medium">Sing-box</span>
						{#if singboxStatusLoading}
							<span class="integration-sub">{m.settings_integrations_loading()}</span>
						{:else if singboxInstalled && singboxStatus}
							<span class="integration-sub">
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
					</div>
				{:else if singboxStatusLoading}
					<Button variant="secondary" size="sm" disabled>{m.settings_integrations_waiting()}</Button>
				{:else}
					<Button variant="primary" size="sm" onclick={oninstallSingbox} loading={singboxInstalling}>
						{singboxInstalling ? m.settings_integrations_installing() : m.settings_integrations_install()}
					</Button>
				{/if}
			</div>
			{#if singboxInstalled && onsaveBootstrapDNS}
				<div class="setting-row bootstrap-row">
					<div class="integration-meta">
						<span class="font-medium">Bootstrap-DNS</span>
						<span class="setting-description">
							{m.settings_integrations_bootstrap_description_1()}
						</span>
						<span class="setting-description">
							{m.settings_integrations_bootstrap_description_2()}
						</span>
					</div>
					<div class="bootstrap-field">
						<Input
							type="text"
							bind:value={bootstrapDraft}
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
							onclick={() => onsaveBootstrapDNS?.(bootstrapDraft.trim())}
						>
							{m.common_save()}
						</Button>
					</div>
				</div>
			{/if}
			{#if singboxInstalled && onsaveClashPort}
				<div class="setting-row bootstrap-row">
					<div class="integration-meta">
						<span class="font-medium">{m.settings_integrations_clash_label()}</span>
						<span class="setting-description">
							{m.settings_integrations_clash_description_1()}
						</span>
						<span class="setting-description">
							{m.settings_integrations_clash_description_2({ port: DEFAULT_CLASH_PORT })}
						</span>
					</div>
					<div class="bootstrap-field">
						<Input
							type="text"
							bind:value={clashPortDraft}
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
		{/if}

		{#if showSusanin}
			<div class="setting-row">
				<div class="integration-item">
					<StatusDot
						variant={susaninStatusLoading ? 'muted' : (susaninInstalled && susaninRunning ? 'success' : 'muted')}
						size="md"
						ariaLabel={
							susaninStatusLoading
								? 'Susanin: получение данных'
								: susaninInstalled && susaninRunning
									? 'Susanin работает'
									: 'Susanin остановлен'
						}
					/>
					<div class="integration-meta">
						<span class="font-medium">Сусанин (Адаптивная маршрутизация)</span>
						{#if susaninStatusLoading}
							<span class="integration-sub">получаю данные…</span>
						{:else if susaninInstalled && susaninStatus}
							<span class="integration-sub">
								v{susaninStatus.version ?? '0.3.10'}
								{#if susaninRunning}· запущен{:else}· остановлен{/if}
							</span>
						{:else}
							<span class="setting-description">
								Автоматическое разделение трафика и самообучающаяся маршрутизация доменов и IP.
							</span>
						{/if}
					</div>
				</div>
				{#if susaninInstalled}
					<div class="integration-actions">
						<Button variant="secondary" size="sm" href="/routing?tab=adaptive">Открыть</Button>
						{#if onuninstallSusanin}
							<Button
								variant="outline-danger"
								size="sm"
								loading={susaninUninstalling}
								onclick={() => (confirmUninstallSusanin = true)}
							>
								{susaninUninstalling ? 'Удаление...' : 'Удалить'}
							</Button>
						{/if}
					</div>
				{:else if susaninStatusLoading}
					<Button variant="secondary" size="sm" disabled>Ожидание…</Button>
				{:else if oninstallSusanin}
					<Button variant="primary" size="sm" onclick={oninstallSusanin} loading={susaninInstalling}>
						{susaninInstalling ? 'Установка...' : 'Установить'}
					</Button>
				{/if}
			</div>
		{/if}

		{#each proxyBinaries as p (p.key)}
			<div class="setting-row">
				<div class="integration-item">
					<StatusDot
						variant={p.present ? 'success' : 'muted'}
						size="md"
						ariaLabel={p.present ? m.settings_integrations_proxy_installed_aria({ label: p.label }) : m.settings_integrations_proxy_not_installed_aria({ label: p.label })}
					/>
					<div class="integration-meta">
						<span class="font-medium">{p.label}</span>
						{#if p.present}
							<span class="integration-sub">
								v{p.installedVersion ?? '?'}
								{#if p.instances > 0}· {m.settings_integrations_instances({ count: p.instances })}{/if}
							</span>
							{#if p.updateAvailable && p.installVersion}
								<span class="integration-sub">{m.settings_integrations_update_available({ version: p.installVersion })}</span>
							{/if}
						{:else}
							<span class="integration-sub">{m.settings_integrations_not_installed()}</span>
						{/if}
					</div>
				</div>
				<div class="integration-actions">
					{#if p.present}
						{#if p.updateAvailable && p.installAvailable}
							<Button variant="primary" size="sm" loading={p.busy} onclick={p.oninstall}>
								{m.common_update()}
							</Button>
						{:else if !p.key.startsWith('obf-')}
							<Button variant="secondary" size="sm" href="/proxy">{m.settings_integrations_open()}</Button>
						{/if}
						<Button
							variant="outline-danger"
							size="sm"
							loading={p.busy}
							disabled={p.instances > 0}
							title={p.instances > 0
								? m.settings_integrations_remove_instances_first()
								: undefined}
							onclick={() => (confirmProxy = p)}
						>
							{m.common_delete()}
						</Button>
					{:else if p.installAvailable}
						<Button variant="primary" size="sm" loading={p.busy} onclick={p.oninstall}>
							{m.settings_integrations_install()}
						</Button>
					{/if}
				</div>
			</div>
		{/each}

		{#if showHydra}
			<div class="setting-row">
				<div class="integration-item">
					<StatusDot
						variant={hydraStatusLoading ? 'muted' : (hydraInstalled && hydraRunning ? 'success' : 'muted')}
						size="md"
						ariaLabel={
							hydraStatusLoading
								? m.settings_integrations_hydra_loading_aria()
								: hydraProcessState === 'dead'
									? 'HydraRoute: stale pid'
									: hydraInstalled && hydraRunning
									? m.settings_integrations_hydra_running_aria()
									: m.settings_integrations_hydra_stopped_aria()
						}
					/>
					<div class="integration-meta">
						<span class="font-medium">HydraRoute Neo</span>
						{#if hydraStatusLoading}
							<span class="integration-sub">{m.settings_integrations_loading()}</span>
						{:else if hydraInstalled}
							<span class="integration-sub">
								v{hydraStatus?.version ?? '?'}
								{#if hydraRunning && hydraStatus?.pid}
									· pid {hydraStatus.pid}
								{:else if hydraProcessState === 'dead' && hydraStatus?.stalePid}
									· dead pid {hydraStatus.stalePid}
								{:else}
									· {m.settings_integrations_stopped()}
								{/if}
							</span>
						{:else}
							<span class="integration-sub">{m.settings_integrations_not_installed()}</span>
						{/if}
						{#if !hydraRunning && hydraStatus?.lastError}
							<span class="setting-description warning" title={hydraStatus.lastError}>{hydraStatus.lastError}</span>
						{/if}
						{#if !hydraStatusLoading && !hydraStatus && hydraStatusError}
							<span class="setting-description warning">{m.settings_integrations_hydra_no_response({ error: hydraStatusError })}</span>
						{/if}
					</div>
				</div>
				{#if hydraInstalled}
					<Button variant="secondary" size="sm" href="/routing?tab=hrneo">{m.settings_integrations_open()}</Button>
				{:else if hydraStatusLoading}
					<Button variant="secondary" size="sm" disabled>{m.settings_integrations_waiting()}</Button>
				{:else}
					<!-- HydraRoute ставится не отсюда: своего установщика у нас нет, ссылка ведёт
					     на инструкцию проекта. Кнопка поэтому и подписана иначе, и выглядит иначе,
					     чем «Установить» у остальных интеграций — те действительно ставят. -->
					<Button
						variant="outline-primary"
						size="sm"
						href="https://github.com/Ground-Zerro/HydraRoute"
						target="_blank"
						rel="noopener noreferrer"
						title={m.settings_integrations_hydra_guide_title()}
					>
						{m.settings_integrations_hydra_guide()}
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
		<Button variant="ghost" size="sm" onclick={copyError}>{m.settings_integrations_copy()}</Button>
		<Button variant="primary" size="sm" onclick={() => (errorModalOpen = false)}>
			{m.common_close()}
		</Button>
	{/snippet}
</Modal>

{#if confirmProxy}
	<ConfirmModal
		open={confirmProxy !== null}
		title={m.settings_integrations_remove_proxy_title({ label: confirmProxy.label })}
		message={m.settings_integrations_remove_proxy_message()}
		secondary={m.settings_integrations_remove_proxy_secondary()}
		confirmLabel={m.common_delete()}
		variant="danger"
		onConfirm={() => {
			const row = confirmProxy;
			confirmProxy = null;
			row?.onuninstall();
		}}
		onClose={() => (confirmProxy = null)}
	/>
{/if}

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

{#if confirmUninstallSusanin}
	<ConfirmModal
		open={confirmUninstallSusanin}
		title="Удалить Сусанин?"
		message="Агент susanin-agent будет остановлен, а его исполняемый файл удален с роутера."
		secondary="Файлы списков vpn_always.txt и vpn_never.txt сохранятся — после повторной установки они продолжат действовать."
		confirmLabel="Удалить"
		variant="danger"
		busy={susaninUninstalling}
		onConfirm={() => {
			confirmUninstallSusanin = false;
			onuninstallSusanin?.();
		}}
		onClose={() => (confirmUninstallSusanin = false)}
	/>
{/if}

<style>
	/* Своя раскладка вместо сетки .setting-row (1fr auto): там колонка с
	   описанием схлопывалась под ширину поля и текст ломался по слову. */
	.setting-row.bootstrap-row {
		display: flex;
		flex-direction: column;
		align-items: stretch;
		gap: 0.5rem;
		/* Выравнивание по текстовой колонке соседних строк: там текст
		   сдвинут статус-точкой (её размер + gap .integration-item). */
		padding-left: 1.25rem;
	}

	.bootstrap-field {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.bootstrap-field :global(.field) {
		flex: 1;
		min-width: 0;
		max-width: 16rem;
	}

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

	/* Same action-button floor as settings actions-card (fits «Обновление…»):
	   ширина не прыгает, когда подпись уезжает в «Установка…»/«Обновление…».
	   Одиночная кнопка в .integration-actions получает тот же пол — иначе
	   «Установить» у sing-box (прямой ребёнок строки) шире, чем у прокси-
	   бинарей (обёрнуты в группу), хотя это одно и то же действие. Пары
	   кнопок («Открыть» + «Удалить») пол не получают: там ширину задаёт
	   содержимое, а 7.5rem на каждую распирало бы строку. */
	@media (min-width: 641px) {
		.setting-row > :global(.btn),
		.integration-actions > :global(.btn:only-child) {
			min-width: 7.5rem;
		}

		.setting-row > :global(.btn) {
			justify-self: end;
			align-self: center;
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

			.setting-row > :global(.btn),
			.integration-actions > :global(.btn:only-child) {
				min-width: 7.5rem;
			}

			.setting-row > :global(.btn) {
				justify-self: end;
				align-self: center;
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
