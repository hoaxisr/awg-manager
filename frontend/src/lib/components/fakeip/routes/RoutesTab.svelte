<!--
  «Маршруты»-чип FakeIP по мокапу page-routes: ОДНА карточка на всю ширину —
  плотная ТАБЛИЦА route-правил (grip | # | match | → outbound | действия) +
  read-only «final»-строка снизу. first-match, порядок задаётся drag'ом.

  Чистая ПЕРЕКОМПОНОВКА route.rules-машинерии sb-router под таблицу мокапа —
  никакого нового рендера матчеров/аутбаундов:
    - Матч-бейджи — общий MatcherChip.svelte (rule_set / domain / ip_cidr / src /
      protocol / port), данные из singboxRuleToCard → RuleCardData.matchers.
    - → outbound — общий RuleOutboundAction.svelte: reject=error-red,
      composite=accent-highlight приходят «бесплатно» (тот же рендер, что в
      RuleCard). Final-строка — resolveOutboundDisplay(status.final) через тот же
      RuleOutboundAction.
    - Add/Edit — RuleEditModal (routing/singboxRouter) ВЕРБАТИМ как в ExpertPanel:
      add при rule===undefined, полный edit при переданном rule; CRUD через
      api.singboxRouter{Add,Update,Delete}Rule + singboxRouter.loadAll().
    - Импорта route-правил нет (ни FE-компонента, ни backend-эндпоинта) —
      кнопку «Импорт» из мокапа не показываем (по решению: добавить, когда
      появится backend bulk-import). Наборы тянутся в Rule sets → «Каталог».
    - Drag — общий reorderDrag.svelte (ВЕРБАТИМ-движок route.rules: floating
      ghost + раскрывающийся/схлопывающийся скелетон-слот + autoscroll + порог +
      pointer-capture). Оптимистика applyRules + api.singboxRouterMoveRule + откат.
      «final»-строка (последний индекс) isFixed — ни схватить, ни уронить под неё.

  RulesPanel.svelte / RuleCard.svelte / страница маршрутов НЕ трогаются — только
  композиция их под-компонентов.

  Движок-гейт: route-правила — это конфиг, рендерится при любом состоянии движка.
-->
<script lang="ts">
	import { onDestroy, onMount } from 'svelte';
	import { get } from 'svelte/store';
	import { fakeipConfig } from '$lib/stores/fakeipConfig';
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { subscriptionsStore } from '$lib/stores/subscriptions';
	import { singboxProxies } from '$lib/stores/singboxProxies';
	import { singboxTunnels } from '$lib/stores/singbox';
	import { presetCatalog } from '$lib/stores/presets';
	import { notifications } from '$lib/stores/notifications';
	import { api } from '$lib/api/client';
	import { createReorderDrag } from '$lib/components/sb-router/reorderDrag.svelte';
	import { singboxRuleToCard, resolveOutboundDisplay } from '$lib/components/sb-router/adapters';
	import MatcherChip from '$lib/components/sb-router/MatcherChip.svelte';
	import RuleOutboundAction from '$lib/components/sb-router/RuleOutboundAction.svelte';
	import { displayRuleSetTag } from '$lib/utils/singboxInlineRules';
	import {
		RuleEditModal,
		computeRuleSetUsage,
	} from '$lib/components/routing/singboxRouter';
	import { ConfirmModal, Dropdown } from '$lib/components/ui';
	import type { DropdownOption } from '$lib/components/ui';
	import { GripVertical, Pencil, Trash2, Plus, Check, X } from 'lucide-svelte';
	import type { SingboxRouterRule } from '$lib/types';
	import type { RuleCardData } from '$lib/components/sb-router/types';

	// ── Store sub-stores ────────────────────────────────────────────────────
	const storeRules = fakeipConfig.rules;
	const storeRuleUiKeys = fakeipConfig.ruleUiKeys;
	const storeRuleSets = fakeipConfig.ruleSets;
	const storeOutbounds = fakeipConfig.outbounds;
	const storeOptions = fakeipConfig.options;

	// singboxRouter.status is mode-aware: in fakeip-tun mode the backend populates
	// status.final from the fakeip slot.
	const routerStatus = singboxRouter.status;

	let currentFinal = $state('direct');

	onMount(() => {
		void fakeipConfig.loadAll();
	});

	// rulesetLabels: tag → отображаемое имя (у набора нет label, только tag).
	const rulesetLabels = $derived.by(() => {
		const labels: Record<string, string> = {};
		for (const rs of $storeRuleSets) {
			if (rs.tag) labels[rs.tag] = displayRuleSetTag(rs.tag);
		}
		return labels;
	});

	// Каждое правило → RuleCardData (матчеры + outbound) тем же адаптером, что
	// RulesPanel — рендер бейджей/аутбаунда идентичен карточному виду.
	// fakeip has no presets; pass [] so service-icon detection still works via
	// the global presetCatalog but no router-preset icons appear.
	const cards: RuleCardData[] = $derived.by(() =>
		$storeRules.map((r, i) =>
			singboxRuleToCard(
				r,
				i,
				$storeOutbounds,
				rulesetLabels,
				[],
				$storeOptions,
				$presetCatalog,
				$storeRuleSets,
				$subscriptionsStore.data,
				$singboxProxies.data ?? [],
				$singboxTunnels.data ?? [],
				$storeRuleUiKeys[i],
			),
		),
	);

	// Final-outbound — rendered via RuleOutboundAction (same as tproxy).
	// currentFinal is seeded from singboxRouter.status.final (mode-aware) and
	// updated on successful save.
	const finalOutbound = $derived(
		resolveOutboundDisplay(
			currentFinal,
			'route',
			$storeOutbounds,
			$storeOptions,
			$subscriptionsStore.data,
			$singboxProxies.data ?? [],
			$singboxTunnels.data ?? [],
		),
	);

	// ── Final-outbound правка (route.final) ─────────────────────────────────
	// Опции: direct + все outbounds, кроме группы «Специальные».
	const routeFinalOptions = $derived<DropdownOption[]>([
		{ value: 'direct', label: 'direct (мимо VPN)' },
		...$storeOptions
			.filter((g) => g.group !== 'Специальные')
			.flatMap((g) => g.items.map((it) => ({ value: it.value, label: it.label, group: g.group }))),
	]);

	let finalEditing = $state(false);
	let draftFinal = $state('direct');
	let finalBusy = $state(false);

	// Sync currentFinal from the mode-aware status when it arrives. Stop syncing
	// while the user has the inline editor open to avoid clobbering an in-progress
	// edit.
	$effect(() => {
		const f = $routerStatus?.final;
		if (f && !finalEditing) currentFinal = f;
	});

	function startEditFinal(): void {
		draftFinal = currentFinal;
		finalEditing = true;
	}

	async function saveFinal(): Promise<void> {
		if (finalBusy) return;
		if (draftFinal === currentFinal) {
			finalEditing = false;
			return;
		}
		finalBusy = true;
		try {
			await api.singboxFakeIPSetRouteFinal(draftFinal);
			currentFinal = draftFinal;
			// Refresh the mode-aware status (the source the seed-effect reads) BEFORE
			// clearing finalEditing, so the effect doesn't briefly revert currentFinal
			// to the pre-save final while waiting for the status SSE event.
			await singboxRouter.reloadStatus();
			await fakeipConfig.loadAll();
			notifications.success('Final-outbound обновлён');
			finalEditing = false;
		} catch (e) {
			notifications.error(`Ошибка: ${e instanceof Error ? e.message : String(e)}`);
		} finally {
			finalBusy = false;
		}
	}

	// ── Drag-reorder (ВЕРБАТИМ-движок route.rules) ─────────────────────────
	let ruleRowEls = $state<Array<HTMLElement | null>>([]);
	let panelEl = $state<HTMLElement | null>(null);

	function reorder<T>(list: T[], from: number, to: number): T[] {
		const next = list.slice();
		const [moved] = next.splice(from, 1);
		next.splice(to, 0, moved);
		return next;
	}

	const drag = createReorderDrag({
		getRowElement: (i) => ruleRowEls[i] ?? null,
		// +1 виртуальная read-only «final»-строка в самом конце.
		count: () => $storeRules.length + 1,
		getPanelEl: () => panelEl,
		// Фиксированы: «final»-строка (последний индекс) и системные правила
		// (sniff / hijack-dns / локальная сеть) — их нельзя ни схватить, ни
		// сделать целью переноса; firstMovableIndex держит юзер-правила ниже них.
		isFixed: (i) => i >= $storeRules.length || !!cards[i]?.isSystem,
		onCommit: async (from, to) => {
			const snapshot = get(fakeipConfig.rules);
			fakeipConfig.applyRules(reorder(snapshot, from, to));
			try {
				await api.singboxFakeIPMoveRule(from, to);
				await fakeipConfig.loadAll();
			} catch (e) {
				fakeipConfig.applyRules(snapshot);
				notifications.error(`Ошибка перемещения: ${e instanceof Error ? e.message : String(e)}`);
			}
		},
	});

	onDestroy(() => drag.destroy());

	// ── Modal state ────────────────────────────────────────────────────────
	let ruleAddOpen = $state(false);
	let ruleEditIdx = $state<number | null>(null);

	const ruleEditTarget = $derived<SingboxRouterRule | undefined>(
		ruleEditIdx !== null ? $storeRules[ruleEditIdx] : undefined,
	);

	// ruleSetUsage для RuleEditModal: исключаем редактируемый индекс.
	const ruleSetUsageForAdd = $derived(computeRuleSetUsage($storeRules));
	const ruleSetUsageForEdit = $derived(
		ruleEditIdx === null
			? new Map<string, number>()
			: computeRuleSetUsage($storeRules, ruleEditIdx),
	);

	// Подтверждение удаления.
	let deleteIdx = $state<number | null>(null);
	let deleteBusy = $state(false);

	function ruleSummary(card: RuleCardData | undefined, idx: number): string {
		if (!card) return `правило #${idx + 1}`;
		return `правило #${idx + 1}: ${card.title}`;
	}

	// ── Handlers ─────────────────────────────────────────────────────────────
	async function handleRuleSave(rule: SingboxRouterRule): Promise<void> {
		if (ruleEditIdx !== null) {
			await api.singboxFakeIPUpdateRule(ruleEditIdx, rule);
		} else {
			await api.singboxFakeIPAddRule(rule);
		}
		ruleEditIdx = null;
		ruleAddOpen = false;
		await fakeipConfig.loadAll();
	}

	async function confirmDelete(): Promise<void> {
		if (deleteIdx === null) return;
		const idx = deleteIdx;
		deleteBusy = true;
		try {
			await api.singboxFakeIPDeleteRule(idx);
			await fakeipConfig.loadAll();
			notifications.success('Правило удалено');
			deleteIdx = null;
		} catch (e) {
			notifications.error(`Ошибка: ${e instanceof Error ? e.message : String(e)}`);
		} finally {
			deleteBusy = false;
		}
	}

	const MAX_CHIPS = 4;

	// ── Маркер «ставит NDMS-маршрут» ────────────────────────────────────────
	// Бэкенд (router/fakeip_cidr_routes.go: loopSafeProxyRule + desiredTunCIDRs)
	// ставит статический NDMS-маршрут на tun для дст-CIDR правила ТОЛЬКО когда оно
	// «loop-safe proxy»: action=route на прокси-outbound (не direct/reject) И его
	// ЕДИНСТВЕННЫЕ матчеры — ip_cidr и/или rule_set (никаких port / source_ip_cidr /
	// domain* / protocol / вложенной логики / ip_is_private), И есть хотя бы один
	// ip_cidr или ссылка на rule_set. Любой сужающий матчер → правило пропускается
	// (by-IP-пакет может не совпасть → петля), и маршрут НЕ ставится. Маркер
	// показываем строго по тем же правилам, иначе UI вводил бы в заблуждение.
	function ruleInstallsTunRoute(rule: SingboxRouterRule): boolean {
		// loop-safe proxy: route на не-direct/reject outbound.
		if (rule.action !== 'route' || !rule.outbound || rule.outbound === 'direct') {
			return false;
		}
		// Никаких сужающих матчеров (включая возможные domain/domain_keyword/
		// domain_regex, которых нет в типе, но могут прийти в JSON).
		const r = rule as SingboxRouterRule & Record<string, unknown>;
		if (
			(rule.domain_suffix?.length ?? 0) > 0 ||
			(rule.source_ip_cidr?.length ?? 0) > 0 ||
			(rule.port?.length ?? 0) > 0 ||
			rule.protocol ||
			rule.ip_is_private != null ||
			rule.rules !== undefined ||
			r.type !== undefined ||
			r.mode !== undefined ||
			(Array.isArray(r.domain) && r.domain.length > 0) ||
			(Array.isArray(r.domain_keyword) && r.domain_keyword.length > 0) ||
			(Array.isArray(r.domain_regex) && r.domain_regex.length > 0)
		) {
			return false;
		}
		// Должен быть хотя бы один ip_cidr ИЛИ ссылка на rule_set.
		return (rule.ip_cidr?.length ?? 0) > 0 || (rule.rule_set?.length ?? 0) > 0;
	}
</script>

<!-- ── Сниппет строки правила (рисуется и в таблице, и в ghost'е) ──────── -->
{#snippet ruleRow(card: RuleCardData, i: number, ghost: boolean)}
	{@const visibleChips = card.matchers.slice(0, MAX_CHIPS)}
	{@const hiddenCount = Math.max(0, card.matchers.length - MAX_CHIPS)}
	<div class="rrow" class:dragging={!ghost && drag.draggingIndex === i}>
		{#if card.isSystem}
			<!-- Системные правила (sniff / hijack-dns / локальная сеть) — фиксированы:
			     ни перетащить, ни редактировать, ни удалить. -->
			<span class="grip grip-fixed" aria-hidden="true"></span>
		{:else}
			<button
				type="button"
				class="grip"
				class:is-busy={drag.busy}
				aria-label={`Перетащить правило #${i + 1}`}
				title="Перетащить для изменения порядка"
				onpointerdown={drag.busy ? undefined : (e) => drag.handlePointerDown(i, e)}
			>
				<GripVertical size={16} strokeWidth={2} />
			</button>
		{/if}
		<span class="num">{i + 1}</span>
		<div class="match">
			{#if visibleChips.length === 0}
				<span class="m-none">—</span>
			{:else}
				{#each visibleChips as chip, ci (chip.kind + ci)}
					<MatcherChip
						kind={chip.kind}
						label={chip.label}
						mono={chip.mono}
						rulesetType={chip.rulesetType}
					/>
				{/each}
				{#if hiddenCount > 0}
					<span class="more">+{hiddenCount} ещё</span>
				{/if}
			{/if}
		</div>
		<div class="outbound">
			<RuleOutboundAction outbound={card.outbound} />
			{#if $storeRules[i] && ruleInstallsTunRoute($storeRules[i])}
				<span
					class="tun-route-mark"
					title="Ставит статический NDMS-маршрут на tun (ловит by-IP)"
				>
					NDMS-маршрут
				</span>
			{/if}
		</div>
		<div class="acts">
			{#if !card.isSystem}
				<button
					type="button"
					class="ib"
					onclick={() => (ruleEditIdx = i)}
					aria-label={`Редактировать правило #${i + 1}`}
					title={`Редактировать правило #${i + 1}`}
				>
					<Pencil size={15} strokeWidth={2} />
				</button>
				<button
					type="button"
					class="ib danger"
					onclick={() => (deleteIdx = i)}
					aria-label={`Удалить правило #${i + 1}`}
					title={`Удалить правило #${i + 1}`}
				>
					<Trash2 size={15} strokeWidth={2} />
				</button>
			{/if}
		</div>
	</div>
{/snippet}

<section class="panel" bind:this={panelEl}>
	<header class="ph">
		<span class="nm">Route rules · {$storeRules.length}</span>
		<button type="button" class="add" onclick={() => (ruleAddOpen = true)}>
			<Plus size={14} strokeWidth={2} aria-hidden="true" /> Правило
		</button>
	</header>
	<p class="pd">
		Куда направить трафик. Порядок важен — first-match. Матч: rule_set / domain /
		ip_cidr / источник (устройство) / порт. Composite-аутбаунды выделены.
	</p>

	<div class="table">
		<div class="thead">
			<span class="th-grip" aria-hidden="true"></span>
			<span class="th-num">#</span>
			<span class="th-match">match</span>
			<span class="th-out">→ outbound</span>
			<span class="th-acts" aria-hidden="true"></span>
		</div>

		<div class="rows" class:is-dragging={drag.active} style={drag.cardsMotionStyle()}>
			{#if $storeRules.length === 0}
				<div class="empty">Нет route-правил.</div>
			{/if}

			{#each cards as card, i (card.id)}
				<div
					class="row-shell"
					class:drag-source-exiting={drag.isDragSource(i)}
					class:drag-source-collapsed={drag.sourceCollapsed(i)}
					style={drag.isDragSource(i) ? drag.dropIndicatorStyle() : undefined}
					bind:this={ruleRowEls[i]}
				>
					{#if drag.showsDropBefore(i)}
						<div
							class="drop-indicator"
							class:expanded={drag.dropBeforeExpanded(i)}
							class:collapsing={drag.dropBeforeCollapsing(i)}
							style={drag.dropIndicatorStyle()}
						></div>
					{/if}
					{@render ruleRow(card, i, false)}
				</div>
			{/each}

			<!-- Итоговая read-only строка: final (не перетаскивается) -->
			<div class="row-shell" bind:this={ruleRowEls[$storeRules.length]}>
				{#if drag.showsDropBefore($storeRules.length)}
					<div
						class="drop-indicator"
						class:expanded={drag.dropBeforeExpanded($storeRules.length)}
						class:collapsing={drag.dropBeforeCollapsing($storeRules.length)}
						style={drag.dropIndicatorStyle()}
					></div>
				{/if}
				<div class="rrow final-row">
					<span class="grip grip-fixed" aria-hidden="true"></span>
					<span class="num">{$storeRules.length + 1}</span>
					<span class="match-final">final</span>
					<div class="outbound">
						{#if finalEditing}
							<div class="final-edit">
								<Dropdown bind:value={draftFinal} options={routeFinalOptions} fullWidth />
							</div>
						{:else}
							<RuleOutboundAction outbound={finalOutbound} />
						{/if}
					</div>
					<div class="acts">
						{#if finalEditing}
							<button
								type="button"
								class="ib ok"
								onclick={saveFinal}
								disabled={finalBusy}
								aria-label="Сохранить final-outbound"
								title="Сохранить"
							>
								<Check size={15} strokeWidth={2} />
							</button>
							<button
								type="button"
								class="ib"
								onclick={() => (finalEditing = false)}
								disabled={finalBusy}
								aria-label="Отменить"
								title="Отменить"
							>
								<X size={15} strokeWidth={2} />
							</button>
						{:else}
							<button
								type="button"
								class="ib"
								onclick={startEditFinal}
								aria-label="Редактировать final-outbound"
								title="Изменить outbound по умолчанию (final)"
							>
								<Pencil size={15} strokeWidth={2} />
							</button>
						{/if}
					</div>
				</div>
			</div>

			{#if drag.showsDropAtEnd()}
				<div
					class="drop-indicator drop-indicator-end"
					class:expanded={drag.dropEndExpanded()}
					class:collapsing={drag.dropEndCollapsing()}
					style={drag.dropIndicatorStyle()}
				></div>
			{/if}
		</div>
	</div>
</section>

<!-- ── Плавающая ghost-карточка (тот же сниппет → пиксель-в-пиксель) ──── -->
{#if drag.ghostVisible && drag.ghostFromIndex !== null && cards[drag.ghostFromIndex]}
	<div
		class="drag-ghost"
		style={`top:${drag.ghostTop}px;left:${drag.ghostLeft}px;width:${drag.ghostWidth}px;`}
	>
		{@render ruleRow(cards[drag.ghostFromIndex], drag.ghostFromIndex, true)}
	</div>
{/if}

<!-- ── Модалы (переиспользуем вербатим) ──────────────────────────────── -->
{#if ruleAddOpen}
	<RuleEditModal
		outboundOptions={$storeOptions}
		availableRuleSets={$storeRuleSets}
		ruleSetUsage={ruleSetUsageForAdd}
		onClose={() => (ruleAddOpen = false)}
		onSave={handleRuleSave}
	/>
{/if}

{#if ruleEditIdx !== null && ruleEditTarget !== undefined}
	<RuleEditModal
		rule={ruleEditTarget}
		outboundOptions={$storeOptions}
		availableRuleSets={$storeRuleSets}
		ruleSetUsage={ruleSetUsageForEdit}
		onClose={() => (ruleEditIdx = null)}
		onSave={handleRuleSave}
	/>
{/if}

<ConfirmModal
	open={deleteIdx !== null}
	title="Удалить правило"
	message={deleteIdx !== null ? `Удалить ${ruleSummary(cards[deleteIdx], deleteIdx)}?` : ''}
	busy={deleteBusy}
	onConfirm={confirmDelete}
	onClose={() => {
		if (!deleteBusy) deleteIdx = null;
	}}
/>

<style>
	.panel {
		background: var(--color-bg-secondary, var(--bg-secondary));
		border: 1px solid var(--color-border, var(--border));
		border-radius: var(--radius, 12px);
		padding: 1rem;
		min-width: 0;
	}

	.ph {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 0.75rem;
		margin-bottom: 0.25rem;
	}
	.nm {
		color: var(--text-primary);
		font-size: 0.875rem;
		font-weight: 700;
	}
	.add {
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		font-size: 0.8125rem;
		font-weight: 600;
		border-radius: var(--radius-sm, 6px);
		padding: 0.3rem 0.6rem;
		cursor: pointer;
		color: var(--color-accent, var(--accent));
		background: transparent;
		border: 1px solid color-mix(in srgb, var(--color-accent, var(--accent)) 35%, transparent);
	}
	.add:hover {
		background: color-mix(in srgb, var(--color-accent, var(--accent)) 12%, transparent);
	}

	.pd {
		color: var(--text-muted);
		font-size: 0.8125rem;
		line-height: 1.4;
		margin: 0 0 0.875rem;
	}

	.empty {
		padding: 0.875rem;
		color: var(--text-muted);
		text-align: center;
		font-size: 0.8125rem;
	}

	.table {
		min-width: 0;
	}

	/* Заголовок столбцов — та же грид-раскладка, что .rrow. */
	.thead {
		display: grid;
		grid-template-columns: 18px 1.5rem minmax(0, 1.4fr) minmax(0, 1fr) auto;
		align-items: center;
		gap: 0.6rem;
		padding: 0.45rem 0.25rem;
		border-bottom: 1px solid var(--color-border, var(--border));
	}
	.thead span {
		color: var(--text-muted);
		font-size: 0.6875rem;
		font-weight: 500;
	}
	.th-num {
		text-align: right;
	}

	.rows {
		display: flex;
		flex-direction: column;
	}

	/* ── Drag-reorder: ВЕРБАТИМ-движок route.rules (ghost + раскрывающийся
	   скелетон-слот + схлопывание источника). Тайминги/easing/переменные
	   идентичны RulesPanel.svelte. Строки разделены border-bottom, без flex-gap
	   → обнуляем gap-математику скелетона (cardsMotionStyle()/dropIndicatorStyle()
	   инлайнят 6px из route.rules, где между карточками зазор; перебиваем). ── */
	.rows,
	.rows .row-shell,
	.rows .drop-indicator {
		--card-gap: 0px !important;
	}
	.rows.is-dragging {
		user-select: none;
	}
	.row-shell {
		position: relative;
		min-width: 0;
	}
	.row-shell.drag-source-exiting {
		overflow: hidden;
		height: var(--drop-height);
		opacity: 1;
		transition:
			height var(--drop-slot-motion-ms, 360ms) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
			opacity var(--drop-slot-motion-ms, 360ms) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
			margin var(--drop-slot-motion-ms, 360ms) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95));
	}
	.row-shell.drag-source-exiting.drag-source-collapsed {
		height: 0;
		max-height: 0;
		opacity: 0;
		margin-bottom: calc(-1 * var(--card-gap, 6px));
	}
	.drop-indicator {
		box-sizing: border-box;
		overflow: hidden;
		border: 1px solid transparent;
		border-radius: 999px;
		background: var(--color-accent, var(--accent));
		box-shadow: 0 0 10px color-mix(in srgb, var(--color-accent, var(--accent)) 45%, transparent);
		opacity: 1;
		pointer-events: none;
		transition:
			height var(--drop-slot-motion-ms, 360ms) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
			margin var(--drop-slot-motion-ms, 360ms) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
			border-radius calc(var(--drop-slot-motion-ms, 360ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
			background calc(var(--drop-slot-motion-ms, 360ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
			box-shadow calc(var(--drop-slot-motion-ms, 360ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
			border-color calc(var(--drop-slot-motion-ms, 360ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
			opacity calc(var(--drop-slot-motion-ms, 360ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95));
	}
	.drop-indicator:not(.expanded):not(.collapsing) {
		position: absolute;
		top: -1px;
		left: 0;
		right: 0;
		height: 2px;
		margin: 0;
		z-index: 2;
	}
	.drop-indicator.expanded:not(.collapsing) {
		position: static;
		top: auto;
		height: var(--drop-height);
		margin: 0 0 var(--card-gap, 6px);
		border-radius: var(--radius-sm, 6px);
		background: color-mix(in srgb, var(--color-accent, var(--accent)) 6%, transparent);
		border-color: color-mix(in srgb, var(--color-accent, var(--accent)) 55%, transparent);
		border-style: dashed;
		box-shadow: none;
	}
	.drop-indicator.collapsing {
		margin: 0 !important;
		opacity: 0;
		border-color: transparent;
		background: transparent;
		box-shadow: none;
	}
	.drop-indicator.collapsing.expanded {
		position: static;
		height: 0 !important;
		transition:
			height var(--drop-slot-motion-ms, 360ms) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
			margin var(--drop-slot-motion-ms, 360ms) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
			border-radius calc(var(--drop-slot-motion-ms, 360ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
			background calc(var(--drop-slot-motion-ms, 360ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
			box-shadow calc(var(--drop-slot-motion-ms, 360ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
			border-color calc(var(--drop-slot-motion-ms, 360ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
			opacity var(--drop-slot-motion-ms, 360ms) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95));
	}
	.drop-indicator.collapsing:not(.expanded) {
		position: absolute;
		top: -1px;
		left: 0;
		right: 0;
		height: 2px !important;
		z-index: 2;
		transition:
			opacity var(--drop-line-collapse-ms, 240ms) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
			box-shadow calc(var(--drop-line-collapse-ms, 240ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
			background calc(var(--drop-line-collapse-ms, 240ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
			border-color calc(var(--drop-line-collapse-ms, 240ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95));
	}
	.drop-indicator-end.collapsing:not(.expanded) {
		position: relative;
		top: auto;
		left: auto;
		right: auto;
		height: 2px !important;
		margin: -1px 0 0 !important;
	}
	.drop-indicator-end:not(.expanded):not(.collapsing) {
		position: relative;
		top: auto;
		height: 2px;
		margin: -1px 0 0;
	}
	.drag-ghost {
		position: fixed;
		z-index: 10000;
		pointer-events: none;
		transform: none;
		opacity: 0.96;
		filter: drop-shadow(0 14px 24px rgba(0, 0, 0, 0.35));
		background: var(--color-bg-secondary, var(--bg-secondary));
		border: 1px solid color-mix(in srgb, var(--color-accent, var(--accent)) 55%, var(--border));
		border-radius: var(--radius-sm, 6px);
	}
	.drag-ghost .rrow {
		border-bottom: none;
	}

	/* ── Строки правил ── */
	.rrow {
		display: grid;
		grid-template-columns: 18px 1.5rem minmax(0, 1.4fr) minmax(0, 1fr) auto;
		align-items: center;
		gap: 0.6rem;
		padding: 0.55rem 0.25rem;
		border-bottom: 1px solid var(--color-border-subtle, var(--border));
	}
	.row-shell:last-of-type .rrow {
		border-bottom: none;
	}
	.rrow.dragging {
		opacity: 0.7;
		border-radius: var(--radius-sm, 6px);
		outline: 1px solid color-mix(in srgb, var(--color-accent, var(--accent)) 55%, var(--border));
		outline-offset: -1px;
	}

	.grip {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		background: transparent;
		border: none;
		padding: 0;
		color: var(--text-muted);
		opacity: 0.55;
		cursor: grab;
		touch-action: none;
		border-radius: 4px;
	}
	.grip-fixed {
		cursor: default;
	}
	button.grip:hover {
		color: var(--text-primary);
		opacity: 1;
	}
	button.grip:active {
		cursor: grabbing;
	}
	.grip.is-busy {
		cursor: wait;
		opacity: 0.3;
		pointer-events: none;
	}

	:global(body.reorder-dragging) {
		user-select: none;
		cursor: grabbing;
	}

	.num {
		color: var(--text-muted);
		font-size: 0.8125rem;
		font-family: var(--font-mono);
		text-align: right;
	}

	.match {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.3rem;
		min-width: 0;
	}
	.m-none {
		color: var(--text-muted);
	}
	.more {
		display: inline-flex;
		align-items: center;
		padding: 2px 7px;
		border-radius: 4px;
		background: transparent;
		border: 1px dashed var(--color-border, var(--border));
		color: var(--text-muted);
		font-size: 10px;
		line-height: 1.4;
	}

	.outbound {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.3rem;
		min-width: 0;
		max-width: 100%;
		overflow: hidden;
	}
	.outbound :global(.tile),
	.outbound :global(.tone-chip) {
		min-width: 0;
		max-width: 100%;
		overflow: hidden;
	}

	/* Информационный маркер: правило дополнительно ставит NDMS-маршрут на tun.
	   Display-only, нейтральный — тот же приглушённый dashed-стиль, что у «+N ещё». */
	.tun-route-mark {
		display: inline-flex;
		align-items: center;
		flex-shrink: 0;
		padding: 2px 7px;
		border-radius: 4px;
		background: transparent;
		border: 1px dashed var(--color-border, var(--border));
		color: var(--text-muted);
		font-size: 10px;
		line-height: 1.4;
		white-space: nowrap;
		cursor: default;
	}

	/* Final-строка (read-only). */
	.final-row {
		opacity: 0.85;
	}
	.match-final {
		color: var(--text-muted);
		font-size: 0.8125rem;
		font-family: var(--font-mono);
	}

	/* ── Действия (Lucide-иконки) ── */
	.acts {
		display: inline-flex;
		align-items: center;
		justify-content: flex-end;
		gap: 0.25rem;
		flex-shrink: 0;
	}
	.ib {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		color: var(--text-muted);
		background: transparent;
		border: 1px solid var(--color-border, var(--border));
		border-radius: var(--radius-sm, 6px);
		padding: 0.25rem;
		cursor: pointer;
	}
	.ib:hover {
		color: var(--text-primary);
		border-color: var(--color-border-hover, var(--border));
	}
	.ib.danger:hover {
		color: var(--color-error, #e06a5a);
		border-color: var(--color-error, #e06a5a);
	}
	.ib.ok {
		color: var(--color-accent, var(--accent));
		border-color: color-mix(in srgb, var(--color-accent, var(--accent)) 45%, transparent);
	}
	.ib:disabled {
		opacity: 0.5;
		cursor: default;
	}
	.final-edit {
		min-width: 12rem;
		max-width: 100%;
	}
</style>
