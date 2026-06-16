<!--
  «Доставка DNS · сегменты» (FE-spec §5.1 / 2.2). Сворачиваемая секция на
  «Обзоре»: список DHCP-пулов-сегментов, у каждого тумблер «в fakeip». Тумблер
  set/clear доставку DNS пула на fakeip-tun .2 (POST /singbox/fakeip/segments),
  затем перечитываем сегменты с бэкенда (ground-truth, не оптимистично).

  ЧЕСТНОСТЬ (§2): inFakeip берём из реального состояния (DHCP dns-server пула ==
  fakeip .2). Глобальный сигнал здоровья egress — status.fakeipEgressUp (задача
  #25): роутер выдаёт .2 пулам ТОЛЬКО при здоровом egress, иначе чистит DNS и ВСЕ
  fakeip-сегменты падают в прямой режим (без чёрной дыры). Когда egress нездоров
  (fakeipEgressUp === false), показываем секционный баннер «доставка придержана»;
  per-pool «придержано» невозможен — бэкенд не хранит per-pool intent.

  Сам запрос сегментов делается лениво — при первом разворачивании секции.
-->
<script lang="ts">
	import { Card, Toggle } from '$lib/components/ui';
	import { ChevronDown, ChevronRight, TriangleAlert } from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { segmentRows, type SegmentRow } from './segmentRows';

	const status = singboxRouter.status;
	// Глобальный «доставка DNS придержана»: явный false (не undefined) = egress
	// нездоров и .2 не выдаётся ни одному пулу. undefined = не fakeip-tun / не
	// провижен — баннер не выдумываем.
	const deliveryHeld = $derived($status?.fakeipEgressUp === false);

	let open = $state(false);
	let loaded = $state(false);
	let loading = $state(false);
	let loadError = $state('');
	let rows = $state<SegmentRow[]>([]);
	// Пулы, у которых тумблер сейчас в полёте — блокируем строку.
	let busy = $state<Record<string, boolean>>({});

	async function loadSegments(): Promise<void> {
		loading = true;
		loadError = '';
		try {
			const data = await api.singboxRouterListSegments();
			rows = segmentRows(data.segments);
			loaded = true;
		} catch (e) {
			loadError = e instanceof Error ? e.message : 'Не удалось загрузить сегменты';
		} finally {
			loading = false;
		}
	}

	function handleHeaderClick(): void {
		open = !open;
		// Ленивая загрузка при первом разворачивании.
		if (open && !loaded && !loading) void loadSegments();
	}

	async function handleToggle(row: SegmentRow, next: boolean): Promise<void> {
		if (busy[row.pool]) return;
		busy = { ...busy, [row.pool]: true };
		try {
			await api.singboxRouterToggleSegment(row.pool, next);
			// Ground-truth refresh — перечитываем фактическое состояние, а не
			// оптимистично доверяем тумблеру.
			await loadSegments();
		} catch (e) {
			const msg = e instanceof Error ? e.message : `Не удалось переключить пул ${row.pool}`;
			notifications.error(msg);
		} finally {
			busy = { ...busy, [row.pool]: false };
		}
	}
</script>

<Card padding="md">
	{#snippet header()}
		<button class="section-toggle" type="button" onclick={handleHeaderClick} aria-expanded={open}>
			{#if open}
				<ChevronDown size={16} />
			{:else}
				<ChevronRight size={16} />
			{/if}
			<span class="section-title">Доставка DNS · сегменты</span>
		</button>
	{/snippet}

	{#if open}
		{#if deliveryHeld}
			<div class="banner" role="status">
				<TriangleAlert size={16} class="banner-icon" />
				<span>
					Egress нездоров — доставка DNS придержана: fakeip-сегменты временно работают
					как прямые. Восстановится автоматически, когда egress поднимется.
				</span>
			</div>
		{/if}

		<p class="note">
			Доставка клиентам работает только при запущенном и здоровом движке fakeip-tun.
			Состояние ниже — фактический DHCP dns-server пула.
		</p>

		{#if loading && !loaded}
			<p class="state-note">Загрузка сегментов…</p>
		{:else if loadError}
			<p class="state-note state-error">{loadError}</p>
		{:else if rows.length === 0}
			<p class="state-note">DHCP-пулы не найдены.</p>
		{:else}
			<ul class="rows">
				{#each rows as row (row.key)}
					<li class="row">
						<div class="row-info">
							<span class="row-pool">{row.pool}</span>
							<span class="row-subnet">{row.subnet}</span>
							{#if row.dnsServer}
								<span class="row-dns">DNS {row.dnsServer}</span>
							{/if}
						</div>
						<Toggle
							size="sm"
							controlled
							checked={row.inFakeip}
							loading={busy[row.pool] ?? false}
							label="в fakeip"
							onchange={(next) => handleToggle(row, next)}
						/>
					</li>
				{/each}
			</ul>
		{/if}
	{/if}
</Card>

<style>
	.section-toggle {
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
		background: none;
		border: none;
		padding: 0;
		cursor: pointer;
		color: var(--text-primary);
	}

	.section-title {
		font-size: 0.9375rem;
		font-weight: 600;
	}

	.banner {
		display: flex;
		align-items: flex-start;
		gap: 0.5rem;
		margin: 0 0 0.75rem;
		padding: 0.625rem 0.75rem;
		border: 1px solid var(--color-warning-border);
		border-radius: 8px;
		background: var(--color-warning-tint);
		color: var(--text-primary);
		font-size: 0.8125rem;
		line-height: 1.45;
		text-wrap: pretty;
	}

	.banner :global(.banner-icon) {
		flex-shrink: 0;
		margin-top: 0.0625rem;
		color: var(--color-warning);
	}

	.note {
		margin: 0 0 0.75rem;
		font-size: 0.8125rem;
		color: var(--text-muted);
		line-height: 1.5;
		text-wrap: pretty;
	}

	.state-note {
		margin: 0;
		font-size: 0.875rem;
		color: var(--text-secondary);
	}

	.state-error {
		color: var(--color-error);
	}

	.rows {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 0.625rem;
	}

	.row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		padding: 0.5rem 0;
		border-bottom: 1px solid var(--color-border);
	}

	.row:last-child {
		border-bottom: none;
	}

	.row-info {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
		min-width: 0;
	}

	.row-pool {
		font-size: 0.875rem;
		font-weight: 500;
		color: var(--text-primary);
	}

	.row-subnet {
		font-size: 0.8125rem;
		color: var(--text-secondary);
	}

	.row-dns {
		font-size: 0.8125rem;
		color: var(--text-muted);
	}
</style>
