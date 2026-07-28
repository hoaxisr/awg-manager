<script lang="ts">
	// «Обзор» — статусная read-only сводка (навигация v3).
	// Страница-оркестратор: праймит непollинговые источники, раскладывает
	// блоки и считает сводный статус. Все метрики — из существующих сторов
	// и ручек (дата-контракт docs/superpowers/2026-07-27-overview-data-audit.md),
	// новых эндпоинтов задача не добавляет.
	import { onMount } from 'svelte';
	import { PageContainer, PageHeader } from '$lib/components/layout';
	import { SectionLabel, StatusDot } from '$lib/components/ui';
	import {
		StatTiles,
		TunnelCounters,
		EngineSingboxCard,
		EngineAwgCard,
		FunctionsRow,
		IntegrationsRow,
		WarnLogCard,
		computeRollup,
	} from '$lib/components/overview';
	import { systemInfo, updateInfo } from '$lib/stores/system';
	import { wanStatus } from '$lib/stores/wanStatus';
	import { tunnels } from '$lib/stores/tunnels';
	import { subscriptionsStore } from '$lib/stores/subscriptions';
	import { hydrarouteStatus } from '$lib/stores/hydraroute';
	import { freeturnStatus } from '$lib/stores/freeturnStatus';
	import { wdttStatus } from '$lib/stores/wdttStatus';
	import { singboxRouter } from '$lib/stores/singboxRouter';

	// singboxRouter — не polling-стор: статус и настройки приезжают по SSE,
	// но при холодном заходе на «Обзор» их ещё некому было загрузить.
	// routingMode живёт только в settings, статус-бейдж — в status.
	const routerStatus = singboxRouter.status;

	onMount(() => {
		void singboxRouter.reloadStatus();
		void singboxRouter.reloadSettings();
	});

	const rollup = $derived(
		computeRollup({
			wan: $wanStatus.data,
			routerStatus: $routerStatus,
			awgTunnels: $tunnels.data?.tunnels ?? [],
			subscriptions: $subscriptionsStore.data ?? [],
			hydraroute: $hydrarouteStatus.data,
			freeturn: $freeturnStatus.data,
			wdtt: $wdttStatus.data,
			updateAvailable: $updateInfo?.available ?? false,
			memoryUsedPercent: $systemInfo.data?.routerDetails?.memoryUsedPercent,
		}),
	);
</script>

<svelte:head>
	<title>Обзор - AWGM</title>
</svelte:head>

<PageContainer width="narrow">
	<PageHeader title="Обзор" description="Состояние подсистем, маршрутизации и интеграций">
		{#snippet actions()}
			<span
				class="rollup"
				class:ok={rollup.ok}
				title={rollup.ok ? 'Проблем не обнаружено' : rollup.reasons.join('\n')}
			>
				<StatusDot variant={rollup.ok ? 'success' : 'warning'} />
				{rollup.ok ? 'Всё работает' : 'Требует внимания'}
			</span>
		{/snippet}
	</PageHeader>

	<StatTiles />

	<section class="block">
		<TunnelCounters />
	</section>

	<section class="block">
		<SectionLabel>Движки</SectionLabel>
		<div class="engines">
			<EngineSingboxCard />
			<EngineAwgCard />
		</div>
	</section>

	<section class="block">
		<FunctionsRow />
	</section>

	<section class="block">
		<IntegrationsRow />
	</section>

	<section class="block">
		<WarnLogCard />
	</section>
</PageContainer>

<style>
	.block {
		margin-top: 1.25rem;
	}

	.engines {
		display: grid;
		grid-template-columns: 1.5fr 1fr;
		gap: 0.75rem;
		margin-top: 0.625rem;
	}

	.rollup {
		display: inline-flex;
		align-items: center;
		gap: 0.4375rem;
		padding: 0.375rem 0.75rem;
		border-radius: var(--radius-sm);
		font-size: 0.75rem;
		font-weight: 500;
		background: var(--color-warning-tint);
		color: var(--color-warning);
		white-space: nowrap;
	}

	.rollup.ok {
		background: var(--color-success-tint);
		color: var(--color-success);
	}

	@media (max-width: 900px) {
		.engines {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
