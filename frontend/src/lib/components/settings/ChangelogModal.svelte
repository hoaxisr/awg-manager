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
		onclose: () => void;
	}

	let { open, fromVersion, toVersion, onclose }: Props = $props();

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
		{#if loading}
			<LoadingSpinner />
		{:else if error}
			<p class="state-msg state-error">Не удалось загрузить changelog. {error}</p>
		{:else if entries.length === 0}
			<p class="state-msg">Нет данных о новых версиях.</p>
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
</style>
