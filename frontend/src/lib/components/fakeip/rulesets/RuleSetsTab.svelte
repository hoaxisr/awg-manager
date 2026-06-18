<!--
  Rule sets-чип FakeIP по мокапу page-rulesets-v3: ОДНА карточка на всю ширину —
  заголовок «Rule sets · N» + «+ Rule set»/«Каталог», описание, тип-фильтр-чипы
  (Все/dat/remote/local/inline со счётчиками) и таблица
  имя | тип | источник | интервал | используется в | действия.

  Чистая ПЕРЕКОМПОНОВКА механики rule-set'ов sb-router под мокап:
    - Модалы переиспользуются ВЕРБАТИМ: RuleSetAddModal (add/edit через prop
      ruleSet) + SbRouterRuleSetCatalogModal (dat-каталог geosite/geoip).
    - CRUD — api.singboxRouter{Add,Update,Delete}RuleSet + каталог через
      applyCatalogPresetsAsRuleSets; после мутаций singboxRouter.loadAll()
      (зеркалит ExpertPanel.svelte и DnsTab).
    - Тип/источник — общие хелперы datInfo/resolveRuleSetDisplayType +
      RuleSetTypeBadge; «используется в» — локальный computeRuleSetUsageRefs
      по dnsRules+rules (computeRuleSetUsage даёт только сумму, не список).

  Существующая RuleSetsTable.svelte (routing-страница) НЕ переиспользована для
  рендера строк: её колонки (Тег/Тип/Источник/Через/Действия + свой segment-row)
  расходятся с мокапом (нет «интервал»/«используется в», нет тип-чипов-счётчиков),
  и выразить мокап через её props нельзя — строки рендерим здесь, всю остальную
  машинерию (хелперы/модалы/CRUD) переиспользуем. RuleSetsTable не трогаем →
  routing-страница не регрессит.

  Движок-гейт: rule-set'ы — это конфиг, доступны при любом состоянии движка
  (никаких live-рантайм блоков), поэтому рендерится всегда.

  Footer-семантика мокапа: для remote/local число доменов НЕ показываем; для
  inline — число правил; «интервал» = update_interval (sing-box тянет сам — нет
  ручного «обновить»); статуса/ошибки загрузки у rule-set нет — не показываем.
-->
<script lang="ts">
	import { fakeipConfig } from '$lib/stores/fakeipConfig';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { pluralize, SET_WORDS } from '$lib/utils/pluralize';
	import { Plus, LayoutGrid, Pencil, Trash2 } from 'lucide-svelte';
	import { ConfirmModal } from '$lib/components/ui';
	import RuleSetAddModal from '$lib/components/routing/singboxRouter/RuleSetAddModal.svelte';
	import SbRouterRuleSetCatalogModal from '$lib/components/sb-router/SbRouterRuleSetCatalogModal.svelte';
	import RuleSetTypeBadge from '$lib/components/sb-router/RuleSetTypeBadge.svelte';
	import { applyCatalogPresetsAsRuleSets } from '$lib/components/sb-router/rulesetCatalogActions';
	import { datInfo, resolveRuleSetDisplayType } from '$lib/utils/ruleSetType';
	import { displayRuleSetTag } from '$lib/utils/singboxInlineRules';
	import { computeRuleSetUsageRefs } from './ruleSetUsageRefs';
	import type { SingboxRouterRuleSet, CatalogPreset } from '$lib/types';

	// ── Store sub-stores ───────────────────────────────────────────────────
	const storeRuleSets = fakeipConfig.ruleSets;
	const storeDnsRules = fakeipConfig.dnsRules;
	const storeRules = fakeipConfig.rules;
	const storeOptions = fakeipConfig.options;

	// ── Тип-фильтр (мокап: Все / dat / remote / local / inline) ───────────
	type RsFilter = 'all' | 'dat' | 'remote' | 'local' | 'inline';
	let filter = $state<RsFilter>('all');

	// dat — это remote-набор с dat-srs URL; чтобы remote-счётчик не двоился,
	// «remote» = remote-наборы БЕЗ dat-формы (как воспринимает их пользователь).
	const counts = $derived.by(() => {
		let dat = 0;
		let remote = 0;
		let local = 0;
		let inline = 0;
		for (const rs of $storeRuleSets) {
			const t = resolveRuleSetDisplayType(rs);
			if (t === 'dat') dat++;
			else if (t === 'remote') remote++;
			else if (t === 'local') local++;
			else if (t === 'inline') inline++;
		}
		return { all: $storeRuleSets.length, dat, remote, local, inline };
	});

	const filtered = $derived.by(() => {
		if (filter === 'all') return $storeRuleSets;
		return $storeRuleSets.filter((rs) => resolveRuleSetDisplayType(rs) === filter);
	});

	const FILTERS: { k: RsFilter; l: string }[] = [
		{ k: 'all', l: 'Все' },
		{ k: 'dat', l: 'dat' },
		{ k: 'remote', l: 'remote' },
		{ k: 'local', l: 'local' },
		{ k: 'inline', l: 'inline' },
	];

	// ── «используется в» (DNS #n · Route #m), 1-based по правилам ─────────
	const usageRefs = $derived(computeRuleSetUsageRefs($storeDnsRules, $storeRules));

	// ── Колонки-хелперы ──────────────────────────────────────────────────
	// «тип»: бейдж remote/local/inline/dat + dim-сабкласс binary/source для dat.
	function formatSub(rs: SingboxRouterRuleSet): string | null {
		if (datInfo(rs)) return rs.format ?? 'binary';
		return null;
	}

	// «источник»: geosite/geoip dat-каталог · URL для remote · path для local ·
	// «N правил в конфиге (.srs собран)» для inline.
	function sourceFor(rs: SingboxRouterRuleSet): string {
		const dat = datInfo(rs);
		if (dat) return `${dat.kind}-каталог (dat-url)`;
		if (rs.type === 'remote') return rs.url ?? '—';
		if (rs.type === 'local') return rs.path ?? '—';
		if (rs.type === 'inline') {
			const n = rs.rules?.length ?? 0;
			return `${pluralize(n, RULE_WORDS)} в конфиге`;
		}
		return '—';
	}
	function inlineMaterialized(rs: SingboxRouterRuleSet): boolean {
		return rs.type === 'inline' && rs.materialized_srs === true;
	}
	const RULE_WORDS: [string, string, string] = ['правило', 'правила', 'правил'];

	// «интервал»: update_interval · «—» для inline/local (sing-box тянет сам).
	function intervalFor(rs: SingboxRouterRuleSet): string {
		if (rs.type === 'remote') return rs.update_interval || '—';
		return '—';
	}

	// ── Модалы (state) ───────────────────────────────────────────────────
	let rsAddOpen = $state(false);
	let rsEditTag = $state<string | null>(null);
	let rsCatalogOpen = $state(false);
	let rsCatalogBusy = $state(false);

	const rsEditTarget = $derived<SingboxRouterRuleSet | undefined>(
		rsEditTag !== null ? $storeRuleSets.find((rs) => rs.tag === rsEditTag) : undefined,
	);

	let pendingConfirm = $state<{ title: string; message: string; run: () => Promise<void> } | null>(
		null,
	);
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
	async function handleRsAddSave(rs: SingboxRouterRuleSet): Promise<void> {
		await api.singboxFakeIPAddRuleSet(rs);
		rsAddOpen = false;
		await fakeipConfig.loadAll();
	}

	async function handleRsEditSave(rs: SingboxRouterRuleSet): Promise<void> {
		if (rsEditTag !== null) {
			await api.singboxFakeIPUpdateRuleSet(rsEditTag, rs);
		}
		rsEditTag = null;
		await fakeipConfig.loadAll();
	}

	function handleDeleteRs(tag: string): void {
		pendingConfirm = {
			title: 'Удалить набор',
			message: `Удалить набор «${displayRuleSetTag(tag)}»?`,
			run: async () => {
				try {
					await api.singboxFakeIPDeleteRuleSet(tag);
					await fakeipConfig.loadAll();
					notifications.success('Набор удалён');
				} catch (e) {
					notifications.error(`Ошибка: ${e instanceof Error ? e.message : String(e)}`);
				}
			},
		};
	}

	async function handleRsCatalogConfirm(presets: CatalogPreset[]): Promise<void> {
		if (rsCatalogBusy || presets.length === 0) return;
		rsCatalogBusy = true;
		try {
			const result = await applyCatalogPresetsAsRuleSets(
				presets,
				$storeRuleSets,
				(rs) => api.singboxFakeIPAddRuleSet(rs),
			);
			await fakeipConfig.loadAll();

			if (result.added.length > 0) {
				notifications.success(`Добавлено ${pluralize(result.added.length, SET_WORDS)} из каталога`);
			} else if (result.failures.length === 0 && result.emptyPresets.length > 0) {
				notifications.error('У выбранных сервисов нет sing-box наборов');
			} else if (result.failures.length === 0) {
				notifications.info('Выбранные наборы уже есть в конфиге');
			}

			if (result.failures.length > 0) {
				const msg = result.failures.map((f) => `${f.tag}: ${f.error}`).join('; ');
				notifications.error(`Не удалось добавить: ${msg}`);
			} else if (result.added.length > 0 || result.emptyPresets.length === 0) {
				rsCatalogOpen = false;
			}
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : String(e));
		} finally {
			rsCatalogBusy = false;
		}
	}
</script>

<section class="panel">
	<header class="ph">
		<span class="nm">Rule sets · {$storeRuleSets.length}</span>
		<div class="head-actions">
			<button type="button" class="add ghost" onclick={() => (rsCatalogOpen = true)}>
				<LayoutGrid size={14} strokeWidth={2} aria-hidden="true" /> Каталог
			</button>
			<button type="button" class="add" onclick={() => (rsAddOpen = true)}>
				<Plus size={14} strokeWidth={2} aria-hidden="true" /> Rule set
			</button>
		</div>
	</header>
	<p class="pd">
		Списки доменов/CIDR для DNS- и route-правил. remote (URL, sing-box обновляет по интервалу) ·
		local (файл) · inline.
	</p>

	<!-- Тип-фильтр-чипы со счётчиками (client-side, «Все» активен по умолчанию) -->
	<div class="filters" role="tablist" aria-label="Фильтр по типу набора">
		{#each FILTERS as f (f.k)}
			<button
				type="button"
				class="f"
				class:on={filter === f.k}
				role="tab"
				aria-selected={filter === f.k}
				onclick={() => (filter = f.k)}
			>
				{f.l}
				<span class="cnt">{counts[f.k]}</span>
			</button>
		{/each}
	</div>

	<div class="table">
		<div class="thead">
			<div>имя</div>
			<div>тип</div>
			<div>источник</div>
			<div class="center">интервал</div>
			<div>используется в</div>
			<div class="actions-col"></div>
		</div>

		{#if filtered.length === 0}
			<div class="empty">Нет наборов</div>
		{:else}
			{#each filtered as rs (rs.tag)}
				{@const ref = usageRefs.get(displayRuleSetTag(rs.tag))}
				{@const sub = formatSub(rs)}
				<div class="row">
					<div class="nm2">{displayRuleSetTag(rs.tag)}</div>
					<div class="type">
						<RuleSetTypeBadge type={resolveRuleSetDisplayType(rs)} />
						{#if sub}<span class="fmt">{sub}</span>{/if}
					</div>
					<div class="src" title={sourceFor(rs)}>
						{sourceFor(rs)}
						{#if inlineMaterialized(rs)}<span class="mut">(.srs собран)</span>{/if}
					</div>
					<div class="interval center">{intervalFor(rs)}</div>
					<div class="used">
						{#if ref && (ref.dns.length > 0 || ref.route.length > 0)}
							{#if ref.dns.length > 0}<span class="used-grp"
									>DNS {#each ref.dns as n, i (n)}<b>#{n}</b>{#if i < ref.dns.length - 1}, {/if}{/each}</span
								>{/if}
							{#if ref.dns.length > 0 && ref.route.length > 0}<span class="used-sep">·</span>{/if}
							{#if ref.route.length > 0}<span class="used-grp"
									>Route {#each ref.route as n, i (n)}<b>#{n}</b>{#if i < ref.route.length - 1}, {/if}{/each}</span
								>{/if}
						{:else}
							<span class="used-none">—</span>
						{/if}
					</div>
					<div class="actions-col acts">
						<button
							type="button"
							class="ib"
							onclick={() => (rsEditTag = rs.tag)}
							aria-label={`Редактировать набор ${displayRuleSetTag(rs.tag)}`}
							title={`Редактировать набор «${displayRuleSetTag(rs.tag)}»`}
						>
							<Pencil size={15} strokeWidth={2} />
						</button>
						<button
							type="button"
							class="ib danger"
							onclick={() => handleDeleteRs(rs.tag)}
							aria-label={`Удалить набор ${displayRuleSetTag(rs.tag)}`}
							title={`Удалить набор «${displayRuleSetTag(rs.tag)}»`}
						>
							<Trash2 size={15} strokeWidth={2} />
						</button>
					</div>
				</div>
			{/each}
		{/if}
	</div>
</section>

<!-- ── Модалы (переиспользуем вербатим) ──────────────────────────────── -->
<SbRouterRuleSetCatalogModal
	open={rsCatalogOpen}
	existingRuleSetTags={$storeRuleSets.map((rs) => rs.tag)}
	submitting={rsCatalogBusy}
	onclose={() => {
		if (!rsCatalogBusy) rsCatalogOpen = false;
	}}
	onconfirm={handleRsCatalogConfirm}
/>

{#if rsAddOpen}
	<RuleSetAddModal
		outboundOptions={$storeOptions}
		onClose={() => (rsAddOpen = false)}
		onSave={handleRsAddSave}
	/>
{/if}

{#if rsEditTag !== null && rsEditTarget !== undefined}
	<RuleSetAddModal
		ruleSet={rsEditTarget}
		outboundOptions={$storeOptions}
		onClose={() => (rsEditTag = null)}
		onSave={handleRsEditSave}
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
	.head-actions {
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
	}
	.add {
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		color: var(--color-bg-secondary, #0a0a0a);
		background: var(--color-accent, var(--accent));
		font-size: 0.8125rem;
		font-weight: 700;
		border: 1px solid var(--color-accent, var(--accent));
		border-radius: var(--radius-sm, 6px);
		padding: 0.3rem 0.7rem;
		cursor: pointer;
	}
	.add:hover {
		filter: brightness(1.08);
	}
	.add.ghost {
		color: var(--color-accent, var(--accent));
		background: transparent;
		font-weight: 600;
		border-color: color-mix(in srgb, var(--color-accent, var(--accent)) 35%, transparent);
	}
	.add.ghost:hover {
		background: color-mix(in srgb, var(--color-accent, var(--accent)) 12%, transparent);
		filter: none;
	}

	.pd {
		color: var(--text-muted);
		font-size: 0.8125rem;
		line-height: 1.4;
		margin: 0 0 0.875rem;
	}

	/* ── Тип-фильтр-чипы ── */
	.filters {
		display: flex;
		gap: 0.5rem;
		flex-wrap: wrap;
		margin-bottom: 0.875rem;
	}
	.f {
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
		border-radius: 999px;
		padding: 0.3rem 0.7rem;
		background: var(--color-bg-tertiary, var(--bg-tertiary));
		border: 1px solid var(--color-border, var(--border));
		color: var(--text-secondary);
		font-size: 0.8125rem;
		font-weight: 600;
		font-family: inherit;
		cursor: pointer;
	}
	.f:hover {
		color: var(--text-primary);
		border-color: var(--color-border-hover, var(--border));
	}
	.f.on {
		background: var(--color-accent, var(--accent));
		color: var(--color-bg-secondary, #0a0a0a);
		border-color: var(--color-accent, var(--accent));
	}
	.f .cnt {
		font-size: 0.6875rem;
		font-weight: 700;
		padding: 0.05rem 0.4rem;
		border-radius: 999px;
		background: color-mix(in srgb, var(--text-muted) 22%, transparent);
		color: var(--text-secondary);
	}
	.f.on .cnt {
		background: color-mix(in srgb, var(--color-bg-secondary, #0a0a0a) 18%, transparent);
		color: var(--color-bg-secondary, #0a0a0a);
	}

	/* ── Таблица ── */
	.table {
		min-width: 0;
	}
	.thead,
	.row {
		display: grid;
		grid-template-columns: minmax(0, 1.2fr) 130px minmax(0, 1.6fr) 90px minmax(0, 1.2fr) 76px;
		align-items: center;
		gap: 0.75rem;
		padding: 0.6rem 0.25rem;
	}
	.thead {
		border-bottom: 1px solid var(--color-border, var(--border));
		font-size: 0.6875rem;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--text-muted);
		font-weight: 600;
	}
	.row {
		border-bottom: 1px solid color-mix(in srgb, var(--color-border, var(--border)) 55%, transparent);
		font-size: 0.8125rem;
	}
	.row:last-child {
		border-bottom: none;
	}
	@media (hover: hover) and (pointer: fine) {
		.row:hover {
			background: color-mix(in srgb, var(--color-bg-hover, var(--bg-hover)) 60%, transparent);
		}
	}
	.center {
		text-align: center;
		justify-self: center;
	}

	.nm2 {
		color: var(--text-primary);
		font-weight: 600;
		font-family: var(--font-mono);
		font-size: 0.8125rem;
		min-width: 0;
		overflow-wrap: anywhere;
	}

	.type {
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
		min-width: 0;
	}
	.fmt {
		color: var(--text-muted);
		font-size: 0.6875rem;
		font-family: var(--font-mono);
	}

	.src {
		min-width: 0;
		font-family: var(--font-mono);
		font-size: 0.75rem;
		color: var(--text-muted);
		overflow-wrap: anywhere;
		word-break: break-word;
		line-height: 1.4;
	}
	.src .mut {
		color: color-mix(in srgb, var(--text-muted) 70%, transparent);
		font-size: 0.6875rem;
		margin-left: 0.25rem;
	}

	.interval {
		font-family: var(--font-mono);
		font-size: 0.8125rem;
		color: var(--text-secondary);
		white-space: nowrap;
	}

	.used {
		color: var(--text-secondary);
		font-size: 0.78rem;
		min-width: 0;
		overflow-wrap: anywhere;
	}
	.used-grp {
		white-space: nowrap;
	}
	.used b {
		color: var(--color-accent, var(--accent));
		font-weight: 700;
	}
	.used-sep {
		color: var(--text-muted);
		margin: 0 0.25rem;
	}
	.used-none {
		color: var(--text-muted);
	}

	.actions-col {
		justify-self: end;
	}
	.acts {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
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

	.empty {
		padding: 1.25rem;
		color: var(--text-muted);
		text-align: center;
		font-size: 0.8125rem;
	}

	/* ── Адаптив: на узком экране колонки сворачиваются в карточки ── */
	@media (max-width: 820px) {
		.thead {
			display: none;
		}
		.row {
			grid-template-columns: minmax(0, 1fr) auto;
			grid-template-areas:
				'name actions'
				'type type'
				'src src'
				'meta meta';
			gap: 0.4rem 0.75rem;
			padding: 0.75rem 0.25rem;
		}
		.row > .nm2 {
			grid-area: name;
		}
		.row > .type {
			grid-area: type;
		}
		.row > .src {
			grid-area: src;
		}
		.row > .interval {
			grid-area: meta;
			justify-self: start;
			text-align: left;
		}
		.row > .used {
			grid-area: meta;
		}
		.row > .actions-col {
			grid-area: actions;
		}
		/* interval + used делят meta-ряд */
		.row {
			grid-template-areas:
				'name actions'
				'type type'
				'src src'
				'meta used';
		}
		.row > .interval {
			grid-area: meta;
		}
		.row > .used {
			grid-area: used;
		}
	}
</style>
