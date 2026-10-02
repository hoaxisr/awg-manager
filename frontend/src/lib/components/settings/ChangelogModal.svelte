<script lang="ts">
	import { m } from '$lib/i18n';
	import { api } from '$lib/api/client';
	import { Modal, Button } from '$lib/components/ui';
	import { LoadingSpinner } from '$lib/components/layout';
	import ChangelogRender from './ChangelogRender.svelte';
	import type { ChangelogEntry } from '$lib/types';

	interface Props {
		open: boolean;
		fromVersion: string;
		toVersion: string;
		/** true — диапазон до pending-релиза; false — уже установленная ветка (minor line). */
		pendingUpdate?: boolean;
		oncheckUpdates?: () => void;
		onclose: () => void;
	}

	let {
		open,
		fromVersion,
		toVersion,
		pendingUpdate = false,
		oncheckUpdates,
		onclose
	}: Props = $props();

	let loading = $state(false);
	let error = $state('');
	let entries = $state<ChangelogEntry[]>([]);

	$effect(() => {
		if (!open) return;
		loading = true;
		error = '';
		entries = [];
		api.getUpdateChangelog(fromVersion, toVersion)
			.then((resp) => {
				entries = resp.entries ?? [];
			})
			.catch((e: unknown) => {
				error = e instanceof Error ? e.message : String(e);
			})
			.finally(() => {
				loading = false;
			});
	});
</script>

<Modal {open} title={m.settings_changelog_title()} size="lg" {onclose}>
	<div class="modal-body">
		{#if !pendingUpdate}
			<div class="changelog-notice" role="status">
				<p>
					{m.settings_changelog_installed_note()}
				</p>
				<p class="changelog-notice-hint">
					{m.settings_changelog_check_hint()}
				</p>
				{#if oncheckUpdates}
					<Button variant="secondary" size="sm" onclick={oncheckUpdates}>
						{m.settings_changelog_check_updates()}
					</Button>
				{/if}
			</div>
		{/if}
		{#if loading}
			<LoadingSpinner />
		{:else if error}
			<p class="state-msg state-error">{m.settings_changelog_load_error({ error })}</p>
		{:else if entries.length === 0}
			<p class="state-msg">{m.settings_changelog_empty()}</p>
		{:else}
			<ChangelogRender {entries} />
		{/if}
	</div>
	{#snippet actions()}
		<Button variant="primary" size="md" onclick={onclose}>{m.settings_changelog_close()}</Button>
	{/snippet}
</Modal>

<style>
	.modal-body {
		max-height: 70vh;
		overflow-y: auto;
	}
	.state-msg {
		margin: 0;
		padding: 12px 0;
		color: var(--text-muted);
	}
	.state-error {
		color: var(--error);
	}
	.changelog-notice {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 0.5rem;
		padding: 0.75rem 1rem;
		margin-bottom: 0.75rem;
		border: 1px solid var(--color-warning-border, var(--border));
		border-radius: var(--radius);
		background: var(--color-warning-tint, var(--bg-secondary, rgba(234, 179, 8, 0.08)));
	}
	.changelog-notice p {
		margin: 0;
		font-size: 0.875rem;
		color: var(--text-secondary);
		line-height: 1.4;
	}
	.changelog-notice-hint {
		color: var(--text-muted) !important;
	}
</style>
