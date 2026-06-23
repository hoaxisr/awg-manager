<!--
  DNS-чип FakeIP по мокапу page-dns-v3: три блока — DNS-серверы (слева) и
  DNS-правила (справа) в верхнем грид-ряду + DNS-перезаписи на всю ширину снизу.
  Иконки только Lucide (GripVertical / Pencil / Trash2 / Lock / Plus).

  Чистая ПЕРЕКОМПОНОВКА существующих кусков sb-router в сетку мокапа:
    - Эдит-модалы переиспользуются ВЕРБАТИМ из routing/singboxRouter:
      DNSServerEditModal, DNSRuleEditModal, DNSRewritesList (+ его внутренний
      DNSRewriteEditModal), DNSGlobalsEditModal.
    - CRUD — через api.singboxRouter*DNS* + singboxRouter.loadAll() после каждой
      мутации (зеркалит ExpertPanel.svelte и OutboundsTab).
    - Бейджи/лейблы — общие хелперы sb-router: dnsRuleTarget, dnsMatcherParts,
      dnsServerDetourDisplay, dnsServerDeleteBlockReasons.

  «Ядро» (мокап): fakeip-сервер неудаляем — бейдж «ядро» + lock вместо корзины.
  Тип fakeip в модели DNS-сервера отсутствует (union udp|tls|https|quic|h3|local),
  поэтому ядро определяется по type==='fakeip' (на будущее) ЛИБО по причине
  блокировки удаления (сервер на который ссылаются rule/final/resolver) — там
  корзина дизейблится с подсказкой, как в DnsServersCompact.

  Движок-гейт: DNS — это конфиг, доступен при любом состоянии движка (никаких
  live-рантайм блоков), поэтому рендерится всегда.

  Drag-reorder: серверы и правила перетаскиваются ВЕРБАТИМ-движком route.rules
  (общий reorderDrag): floating ghost-card + раскрывающийся/схлопывающийся
  скелетон-слот в точке вставки + auto-scroll у краёв + порог + pointer-capture +
  оптимистика-Move + откат. Каждая строка рендерится через сниппет, который тот же
  сниппет рисует в плавающем ghost'е — карточка под курсором пиксель-в-пиксель.
  Read-only «final»-строка правил не движется (isFixed-гард). Перезаписи drag НЕ
  поддерживают (общий DNSRewritesList без грипа). MoveDNSServer — в бэкенде.
-->
<script lang="ts">
	import { onDestroy } from 'svelte';
	import { get } from 'svelte/store';
	import { fakeipConfig } from '$lib/stores/fakeipConfig';
	import { createReorderDrag } from '$lib/components/sb-router/reorderDrag.svelte';
	import { subscriptionsStore } from '$lib/stores/subscriptions';
	import { singboxProxies } from '$lib/stores/singboxProxies';
	import { singboxTunnels } from '$lib/stores/singbox';
	import { notifications } from '$lib/stores/notifications';
	import { api } from '$lib/api/client';
	import {
		computeRuleSetUsage,
		DNSServerEditModal,
		DNSRuleEditModal,
		DNSGlobalsEditModal,
	} from '$lib/components/routing/singboxRouter';
	import { ConfirmModal, Badge } from '$lib/components/ui';
	import { GripVertical, Pencil, Trash2, Lock, Plus } from 'lucide-svelte';
	import { dnsRuleTarget } from '$lib/components/sb-router/dnsRuleLabel';
	import { dnsMatcherParts, dnsMatcherSummary } from '$lib/components/sb-router/dnsMatcherParts';
	import { dnsServerDetourDisplay } from '$lib/components/sb-router/dnsServerDetourDisplay';
	import { dnsServerDeleteBlockReasons } from '$lib/components/sb-router/dnsServerUsage';
	import OutboundTile from '$lib/components/sb-router/OutboundTile.svelte';
	import type { SingboxRouterDNSServer, SingboxRouterDNSRule, SingboxRouterDNSStrategy } from '$lib/types';

	// ── Store sub-stores ───────────────────────────────────────────────────
	const storeDnsServers = fakeipConfig.dnsServers;
	const storeDnsRules = fakeipConfig.dnsRules;
	const storeDnsGlobals = fakeipConfig.dnsGlobals;
	const storeRuleSets = fakeipConfig.ruleSets;
	const storeOutbounds = fakeipConfig.outbounds;
	const storeOptions = fakeipConfig.options;

	// Контекст блокировки удаления серверов (один проход на список).
	const dnsServerUsageContext = $derived({
		rules: $storeDnsRules,
		servers: $storeDnsServers,
		dnsFinal: $storeDnsGlobals.final || '',
	});
	const serverDeleteReasons = $derived(
		dnsServerDeleteBlockReasons($storeDnsServers, dnsServerUsageContext),
	);

	// Сервер — «ядро» (неудаляемое): fakeip-тип ИЛИ есть причина блокировки.
	function isCoreServer(s: SingboxRouterDNSServer): boolean {
		return s.type === ('fakeip' as SingboxRouterDNSServer['type']) || serverDeleteReasons.get(s.tag) != null;
	}
	function serverLockTitle(s: SingboxRouterDNSServer): string {
		if (s.type === ('fakeip' as SingboxRouterDNSServer['type'])) {
			return 'fakeip — ядро движка, удаление недоступно';
		}
		return serverDeleteReasons.get(s.tag) ?? 'Удаление недоступно';
	}

	// Тип-бейдж сервера: fakeip / local — особые тона, остальное — нейтральный.
	function serverTypeVariant(type: string): 'accent' | 'success' | 'default' {
		if (type === 'fakeip') return 'accent';
		if (type === 'local') return 'success';
		return 'default';
	}
	function serverAddr(s: SingboxRouterDNSServer): string {
		if (s.type === 'local') return 'системный resolver';
		// fakeip-сервер синтезирует адреса в туннель — у него нет upstream-адреса,
		// поэтому s.server пустой; показываем «синтез», а не «undefined».
		if (s.type === 'fakeip') return 'синтез';
		const port = s.server_port ? `:${s.server_port}` : '';
		const path = s.path ?? '';
		return `${s.server}${port}${path}`;
	}

	// ── Drag-reorder (ВЕРБАТИМ-движок route.rules: ghost + skeleton + autoscroll) ──
	let serverRowEls = $state<Array<HTMLElement | null>>([]);
	let ruleRowEls = $state<Array<HTMLElement | null>>([]);
	let serverPanelEl = $state<HTMLElement | null>(null);
	let rulePanelEl = $state<HTMLElement | null>(null);

	function reorder<T>(list: T[], from: number, to: number): T[] {
		const next = list.slice();
		const [moved] = next.splice(from, 1);
		next.splice(to, 0, moved);
		return next;
	}

	const serverDrag = createReorderDrag({
		getRowElement: (i) => serverRowEls[i] ?? null,
		count: () => $storeDnsServers.length,
		getPanelEl: () => serverPanelEl,
		onCommit: async (from, to) => {
			const snapshot = get(fakeipConfig.dnsServers);
			fakeipConfig.applyDNSServers(reorder(snapshot, from, to));
			try {
				await api.singboxFakeIPMoveDNSServer(from, to);
				await fakeipConfig.loadAll();
			} catch (e) {
				fakeipConfig.applyDNSServers(snapshot);
				notifications.error(`Ошибка перемещения: ${e instanceof Error ? e.message : String(e)}`);
			}
		},
	});

	const ruleDrag = createReorderDrag({
		getRowElement: (i) => ruleRowEls[i] ?? null,
		// +1 виртуальная read-only «final»-строка в самом конце.
		count: () => $storeDnsRules.length + 1,
		getPanelEl: () => rulePanelEl,
		// «final»-строка (последний индекс) фиксирована: ни схватить, ни уронить под неё.
		isFixed: (i) => i >= $storeDnsRules.length,
		onCommit: async (from, to) => {
			const snapshot = get(fakeipConfig.dnsRules);
			fakeipConfig.applyDNSRules(reorder(snapshot, from, to));
			try {
				await api.singboxFakeIPMoveDNSRule(from, to);
				await fakeipConfig.loadAll();
			} catch (e) {
				fakeipConfig.applyDNSRules(snapshot);
				notifications.error(`Ошибка перемещения: ${e instanceof Error ? e.message : String(e)}`);
			}
		},
	});

	onDestroy(() => {
		serverDrag.destroy();
		ruleDrag.destroy();
	});

	// ── Modal state ───────────────────────────────────────────────────────
	let dnsServerEditTag = $state<string | null>(null);
	let dnsServerAddOpen = $state(false);
	let dnsRuleEditIdx = $state<number | null>(null);
	let dnsRuleAddOpen = $state(false);
	let dnsGlobalsModalOpen = $state(false);

	const dnsServerEditTarget = $derived<SingboxRouterDNSServer | undefined>(
		dnsServerEditTag !== null ? $storeDnsServers.find((s) => s.tag === dnsServerEditTag) : undefined,
	);
	const dnsRuleEditTarget = $derived<SingboxRouterDNSRule | undefined>(
		dnsRuleEditIdx !== null ? $storeDnsRules[dnsRuleEditIdx] : undefined,
	);

	// ruleSetUsage для DNSRuleEditModal: исключаем редактируемый индекс.
	const ruleSetUsageForDnsAdd = $derived(computeRuleSetUsage($storeDnsRules));
	const ruleSetUsageForDnsEdit = $derived(
		dnsRuleEditIdx === null
			? new Map<string, number>()
			: computeRuleSetUsage($storeDnsRules, dnsRuleEditIdx),
	);

	// Унифицированное подтверждение удаления (server / rule).
	let pendingConfirm = $state<{ title: string; message: string; run: () => Promise<void> } | null>(null);
	let confirmBusy = $state(false);

	async function runConfirm(): Promise<void> {
		if (!pendingConfirm) return;
		confirmBusy = true;
		try {
			await pendingConfirm.run();
			pendingConfirm = null;
		} finally {
			confirmBusy = false;
		}
	}

	// ── Handlers ─────────────────────────────────────────────────────────────
	async function handleDnsServerAddSave(server: SingboxRouterDNSServer): Promise<void> {
		await api.singboxFakeIPAddDNSServer(server);
		dnsServerAddOpen = false;
		await fakeipConfig.loadAll();
	}
	async function handleDnsServerEditSave(server: SingboxRouterDNSServer): Promise<void> {
		if (dnsServerEditTag !== null) {
			await api.singboxFakeIPUpdateDNSServer(dnsServerEditTag, server);
		}
		dnsServerEditTag = null;
		await fakeipConfig.loadAll();
	}
	function handleDeleteDnsServer(tag: string): void {
		pendingConfirm = {
			title: 'Удалить DNS-сервер',
			message: `Удалить DNS-сервер «${tag}»?`,
			run: async () => {
				try {
					await api.singboxFakeIPDeleteDNSServer(tag);
					await fakeipConfig.loadAll();
					notifications.success('DNS-сервер удалён');
				} catch (e) {
					notifications.error(`Ошибка: ${e instanceof Error ? e.message : String(e)}`);
				}
			},
		};
	}

	async function handleDnsRuleAddSave(rule: SingboxRouterDNSRule): Promise<void> {
		await api.singboxFakeIPAddDNSRule(rule);
		dnsRuleAddOpen = false;
		await fakeipConfig.loadAll();
	}
	async function handleDnsRuleEditSave(rule: SingboxRouterDNSRule): Promise<void> {
		if (dnsRuleEditIdx !== null) {
			await api.singboxFakeIPUpdateDNSRule(dnsRuleEditIdx, rule);
		}
		dnsRuleEditIdx = null;
		await fakeipConfig.loadAll();
	}
	function handleDeleteDNSRule(idx: number): void {
		pendingConfirm = {
			title: 'Удалить DNS-правило',
			message: `Удалить DNS-правило #${idx + 1}?`,
			run: async () => {
				try {
					await api.singboxFakeIPDeleteDNSRule(idx);
					await fakeipConfig.loadAll();
					notifications.success('DNS-правило удалено');
				} catch (e) {
					notifications.error(`Ошибка: ${e instanceof Error ? e.message : String(e)}`);
				}
			},
		};
	}

	async function handleDnsGlobalsSave(globals: {
		final: string;
		strategy: SingboxRouterDNSStrategy;
	}): Promise<void> {
		await api.singboxFakeIPSetDNSGlobals(globals);
		dnsGlobalsModalOpen = false;
		await fakeipConfig.loadAll();
	}
</script>

<!-- ── Сниппет строки DNS-сервера (рисуется и в списке, и в ghost'е) ─── -->
{#snippet serverRow(s: SingboxRouterDNSServer, i: number, ghost: boolean)}
	{@const core = isCoreServer(s)}
	<div class="srow" class:dragging={!ghost && serverDrag.draggingIndex === i}>
		<button
			type="button"
			class="grip"
			class:is-busy={serverDrag.busy}
			aria-label={`Перетащить DNS-сервер ${s.tag}`}
			title="Перетащить для изменения порядка"
			onpointerdown={serverDrag.busy ? undefined : (e) => serverDrag.handlePointerDown(i, e)}
		>
			<GripVertical size={16} strokeWidth={2} />
		</button>
		<div class="tag-cell">
			<span class="stag">{s.tag}</span>
			{#if s.type === ('fakeip' as typeof s.type)}<span class="core">ядро</span>{/if}
		</div>
		<span class="type-cell">
			<Badge variant={serverTypeVariant(s.type)} size="sm" mono>{s.type}</Badge>
		</span>
		<span class="addr" title={serverAddr(s)}>{serverAddr(s)}</span>
		<span class="detour">
			<OutboundTile
				outbound={dnsServerDetourDisplay(
					s,
					$storeOutbounds,
					$storeOptions,
					$subscriptionsStore.data,
					$singboxProxies.data ?? [],
					$singboxTunnels.data ?? [],
				)}
				size="compact"
			/>
		</span>
		<div class="acts">
			<button
				type="button"
				class="ib"
				onclick={() => (dnsServerEditTag = s.tag)}
				aria-label={`Редактировать DNS-сервер ${s.tag}`}
				title={`Редактировать DNS-сервер «${s.tag}»`}
			>
				<Pencil size={15} strokeWidth={2} />
			</button>
			{#if core}
				<span class="ib lock" title={serverLockTitle(s)} aria-label={serverLockTitle(s)}>
					<Lock size={15} strokeWidth={2} />
				</span>
			{:else}
				<button
					type="button"
					class="ib danger"
					onclick={() => handleDeleteDnsServer(s.tag)}
					aria-label={`Удалить DNS-сервер ${s.tag}`}
					title={`Удалить DNS-сервер «${s.tag}»`}
				>
					<Trash2 size={15} strokeWidth={2} />
				</button>
			{/if}
		</div>
	</div>
{/snippet}

<!-- ── Сниппет строки DNS-правила ────────────────────────────────────── -->
{#snippet ruleRow(r: SingboxRouterDNSRule, i: number, ghost: boolean)}
	{@const tgt = dnsRuleTarget(r)}
	{@const matchers = dnsMatcherParts(r)}
	<div class="rrow" class:dragging={!ghost && ruleDrag.draggingIndex === i}>
		<button
			type="button"
			class="grip"
			class:is-busy={ruleDrag.busy}
			aria-label={`Перетащить DNS-правило #${i + 1}`}
			title="Перетащить для изменения порядка"
			onpointerdown={ruleDrag.busy ? undefined : (e) => ruleDrag.handlePointerDown(i, e)}
		>
			<GripVertical size={16} strokeWidth={2} />
		</button>
		<span class="num">{i + 1}</span>
		<button
			type="button"
			class="match-btn"
			onclick={() => (dnsRuleEditIdx = i)}
			title={`${dnsMatcherSummary(r)} → ${tgt.label}`}
		>
			{#if matchers.length === 0}
				<span class="m-none">—</span>
			{:else}
				{#each matchers as part, pi (part.key + pi)}
					<span class="m-part">
						{#if pi > 0}<span class="m-sep">·</span>{/if}
						<span class="mtag">{part.key}</span>
						<span class="m-val">{part.value}</span>
					</span>
				{/each}
			{/if}
			<span class="r-arrow" aria-hidden="true">→</span>
			{#if tgt.kind === 'block'}
				<Badge variant="error" size="sm" mono>{tgt.label}</Badge>
			{:else if tgt.kind === 'none'}
				<span class="r-target none">{tgt.label}</span>
			{:else}
				<Badge variant="accent" size="sm" mono>{tgt.label}</Badge>
			{/if}
		</button>
		<div class="acts">
			<button
				type="button"
				class="ib"
				onclick={() => (dnsRuleEditIdx = i)}
				aria-label={`Редактировать DNS-правило #${i + 1}`}
				title={`Редактировать DNS-правило #${i + 1}`}
			>
				<Pencil size={15} strokeWidth={2} />
			</button>
			<button
				type="button"
				class="ib danger"
				onclick={() => handleDeleteDNSRule(i)}
				aria-label={`Удалить DNS-правило #${i + 1}`}
				title={`Удалить DNS-правило #${i + 1}`}
			>
				<Trash2 size={15} strokeWidth={2} />
			</button>
		</div>
	</div>
{/snippet}

<div class="dns-grid">
	<!-- ── Блок 1: DNS-серверы ─────────────────────────────────────────── -->
	<section class="panel" bind:this={serverPanelEl}>
		<header class="ph">
			<span class="nm">DNS-серверы · {$storeDnsServers.length}</span>
			<button type="button" class="add" onclick={() => (dnsServerAddOpen = true)}>
				<Plus size={14} strokeWidth={2} aria-hidden="true" /> Сервер
			</button>
		</header>
		<p class="pd">
			Резолверы. fakeip синтезирует адреса (в туннель), real резолвит через outbound,
			local — роутер для direct.
		</p>

		{#if $storeDnsServers.length === 0}
			<div class="empty">Нет DNS-серверов.</div>
		{:else}
			<div class="rows" class:is-dragging={serverDrag.active} style={serverDrag.cardsMotionStyle()}>
				{#each $storeDnsServers as s, i (s.tag)}
					<div
						class="row-shell"
						class:drag-source-exiting={serverDrag.isDragSource(i)}
						class:drag-source-collapsed={serverDrag.sourceCollapsed(i)}
						style={serverDrag.isDragSource(i) ? serverDrag.dropIndicatorStyle() : undefined}
						bind:this={serverRowEls[i]}
					>
						{#if serverDrag.showsDropBefore(i)}
							<div
								class="drop-indicator"
								class:expanded={serverDrag.dropBeforeExpanded(i)}
								class:collapsing={serverDrag.dropBeforeCollapsing(i)}
								style={serverDrag.dropIndicatorStyle()}
							></div>
						{/if}
						{@render serverRow(s, i, false)}
					</div>
				{/each}
				{#if serverDrag.showsDropAtEnd()}
					<div
						class="drop-indicator drop-indicator-end"
						class:expanded={serverDrag.dropEndExpanded()}
						class:collapsing={serverDrag.dropEndCollapsing()}
						style={serverDrag.dropIndicatorStyle()}
					></div>
				{/if}
			</div>
		{/if}

		<!--
			Info-бокс DNS по умолчанию (мокап: «default_domain_resolver = … · final = …»).
			В модели globals только final + strategy (нет отдельного
			default_domain_resolver), поэтому показываем реальные поля, которые правит
			DNSGlobalsEditModal. Клик открывает модал.
		-->
		<button type="button" class="resolver" onclick={() => (dnsGlobalsModalOpen = true)} title="Настроить DNS по умолчанию">
			final = <b>{$storeDnsGlobals.final || '—'}</b> · strategy =
			<b>{$storeDnsGlobals.strategy || 'default'}</b>
		</button>
	</section>

	<!-- ── Блок 2: DNS-правила ─────────────────────────────────────────── -->
	<section class="panel" bind:this={rulePanelEl}>
		<header class="ph">
			<span class="nm">DNS-правила · {$storeDnsRules.length}</span>
			<button type="button" class="add" onclick={() => (dnsRuleAddOpen = true)}>
				<Plus size={14} strokeWidth={2} aria-hidden="true" /> Правило
			</button>
		</header>
		<p class="pd">
			Какой сервер для какого запроса. first-match. Матч: домен / rule_set / query_type /
			источник.
		</p>

		<div class="rows" class:is-dragging={ruleDrag.active} style={ruleDrag.cardsMotionStyle()}>
			{#each $storeDnsRules as r, i (i)}
				<div
					class="row-shell"
					class:drag-source-exiting={ruleDrag.isDragSource(i)}
					class:drag-source-collapsed={ruleDrag.sourceCollapsed(i)}
					style={ruleDrag.isDragSource(i) ? ruleDrag.dropIndicatorStyle() : undefined}
					bind:this={ruleRowEls[i]}
				>
					{#if ruleDrag.showsDropBefore(i)}
						<div
							class="drop-indicator"
							class:expanded={ruleDrag.dropBeforeExpanded(i)}
							class:collapsing={ruleDrag.dropBeforeCollapsing(i)}
							style={ruleDrag.dropIndicatorStyle()}
						></div>
					{/if}
					{@render ruleRow(r, i, false)}
				</div>
			{/each}

			<!-- Итоговая read-only строка: final → globals.final (не перетаскивается) -->
			<div class="row-shell" bind:this={ruleRowEls[$storeDnsRules.length]}>
				{#if ruleDrag.showsDropBefore($storeDnsRules.length)}
					<div
						class="drop-indicator"
						class:expanded={ruleDrag.dropBeforeExpanded($storeDnsRules.length)}
						class:collapsing={ruleDrag.dropBeforeCollapsing($storeDnsRules.length)}
						style={ruleDrag.dropIndicatorStyle()}
					></div>
				{/if}
				<div class="rrow final-row">
					<span class="grip" aria-hidden="true"></span>
					<span class="num">{$storeDnsRules.length + 1}</span>
					<span class="match-final">
						<span class="match-final-label">final</span>
						<span class="r-arrow" aria-hidden="true">→</span>
						<span class="final-target">{$storeDnsGlobals.final || '—'}</span>
					</span>
					<div class="acts"></div>
				</div>
			</div>
		</div>
	</section>

</div>

<!-- ── Плавающие ghost-карточки (тот же сниппет строки → пиксель-в-пиксель) ── -->
{#if serverDrag.ghostVisible && serverDrag.ghostFromIndex !== null && $storeDnsServers[serverDrag.ghostFromIndex]}
	<div
		class="drag-ghost"
		style={`top:${serverDrag.ghostTop}px;left:${serverDrag.ghostLeft}px;width:${serverDrag.ghostWidth}px;`}
	>
		{@render serverRow($storeDnsServers[serverDrag.ghostFromIndex], serverDrag.ghostFromIndex, true)}
	</div>
{/if}

{#if ruleDrag.ghostVisible && ruleDrag.ghostFromIndex !== null && $storeDnsRules[ruleDrag.ghostFromIndex]}
	<div
		class="drag-ghost"
		style={`top:${ruleDrag.ghostTop}px;left:${ruleDrag.ghostLeft}px;width:${ruleDrag.ghostWidth}px;`}
	>
		{@render ruleRow($storeDnsRules[ruleDrag.ghostFromIndex], ruleDrag.ghostFromIndex, true)}
	</div>
{/if}

<!-- ── Модалы (переиспользуем вербатим) ──────────────────────────────── -->
{#if dnsServerAddOpen}
	<DNSServerEditModal
		servers={$storeDnsServers}
		outboundOptions={$storeOptions}
		onClose={() => (dnsServerAddOpen = false)}
		onSave={handleDnsServerAddSave}
	/>
{/if}

{#if dnsServerEditTag !== null && dnsServerEditTarget !== undefined}
	<DNSServerEditModal
		server={dnsServerEditTarget}
		servers={$storeDnsServers}
		outboundOptions={$storeOptions}
		onClose={() => (dnsServerEditTag = null)}
		onSave={handleDnsServerEditSave}
	/>
{/if}

{#if dnsRuleAddOpen}
	<DNSRuleEditModal
		servers={$storeDnsServers}
		availableRuleSets={$storeRuleSets}
		ruleSetUsage={ruleSetUsageForDnsAdd}
		onClose={() => (dnsRuleAddOpen = false)}
		onSave={handleDnsRuleAddSave}
	/>
{/if}

{#if dnsRuleEditIdx !== null && dnsRuleEditTarget !== undefined}
	<DNSRuleEditModal
		rule={dnsRuleEditTarget}
		servers={$storeDnsServers}
		availableRuleSets={$storeRuleSets}
		ruleSetUsage={ruleSetUsageForDnsEdit}
		onClose={() => (dnsRuleEditIdx = null)}
		onSave={handleDnsRuleEditSave}
	/>
{/if}

{#if dnsGlobalsModalOpen}
	<DNSGlobalsEditModal
		servers={$storeDnsServers}
		final={$storeDnsGlobals.final}
		strategy={$storeDnsGlobals.strategy}
		onClose={() => (dnsGlobalsModalOpen = false)}
		onSave={handleDnsGlobalsSave}
	/>
{/if}

<ConfirmModal
	open={pendingConfirm !== null}
	title={pendingConfirm?.title ?? ''}
	message={pendingConfirm?.message ?? ''}
	busy={confirmBusy}
	onConfirm={runConfirm}
	onClose={() => {
		if (!confirmBusy) pendingConfirm = null;
	}}
/>

<style>
	/* Сетка мокапа: два блока в ряд (серверы уже, правила шире). */
	.dns-grid {
		display: grid;
		grid-template-columns: 1fr 1.25fr;
		gap: 1rem;
	}
	@media (max-width: 900px) {
		.dns-grid {
			grid-template-columns: 1fr;
		}
	}

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
		color: var(--color-accent, var(--accent));
		font-size: 0.8125rem;
		font-weight: 600;
		background: transparent;
		border: 1px solid color-mix(in srgb, var(--color-accent, var(--accent)) 35%, transparent);
		border-radius: var(--radius-sm, 6px);
		padding: 0.3rem 0.6rem;
		cursor: pointer;
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

	.rows {
		display: flex;
		flex-direction: column;
	}

	/* ── Drag-reorder: ВЕРБАТИМ-движок route.rules (ghost + раскрывающийся
	   скелетон-слот + схлопывание источника). Тайминги/easing/переменные
	   идентичны RulesPanel.svelte — карточка под курсором и анимация слота
	   ощущаются один-в-один. ── */
	/* Строки разделены border-bottom, без flex-gap → обнуляем gap-математику
	   скелетона (cardsMotionStyle()/dropIndicatorStyle() инлайнят 6px из route.rules,
	   где между карточками есть зазор; перебиваем !important). */
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
	.drag-ghost .srow,
	.drag-ghost .rrow {
		border-bottom: none;
	}

	/* ── DNS-серверы строки ── */
	.srow {
		display: grid;
		grid-template-columns: 18px minmax(0, 1.1fr) auto minmax(0, 1.3fr) auto auto;
		align-items: center;
		gap: 0.6rem;
		padding: 0.55rem 0.25rem;
		border-bottom: 1px solid var(--color-border, var(--border));
	}
	.row-shell:last-of-type .srow {
		border-bottom: none;
	}
	.srow.dragging,
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

	.tag-cell {
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
		min-width: 0;
	}
	.stag {
		color: var(--text-primary);
		font-weight: 600;
		font-family: var(--font-mono);
		font-size: 0.8125rem;
		overflow-wrap: anywhere;
	}
	.core {
		flex: 0 0 auto;
		font-size: 0.625rem;
		font-weight: 700;
		color: var(--color-bg-secondary, #0a0a0a);
		background: var(--color-accent, var(--accent));
		border-radius: 4px;
		padding: 0.05rem 0.35rem;
	}

	.type-cell {
		flex: 0 0 auto;
	}
	.addr {
		color: var(--text-secondary);
		font-size: 0.8125rem;
		font-family: var(--font-mono);
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.detour {
		min-width: 0;
		max-width: 100%;
		overflow: hidden;
	}
	.detour :global(.tone-chip) {
		max-width: 100%;
		min-width: 0;
		overflow: hidden;
	}

	/* ── DNS-правила строки ── */
	.rrow {
		display: grid;
		grid-template-columns: 18px 1.25rem minmax(0, 1fr) auto;
		align-items: center;
		gap: 0.6rem;
		padding: 0.55rem 0.25rem;
		border-bottom: 1px solid var(--color-border, var(--border));
	}
	.row-shell:last-of-type .rrow {
		border-bottom: none;
	}
	.num {
		color: var(--text-muted);
		font-size: 0.8125rem;
		font-family: var(--font-mono);
		text-align: right;
	}

	.match-btn {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.3rem;
		min-width: 0;
		background: transparent;
		border: 0;
		padding: 0;
		color: inherit;
		text-align: left;
		cursor: pointer;
		font-size: 0.8125rem;
	}
	.m-part {
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		min-width: 0;
	}
	.m-sep {
		color: var(--text-muted);
	}
	.mtag {
		background: var(--color-bg-tertiary, var(--bg-tertiary));
		border: 1px solid var(--color-border, var(--border));
		border-radius: 5px;
		padding: 0.05rem 0.35rem;
		font-size: 0.6875rem;
		color: var(--text-secondary);
		font-family: var(--font-mono);
	}
	.m-val {
		color: var(--text-secondary);
		overflow-wrap: anywhere;
	}
	.r-arrow {
		color: var(--text-muted);
		opacity: 0.85;
	}
	.r-target.none {
		color: var(--text-muted);
	}
	.m-none {
		color: var(--text-muted);
	}

	.final-row {
		opacity: 0.85;
	}
	.match-final {
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
		font-size: 0.8125rem;
		font-family: var(--font-mono);
	}
	.match-final-label {
		color: var(--text-muted);
	}
	.final-target {
		color: var(--text-secondary);
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
	.ib.lock {
		color: var(--text-muted);
		opacity: 0.6;
		border-style: dashed;
		cursor: not-allowed;
	}

	/* Итоговый info-бокс default_domain_resolver / final (кликабельный → globals). */
	.resolver {
		display: block;
		width: 100%;
		text-align: left;
		margin-top: 0.875rem;
		padding: 0.55rem 0.75rem;
		font-size: 0.8125rem;
		color: var(--text-secondary);
		background: color-mix(in srgb, var(--color-accent, var(--accent)) 8%, transparent);
		border: 1px solid color-mix(in srgb, var(--color-accent, var(--accent)) 30%, transparent);
		border-radius: var(--radius-sm, 8px);
		cursor: pointer;
	}
	.resolver:hover {
		background: color-mix(in srgb, var(--color-accent, var(--accent)) 14%, transparent);
	}
	.resolver b {
		color: var(--color-accent, var(--accent));
		font-weight: 700;
	}
</style>
