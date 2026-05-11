<script lang="ts">
	import { untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/stores';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import {
		Button,
		IconButton,
		Badge,
		StatRow,
		ConfirmModal,
	} from '$lib/components/ui';
	import type { StatTile } from '$lib/components/ui';
	import type { SingboxRouterRule, SingboxRouterRuleSet } from '$lib/types';
	import { RuleEditModal, RouteGlobals } from '$lib/components/routing/singboxRouter';

	const statusStore = singboxRouter.status;
	const rulesStore = singboxRouter.rules;
	const ruleSetsStore = singboxRouter.ruleSets;
	const optionsStore = singboxRouter.options;

	const status = $derived($statusStore);
	const rules = $derived($rulesStore);
	const ruleSets = $derived<SingboxRouterRuleSet[]>($ruleSetsStore);
	const outboundOptions = $derived($optionsStore);

	async function refresh(): Promise<void> {
		await singboxRouter.loadAll();
	}

	// Deep-link: ?newRuleSet=<tag> opens add-modal with that tag preselected.
	// Triggered by the "+ Правило" button on RuleSetsSubTab cards via
	// goto(?sub=rules&newRuleSet=<tag>). Cleans the URL after consumption so
	// the param doesn't linger or re-trigger on re-renders.
	$effect(() => {
		const t = $page.url.searchParams.get('newRuleSet');
		if (!t) return;
		if (untrack(() => addMode || editIndex !== null)) return;
		prefillRuleSet = t;
		addMode = true;
		const sp = new URLSearchParams($page.url.search);
		sp.delete('newRuleSet');
		const url = $page.url.pathname + (sp.toString() ? '?' + sp : '') + $page.url.hash;
		void goto(url, { replaceState: true, keepFocus: true, noScroll: true });
	});

	const finalLabel = $derived(status?.final || 'direct');

	function isSystem(r: SingboxRouterRule): boolean {
		return r.action === 'sniff' || r.action === 'hijack-dns';
	}

	const userRules = $derived(rules.filter((r) => !isSystem(r)));
	const rejectCount = $derived(
		rules.filter((r) => r.action === 'reject').length,
	);

	const statTiles = $derived<StatTile[]>([
		{ label: 'Правил', value: rules.length },
		{ label: 'Активных', value: userRules.length },
		{
			label: 'Reject',
			value: rejectCount,
			accent: rejectCount > 0 ? 'error' : 'default',
		},
	]);

	const firstUserRuleIndex = $derived.by(() => {
		const idx = rules.findIndex((r) => !isSystem(r));
		return idx === -1 ? rules.length : idx;
	});

	async function moveRule(from: number, to: number): Promise<void> {
		if (movingIndex !== null) return;
		if (from < firstUserRuleIndex || to < firstUserRuleIndex) return;
		if (from < 0 || from >= rules.length || to < 0 || to >= rules.length) return;
		if (from === to) return;

		movingIndex = from;
		try {
			await api.singboxRouterMoveRule(from, to);
			await refresh();
		} catch (e) {
			notifications.error((e as Error).message);
		} finally {
			movingIndex = null;
		}
	}

	function onDragStart(i: number, e: DragEvent): void {
		if (isSystem(rules[i])) return;
		if (i < firstUserRuleIndex) return;
		draggingIndex = i;
		e.dataTransfer?.setData('text/plain', String(i));
		if (e.dataTransfer) {
			e.dataTransfer.effectAllowed = 'move';
		}
	}

	function onDragOver(i: number, e: DragEvent): void {
		if (draggingIndex === null) return;
		if (i < firstUserRuleIndex) return;
		e.preventDefault();
		dragOverIndex = i;
		if (e.dataTransfer) {
			e.dataTransfer.dropEffect = 'move';
		}
	}

	async function onDrop(i: number, e: DragEvent): Promise<void> {
		e.preventDefault();
		const from = draggingIndex;
		draggingIndex = null;
		dragOverIndex = null;
		if (from === null) return;
		if (i < firstUserRuleIndex) return;
		await moveRule(from, i);
	}

	function onDragEnd(): void {
		draggingIndex = null;
		dragOverIndex = null;
	}

	function actionVariant(
		r: SingboxRouterRule,
	): 'success' | 'error' | 'muted' {
		if (isSystem(r)) return 'muted';
		if (r.action === 'reject') return 'error';
		return 'success';
	}

	function actionLabel(r: SingboxRouterRule): string {
		if (r.action === 'sniff') return 'SNIFF';
		if (r.action === 'hijack-dns') return 'HIJACK';
		if (r.action === 'reject') return 'REJECT';
		return 'ROUTE';
	}

	function matcherSummary(r: SingboxRouterRule): string {
		if (r.action === 'sniff') return 'sniff';
		if (r.action === 'hijack-dns') return 'hijack-dns (protocol=dns)';
		const parts: string[] = [];
		if (r.domain_suffix?.length) {
			const more =
				r.domain_suffix.length > 1 ? ` +${r.domain_suffix.length - 1}` : '';
			parts.push(`domain: ${r.domain_suffix[0]}${more}`);
		}
		if (r.ip_cidr?.length) {
			const more = r.ip_cidr.length > 1 ? ` +${r.ip_cidr.length - 1}` : '';
			parts.push(`ip: ${r.ip_cidr[0]}${more}`);
		}
		if (r.source_ip_cidr?.length) parts.push(`src: ${r.source_ip_cidr[0]}`);
		if (r.rule_set?.length) parts.push(`set: ${r.rule_set.join(', ')}`);
		if (r.port?.length) parts.push(`port: ${r.port.join(',')}`);
		return parts.join(' · ') || '—';
	}

	let editIndex = $state<number | null>(null);
	let addMode = $state(false);
	let prefillRuleSet = $state<string | null>(null);
	let deleteIndex = $state<number | null>(null);
	let deletingBusy = $state(false);
	let movingIndex = $state<number | null>(null);
	let draggingIndex = $state<number | null>(null);
	let dragOverIndex = $state<number | null>(null);

	function openAdd(): void {
		editIndex = null;
		addMode = true;
	}

	function openEdit(i: number): void {
		if (isSystem(rules[i])) return;
		addMode = false;
		editIndex = i;
	}

	function requestDelete(i: number): void {
		deleteIndex = i;
	}

	async function confirmDelete(): Promise<void> {
		if (deleteIndex === null) return;
		deletingBusy = true;
		try {
			await api.singboxRouterDeleteRule(deleteIndex);
			deleteIndex = null;
			await refresh();
		} catch (e) {
			notifications.error((e as Error).message);
		} finally {
			deletingBusy = false;
		}
	}
</script>

{#if status}
	<RouteGlobals
		currentFinal={status.final}
		{outboundOptions}
		onChange={refresh}
	/>

	<div class="stat-row-wrap">
		<StatRow tiles={statTiles} columns={3} />
	</div>

	<div class="action-row">
		<div class="hint">first-match-wins · final: <strong>{finalLabel}</strong></div>
		<div class="actions">
			<Button variant="primary" size="sm" onclick={openAdd}>+ Правило</Button>
		</div>
	</div>

	<div class="table" role="list">
		<div class="t-head">
			<div class="col-idx">#</div>
			<div class="col-order">Порядок</div>
			<div class="col-action">Действие</div>
			<div class="col-match">Matchers</div>
			<div class="col-out">Outbound</div>
			<div class="col-edit"></div>
		</div>

		{#if rules.length === 0}
			<div class="empty">Нет правил</div>
		{:else}
			{#each rules as r, i (i)}
				{@const sys = isSystem(r)}
				<div
					class="t-row"
					class:system={sys}
					class:dragging={draggingIndex === i}
					class:drag-over={dragOverIndex === i && draggingIndex !== i}
					role="listitem"
					tabindex="-1"
					ondragover={(e) => onDragOver(i, e)}
					ondrop={(e) => onDrop(i, e)}
					ondragend={onDragEnd}
				>
					<div class="col-idx mono">{i}</div>
					<div class="col-order">
						{#if !sys}
							<span
								class="drag-handle"
								title="Перетащить"
								role="button"
								aria-label={`Перетащить правило #${i}`}
								tabindex="-1"
								draggable={movingIndex === null}
								ondragstart={(e) => onDragStart(i, e)}
								ondragend={onDragEnd}
							>⋮⋮</span>
							<button
								class="move-btn"
								title="Выше"
								disabled={movingIndex !== null || i <= firstUserRuleIndex}
								onclick={() => moveRule(i, i - 1)}
							>↑</button>
							<button
								class="move-btn"
								title="Ниже"
								disabled={movingIndex !== null || i >= rules.length - 1}
								onclick={() => moveRule(i, i + 1)}
							>↓</button>
						{/if}
					</div>
					<div class="col-action">
						<Badge variant={actionVariant(r)} size="sm" uppercase mono>
							{actionLabel(r)}
						</Badge>
					</div>
					<div class="col-match mono" title={matcherSummary(r)}>
						{matcherSummary(r)}
					</div>
					<div class="col-out mono">
						{#if r.action === 'route' && r.outbound}
							<Badge variant="accent" size="sm" mono>{r.outbound}</Badge>
						{:else}
							<span class="dim">—</span>
						{/if}
					</div>
					<div class="col-edit">
						{#if !sys}
							<IconButton
								ariaLabel="Редактировать"
								size="sm"
								onclick={() => openEdit(i)}
							>
								<svg
									viewBox="0 0 24 24"
									fill="none"
									stroke="currentColor"
									stroke-width="2"
									stroke-linecap="round"
									stroke-linejoin="round"
									aria-hidden="true"
								>
									<path
										d="M12 20h9M16.5 3.5a2.121 2.121 0 0 1 3 3L7 19l-4 1 1-4 12.5-12.5z"
									/>
								</svg>
							</IconButton>
							<IconButton
								ariaLabel="Удалить"
								size="sm"
								variant="danger"
								onclick={() => requestDelete(i)}
							>
								<svg
									viewBox="0 0 24 24"
									fill="none"
									stroke="currentColor"
									stroke-width="2"
									stroke-linecap="round"
									stroke-linejoin="round"
									aria-hidden="true"
								>
									<path d="M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"/>
								</svg>
							</IconButton>
						{/if}
					</div>
				</div>
			{/each}
		{/if}
	</div>
{:else}
	<div class="loading">Загрузка…</div>
{/if}

{#if addMode}
	<RuleEditModal
		initialRuleSetTags={prefillRuleSet ? [prefillRuleSet] : undefined}
		{outboundOptions}
		availableRuleSets={ruleSets}
		onClose={() => { addMode = false; prefillRuleSet = null; }}
		onSave={async (rule) => {
			await api.singboxRouterAddRule(rule);
			addMode = false;
			prefillRuleSet = null;
			await refresh();
		}}
	/>
{/if}

{#if editIndex !== null}
	{@const idx = editIndex}
	<RuleEditModal
		rule={rules[idx]}
		{outboundOptions}
		availableRuleSets={ruleSets}
		onClose={() => (editIndex = null)}
		onSave={async (rule) => {
			await api.singboxRouterUpdateRule(idx, rule);
			editIndex = null;
			await refresh();
		}}
	/>
{/if}

<ConfirmModal
	open={deleteIndex !== null}
	title="Удалить правило"
	message={deleteIndex !== null ? `Удалить правило #${deleteIndex}?` : ''}
	busy={deletingBusy}
	onConfirm={confirmDelete}
	onClose={() => {
		if (!deletingBusy) deleteIndex = null;
	}}
/>

<style>
	.stat-row-wrap {
		margin-bottom: 1rem;
	}
	.action-row {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 0.75rem;
		margin-bottom: 0.625rem;
		flex-wrap: wrap;
	}
	.hint {
		font-size: 0.8rem;
		color: var(--color-text-muted);
	}
	.hint strong {
		color: var(--color-success);
		font-family: var(--font-mono, ui-monospace, monospace);
		font-weight: 600;
	}
	.actions {
		display: flex;
		gap: 0.5rem;
	}
	.table {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		overflow: hidden;
		background: var(--color-bg-secondary);
	}
	.t-head,
	.t-row {
		display: grid;
		grid-template-columns: 36px 64px 80px 1fr 160px 72px;
		gap: 0.625rem;
		align-items: center;
		padding: 0.5rem 0.875rem;
	}
	.t-head {
		background: var(--color-bg-tertiary);
		border-bottom: 1px solid var(--color-border);
		font-size: 0.6875rem;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
		padding: 0.45rem 0.875rem;
	}
	.t-row {
		border-bottom: 1px solid var(--color-border);
	}
	.t-row:last-child {
		border-bottom: none;
	}
	.t-row.system {
		opacity: 0.7;
	}
	.drag-handle {
		cursor: grab;
		color: var(--color-text-muted);
		user-select: none;
		line-height: 1;
		padding: 0 0.125rem;
	}
	.drag-handle:active {
		cursor: grabbing;
	}
	.col-order {
		display: flex;
		gap: 0.25rem;
		justify-content: center;
		align-items: center;
	}
	.t-row.dragging {
		opacity: 0.45;
	}
	.t-row.drag-over {
		outline: 1px dashed var(--color-accent);
		outline-offset: -2px;
		background: var(--color-bg-tertiary);
	}
	.col-idx {
		color: var(--color-text-muted);
	}
	.col-match {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		color: var(--color-text-secondary);
	}
	.col-edit {
		display: flex;
		gap: 0.25rem;
		justify-content: flex-end;
	}
	.move-btn {
		width: 1.5rem;
		height: 1.5rem;
		border: 1px solid var(--color-border);
		border-radius: 0.375rem;
		background: var(--color-bg-tertiary);
		color: var(--color-text-secondary);
		cursor: pointer;
		font-size: 0.75rem;
		line-height: 1;
	}
	.move-btn:hover:not(:disabled) {
		border-color: var(--color-accent);
		color: var(--color-text-primary);
	}
	.move-btn:disabled {
		opacity: 0.4;
		cursor: not-allowed;
	}
	.mono {
		font-family: var(--font-mono, ui-monospace, monospace);
		font-size: 0.8rem;
	}
	.dim {
		color: var(--color-text-muted);
	}
	.empty {
		padding: 1.5rem;
		text-align: center;
		color: var(--color-text-muted);
		font-size: 0.875rem;
	}
	.loading {
		padding: 2rem;
		text-align: center;
		color: var(--color-text-secondary);
	}
	@media (max-width: 720px) {
		.t-head,
		.t-row {
			grid-template-columns: 28px 52px 70px 1fr 60px;
		}
		.col-out {
			display: none;
		}
	}
</style>
