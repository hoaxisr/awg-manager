<script lang="ts">
	// Стат-полоса страницы «Движок»: память · скорость сейчас · соединений ·
	// за сессию (раздел 3 спеки nav-v3 5D).
	//
	// ЭТО НЕ StatStrip ЭКСПЕРТ-ВИДА. Там девять ячеек с help-поповерами и шесть
	// конфиг-счётчиков, которые теперь дублируются бейджами сайдбара; полоса и
	// поповеры уходят по разделу 8. Живой бар-график трафика (TrafficPanel /
	// TrafficSpark) сюда тоже не переезжает — форма графиков решается в 5E.
	//
	// ГЕЙТ ЧЕСТНОСТИ. singboxMemory и singboxTrafficLive держат последние
	// значения после остановки движка: SSE просто замолкает, а сторы не
	// обнуляются. Поэтому все четыре ячейки гейтятся общим «движок работает»
	// (isEngineRunning) и при неработающем движке показывают «—».
	//
	// Условие именно «движок работает», а НЕ перенос `resourcesVisible` из
	// StatusDrawer (`engineActive && activeMode !== null`): activeMode там знал
	// только tproxy и policy-tun, и в режиме FakeIP полоса оказалась бы пустой
	// при живом движке — хуже, чем сегодняшний «Обзор» FakeIP, который те же
	// числа показывает.
	//
	// Значения «—», а не скрытая секция: полоса — часть шапки страницы, и её
	// исчезновение дёргало бы всю раскладку при каждом выключении движка.
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { modeSwitch, modeSwitchInFlight } from '$lib/stores/modeSwitch';
	import { singboxMemory } from '$lib/stores/singboxMemory';
	import { singboxTrafficLive } from '$lib/stores/singboxEngineStats';
	import {
		liveConnectionsSnapshot,
		liveConnectionsWsStatus,
	} from '$lib/components/sb-router/liveConnectionsStore';
	import { formatBytes, formatByteRate } from '$lib/utils/format';
	import { isEngineRunning } from './engineRunState';

	const status = singboxRouter.status;
	const settings = singboxRouter.settings;

	const running = $derived(
		isEngineRunning({
			enabled: $status?.enabled ?? false,
			active: $status?.active ?? false,
			routingMode: $settings?.routingMode,
			// Во время смены режима движок перезапускают: счётчики Clash API
			// остались от прежнего запуска, показывать их как живые нельзя.
			switching: modeSwitchInFlight($modeSwitch),
		}),
	);

	const live = $derived($singboxTrafficLive);
	const DASH = '—';
	const IDLE_HINT = 'Движок не работает — живых чисел нет';

	const memoryValue = $derived(running && $singboxMemory > 0 ? formatBytes($singboxMemory) : DASH);
	const memoryHint = $derived(
		running
			? 'Память Go-рантайма sing-box по данным Clash API; фактический RSS процесса выше'
			: IDLE_HINT,
	);

	// Скорость и объём показываем по загрузке (как в макете), отдачу — в
	// подсказке: четыре ячейки в строку не переживают «↓ … · ↑ …» на 390px.
	const rateValue = $derived(
		running && live.rate.hasRate ? formatByteRate(live.rate.downloadRate) : DASH,
	);
	const rateHint = $derived(
		running
			? live.rate.hasRate
				? `Отдача сейчас: ${formatByteRate(live.rate.uploadRate)}`
				: 'Скорость появится после второго снимка счётчиков (~2 с)'
			: IDLE_HINT,
	);

	// Поток соединений — отдельный источник (Clash WS), он рвётся независимо от
	// движка. Число оставляем последнее известное, но помечаем протухшим:
	// обнулять его на каждом переподключении означало бы мигать нулём.
	const connectionsStale = $derived(running && $liveConnectionsWsStatus !== 'open');
	const connectionsValue = $derived(running ? String($liveConnectionsSnapshot.connectionsTotal) : DASH);
	const connectionsHint = $derived(
		!running
			? IDLE_HINT
			: connectionsStale
				? 'Поток живых соединений оборван — число последнее известное'
				: 'Живых соединений через движок сейчас',
	);

	const sessionValue = $derived(running ? formatBytes(live.totals.downloadBytes) : DASH);
	const sessionHint = $derived(
		running
			? `За сессию движка: ↓ ${formatBytes(live.totals.downloadBytes)} · ↑ ${formatBytes(live.totals.uploadBytes)}`
			: IDLE_HINT,
	);
</script>

<div class="stats" data-running={running}>
	<div class="stat" title={memoryHint}>
		<div class="v">{memoryValue}</div>
		<div class="l">Память</div>
	</div>
	<div class="stat" title={rateHint}>
		<div class="v">{rateValue}</div>
		<div class="l">Сейчас ↓</div>
	</div>
	<div class="stat" title={connectionsHint}>
		<div class="v" class:is-stale={connectionsStale}>{connectionsValue}</div>
		<div class="l">Соединений</div>
	</div>
	<div class="stat" title={sessionHint}>
		<div class="v">{sessionValue}</div>
		<div class="l">За сессию ↓</div>
	</div>
</div>

<style>
	/* Разделители — фон сетки в зазорах (gap: 1px), а не border у ячеек: так
	   не двоятся линии на переносе строк в две колонки. */
	.stats {
		display: grid;
		grid-template-columns: repeat(4, 1fr);
		gap: 1px;
		margin-bottom: 1rem;
		background: var(--border);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		overflow: hidden;
	}

	.stat {
		background: var(--bg-secondary);
		padding: 0.6rem 0.75rem;
		min-width: 0;
	}

	.v {
		font-size: 1.05rem;
		font-weight: 700;
		color: var(--text-primary);
		font-variant-numeric: tabular-nums;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.v.is-stale {
		color: var(--color-warning);
	}

	.l {
		margin-top: 1px;
		font-size: 0.625rem;
		letter-spacing: 0.07em;
		text-transform: uppercase;
		color: var(--text-muted);
	}

	/* Ниже 560px четыре колонки дают «38 МБ» в две строки и обрезанные
	   подписи — переходим на две. */
	@media (max-width: 560px) {
		.stats {
			grid-template-columns: repeat(2, 1fr);
		}
	}
</style>
