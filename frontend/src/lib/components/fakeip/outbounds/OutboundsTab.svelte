<!--
  Вкладка «Outbounds» страницы FakeIP (Slice 3.2, FE-spec §5.3).

  Две секции:
  - ATOMIC — пул прокси-выходов (AWG/NWG-туннели + прокси подписок), те же
    сущности, что появляются участниками composite-групп. Спайн — singboxRouter
    .options, обогащённый join'ом с singboxTunnels + subscription-members
    (buildAtomicEgresses), минус специальные (direct/block) и composite-группы.
    Read-only здесь: правка на страницах Туннели / Подписки (ссылка-выход у
    карточки). Тест задержки — per-egress, по запросу (singbox/tunnels/delay-
    check). Кнопки «+ outbound» нет — выходы создаются не тут.
  - COMPOSITE — selector / urltest группы (роутер-каталог); правятся здесь.

  Тот же каталог outbounds роутера, что и у sb-router Expert-вида, поэтому
  максимально ПЕРЕИСПОЛЬЗУЕМ существующее:
  - singboxRouter.outbounds / .options — конфиг каталога (sub-stores).
  - singboxTunnels — AWG/NWG-туннели: протокол/endpoint/sni/transport для
    обогащения атомарных выходов.
  - singboxProxies — reference-counted polling: живой снимок proxy-групп
    (активный участник `now`, lastDelay). Подписка стартует/останавливает опрос.
  - subscriptionsStore — имена подписочных композитов + члены-выходы.
  - resolveCompositeOutboundView (sb-router helper) — активный участник.
  - CompositeOutboundEditModal (routing/singboxRouter) — add / edit / rename
    (поле tag редактируемо → переименование). Тот же модал, что в ExpertPanel.
  - ConfirmModal — удаление.
  - proxies/test (singboxRouterTestProxy) — тест группы по запросу (задержки).
  - proxies/select (singboxRouterSelectProxy) — выбор активного участника
    selector-группы.

  Конфиг-список виден всегда (даже когда движок остановлен). Живые сигналы
  (активный участник, health, задержки, select/test) деградируют по `live`
  (FE-spec §12.1). ЧЕСТНОСТЬ (§4): задержка — единственное per-outbound число,
  и только по запросу; никакого throughput.

  Тонкий оркестратор: деривации каталога/партиции + обработчики CRUD/runtime;
  рендер делегирован под-карточкам.
-->
<script lang="ts">
	import { fakeipConfig } from '$lib/stores/fakeipConfig';
	import { singboxProxies } from '$lib/stores/singboxProxies';
	import { singboxDelayHistory, triggerDelayCheck } from '$lib/stores/singbox';
	import { singboxDelayFromHistory } from '$lib/utils/singboxDelay';
	import { singboxTunnels } from '$lib/stores/singbox';
	import { subscriptionsStore } from '$lib/stores/subscriptions';
	import { notifications } from '$lib/stores/notifications';
	import { api } from '$lib/api/client';
	import { ConfirmModal } from '$lib/components/ui';
	import { Plus } from 'lucide-svelte';
	import CompositeOutboundEditModal from '$lib/components/routing/singboxRouter/CompositeOutboundEditModal.svelte';
	import type { SingboxRouterOutbound } from '$lib/types';
	import type { FakeIPEngineState } from '../engineState';
	import { partitionOutbounds } from './partitionOutbounds';
	import { buildAtomicEgresses } from './atomicEgress';
	import AtomicOutboundCard from './AtomicOutboundCard.svelte';
	import CompositeOutboundCard from './CompositeOutboundCard.svelte';

	interface Props {
		/** Состояние движка — гейтит живые сигналы (FE-spec §12.1). */
		engineState: FakeIPEngineState;
	}

	let { engineState }: Props = $props();

	// live — runtime-сигналы (активный участник / health / задержки / select /
	// test) доступны только когда движок реально работает.
	const live = $derived(engineState !== 'stopped' && engineState !== 'clash-down');

	const storeOutbounds = fakeipConfig.outbounds;
	const storeOptions = fakeipConfig.options;

	const partitioned = $derived(partitionOutbounds($storeOutbounds));
	const subscriptions = $derived($subscriptionsStore.data ?? []);
	const proxyGroups = $derived($singboxProxies.data ?? []);

	// ATOMIC section = the proxy egress pool (AWG/NWG tunnels + subscription
	// proxies) — the same entities that appear as composite members. Built from
	// the options spine joined with tunnels + subscription members, excluding
	// specials (direct/block) and composite groups. Config-visible regardless
	// of engine state; live signals (delay/dot) gate on `live`.
	const atomicEgresses = $derived(
		buildAtomicEgresses($storeOptions, $singboxTunnels.data, subscriptions),
	);

	// ── CRUD-модалы (переиспользуем CompositeOutboundEditModal) ──────────
	let addOpen = $state(false);
	let editTag = $state<string | null>(null);
	const editTarget = $derived<SingboxRouterOutbound | undefined>(
		editTag !== null ? $storeOutbounds.find((o) => o.tag === editTag) : undefined,
	);

	async function handleAddSave(o: SingboxRouterOutbound): Promise<void> {
		await api.singboxFakeIPAddOutbound(o);
		addOpen = false;
		await fakeipConfig.loadAll();
	}

	async function handleEditSave(o: SingboxRouterOutbound): Promise<void> {
		if (editTag !== null) {
			await api.singboxFakeIPUpdateOutbound(editTag, o);
		}
		editTag = null;
		await fakeipConfig.loadAll();
	}

	let pendingDelete = $state<{ tag: string; title: string } | null>(null);
	let deleteBusy = $state(false);

	function requestDelete(tag: string): void {
		pendingDelete = { tag, title: tag };
	}

	async function confirmDelete(): Promise<void> {
		if (!pendingDelete) return;
		deleteBusy = true;
		try {
			await api.singboxFakeIPDeleteOutbound(pendingDelete.tag);
			await fakeipConfig.loadAll();
			notifications.success('Outbound удалён');
			pendingDelete = null;
		} catch (e) {
			notifications.error(`Ошибка: ${e instanceof Error ? e.message : String(e)}`);
		} finally {
			deleteBusy = false;
		}
	}

	// ── Runtime: тест группы (proxies/test) ──────────────────────────────
	// per-group: результаты последнего теста (memberTag → delay) + флаг busy.
	let testResults = $state<Record<string, Record<string, number>>>({});
	let testingTag = $state<string | null>(null);
	// Время последнего теста по группе (hh:mm) — для «members · last hh:mm»;
	// `lastTestAtAny` — общий «health-check … · last hh:mm» в шапке ATOMIC.
	let lastTestAt = $state<Record<string, string>>({});
	let lastTestAtAny = $state<string | null>(null);

	function nowHHMM(): string {
		return new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
	}

	async function handleTest(tag: string): Promise<void> {
		if (testingTag) return;
		testingTag = tag;
		try {
			const res = await api.singboxRouterTestProxy({ group: tag });
			testResults = { ...testResults, [tag]: res.delays };
			const stamp = nowHHMM();
			lastTestAt = { ...lastTestAt, [tag]: stamp };
			lastTestAtAny = stamp;
			// Refresh the live snapshot so `now` / lastDelay reflect the probe.
			await singboxProxies.refetch();
		} catch (e) {
			notifications.error(`Тест не удался: ${e instanceof Error ? e.message : String(e)}`);
		} finally {
			testingTag = null;
		}
	}

	// ── Runtime: per-egress тест задержки ────────────────────────────────
	// Тот же механизм, что у страниц туннелей/подписок: triggerDelayCheck(tag)
	// запускает Clash-замер по тегу — работает И для туннелей, И для подписочных
	// прокси (backend тестирует любой Clash-тег). Результат прилетает в общий
	// стор singboxDelayHistory через глобальный SSE (+layout), отсюда и читаем.
	let egressTesting = $state<string | null>(null);

	const egressDelays = $derived.by<Record<string, number>>(() => {
		const out: Record<string, number> = {};
		for (const [tag, hist] of $singboxDelayHistory) {
			const latest = singboxDelayFromHistory(hist).latest;
			if (latest !== undefined) out[tag] = latest;
		}
		return out;
	});

	async function handleEgressTest(tag: string): Promise<void> {
		if (egressTesting) return;
		egressTesting = tag;
		try {
			await triggerDelayCheck(tag);
			lastTestAtAny = nowHHMM();
		} finally {
			egressTesting = null;
		}
	}

	// ── Runtime: выбор активного участника (proxies/select) ──────────────
	let selectingTag = $state<string | null>(null);

	async function handleSelect(group: string, member: string): Promise<void> {
		if (selectingTag) return;
		selectingTag = group;
		try {
			await api.singboxRouterSelectProxy({ group, member });
			await singboxProxies.refetch();
		} catch (e) {
			notifications.error(`Не удалось выбрать: ${e instanceof Error ? e.message : String(e)}`);
		} finally {
			selectingTag = null;
		}
	}
</script>

<section class="outbounds-tab">
	<div class="section">
		<div class="sectlbl">
			<span class="sect-name"
				>Outbounds · atomic <span class="sect-count">· {atomicEgresses.length}</span></span
			>
			<span class="sect-right">
				<span class="hc">health-check по запросу{#if lastTestAtAny} · last {lastTestAtAny}{/if}</span>
			</span>
		</div>
		{#if atomicEgresses.length === 0}
			<p class="section-empty">
				Пул прокси-выходов пуст. Добавьте туннели или подписку.
			</p>
		{:else}
			<div class="ocards">
				{#each atomicEgresses as e (e.tag)}
					<AtomicOutboundCard
						egress={e}
						{live}
						delay={egressDelays[e.tag]}
						testing={egressTesting === e.tag}
						onTest={handleEgressTest}
					/>
				{/each}
			</div>
		{/if}
	</div>

	<div class="section">
		<div class="sectlbl">
			<span class="sect-name"
				>Outbounds · composite <span class="sect-count">· {partitioned.composite.length}</span
				></span
			>
			<button type="button" class="add" onclick={() => (addOpen = true)}>
				<Plus size={13} aria-hidden="true" /> Новая группа
			</button>
		</div>
		{#if partitioned.composite.length === 0}
			<p class="section-empty">Composite-группы не настроены.</p>
		{:else}
			<div class="ocards">
				{#each partitioned.composite as o (o.tag)}
					<CompositeOutboundCard
						outbound={o}
						outbounds={$storeOutbounds}
						outboundOptions={$storeOptions}
						{subscriptions}
						{proxyGroups}
						{live}
						testDelays={testResults[o.tag]}
						testing={testingTag === o.tag}
						selecting={selectingTag === o.tag}
						lastTestAt={lastTestAt[o.tag]}
						onEdit={(tag) => (editTag = tag)}
						onDelete={requestDelete}
						onTest={handleTest}
						onSelect={handleSelect}
					/>
				{/each}
			</div>
		{/if}
	</div>
</section>

{#if addOpen}
	<CompositeOutboundEditModal
		outboundOptions={$storeOptions}
		onClose={() => (addOpen = false)}
		onSave={handleAddSave}
	/>
{/if}

{#if editTag !== null && editTarget !== undefined}
	<CompositeOutboundEditModal
		outbound={editTarget}
		outboundOptions={$storeOptions}
		onClose={() => (editTag = null)}
		onSave={handleEditSave}
	/>
{/if}

<ConfirmModal
	open={pendingDelete !== null}
	title="Удалить outbound"
	message={pendingDelete ? `Удалить outbound «${pendingDelete.title}»?` : ''}
	busy={deleteBusy}
	onConfirm={confirmDelete}
	onClose={() => {
		if (!deleteBusy) pendingDelete = null;
	}}
/>

<style>
	.outbounds-tab {
		display: flex;
		flex-direction: column;
		gap: 1.125rem;
	}

	.section {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
	}

	.sectlbl {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		flex-wrap: wrap;
	}

	.sect-name {
		font-size: 0.8125rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.08em;
		color: var(--text-secondary);
	}

	.sect-count {
		color: var(--text-muted);
		font-family: var(--font-mono);
	}

	.sect-right {
		display: inline-flex;
		align-items: center;
		gap: 0.625rem;
	}

	.hc {
		font-size: 0.8125rem;
		color: var(--text-muted);
	}

	.add {
		display: inline-flex;
		align-items: center;
		gap: 0.3125rem;
		color: var(--color-accent);
		border: 1px solid var(--color-accent-border, var(--color-accent));
		border-radius: var(--radius-sm);
		padding: 0.25rem 0.5625rem;
		background: transparent;
		cursor: pointer;
		font-size: 0.8125rem;
	}
	.add:hover {
		background: var(--accent-soft);
	}

	.section-empty {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--text-muted);
	}

	.ocards {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(18rem, 1fr));
		gap: 0.75rem;
		align-items: stretch;
	}

</style>
