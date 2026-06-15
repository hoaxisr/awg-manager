<!--
  «Доставка DNS · сегменты» (FE-spec §5.1 / 2.2). Сворачиваемая секция на
  «Обзоре»: список DHCP-пулов-сегментов, у каждого тумблер «в fakeip». Тумблер
  set/clear доставку DNS пула на fakeip-tun .2 (POST /singbox/fakeip/segments),
  затем перечитываем сегменты с бэкенда (ground-truth, не оптимистично).

  ЧЕСТНОСТЬ (§2): inFakeip берём из реального состояния (DHCP dns-server пула ==
  fakeip .2). Индикатор «придержано до здорового egress» НЕ выдумываем — поле
  egress-health бэкенда (задача #25) ещё отсутствует. Поэтому секция несёт общую
  заметку: доставка клиентам зависит от работающего и здорового движка.

  Сам запрос делается лениво — при первом разворачивании секции.
-->
<script lang="ts">
	import { Card, Toggle } from '$lib/components/ui';
	import { ChevronDown, ChevronRight } from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { segmentRows, type SegmentRow } from './segmentRows';

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
			const segments = await api.singboxRouterListSegments();
			rows = segmentRows(segments);
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
