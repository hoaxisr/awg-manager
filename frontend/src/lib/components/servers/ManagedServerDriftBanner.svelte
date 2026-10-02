<script lang="ts">
	import { m } from '$lib/i18n';
	import { onMount } from 'svelte';
	import { Button } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import type { ManagedServerExport, RestoreOutcome } from '$lib/types';

	let drift = $state<ManagedServerExport[]>([]);
	let restoring = $state(false);
	let lastResult = $state<RestoreOutcome[] | null>(null);

	async function refresh() {
		try {
			const resp = await api.managedServerDrift();
			drift = resp.drift ?? [];
		} catch (e) {
			notifications.error(m.servers_drift_list_failed({ error: (e as Error).message }));
		}
	}

	onMount(refresh);

	async function restore() {
		restoring = true;
		try {
			const resp = await api.managedServerRestoreDrift({ allowRenumber: false });
			lastResult = resp.outcomes;
			await refresh();
		} catch (e) {
			notifications.error((e as Error).message);
		} finally {
			restoring = false;
		}
	}
</script>

{#if drift.length > 0}
	<div class="drift-banner">
		<div class="drift-text">
			<strong>{m.servers_drift_detected({ count: drift.length })}</strong>
			<div class="drift-list">{drift.map((d) => d.interfaceName).join(', ')}</div>
		</div>
		<Button variant="outline-primary" size="sm" onclick={restore} loading={restoring}>
			{m.settings_backup_restore()}
		</Button>
	</div>
{/if}

{#if lastResult}
	<div class="drift-result">
		{m.servers_drift_done()}
		{#each lastResult as r}
			<span class="drift-pill drift-{r.action}">{r.name}: {r.action}</span>
		{/each}
	</div>
{/if}

<style>
	.drift-banner {
		display: flex;
		align-items: center;
		gap: 1rem;
		padding: 0.75rem 1rem;
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
		border-radius: var(--radius);
		margin-bottom: 1rem;
	}
	.drift-text {
		flex: 1;
		min-width: 0;
	}
	.drift-list {
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		margin-top: 0.25rem;
	}
	.drift-result {
		margin: 0.5rem 0;
		display: flex;
		gap: 0.5rem;
		flex-wrap: wrap;
		font-size: 0.8125rem;
	}
	.drift-pill {
		padding: 0.125rem 0.5rem;
		border-radius: 9999px;
		background: var(--color-bg-tertiary);
	}
</style>
