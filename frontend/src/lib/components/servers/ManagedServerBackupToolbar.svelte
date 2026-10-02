<script lang="ts">
	import { m } from '$lib/i18n';
	import { Download, Upload } from 'lucide-svelte';
	import { Button, Modal } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import ManagedServerImportModal from './ManagedServerImportModal.svelte';
	import type { ManagedServerBackupFile } from '$lib/types';

	let { showExport = true }: { showExport?: boolean } = $props();

	let exportModalOpen = $state(false);
	let importModalOpen = $state(false);
	let pendingFile = $state<ManagedServerBackupFile | null>(null);
	let exporting = $state(false);
	let exportWarnings = $state<string[]>([]);
	let preparedExport = $state<ManagedServerBackupFile | null>(null);

	function isManagedServerBackupFile(v: unknown): v is ManagedServerBackupFile {
		if (!v || typeof v !== 'object') return false;
		const obj = v as Record<string, unknown>;
		if (obj.type !== 'awg-manager-managed-server-backup') return false;
		if (obj.version !== 1) return false;
		if (!Array.isArray(obj.managedServers)) return false;
		for (const server of obj.managedServers) {
			if (!server || typeof server !== 'object') return false;
			const s = server as Record<string, unknown>;
			if (typeof s.interfaceName !== 'string' || !s.interfaceName) return false;
			if (typeof s.address !== 'string' || !s.address) return false;
			if (typeof s.mask !== 'string' || !s.mask) return false;
			if (typeof s.listenPort !== 'number') return false;
			if (!Array.isArray(s.peers)) return false;
			for (const peer of s.peers) {
				if (!peer || typeof peer !== 'object') return false;
				const p = peer as Record<string, unknown>;
				if (typeof p.publicKey !== 'string' || !p.publicKey) return false;
				if (typeof p.tunnelIP !== 'string' || !p.tunnelIP) return false;
				if (p.enabled !== undefined && typeof p.enabled !== 'boolean') return false;
			}
		}
		return true;
	}

	function startExport() {
		exportWarnings = [];
		preparedExport = null;
		exportModalOpen = true;
	}

	function downloadBackup(data: ManagedServerBackupFile) {
		const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' });
		const url = URL.createObjectURL(blob);
		const a = document.createElement('a');
		const date = new Date().toISOString().slice(0, 10);
		a.href = url;
		a.download = `managed-backup-${date}.json`;
		document.body.appendChild(a);
		a.click();
		document.body.removeChild(a);
		URL.revokeObjectURL(url);
	}

	async function confirmExport() {
		exporting = true;
		try {
			if (!preparedExport) {
				const data = await api.managedServerExport();
				exportWarnings = (data.warnings ?? []).map((w) =>
					w.interfaceName ? `${w.interfaceName}: ${w.message}` : w.message,
				);
				preparedExport = data;
				if (exportWarnings.length > 0) {
					notifications.warning(
						m.servers_backup_warning({ count: exportWarnings.length }),
					);
					return;
				}
				downloadBackup(data);
			} else {
				downloadBackup(preparedExport);
			}
			exportModalOpen = false;
		} catch (e) {
			notifications.error((e as Error).message);
		} finally {
			exporting = false;
		}
	}

	function openFilePicker() {
		const input = document.createElement('input');
		input.type = 'file';
		input.accept = 'application/json';
		input.onchange = async () => {
			const file = input.files?.[0];
			if (!file) return;
			try {
				const text = await file.text();
				const parsed = JSON.parse(text) as unknown;
				if (!isManagedServerBackupFile(parsed)) {
					notifications.error(m.servers_backup_not_backup_file());
					return;
				}
				pendingFile = parsed;
				importModalOpen = true;
			} catch (e) {
				notifications.error(m.servers_backup_read_failed({ error: (e as Error).message }));
			}
		};
		input.click();
	}
</script>

{#snippet exportIcon()}
	<Download size={14} strokeWidth={2} aria-hidden="true" />
{/snippet}

{#snippet importIcon()}
	<Upload size={14} strokeWidth={2} aria-hidden="true" />
{/snippet}

<div class="backup-toolbar" class:importOnly={!showExport}>
	{#if showExport}
		<Button
			variant="secondary"
			size="md"
			onclick={startExport}
			iconBefore={exportIcon}
			title={m.servers_backup_export_servers()}
		>
			{m.tunnels_dashboard_export()}
		</Button>
	{/if}
	<Button
		variant="secondary"
		size="md"
		onclick={openFilePicker}
		iconBefore={importIcon}
		title={m.servers_backup_import_servers()}
	>
		{m.servers_backup_import()}
	</Button>
</div>

<Modal
	bind:open={exportModalOpen}
	title={m.servers_backup_export_title()}
	size="sm"
	onclose={() => {
		exportModalOpen = false;
		exportWarnings = [];
		preparedExport = null;
	}}
>
	<p>{m.servers_backup_private_keys_note()}</p>
	{#if exportWarnings.length > 0}
		<div class="warn-box">
			<strong>{m.servers_backup_warning_label()}</strong> {m.servers_backup_incomplete()}
			<ul>
				{#each exportWarnings as w}
					<li>{w}</li>
				{/each}
			</ul>
		</div>
	{/if}
	{#snippet actions()}
		<Button
			variant="secondary"
			size="md"
			onclick={() => {
				exportModalOpen = false;
				exportWarnings = [];
				preparedExport = null;
			}}
		>
			{m.common_cancel()}
		</Button>
		<Button variant="outline-primary" size="md" onclick={confirmExport} loading={exporting}>
			{exportWarnings.length > 0 ? m.servers_backup_download_anyway() : m.tunnel_edit_header_download()}
		</Button>
	{/snippet}
</Modal>

{#if importModalOpen && pendingFile}
	<ManagedServerImportModal
		bind:open={importModalOpen}
		file={pendingFile}
		onclose={() => {
			importModalOpen = false;
			pendingFile = null;
		}}
	/>
{/if}

<style>
	.backup-toolbar {
		display: flex;
		gap: 0.5rem;
	}
	@media (max-width: 768px) {
		.backup-toolbar {
			flex: 1 0 100%;
			display: grid;
			grid-template-columns: repeat(2, minmax(0, 1fr));
			gap: 0.5rem;
			width: 100%;
			min-width: 0;
		}

		.backup-toolbar.importOnly {
			justify-self: stretch;
		}

		.backup-toolbar :global(.btn) {
			width: 100%;
			min-width: 0;
			justify-content: center;
		}

		.backup-toolbar.importOnly :global(.btn) {
			grid-column: 2;
		}
	}
	.warn-box {
		margin-top: 0.75rem;
		padding: 0.5rem 0.75rem;
		border: 1px solid var(--color-warning, #cc9a06);
		border-radius: 0.5rem;
		background: color-mix(in srgb, var(--color-warning, #cc9a06) 8%, transparent);
		font-size: 0.9rem;
	}
</style>
