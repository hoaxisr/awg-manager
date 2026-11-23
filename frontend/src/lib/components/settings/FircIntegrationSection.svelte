<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { Button, ConfirmModal, StatusDot } from '$lib/components/ui';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';
	import { Shield } from 'lucide-svelte';
	import type { FircInstallStatus } from '$lib/types/firc';

	let status = $state<FircInstallStatus | null>(null);
	let fetchError = $state('');
	let actionError = $state('');
	let loading = $state(true);
	let submitting = $state(false);
	let confirmRemoval = $state(false);
	let confirmExternalMigration = $state(false);
	let confirmUpgrade = $state(false);
	let alive = false;
	let refreshing = false;
	let revision = 0;
	const busy = $derived(submitting || status?.busy === true);
	const installedVersion = $derived(status?.installedVersion?.trim() || '');
	const availableVersion = $derived(status?.availableVersion?.trim() || '');
	const installed = $derived(
		Boolean(
			status?.fircdPresent ||
				status?.tunvlessPresent ||
				status?.initScriptPresent ||
				status?.ndmHookPresent ||
				status?.installedVersion,
		),
	);
	const externalPackage = $derived(status?.externalPackage?.trim() || '');
	const externalOwned = $derived(Boolean(externalPackage));
	const externalMigratable = $derived(externalOwned && Boolean(status?.externalMigratable));
	const updateAvailable = $derived(
		Boolean(
			status?.updateAvailable &&
				installedVersion &&
				availableVersion &&
				installedVersion !== availableVersion &&
				!externalOwned &&
				(status?.state === 'installed' || status?.state === 'running'),
		),
	);
	const showAvailableVersion = $derived(
		Boolean(
			availableVersion &&
				availableVersion !== installedVersion &&
				!externalOwned &&
				(updateAvailable || !installed),
		),
	);
	const blocked = $derived(Boolean(status?.activeEngine || status?.state === 'running' || status?.dependentServices?.length));
	const repairUnblocked = $derived(
		!externalOwned &&
			status?.state !== 'running' &&
			(status?.state === 'not_installed' || status?.state === 'recovery_required'),
	);
	const installDisabled = $derived(
		loading ||
			busy ||
			!status?.available ||
			Boolean(fetchError) ||
			(externalOwned
				? !externalMigratable || Boolean(status?.dependentServices?.length)
				: !repairUnblocked && blocked),
	);
	const upgradeDisabled = $derived(loading || busy || !status?.available || Boolean(fetchError) || !updateAvailable);
	const removeDisabled = $derived(busy || externalOwned || blocked || Boolean(fetchError));
	const upgradeConfirmSecondary = $derived.by(() => {
		const baseMsg =
			status?.activeEngine || status?.state === 'running'
				? m.firc_runtime_upgrade_confirm_active({ current: installedVersion })
				: m.firc_runtime_upgrade_confirm_idle({ current: installedVersion });
		if (status?.dependentServices?.length) {
			return `${baseMsg} ${m.firc_runtime_upgrade_confirm_dependents({ services: status.dependentServices.join(', ') })}`;
		}
		return baseMsg;
	});
	const stateLabel = $derived.by(() => {
		switch (status?.state) {
			case 'not_installed': return m.firc_runtime_absent();
			case 'installed': return m.firc_runtime_installed();
			case 'running': return m.firc_runtime_running();
			case 'installing': return status?.busyAction === 'upgrade' ? m.firc_runtime_upgrading() : m.firc_runtime_installing();
			case 'uninstalling': return m.firc_runtime_removing();
			case 'recovery_required': return m.firc_runtime_recovery();
			case 'unavailable': return m.firc_runtime_unavailable();
			default: return m.firc_runtime_loading();
		}
	});

	async function refresh() {
		if (refreshing) return;
		refreshing = true;
		const currentRevision = revision;
		try {
			const next = await api.fircGetInstallStatus();
			if (alive && currentRevision === revision) {
				status = next;
				fetchError = '';
				if (next.busy || next.state === 'running') {
					actionError = '';
				}
			}
		} catch (e) {
			if (alive) fetchError = e instanceof Error ? e.message : String(e);
		} finally {
			refreshing = false;
			if (alive) loading = false;
		}
	}
	onMount(() => {
		alive = true;
		void refresh();
		const timer = setInterval(refresh, 2000);
		return () => { alive = false; clearInterval(timer); };
	});
	async function act(action: 'install' | 'upgrade' | 'uninstall' | 'migrate-external') {
		if (busy) return;
		confirmRemoval = false;
		confirmExternalMigration = false;
		confirmUpgrade = false;
		submitting = true;
		actionError = '';
		try {
			if (action === 'migrate-external') await api.fircMigrateRuntime({ replaceExternalPackage: true });
			else if (action === 'upgrade') await api.fircUpgrade();
			else if (action === 'install') await api.fircInstall();
			else await api.fircUninstall(false);
			revision++;
			if (alive && status) {
				status = {
					...status,
					busy: true,
					busyAction: action === 'upgrade' ? 'upgrade' : status.busyAction,
					state: action === 'uninstall' ? 'uninstalling' : 'installing',
				};
			}
			// 202 is only acceptance, not completion. Polling shows the actual result.
			await refresh();
		} catch (e) {
			if (alive) actionError = e instanceof Error ? e.message : String(e);
		} finally {
			if (alive) submitting = false;
		}
	}
</script>

<div class="settings-block">
	<div class="card">
		<SettingsSectionLabel label="FIRC" icon={Shield} tone="purple" header />
		<div class="setting-row firc-header-row">
			<div class="firc-status-line">
				<StatusDot variant={status?.state === 'running' ? 'success' : status?.state === 'recovery_required' ? 'error' : 'muted'} />
				<span class="firc-state-label">{stateLabel}</span>
				{#if installedVersion}<span class="firc-version">{installedVersion}</span>{/if}
				{#if showAvailableVersion}
					<span class="firc-version-available" class:is-update={updateAvailable}>
						{m.firc_runtime_available_version({ version: availableVersion })}
					</span>
				{/if}
			</div>
			<div class="firc-actions-row">
				{#if updateAvailable}
					<Button size="sm" onclick={() => (confirmUpgrade = true)} disabled={upgradeDisabled}>
						{m.firc_runtime_upgrade()}
					</Button>
				{:else}
					<Button size="sm" onclick={() => (externalMigratable ? (confirmExternalMigration = true) : act('install'))} disabled={installDisabled}>
						{externalMigratable ? m.firc_runtime_migrate_external() : status?.state === 'recovery_required' ? m.firc_runtime_repair() : m.firc_runtime_install()}
					</Button>
				{/if}
				{#if installed}
					<Button size="sm" variant="outline-danger" onclick={() => (confirmRemoval = true)} disabled={removeDisabled}>{m.firc_runtime_remove()}</Button>
				{/if}
			</div>
		</div>
		{#if status?.busy}
			<div class="setting-row"><progress class="firc-progress" max="1" value={status.progress ?? 0} aria-label={stateLabel}></progress></div>
		{/if}
		{#if externalOwned}
			<div class="setting-row text-secondary">{m.firc_runtime_external_notice({ pkg: externalPackage })}</div>
		{/if}
		{#if !status?.available && status && !externalOwned}
			<div class="setting-row text-secondary">{m.firc_runtime_release_pending()}</div>
		{/if}
		{#if (!externalOwned && ((blocked && !repairUnblocked) || Boolean(status?.dependentServices?.length))) || (externalMigratable && Boolean(status?.dependentServices?.length))}
			<div class="setting-row text-secondary">{m.firc_runtime_dependency_warning()} {status?.dependentServices?.join(', ')}</div>
		{/if}
		{#if fetchError || actionError || status?.error}
			<div class="setting-row text-danger" role="alert">{fetchError || actionError || status?.error}</div>
		{/if}
		<div class="setting-row text-secondary">{m.firc_runtime_activation_note()}</div>
	</div>
</div>

{#if confirmRemoval}
	<ConfirmModal open title={m.firc_runtime_remove()} message={m.firc_runtime_remove_confirm()} secondary={m.firc_runtime_config_preserved()} confirmLabel={m.firc_runtime_remove()} cancelLabel={m.common_cancel()} variant="danger" onConfirm={() => act('uninstall')} onClose={() => (confirmRemoval = false)} />
{/if}
{#if confirmExternalMigration}
	<ConfirmModal open title={m.firc_runtime_migrate_external()} message={m.firc_runtime_migrate_external_confirm({ pkg: externalPackage })} secondary={m.firc_runtime_migrate_external_secondary()} confirmLabel={m.firc_runtime_migrate_external()} cancelLabel={m.common_cancel()} variant="primary" onConfirm={() => act('migrate-external')} onClose={() => (confirmExternalMigration = false)} />
{/if}
{#if confirmUpgrade}
	<ConfirmModal open title={m.firc_runtime_upgrade_confirm_title()} message={m.firc_runtime_upgrade_confirm({ current: installedVersion, target: availableVersion })} secondary={upgradeConfirmSecondary} confirmLabel={m.firc_runtime_upgrade()} cancelLabel={m.common_cancel()} variant="primary" onConfirm={() => act('upgrade')} onClose={() => (confirmUpgrade = false)} />
{/if}

<style>
	.firc-header-row {
		flex-direction: column;
		align-items: stretch;
		justify-content: flex-start;
		gap: 0.625rem;
	}

	.firc-header-row > .firc-status-line {
		flex: 0 0 auto;
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.375rem 0.5rem;
		min-width: 0;
	}

	.firc-state-label {
		font-weight: 500;
		color: var(--color-text-primary);
	}

	.firc-version {
		font-family: var(--font-mono);
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		overflow-wrap: anywhere;
		min-width: 0;
	}

	.firc-version-available {
		font-family: var(--font-mono);
		font-size: 0.75rem;
		padding: 0.125rem 0.4375rem;
		border-radius: var(--radius-sm, 0.375rem);
		background: var(--color-surface-subtle, rgba(148, 163, 184, 0.12));
		color: var(--color-text-secondary);
		overflow-wrap: anywhere;
		min-width: 0;
	}

	.firc-version-available.is-update {
		background: var(--color-accent-subtle, rgba(59, 130, 246, 0.14));
		color: var(--color-accent, #2563eb);
		font-weight: 500;
	}

	.firc-actions-row {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.5rem;
	}

	.firc-progress {
		width: 100%;
	}
</style>
