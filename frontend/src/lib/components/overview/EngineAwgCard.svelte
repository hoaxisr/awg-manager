<script lang="ts">
	// Движок AmneziaWG: сколько туннелей, сколько включено, кто держит
	// маршрут по умолчанию (TunnelListItem.defaultRoute — реальное поле).
	import { ChevronRight, Server } from 'lucide-svelte';
	import { StatusDot } from '$lib/components/ui';
	import { tunnels } from '$lib/stores/tunnels';

	const DASH = '—';

	const list = $derived($tunnels.data?.tunnels ?? []);
	const enabled = $derived(list.filter((t) => t.enabled).length);
	const running = $derived(list.filter((t) => t.status === 'running').length);
	const broken = $derived(list.filter((t) => t.status === 'broken').length);
	const defaultRoute = $derived(list.find((t) => t.defaultRoute)?.name ?? '');
	const known = $derived($tunnels.lastFetchedAt > 0 || $tunnels.status === 'error');
</script>

<div class="engine">
	<div class="head">
		<span class="icon"><Server size={17} aria-hidden="true" /></span>
		<span class="titles">
			<span class="title">AmneziaWG</span>
			<span class="subtitle">VPN-транспорт</span>
		</span>
		{#if list.length > 0}
			<span class="state" class:alarm={broken > 0}>
				<StatusDot variant={broken > 0 ? 'broken' : running > 0 ? 'success' : 'muted'} size="sm" />
				{broken > 0 ? `${broken} с ошибкой` : `${running} активно`}
			</span>
		{/if}
	</div>

	{#if known && list.length === 0}
		<p class="empty">Туннелей AmneziaWG нет.</p>
	{:else}
		<div class="stats">
			<div class="stat">
				<span class="value">{list.length}</span>
				<span class="label">Туннели</span>
			</div>
			<div class="stat">
				<span class="value">{enabled}</span>
				<span class="label">Включены</span>
			</div>
			<div class="stat">
				<span class="value accent" class:muted={!defaultRoute}>{defaultRoute || DASH}</span>
				<span class="label">Маршрут по умолч.</span>
			</div>
		</div>
	{/if}

	<a class="link" href="/awg/tunnels">Открыть туннели <ChevronRight size={13} aria-hidden="true" /></a>
</div>

<style>
	.engine {
		display: flex;
		flex-direction: column;
		padding: 0.9375rem 1rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		min-width: 0;
	}

	.head {
		display: flex;
		align-items: center;
		gap: 0.625rem;
	}

	.icon {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 32px;
		height: 32px;
		flex: none;
		border-radius: var(--radius-sm);
		background: var(--color-accent-tint);
		color: var(--color-accent);
	}

	.titles {
		display: flex;
		flex-direction: column;
		flex: 1;
		min-width: 0;
	}

	.title {
		font-size: 0.875rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.subtitle {
		font-size: 0.6875rem;
		color: var(--color-text-muted);
	}

	.state {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		flex: none;
		font-size: 0.6875rem;
		color: var(--color-success);
	}

	.state.alarm { color: var(--color-broken); }

	.stats {
		display: flex;
		gap: 1.25rem;
		margin-top: 0.875rem;
		padding-top: 0.8125rem;
		border-top: 1px solid var(--color-border);
	}

	.stat {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
		min-width: 0;
	}

	.value {
		font-family: var(--font-mono);
		font-size: 1rem;
		font-weight: 600;
		color: var(--color-text-primary);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.value.accent { color: var(--color-accent); }
	.value.muted { color: var(--color-text-muted); }

	.label {
		font-size: 0.6875rem;
		color: var(--color-text-muted);
	}

	.empty {
		margin: 0.875rem 0 0;
		padding-top: 0.8125rem;
		border-top: 1px solid var(--color-border);
		font-size: 0.8125rem;
		color: var(--color-text-muted);
	}

	.link {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
		margin-top: auto;
		padding-top: 0.8125rem;
		font-size: 0.75rem;
		font-weight: 500;
		color: var(--color-accent);
		text-decoration: none;
	}

	.link:hover { text-decoration: underline; }
</style>
