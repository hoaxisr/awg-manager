<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { Button, ConfirmModal, StatusDot } from '$lib/components/ui';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';
	import { Shield } from 'lucide-svelte';
	import type { FircInstallStatus } from '$lib/types/firc';

	let status = $state<FircInstallStatus | null>(null);
	let error = $state('');
	let loading = $state(true);
	let submitting = $state(false);
	let confirmRemoval = $state(false);
	let alive = false;
	let refreshing = false;
	let revision = 0;
	const busy = $derived(submitting || status?.busy === true);
	const installed = $derived(Boolean(status?.fircdPresent || status?.tunvlessPresent || status?.installedVersion));
	const legacyOwned = $derived(Boolean(status?.legacyBundledOwnership));
	const blocked = $derived(Boolean(status?.activeEngine || status?.state === 'running' || status?.dependentServices?.length));
	const installDisabled = $derived(
		loading ||
			busy ||
			!status?.available ||
			Boolean(error) ||
			(legacyOwned ? Boolean(status?.dependentServices?.length) : blocked),
	);
	const removeDisabled = $derived(busy || blocked || legacyOwned || Boolean(error));
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
			if (alive && currentRevision === revision) { status = next; error = ''; }
		} catch (e) {
			if (alive) error = e instanceof Error ? e.message : String(e);
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
	async function act(action: 'install' | 'uninstall' | 'migrate') {
		if (busy) return;
		confirmRemoval = false;
		submitting = true;
		error = '';
		try {
			if (action === 'migrate') await api.fircMigrateRuntime();
			else if (action === 'install') await api.fircInstall();
			else await api.fircUninstall(false);
			revision++;
			if (alive && status) status = { ...status, busy: true, state: action === 'uninstall' ? 'uninstalling' : 'installing' };
			// 202 is only acceptance, not completion. Polling shows the actual result.
			await refresh();
		} catch (e) {
			if (alive) error = e instanceof Error ? e.message : String(e);
		} finally {
			if (alive) submitting = false;
		}
	}
</script>

<div class="settings-block">
	<div class="card">
		<SettingsSectionLabel label="FIRC" icon={Shield} tone="purple" header />
		<div class="setting-row">
			<div class="flex items-center gap-2">
				<StatusDot variant={status?.state === 'running' ? 'success' : status?.state === 'recovery_required' ? 'error' : 'muted'} />
				<span>{stateLabel}</span>
				{#if status?.installedVersion}<span class="text-secondary">{status.installedVersion}</span>{/if}
			</div>
			<div class="flex gap-2">
				<Button size="sm" onclick={() => act(legacyOwned ? 'migrate' : 'install')} disabled={installDisabled}>
					{legacyOwned ? m.firc_runtime_migrate() : status?.state === 'recovery_required' ? m.firc_runtime_repair() : m.firc_runtime_install()}
				</Button>
				{#if installed}
					<Button size="sm" variant="outline-danger" onclick={() => (confirmRemoval = true)} disabled={removeDisabled}>{m.firc_runtime_remove()}</Button>
				{/if}
			</div>
		</div>
		{#if status?.busy}
			<div class="setting-row"><progress max="1" value={status.progress ?? 0} aria-label={stateLabel}></progress></div>
		{/if}
		{#if legacyOwned}
			<div class="setting-row text-secondary">{m.firc_runtime_legacy_notice()}</div>
		{/if}
		{#if !status?.available && status}
			<div class="setting-row text-secondary">{m.firc_runtime_release_pending()}</div>
		{/if}
		{#if blocked && !legacyOwned}
			<div class="setting-row text-secondary">{m.firc_runtime_dependency_warning()} {status?.dependentServices?.join(', ')}</div>
		{/if}
		{#if error || status?.error}
			<div class="setting-row text-danger" role="alert">{error || status?.error}</div>
		{/if}
		<div class="setting-row text-secondary">{m.firc_runtime_activation_note()}</div>
	</div>
</div>

{#if confirmRemoval}
	<ConfirmModal open title={m.firc_runtime_remove()} message={m.firc_runtime_remove_confirm()} secondary={m.firc_runtime_config_preserved()} confirmLabel={m.firc_runtime_remove()} cancelLabel={m.common_cancel()} variant="danger" onConfirm={() => act('uninstall')} onClose={() => (confirmRemoval = false)} />
{/if}
