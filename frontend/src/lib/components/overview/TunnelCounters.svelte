<script lang="ts">
	// Счётчики туннелей: AmneziaWG / Sing-box / AWG3 / Подписки.
	// Источники: tunnels (poll 5s), singboxTunnels (poll 5s), awg3Tunnels
	// (poll 10s), subscriptionsStore (poll 30s, подписан глобально в +layout).
	// У AWG3 up-статуса не существует — показываем только количество.
	import { ChevronRight } from 'lucide-svelte';
	import { SectionLabel } from '$lib/components/ui';
	import { tunnels } from '$lib/stores/tunnels';
	import { singboxTunnels } from '$lib/stores/singbox';
	import { awg3Tunnels } from '$lib/stores/awg3';
	import { subscriptionsStore } from '$lib/stores/subscriptions';

	const awg = $derived($tunnels.data?.tunnels ?? []);
	const awgActive = $derived(awg.filter((t) => t.status === 'running').length);
	const awgBroken = $derived(awg.filter((t) => t.status === 'broken').length);

	const sb = $derived($singboxTunnels.data ?? []);
	const sbRunning = $derived(sb.filter((t) => t.running).length);

	const awg3 = $derived($awg3Tunnels.data ?? []);
	const subs = $derived($subscriptionsStore.data ?? []);

	const subsSub = $derived.by(() => {
		if (subs.length === 0) return 'нет подписок';
		const enabled = subs.filter((s) => s.enabled);
		if (enabled.length === 0) return 'все выключены';
		const failed = enabled.filter((s) => s.lastError).length;
		if (failed > 0) return `${failed} с ошибкой`;
		return enabled.every((s) => s.lastFetched) ? 'синхронизированы' : 'не обновлялись';
	});

	const cards = $derived([
		{
			href: '/awg/tunnels',
			title: 'AmneziaWG',
			value: awg.length,
			sub: awg.length === 0 ? 'нет туннелей' : awgBroken > 0 ? `${awgActive} активны · ${awgBroken} с ошибкой` : `${awgActive} активны`,
			alarm: awgBroken > 0,
		},
		{
			href: '/sb/tunnels',
			title: 'Sing-box',
			value: sb.length,
			sub: sb.length === 0 ? 'нет туннелей' : `${sbRunning} работают`,
			alarm: false,
		},
		{
			href: '/sb/awg3',
			title: 'Endpoints',
			value: awg3.length,
			sub: awg3.length === 0 ? 'нет туннелей' : 'endpoint-туннели',
			alarm: false,
		},
		{
			href: '/sb/subscriptions',
			title: 'Подписки',
			value: subs.length,
			sub: subsSub,
			alarm: subsSub.endsWith('с ошибкой'),
		},
	]);
</script>

<SectionLabel>Туннели</SectionLabel>

<div class="counters">
	{#each cards as card (card.href)}
		<a class="counter" href={card.href}>
			<span class="text">
				<span class="title">{card.title}</span>
				<span class="sub" class:alarm={card.alarm}>{card.sub}</span>
			</span>
			<span class="value">{card.value}</span>
			<ChevronRight size={14} aria-hidden="true" />
		</a>
	{/each}
</div>

<style>
	.counters {
		display: grid;
		grid-template-columns: repeat(4, minmax(0, 1fr));
		gap: 0.75rem;
		margin-top: 0.625rem;
	}

	.counter {
		display: flex;
		align-items: center;
		gap: 0.625rem;
		padding: 0.75rem 0.875rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		color: var(--color-text-muted);
		text-decoration: none;
		min-width: 0;
	}

	.counter:hover {
		background: var(--color-bg-hover);
		border-color: var(--color-border-hover);
	}

	.text {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
		min-width: 0;
	}

	.title {
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--color-text-secondary);
	}

	.sub {
		font-size: 0.6875rem;
		color: var(--color-text-muted);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.sub.alarm {
		color: var(--color-error);
	}

	.value {
		margin-left: auto;
		font-family: var(--font-mono);
		font-size: 1.25rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	@media (max-width: 900px) {
		.counters {
			grid-template-columns: repeat(2, minmax(0, 1fr));
		}
	}
</style>
