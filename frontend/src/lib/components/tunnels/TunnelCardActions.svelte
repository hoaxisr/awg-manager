<script lang="ts">
	interface Props {
		tunnel: { id: string; status: string };
		deleteLoading: boolean;
		ondelete: () => void;
	}

	let {
		tunnel,
		deleteLoading = false,
		ondelete
	}: Props = $props();
</script>

<div class="actions-wrapper">
	<div class="actions-row flex gap-2 items-center justify-center">
		<a href="/tunnels/{tunnel.id}" class="btn btn-ghost">
			<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
				<path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/>
				<path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/>
			</svg>
			Изменить
		</a>

		<a href="/tunnels/{tunnel.id}/test" class="btn btn-ghost" title="Тестирование туннеля">
			<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
				<path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/>
				<polyline points="22,4 12,14.01 9,11.01"/>
			</svg>
			Тест
		</a>

		<button
			class="btn btn-ghost btn-danger-ghost"
			class:btn-loading={deleteLoading}
			disabled={deleteLoading}
			onclick={() => ondelete?.()}
			title="Удалить туннель"
		>
			{#if deleteLoading}
				<span class="spinner"></span>
			{:else}
				<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
					<polyline points="3,6 5,6 21,6"/>
					<path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/>
				</svg>
			{/if}
			Удалить
		</button>
	</div>
</div>

<style>
	.actions-wrapper {
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding-top: 12px;
		border-top: 1px solid var(--border);
	}


	.btn-danger-ghost {
		color: var(--text-muted);
	}

	.btn-danger-ghost:hover {
		background: rgba(239, 68, 68, 0.15);
		color: var(--error);
	}

	@media (max-width: 400px) {
		.actions-row {
			flex-wrap: wrap;
		}
	}
</style>
