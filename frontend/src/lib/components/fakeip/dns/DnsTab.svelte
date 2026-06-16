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

  Drag-reorder: для DNS-серверов backend-метода переупорядочивания НЕТ
  (есть только singboxRouterMoveDNSRule / *MoveDNSRewrite). Чтобы не выдумывать
  частичный UX, грип-ручки рендерятся для визуального паритета с мокапом, но
  функциональный drag отложен для ВСЕХ трёх блоков.
  // ponytail: drag-reorder deferred — единый визуальный грип без переноса; для
  // DNS-серверов reorder-API отсутствует, для правил/перезаписей есть Move, но
  // вводить drag только для двух из трёх блоков — рассинхрон UX. Включим разом,
  // когда появится reorder DNS-серверов.
-->
<script lang="ts">
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { subscriptionsStore } from '$lib/stores/subscriptions';
	import { singboxProxies } from '$lib/stores/singboxProxies';
	import { singboxTunnels } from '$lib/stores/singbox';
	import { notifications } from '$lib/stores/notifications';
	import { api } from '$lib/api/client';
	import {
		computeRuleSetUsage,
		DNSServerEditModal,
		DNSRuleEditModal,
		DNSRewritesList,
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

	// ── Store sub-stores (как в ExpertPanel) ──────────────────────────────
	const storeDnsServers = singboxRouter.dnsServers;
	const storeDnsRules = singboxRouter.dnsRules;
	const storeDnsRewrites = singboxRouter.dnsRewrites;
	const storeDnsGlobals = singboxRouter.dnsGlobals;
	const storeRuleSets = singboxRouter.ruleSets;
	const storeOutbounds = singboxRouter.outbounds;
	const storeOptions = singboxRouter.options;

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
		const port = s.server_port ? `:${s.server_port}` : '';
		const path = s.path ?? '';
		return `${s.server}${port}${path}`;
	}

	// ── Modal state ───────────────────────────────────────────────────────
	let dnsServerEditTag = $state<string | null>(null);
	let dnsServerAddOpen = $state(false);
	let dnsRuleEditIdx = $state<number | null>(null);
	let dnsRuleAddOpen = $state(false);
	let dnsGlobalsModalOpen = $state(false);
	let rewriteAddMode = $state(false);

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

	// ── Handlers (зеркалят ExpertPanel) ──────────────────────────────────
	async function handleDnsServerAddSave(server: SingboxRouterDNSServer): Promise<void> {
		await api.singboxRouterAddDNSServer(server);
		dnsServerAddOpen = false;
		await singboxRouter.loadAll();
	}
	async function handleDnsServerEditSave(server: SingboxRouterDNSServer): Promise<void> {
		if (dnsServerEditTag !== null) {
			await api.singboxRouterUpdateDNSServer(dnsServerEditTag, server);
		}
		dnsServerEditTag = null;
		await singboxRouter.loadAll();
	}
	function handleDeleteDnsServer(tag: string): void {
		pendingConfirm = {
			title: 'Удалить DNS-сервер',
			message: `Удалить DNS-сервер «${tag}»?`,
			run: async () => {
				try {
					await api.singboxRouterDeleteDNSServer(tag);
					await singboxRouter.loadAll();
					notifications.success('DNS-сервер удалён');
				} catch (e) {
					notifications.error(`Ошибка: ${e instanceof Error ? e.message : String(e)}`);
				}
			},
		};
	}

	async function handleDnsRuleAddSave(rule: SingboxRouterDNSRule): Promise<void> {
		await api.singboxRouterAddDNSRule(rule);
		dnsRuleAddOpen = false;
		await singboxRouter.loadAll();
	}
	async function handleDnsRuleEditSave(rule: SingboxRouterDNSRule): Promise<void> {
		if (dnsRuleEditIdx !== null) {
			await api.singboxRouterUpdateDNSRule(dnsRuleEditIdx, rule);
		}
		dnsRuleEditIdx = null;
		await singboxRouter.loadAll();
	}
	function handleDeleteDNSRule(idx: number): void {
		pendingConfirm = {
			title: 'Удалить DNS-правило',
			message: `Удалить DNS-правило #${idx + 1}?`,
			run: async () => {
				try {
					await api.singboxRouterDeleteDNSRule(idx);
					await singboxRouter.loadAll();
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
		await api.singboxRouterPutDNSGlobals(globals);
		dnsGlobalsModalOpen = false;
		await singboxRouter.loadAll();
	}
</script>

<div class="dns-grid">
	<!-- ── Блок 1: DNS-серверы ─────────────────────────────────────────── -->
	<section class="panel">
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
			<div class="rows">
				{#each $storeDnsServers as s (s.tag)}
					{@const core = isCoreServer(s)}
					<div class="srow">
						<span class="grip" aria-hidden="true"><GripVertical size={16} strokeWidth={2} /></span>
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
				{/each}
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
	<section class="panel">
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

		<div class="rows">
			{#each $storeDnsRules as r, i (i)}
				{@const tgt = dnsRuleTarget(r)}
				{@const matchers = dnsMatcherParts(r)}
				<div class="rrow">
					<span class="grip" aria-hidden="true"><GripVertical size={16} strokeWidth={2} /></span>
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
			{/each}

			<!-- Итоговая read-only строка: final → globals.final -->
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
	</section>

	<!-- ── Блок 3: DNS-перезаписи (на всю ширину) ──────────────────────── -->
	<section class="panel panel-full">
		<header class="ph">
			<span class="nm">DNS-перезаписи · {$storeDnsRewrites.length}</span>
			<button type="button" class="add" onclick={() => (rewriteAddMode = true)}>
				<Plus size={14} strokeWidth={2} aria-hidden="true" /> Перезапись
			</button>
		</header>
		<p class="pd">
			Статические ответы: домен → фиксированный IP (A/AAAA), мимо резолва (как static_a).
		</p>
		<DNSRewritesList
			rewrites={$storeDnsRewrites}
			onChange={() => singboxRouter.loadAll()}
			showHeader={false}
			hideColumnHeader={true}
			bind:addMode={rewriteAddMode}
		/>
	</section>
</div>

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
	/* Сетка мокапа: два верхних блока в ряд (серверы уже, правила шире) +
	   перезаписи на всю ширину снизу. */
	.dns-grid {
		display: grid;
		grid-template-columns: 1fr 1.25fr;
		gap: 1rem;
	}
	.panel-full {
		grid-column: 1 / -1;
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

	/* ── DNS-серверы строки ── */
	.srow {
		display: grid;
		grid-template-columns: 18px minmax(0, 1.1fr) auto minmax(0, 1.3fr) auto auto;
		align-items: center;
		gap: 0.6rem;
		padding: 0.55rem 0.25rem;
		border-bottom: 1px solid var(--color-border, var(--border));
	}
	.srow:last-of-type {
		border-bottom: none;
	}

	.grip {
		display: inline-flex;
		color: var(--text-muted);
		opacity: 0.55;
		cursor: grab;
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
	.rrow:last-of-type {
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
