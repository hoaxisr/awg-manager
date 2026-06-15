<!--
  Вкладка «Outbounds» страницы FakeIP (Slice 3.2, FE-spec §5.3).

  Тот же каталог outbounds роутера, что и у sb-router Expert-вида, поэтому
  максимально ПЕРЕИСПОЛЬЗУЕМ существующее:
  - singboxRouter.outbounds / .options — конфиг каталога (sub-stores).
  - singboxProxies — reference-counted polling: живой снимок proxy-групп
    (активный участник `now`, lastDelay). Подписка стартует/останавливает опрос.
  - subscriptionsStore — имена подписочных композитов.
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
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { singboxProxies } from '$lib/stores/singboxProxies';
	import { subscriptionsStore } from '$lib/stores/subscriptions';
	import { notifications } from '$lib/stores/notifications';
	import { api } from '$lib/api/client';
	import { Button, ConfirmModal } from '$lib/components/ui';
	import { Plus } from 'lucide-svelte';
	import CompositeOutboundEditModal from '$lib/components/routing/singboxRouter/CompositeOutboundEditModal.svelte';
	import type { SingboxRouterOutbound } from '$lib/types';
	import type { FakeIPEngineState } from '../engineState';
	import { partitionOutbounds } from './partitionOutbounds';
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

	const storeOutbounds = singboxRouter.outbounds;
	const storeOptions = singboxRouter.options;

	const partitioned = $derived(partitionOutbounds($storeOutbounds));
	const subscriptions = $derived($subscriptionsStore.data ?? []);
	const proxyGroups = $derived($singboxProxies.data ?? []);

	// ── CRUD-модалы (переиспользуем CompositeOutboundEditModal) ──────────
	let addOpen = $state(false);
	let editTag = $state<string | null>(null);
	const editTarget = $derived<SingboxRouterOutbound | undefined>(
		editTag !== null ? $storeOutbounds.find((o) => o.tag === editTag) : undefined,
	);

	async function handleAddSave(o: SingboxRouterOutbound): Promise<void> {
		await api.singboxRouterAddOutbound(o);
		addOpen = false;
		await singboxRouter.loadAll();
	}

	async function handleEditSave(o: SingboxRouterOutbound): Promise<void> {
		if (editTag !== null) {
			await api.singboxRouterUpdateOutbound(editTag, o);
		}
		editTag = null;
		await singboxRouter.loadAll();
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
			await api.singboxRouterDeleteOutbound(pendingDelete.tag);
			await singboxRouter.loadAll();
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

	async function handleTest(tag: string): Promise<void> {
		if (testingTag) return;
		testingTag = tag;
		try {
			const res = await api.singboxRouterTestProxy({ group: tag });
			testResults = { ...testResults, [tag]: res.delays };
			// Refresh the live snapshot so `now` / lastDelay reflect the probe.
			await singboxProxies.refetch();
		} catch (e) {
			notifications.error(`Тест не удался: ${e instanceof Error ? e.message : String(e)}`);
		} finally {
			testingTag = null;
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
	<header class="tab-head">
		<div class="head-text">
			<h2 class="head-title">Outbounds</h2>
			<p class="head-sub">
				Направления трафика: прямой выход и composite-группы
				(selector / urltest / loadbalance).
			</p>
		</div>
		<Button variant="primary" size="sm" onclick={() => (addOpen = true)}>
			{#snippet iconBefore()}
				<Plus size={14} aria-hidden="true" />
			{/snippet}
			Outbound
		</Button>
	</header>

	<div class="section">
		<div class="section-head">
			<h3 class="section-title">Атомарные</h3>
			<span class="section-count">{partitioned.atomic.length}</span>
		</div>
		{#if partitioned.atomic.length === 0}
			<p class="section-empty">Нет атомарных outbounds.</p>
		{:else}
			<div class="cards">
				{#each partitioned.atomic as o (o.tag)}
					<AtomicOutboundCard
						outbound={o}
						{subscriptions}
						onEdit={(tag) => (editTag = tag)}
						onDelete={requestDelete}
					/>
				{/each}
			</div>
		{/if}
	</div>

	<div class="section">
		<div class="section-head">
			<h3 class="section-title">Composite-группы</h3>
			<span class="section-count">{partitioned.composite.length}</span>
		</div>
		{#if partitioned.composite.length === 0}
			<p class="section-empty">Composite-группы не настроены.</p>
		{:else}
			<div class="cards">
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
		gap: 1.25rem;
	}

	.tab-head {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 1rem;
	}

	.head-title {
		margin: 0;
		font-size: 1.0625rem;
		font-weight: 600;
		color: var(--text-primary);
	}

	.head-sub {
		margin: 0.25rem 0 0;
		font-size: 0.8125rem;
		color: var(--text-muted);
	}

	.section {
		display: flex;
		flex-direction: column;
		gap: 0.625rem;
	}

	.section-head {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.section-title {
		margin: 0;
		font-size: 0.8125rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--text-secondary);
	}

	.section-count {
		font-size: 0.75rem;
		font-family: var(--font-mono);
		color: var(--text-muted);
	}

	.section-empty {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--text-muted);
	}

	.cards {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(18rem, 1fr));
		gap: 0.75rem;
		align-items: start;
	}
</style>
