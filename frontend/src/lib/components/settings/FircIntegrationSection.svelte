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
	let alive = false;
	let refreshing = false;
	let revision = 0;
	const busy = $derived(submitting || status?.busy === true);
	const installed = $derived(
		Boolean(
			status?.fircdPresent ||
				status?.tunvlessPresent ||
				status?.initScriptPresent ||
				status?.ndmHookPresent ||
				status?.installedVersion,
		),
	);
	const legacyOwned = $derived(Boolean(status?.legacyBundledOwnership));
	const externalPackage = $derived(status?.externalPackage?.trim() || '');
	const externalOwned = $derived(Boolean(externalPackage));
	const externalMigratable = $derived(externalOwned && Boolean(status?.externalMigratable));
	const blocked = $derived(Boolean(status?.activeEngine || status?.state === 'running' || status?.dependentServices?.length));
	const repairUnblocked = $derived(
		!externalOwned &&
			!legacyOwned &&
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
				: legacyOwned
					? Boolean(status?.dependentServices?.length)
					: !repairUnblocked && blocked),
	);
	const removeDisabled = $derived(busy || externalOwned || blocked || legacyOwned || Boolean(fetchError));
	const stateLabel = $derived.by(() => {
		switch (status?.state) {
			case 'not_installed': return m.firc_runtime_absent();
			case 'installed': return m.firc_runtime_installed();
			case 'running': return m.firc_runtime_running();
			case 'installing': return m.firc_runtime_installing();
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
	async function act(action: 'install' | 'uninstall' | 'migrate' | 'migrate-external') {
		if (busy) return;
		confirmRemoval = false;
		confirmExternalMigration = false;
		submitting = true;
		actionError = '';
		try {
			if (action === 'migrate') await api.fircMigrateRuntime();
			else if (action === 'migrate-external') await api.fircMigrateRuntime({ replaceExternalPackage: true });
			else if (action === 'install') await api.fircInstall();
			else await api.fircUninstall(false);
			revision++;
			if (alive && status) status = { ...status, busy: true, state: action === 'uninstall' ? 'uninstalling' : 'installing' };
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
				{#if status?.installedVersion}<span class="firc-version">{status.installedVersion}</span>{/if}
			</div>
			<div class="firc-actions-row">
				<Button size="sm" onclick={() => (externalMigratable ? (confirmExternalMigration = true) : act(legacyOwned ? 'migrate' : 'install'))} disabled={installDisabled}>
					{externalMigratable ? m.firc_runtime_migrate_external() : legacyOwned ? m.firc_runtime_migrate() : status?.state === 'recovery_required' ? m.firc_runtime_repair() : m.firc_runtime_install()}
				</Button>
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
		{#if legacyOwned}
			<div class="setting-row text-secondary">{m.firc_runtime_legacy_notice()}</div>
		{/if}
		{#if !status?.available && status && !externalOwned}
			<div class="setting-row text-secondary">{m.firc_runtime_release_pending()}</div>
		{/if}
		{#if (!externalOwned && !legacyOwned && ((blocked && !repairUnblocked) || Boolean(status?.dependentServices?.length))) || ((legacyOwned || externalMigratable) && Boolean(status?.dependentServices?.length))}
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
