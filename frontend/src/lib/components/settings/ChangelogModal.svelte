<script lang="ts">
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

<Modal {open} title="Что нового" size="lg" {onclose}>
	<div class="modal-body">
		{#if !pendingUpdate}
			<div class="changelog-notice" role="status">
				<p>
					В данном списке представлены изменения из версий, которые были выпущены и установлены ранее.
				</p>
				<p class="changelog-notice-hint">
					Вы можете проверить, доступно ли обновление, нажав кнопку ниже.
				</p>
				{#if oncheckUpdates}
					<Button variant="secondary" size="sm" onclick={oncheckUpdates}>
						Проверить обновления
					</Button>
				{/if}
			</div>
		{/if}
		{#if loading}
			<LoadingSpinner />
		{:else if error}
			<p class="state-msg state-error">Не удалось загрузить changelog. {error}</p>
		{:else if entries.length === 0}
			<p class="state-msg">В CHANGELOG нет записей для этой ветки версий.</p>
		{:else}
			<ChangelogRender {entries} />
		{/if}
	</div>
	{#snippet actions()}
		<Button variant="primary" size="md" onclick={onclose}>Закрыть</Button>
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
		background: var(--color-warning-tint);
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
