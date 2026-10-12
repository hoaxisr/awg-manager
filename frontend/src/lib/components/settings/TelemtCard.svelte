<!--
  Карточка «Telegram Proxy (telemt)»: встроенный MTProto-прокси в Fake-TLS режиме.
  Использует SSE-инвалидируемый стор telemtStatus и API telemt*.
-->
<script lang="ts">
	import { onMount } from 'svelte';
	import { m } from '$lib/i18n';
	import { api } from '$lib/api/client';
	import { telemtStatus } from '$lib/stores/telemt';
	import { notifications } from '$lib/stores/notifications';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { Button, ConfirmModal, StatusDot, Toggle } from '$lib/components/ui';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';
	import { Send, Copy, RefreshCw, Key, ExternalLink } from 'lucide-svelte';
	import type { RouterInterface, TelemtConfig } from '$lib/types';

	const status = $derived($telemtStatus.data ?? null);
	const loading = $derived($telemtStatus.status === 'loading' && $telemtStatus.lastFetchedAt === 0);
	const installed = $derived(status?.installed ?? false);
	const running = $derived(status?.running ?? false);
	const updateAvailable = $derived(status?.updateAvailable ?? false);
	const archSupported = $derived(status?.archSupported ?? true);

	let ifaces = $state<RouterInterface[]>([]);
	let ifaceDraft = $state('');
	let portDraft = $state<number | string>(8443);
	let secretDraft = $state('');
	let domainDraft = $state('yandex.ru');
	let loadedConfig = $state<TelemtConfig | null>(null);
	let touched = $state(false);

	let busy = $state(false);
	let installing = $state(false);
	let updating = $state(false);
	let restarting = $state(false);
	let uninstalling = $state(false);
	let savingConfig = $state(false);
	let confirmUninstallOpen = $state(false);

	function isNoiseIface(name: string): boolean {
		const n = name.toLowerCase();
		if (n === 'lo') return true;
		if (/^ra\d/.test(n) || n.startsWith('rai')) return true;
		if (n.startsWith('apcli') || n.startsWith('wlan') || n.startsWith('ath')) return true;
		return false;
	}

	function ifaceRank(name: string): number {
		if (name.startsWith('br')) return 0;
		if (name.startsWith('nwg')) return 1;
		return 2;
	}

	const ifaceOptions = $derived.by(() => {
		const filtered = ifaces.filter((i) => !isNoiseIface(i.name));
		filtered.sort(
			(a, b) =>
				ifaceRank(a.name) - ifaceRank(b.name) ||
				a.name.localeCompare(b.name, undefined, { numeric: true }),
		);
		const opts = filtered.map((i) => ({
			value: i.name,
			label: i.label ? `${i.name} · ${i.label}` : i.name,
		}));

		if (ifaceDraft && !opts.some((o) => o.value === ifaceDraft)) {
			opts.unshift({ value: ifaceDraft, label: ifaceDraft });
		}
		return opts;
	});

	async function loadConfig() {
		try {
			const cfg = await api.telemtConfig();
			loadedConfig = cfg;
			if (!touched) {
				ifaceDraft = cfg.interface ?? '';
				portDraft = cfg.port || 8443;
				secretDraft = cfg.secret ?? '';
				domainDraft = cfg.tlsDomain || 'yandex.ru';
			}
		} catch {
			// ignore if not configured or uninstalled
		}
	}

	$effect(() => {
		if (installed && !loadedConfig) {
			void loadConfig();
		}
	});

	onMount(async () => {
		try {
			ifaces = await api.getAllInterfaces();
		} catch {
			ifaces = [];
		}
		if (installed) {
			void loadConfig();
		}
	});

	const portNum = $derived(Number(portDraft));
	const portReserved = $derived([22, 2222, 1099, 51820].includes(portNum));
	const portValid = $derived(Number.isInteger(portNum) && portNum >= 1 && portNum <= 65535 && !portReserved);
	const secretValid = $derived(/^[0-9a-fA-F]{32}$/.test(secretDraft.trim()));
	const domainValid = $derived(domainDraft.trim().length > 0);
	const ifaceValid = $derived(ifaceDraft.trim().length > 0);
	const isValid = $derived(portValid && secretValid && domainValid && ifaceValid);

	const dirty = $derived.by(() => {
		const base = loadedConfig ?? status?.config;
		if (!base) return touched;
		return (
			ifaceDraft.trim() !== (base.interface ?? '').trim() ||
			portNum !== (base.port || 8443) ||
			secretDraft.trim() !== (base.secret ?? '').trim() ||
			domainDraft.trim() !== (base.tlsDomain || 'yandex.ru').trim()
		);
	});

	function markTouched(): void {
		touched = true;
	}

	async function toggleRunning(next: boolean) {
		if (busy) return;
		if (next && !ifaceValid) {
			notifications.error(m.settings_telemt_interface_required());
			return;
		}
		busy = true;
		try {
			if (next) {
				const fresh = await api.telemtStart();
				telemtStatus.applyMutationResponse(fresh);
				notifications.success(m.settings_telemt_started());
			} else {
				const fresh = await api.telemtStop();
				telemtStatus.applyMutationResponse(fresh);
				notifications.success(m.settings_telemt_stopped());
			}
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_telemt_control_failed());
		} finally {
			busy = false;
		}
	}

	async function handleSaveConfig() {
		if (!isValid || savingConfig) return;
		savingConfig = true;
		try {
			const cfg: TelemtConfig = {
				enabled: status?.config?.enabled ?? false,
				interface: ifaceDraft.trim(),
				port: portNum,
				secret: secretDraft.trim(),
				tlsDomain: domainDraft.trim(),
			};
			const fresh = await api.telemtSaveConfig(cfg);
			telemtStatus.applyMutationResponse(fresh);
			loadedConfig = { ...cfg };
			touched = false;
			notifications.success(m.settings_telemt_config_saved());
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_telemt_config_save_failed());
		} finally {
			savingConfig = false;
		}
	}

	function generateSecret() {
		const bytes = new Uint8Array(16);
		crypto.getRandomValues(bytes);
		secretDraft = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
		markTouched();
	}

	async function handleInstall() {
		if (installing) return;
		installing = true;
		try {
			const fresh = await api.telemtInstall();
			telemtStatus.applyMutationResponse(fresh);
			loadedConfig = null;
			touched = false;
			await loadConfig();
			notifications.success(m.settings_page_telemt_installed());
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_page_telemt_install_failed());
		} finally {
			installing = false;
		}
	}

	async function handleUpdate() {
		if (updating) return;
		updating = true;
		try {
			const fresh = await api.telemtUpdate();
			telemtStatus.applyMutationResponse(fresh);
			notifications.success(m.settings_page_telemt_updated());
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_page_telemt_update_failed());
		} finally {
			updating = false;
		}
	}

	async function handleRestart() {
		if (restarting) return;
		restarting = true;
		try {
			const fresh = await api.telemtRestart();
			telemtStatus.applyMutationResponse(fresh);
			notifications.success(m.settings_page_telemt_restarted());
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_page_telemt_restart_failed());
		} finally {
			restarting = false;
		}
	}

	async function handleUninstall() {
		if (uninstalling) return;
		uninstalling = true;
		try {
			const fresh = await api.telemtUninstall();
			telemtStatus.applyMutationResponse(fresh);
			loadedConfig = null;
			touched = false;
			ifaceDraft = '';
			secretDraft = '';
			confirmUninstallOpen = false;
			notifications.success(m.settings_page_telemt_removed());
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_page_telemt_remove_failed());
		} finally {
			uninstalling = false;
		}
	}

	async function copyLink() {
		const link = status?.link ?? '';
		if (!link) return;
		if (await copyToClipboard(link)) {
			notifications.success(m.settings_integrations_telemt_link_copied());
		} else {
			notifications.error(m.settings_integrations_telemt_link_copy_failed());
		}
	}
</script>

<div class="settings-block" id="settings-telemt">
	<div class="card">
		<SettingsSectionLabel label={m.settings_telemt_title()} icon={Send} tone="blue" header />

		<!-- Header summary row -->
		<div class="setting-row">
			<div class="integration-item">
				<StatusDot
					variant={loading ? 'muted' : installed && running ? 'success' : 'muted'}
					size="md"
					ariaLabel={
						loading
							? m.settings_integrations_telemt_loading_aria()
							: running
								? m.settings_integrations_telemt_running_aria()
								: m.settings_integrations_telemt_stopped_aria()
					}
				/>
				<div class="integration-meta">
					<span class="font-medium">{m.settings_telemt_title()}</span>
					{#if loading}
						<span class="integration-sub">{m.settings_integrations_loading()}</span>
					{:else if installed}
						<span class="integration-sub">
							v{status?.version ?? '?'}
							{#if running && status?.pid}· pid {status.pid}{/if}
							· {running ? m.settings_telemt_status_running() : m.settings_telemt_status_stopped()}
						</span>
						{#if updateAvailable && status?.latestVersion}
							<span class="setting-description warning">
								{m.settings_integrations_update_available({ version: status.latestVersion })}
							</span>
						{/if}
					{:else}
						<span class="integration-sub">{m.settings_telemt_status_not_installed()}</span>
						<span class="setting-description">
							{m.settings_telemt_description()}
						</span>
						{#if !archSupported}
							<span class="setting-description warning">
								{m.settings_integrations_telemt_arch_not_supported({ arch: status?.arch ?? 'unknown' })}
							</span>
						{/if}
					{/if}
				</div>
			</div>

			<div class="integration-actions">
				{#if !installed}
					{#if archSupported}
						<Button variant="primary" size="sm" onclick={handleInstall} loading={installing}>
							{installing ? m.settings_integrations_installing() : m.settings_integrations_install()}
						</Button>
					{:else}
						<Button variant="secondary" size="sm" disabled>
							{m.settings_integrations_telemt_unavailable()}
						</Button>
					{/if}
				{:else}
					{#if updateAvailable}
						<Button variant="primary" size="sm" onclick={handleUpdate} loading={updating}>
							{updating ? m.settings_integrations_updating() : m.common_update()}
						</Button>
					{/if}
					{#if running}
						<Button variant="secondary" size="sm" onclick={handleRestart} loading={restarting}>
							{restarting ? m.settings_integrations_telemt_restarting() : m.common_restart()}
						</Button>
					{/if}
					<Button
						variant="outline-danger"
						size="sm"
						onclick={() => (confirmUninstallOpen = true)}
						loading={uninstalling}
					>
						{m.common_delete()}
					</Button>
				{/if}
			</div>
		</div>

		{#if installed}
			<!-- Switch enable/disable row -->
			<div class="setting-row toggle-inline-row">
				<div class="flex flex-col gap-1">
					<span class="font-medium">{m.settings_telemt_enabled()}</span>
					<span class="setting-description">
						{running ? m.settings_page_process_running() : m.settings_page_process_stopped()}
						{#if !ifaceValid}
							<span class="warn-inline">· {m.settings_telemt_interface_required()}</span>
						{/if}
					</span>
				</div>
				<Toggle
					checked={running}
					onchange={toggleRunning}
					disabled={busy || !ifaceValid}
				/>
			</div>

			<!-- Interface selection row -->
			<div class="setting-row">
				<div class="flex flex-col gap-1">
					<span class="font-medium">{m.settings_telemt_interface_label()}</span>
					<span class="setting-description">
						{m.settings_telemt_interface_description()}
					</span>
				</div>
				<div class="field-control">
					<select
						class="settings-select"
						bind:value={ifaceDraft}
						onchange={markTouched}
					>
						<option value="" disabled>{m.settings_telemt_interface_placeholder()}</option>
						{#each ifaceOptions as opt}
							<option value={opt.value}>{opt.label}</option>
						{/each}
					</select>
				</div>
			</div>

			<!-- Port row -->
			<div class="setting-row">
				<div class="flex flex-col gap-1">
					<span class="font-medium">{m.settings_telemt_port_label()}</span>
					<span class="setting-description">
						{m.settings_telemt_port_description()}
					</span>
				</div>
				<div class="field-control hs-port-form">
					<input
						type="number"
						min="1"
						max="65535"
						class="settings-text-input port-input"
						bind:value={portDraft}
						oninput={markTouched}
					/>
					{#if portReserved}
						<span class="err">{m.settings_telemt_port_reserved()}</span>
					{:else if !portValid && portDraft !== ''}
						<span class="err">1–65535</span>
					{/if}
				</div>
			</div>

			<!-- Fake-TLS Domain row -->
			<div class="setting-row">
				<div class="flex flex-col gap-1">
					<span class="font-medium">{m.settings_telemt_tls_domain_label()}</span>
					<span class="setting-description">
						{m.settings_telemt_tls_domain_description()}
					</span>
				</div>
				<div class="field-control">
					<input
						type="text"
						class="settings-text-input domain-input"
						bind:value={domainDraft}
						oninput={markTouched}
						placeholder="yandex.ru"
					/>
				</div>
			</div>

			<!-- Secret key row -->
			<div class="setting-row">
				<div class="flex flex-col gap-1">
					<span class="font-medium">{m.settings_telemt_secret_label()}</span>
					<span class="setting-description">
						{m.settings_telemt_secret_description()}
					</span>
				</div>
				<div class="field-control secret-controls">
					<input
						type="text"
						maxlength="32"
						class="settings-text-input secret-input font-mono"
						bind:value={secretDraft}
						oninput={markTouched}
					/>
					<Button variant="secondary" size="sm" onclick={generateSecret}>
						{m.settings_telemt_generate_secret()}
					</Button>
				</div>
			</div>

			<!-- Save button row if modified -->
			{#if touched && dirty}
				<div class="setting-row save-actions-row">
					<div></div>
					<Button
						variant="primary"
						size="sm"
						onclick={handleSaveConfig}
						loading={savingConfig}
						disabled={!isValid}
					>
						{m.common_save()}
					</Button>
				</div>
			{/if}

			<!-- Telegram Client Link row -->
			{#if status?.link}
				<div class="setting-row client-link-row">
					<div class="flex flex-col gap-1">
						<span class="font-medium">{m.settings_telemt_client_link_label()}</span>
						<span class="setting-description">
							{m.settings_telemt_client_link_description()}
						</span>
					</div>
					<div class="field-control link-controls">
						<input
							type="text"
							readonly
							class="settings-text-input link-input font-mono"
							value={status.link}
							onclick={copyLink}
						/>
						<Button variant="secondary" size="sm" onclick={copyLink}>
							{m.settings_integrations_telemt_copy_link()}
						</Button>
					</div>
				</div>
			{/if}
		{/if}
	</div>
</div>

<ConfirmModal
	open={confirmUninstallOpen}
	title={m.settings_integrations_telemt_remove_title()}
	message={m.settings_integrations_telemt_remove_message()}
	secondary={m.settings_integrations_telemt_remove_secondary()}
	confirmLabel={m.common_delete()}
	variant="danger"
	busy={uninstalling}
	onConfirm={handleUninstall}
	onClose={() => (confirmUninstallOpen = false)}
/>

<style>
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
		display: flex;
		flex-direction: column;
		gap: 0.2rem;
		min-width: 0;
	}

	.integration-sub {
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}

	.integration-actions {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-shrink: 0;
	}

	.field-control {
		display: flex;
		align-items: center;
		justify-content: flex-end;
		gap: 0.5rem;
		min-width: 0;
	}

	.settings-select {
		width: 100%;
		max-width: 18rem;
		padding: 0.375rem 0.625rem;
		font-size: 0.875rem;
		color: var(--color-text-primary);
		background-color: var(--color-bg-input, var(--color-surface));
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm, 6px);
	}

	.port-input {
		width: 6rem;
	}

	.domain-input {
		width: 100%;
		max-width: 18rem;
	}

	.secret-controls {
		width: 100%;
		max-width: 24rem;
	}

	.secret-input {
		width: 100%;
		font-size: 0.8125rem;
	}

	.link-controls {
		width: 100%;
		max-width: 28rem;
	}

	.link-input {
		width: 100%;
		font-size: 0.75rem;
		cursor: pointer;
	}

	.save-actions-row {
		display: flex;
		justify-content: flex-end;
		padding-top: 0.25rem;
	}

	.err {
		font-size: 0.75rem;
		color: var(--color-danger, #ef4444);
	}

	.warn-inline {
		color: var(--color-warning, #f59e0b);
		font-weight: 500;
	}

	@media (max-width: 640px) {
		.setting-row {
			display: flex;
			flex-direction: column;
			align-items: stretch;
			gap: 0.75rem;
		}

		.field-control {
			justify-content: flex-start;
			width: 100%;
		}

		.settings-select,
		.domain-input,
		.secret-controls,
		.link-controls {
			max-width: 100%;
		}
	}
</style>
