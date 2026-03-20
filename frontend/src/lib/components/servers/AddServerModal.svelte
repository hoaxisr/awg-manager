<script lang="ts">
	import type { SystemTunnel } from '$lib/types';
	import { api } from '$lib/api/client';
	import { Modal } from '$lib/components/ui';
	import { LoadingSpinner } from '$lib/components/layout';

	interface Props {
		open: boolean;
		existingServerIds: string[];
		onclose: () => void;
		onAdded: () => void;
	}

	let { open = $bindable(false), existingServerIds, onclose, onAdded }: Props = $props();

	let tunnels = $state<SystemTunnel[]>([]);
	let loading = $state(false);
	let adding = $state('');

	$effect(() => {
		if (open) {
			loadTunnels();
		}
	});

	async function loadTunnels() {
		loading = true;
		try {
			const all = await api.listSystemTunnels();
			tunnels = all.filter(t => !existingServerIds.includes(t.id));
		} catch {
			tunnels = [];
		} finally {
			loading = false;
		}
	}

	async function addServer(id: string) {
		if (adding) return;
		adding = id;
		try {
			await api.markServerInterface(id);
			onAdded();
			onclose();
		} catch {
			// error handled by caller
		} finally {
			adding = '';
		}
	}
</script>

<Modal {open} title="Добавить интерфейс" size="sm" {onclose}>
	{#if loading}
		<div class="flex justify-center py-4">
			<LoadingSpinner size="sm" />
		</div>
	{:else if tunnels.length === 0}
		<p class="text-muted">Нет доступных системных туннелей для добавления.</p>
	{:else}
		<div class="tunnel-list">
			{#each tunnels as tunnel (tunnel.id)}
				<button
					class="tunnel-option"
					disabled={adding !== ''}
					onclick={() => addServer(tunnel.id)}
				>
					<div class="tunnel-info">
						<span class="tunnel-name">{tunnel.description || tunnel.id}</span>
						<span class="tunnel-iface">{tunnel.interfaceName}</span>
					</div>
					<div class="tunnel-right">
						{#if adding === tunnel.id}
							<LoadingSpinner size="sm" />
						{:else}
							<span class="tunnel-status" class:up={tunnel.status === 'up'}>
								{tunnel.status === 'up' ? 'up' : 'down'}
							</span>
						{/if}
					</div>
				</button>
			{/each}
		</div>
	{/if}
</Modal>

<style>
	.tunnel-list {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.tunnel-option {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		padding: 0.75rem;
		border: 1px solid var(--border);
		border-radius: var(--radius);
		cursor: pointer;
		transition: border-color 0.15s, background 0.15s;
		background: transparent;
		text-align: left;
		width: 100%;
	}

	.tunnel-option:hover:not(:disabled) {
		border-color: var(--accent);
		background: rgba(var(--accent-rgb, 59, 130, 246), 0.05);
	}

	.tunnel-option:disabled {
		opacity: 0.6;
		cursor: not-allowed;
	}

	.tunnel-info {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
		flex: 1;
		min-width: 0;
	}

	.tunnel-name {
		font-weight: 500;
		font-size: 0.875rem;
		color: var(--text-primary);
	}

	.tunnel-iface {
		font-family: var(--font-mono, monospace);
		font-size: 0.75rem;
		color: var(--text-muted);
	}

	.tunnel-right {
		flex-shrink: 0;
	}

	.tunnel-status {
		font-size: 0.75rem;
		font-weight: 500;
		color: var(--text-muted);
	}

	.tunnel-status.up {
		color: var(--success);
	}
</style>
