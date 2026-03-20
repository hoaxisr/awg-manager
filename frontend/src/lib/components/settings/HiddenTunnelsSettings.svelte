<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';

	let hiddenTunnels = $state<string[]>([]);
	let loading = $state(true);
	let unhiding = $state<Record<string, boolean>>({});

	onMount(async () => {
		try {
			hiddenTunnels = await api.getHiddenSystemTunnels();
		} catch {
			// ignore
		} finally {
			loading = false;
		}
	});

	async function unhide(id: string) {
		unhiding = { ...unhiding, [id]: true };
		try {
			await api.unhideSystemTunnel(id);
			hiddenTunnels = hiddenTunnels.filter(t => t !== id);
			notifications.success(`Туннель ${id} снова отображается`);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка');
		} finally {
			const { [id]: _, ...rest } = unhiding;
			unhiding = rest;
		}
	}
</script>

{#if !loading && hiddenTunnels.length > 0}
	<section class="card">
		<h2 class="section-title">Скрытые системные туннели</h2>
		<p class="section-desc">Эти туннели не отображаются на главной странице.</p>

		<div class="hidden-list">
			{#each hiddenTunnels as id (id)}
				<div class="hidden-item">
					<span class="hidden-name">{id}</span>
					<button
						class="btn btn-secondary btn-sm"
						disabled={unhiding[id]}
						onclick={() => unhide(id)}
					>
						{unhiding[id] ? 'Показываю...' : 'Показать'}
					</button>
				</div>
			{/each}
		</div>
	</section>
{/if}

<style>
	.section-title {
		font-size: 1rem;
		font-weight: 600;
		margin-bottom: 0.25rem;
	}

	.section-desc {
		font-size: 0.8125rem;
		color: var(--text-muted);
		margin-bottom: 1rem;
	}

	.hidden-list {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.hidden-item {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 0.5rem 0.75rem;
		background: var(--bg-tertiary);
		border-radius: 8px;
	}

	.hidden-name {
		font-family: var(--font-mono, monospace);
		font-size: 0.875rem;
	}

	.btn-sm {
		padding: 0.25rem 0.75rem;
		font-size: 0.8125rem;
	}
</style>
