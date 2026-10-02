<script lang="ts">
	import { m } from '$lib/i18n';
	import { api, type SystemFileEntry } from '$lib/api/client';
	import { Button } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import { errorMessage } from '$lib/utils/errorMessage';
	import { Hash } from 'lucide-svelte';
	import FileCopyButton from './FileCopyButton.svelte';

	interface Props {
		entry: SystemFileEntry;
	}

	let { entry }: Props = $props();

	let md5Hash = $state<string | null>(null);
	let sha256Hash = $state<string | null>(null);
	let calculatingMd5 = $state(false);
	let calculatingSha = $state(false);
	let hashedPath = $state<string | null>(null);

	$effect(() => {
		if (hashedPath === entry.path) return;
		hashedPath = entry.path;
		md5Hash = null;
		sha256Hash = null;
	});

	async function calcMd5() {
		calculatingMd5 = true;
		try {
			const res = await api.systemFilesChecksum(entry.path, 'md5');
			md5Hash = res.checksum;
		} catch (e) {
			notifications.error(errorMessage(e, m.system_files_sum_md5_failed()));
		} finally {
			calculatingMd5 = false;
		}
	}

	async function calcSha256() {
		calculatingSha = true;
		try {
			const res = await api.systemFilesChecksum(entry.path, 'sha256');
			sha256Hash = res.checksum;
		} catch (e) {
			notifications.error(errorMessage(e, m.system_files_sum_sha_failed()));
		} finally {
			calculatingSha = false;
		}
	}
</script>

<div class="section-box">
	<div class="section-title">
		<Hash size={15} />
		<span>{m.system_files_sum_title()}</span>
	</div>

	<div class="hash-list">
		<div class="hash-item">
			<span class="hash-name">MD5:</span>
			{#if md5Hash}
				<code class="hash-val">{md5Hash}</code>
				<FileCopyButton value={md5Hash} />
			{:else}
				<Button size="sm" variant="ghost" loading={calculatingMd5} onclick={calcMd5}>
					{m.system_files_sum_calc_md5()}
				</Button>
			{/if}
		</div>

		<div class="hash-item">
			<span class="hash-name">SHA256:</span>
			{#if sha256Hash}
				<code class="hash-val">{sha256Hash}</code>
				<FileCopyButton value={sha256Hash} />
			{:else}
				<Button size="sm" variant="ghost" loading={calculatingSha} onclick={calcSha256}>
					{m.system_files_sum_calc_sha()}
				</Button>
			{/if}
		</div>
	</div>
</div>

<style>
	.section-box {
		padding: 0.75rem;
		background: var(--color-bg-secondary);
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}
	.section-title {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		font-size: 0.85rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.hash-list {
		display: flex;
		flex-direction: column;
		gap: 0.4rem;
		font-size: 0.82rem;
	}
	.hash-item {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		min-height: 28px;
	}
	.hash-name {
		font-weight: 600;
		width: 65px;
		color: var(--color-text-muted);
	}
	.hash-val {
		font-size: 0.75rem;
		background: var(--color-bg-tertiary);
		padding: 0.2rem 0.4rem;
		border-radius: 4px;
		word-break: break-all;
		flex: 1;
	}
</style>
