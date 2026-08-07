<script lang="ts">
	// Карточка «Конфигурация» страницы «Движок»: итоговый JSON, редактор слотов
	// config.d и чипы слотов. Источник — кнопки шапки PageShell:76-99 и монтаж
	// шторок в SingboxRouterRedesignPage:208-212.
	//
	// ГЕЙТА ПО ЭКСПЕРТ-РЕЖИМУ НЕТ. Кнопка «Редактор» жила под
	// `mode === 'expert'`, но самого режима в nav-v3 не осталось (§2.1 спеки), а
	// §3 снимает гейт явно: от ошибки защищают валидация и черновик (слот
	// проверяется `sing-box check` до применения), а не спрятанная кнопка.
	//
	// ЧИПЫ СЛОТОВ — ИЗ API, СПИСКА В КОДЕ НЕТ (см. configSlots.ts). Загружаем их
	// сама карточка: `singboxConfigSlots` не зовёт никто, кроме шторки слотов, а
	// чипы нужны до её открытия — они и есть ответ на вопрос «из чего собран
	// конфиг». Один запрос на монтаж и повтор после закрытия редактора: там
	// слот включают, выключают и применяют черновик, и устаревшие чипы врали бы
	// ровно про то, что пользователь только что менял.
	import { onMount } from 'svelte';
	import { Button, Card } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import JsonConfigDrawer from '$lib/components/singbox-routing/JsonConfigDrawer.svelte';
	import ConfigSlotsDrawer from '$lib/components/singbox-routing/ConfigSlotsDrawer.svelte';
	import { slotChips } from './configSlots';
	import type { ConfigSlotInfo } from '$lib/types';

	let jsonOpen = $state(false);
	let slotsOpen = $state(false);

	let slots = $state<ConfigSlotInfo[]>([]);
	let slotsFailed = $state(false);
	let slotsLoaded = $state(false);

	const chips = $derived(slotChips(slots));

	onMount(loadSlots);

	async function loadSlots(): Promise<void> {
		try {
			const res = await api.singboxConfigSlots();
			slots = res.slots;
			slotsFailed = false;
		} catch (_e) {
			// Молча и без тоста: чипы — сводка, а не действие пользователя. Обе
			// кнопки карточки работают и без них, и текст ошибки покажет та
			// шторка, которую пользователь откроет.
			slots = [];
			slotsFailed = true;
		} finally {
			slotsLoaded = true;
		}
	}

	function closeSlots(): void {
		slotsOpen = false;
		void loadSlots();
	}

	// «Итоговый конфиг» изнутри шторки слотов: закрываем список, а не кладём
	// вторую шторку поверх первой. Кнопка живёт только в списке (в редакторе
	// слота её нет), поэтому несохранённых правок под ней не бывает.
	//
	// Список перезагружаем как и при обычном закрытии: путь «выключил слот →
	// К списку → Итоговый конфиг» уходит из шторки мимо closeSlots, и чипы под
	// ней остались бы доreload-ными.
	function showMerged(): void {
		slotsOpen = false;
		jsonOpen = true;
		void loadSlots();
	}
</script>

<Card padding="none">
	<div class="head-row">
		<div class="title">Конфигурация</div>
		<div class="sub">Что движок читает на самом деле</div>
	</div>

	<div class="rows">
		<div class="row">
			<div class="row-text">
				<div class="k">Итоговый JSON</div>
				<div class="h">Собранный конфиг, который получает движок</div>
			</div>
			<Button variant="secondary" size="sm" onclick={() => (jsonOpen = true)}>Показать</Button>
		</div>

		<div class="row">
			<div class="row-text">
				<div class="k">Слоты config.d</div>
				<div class="h">Прямая правка исходных фрагментов</div>
			</div>
			<Button variant="secondary" size="sm" onclick={() => (slotsOpen = true)}>Редактор</Button>
		</div>
	</div>

	<section class="sec">
		<div class="sec-cap">Слоты в сборке</div>
		{#if chips.length > 0}
			<div class="chips">
				{#each chips as chip (chip.slot)}
					<span class="chip" data-state={chip.state} title={chip.title}>
						<span class="chip-name">{chip.label}</span>
						{#if chip.note}<span class="chip-note">· {chip.note}</span>{/if}
					</span>
				{/each}
			</div>
			<p class="hint">
				Файлы сливаются в порядке имён. Системные слоты перезаписывают свои генераторы —
				руками правится только 90-user.json.
			</p>
		{:else if !slotsLoaded}
			<p class="hint" data-slots="loading">Загружаем список слотов…</p>
		{:else if slotsFailed}
			<!-- Пустой ответ и упавший запрос — РАЗНЫЕ состояния: «слотов нет» на
			     месте сетевой ошибки сказало бы, что конфиг пуст, хотя мы про него
			     просто ничего не знаем. -->
			<p class="hint" data-slots="failed">
				Список слотов недоступен — откройте редактор, он покажет причину.
			</p>
		{:else}
			<p class="hint" data-slots="empty">Ни один слот пока не собран.</p>
		{/if}
	</section>
</Card>

<JsonConfigDrawer open={jsonOpen} onClose={() => (jsonOpen = false)} />
<ConfigSlotsDrawer open={slotsOpen} onClose={closeSlots} onOpenMerged={showMerged} />

<style>
	.head-row {
		display: flex;
		flex-direction: column;
		gap: 2px;
		padding: 12px var(--sp-4);
		border-bottom: 1px solid var(--border);
	}
	.title {
		font-size: 15px;
		font-weight: 700;
		color: var(--text-primary);
	}
	.sub {
		font-size: 12px;
		line-height: 1.4;
		color: var(--text-muted);
	}
	.rows {
		padding: 4px var(--sp-4);
		border-bottom: 1px solid var(--border);
	}
	.row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 12px;
		padding: 9px 0;
		border-top: 1px solid var(--border);
	}
	.row:first-child {
		border-top: 0;
	}
	/* Текст обязан ужиматься до переноса, иначе на 390px кнопка выдавливается
	   за край карточки. */
	.row-text {
		min-width: 0;
	}
	.k {
		font-size: 13px;
		color: var(--text-primary);
	}
	.h {
		margin-top: 1px;
		font-size: 11.5px;
		line-height: 1.4;
		color: var(--text-muted);
	}
	.sec {
		display: flex;
		flex-direction: column;
		gap: 10px;
		padding: 14px var(--sp-4);
	}
	.sec-cap {
		font-size: 11px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--text-muted);
	}
	.chips {
		display: flex;
		flex-wrap: wrap;
		gap: 5px;
	}
	.chip {
		display: inline-flex;
		align-items: baseline;
		gap: 4px;
		padding: 2px 9px;
		border-radius: 999px;
		background: var(--bg-tertiary);
		border: 1px solid var(--border);
		font-family: var(--font-mono);
		font-size: 11.5px;
		color: var(--text-secondary);
	}
	/* Выключенный слот приглушён, слот с черновиком — тоном черновика (тот же,
	   что у баннера «правки не применены»). */
	.chip[data-state='off'] {
		color: var(--text-muted);
		opacity: 0.75;
	}
	.chip[data-state='draft'] {
		border-color: color-mix(in srgb, var(--color-warning) 45%, transparent);
		color: var(--color-warning);
	}
	.chip-note {
		font-size: 10.5px;
	}
	.hint {
		margin: 0;
		font-size: 11.5px;
		line-height: 1.4;
		color: var(--text-muted);
	}
</style>
