<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { singboxTunnels } from '$lib/stores/singbox';
	import {
		Button,
		IconButton,
		Badge,
		StatRow,
		ConfirmModal,
	} from '$lib/components/ui';
	import type { StatTile } from '$lib/components/ui';
	import type { AWGTagInfo, SingboxRouterRule, SingboxTunnel } from '$lib/types';
	import {
		buildOutboundOptions,
		RuleEditModal,
	} from '$lib/components/routing/singboxRouter';

	const statusStore = singboxRouter.status;
	const rulesStore = singboxRouter.rules;
	const outboundsStore = singboxRouter.outbounds;
	const phase1Store = singboxTunnels;

	const status = $derived($statusStore);
	const rules = $derived($rulesStore);
	const outbounds = $derived($outboundsStore);
	const phase1Tunnels = $derived(($phase1Store.data ?? []) as SingboxTunnel[]);

	let awgTags = $state<AWGTagInfo[]>([]);

	async function loadAWGTags(): Promise<void> {
		try {
			awgTags = await api.getAWGTags();
		} catch {
			awgTags = [];
		}
	}

	async function refresh(): Promise<void> {
		await singboxRouter.loadAll();
	}

	onMount(() => {
		loadAWGTags();
	});

	const outboundOptions = $derived(
		buildOutboundOptions(awgTags, phase1Tunnels, outbounds, true),
	);

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
	let deleteIndex = $state<number | null>(null);
	let deleteSnapshot = $state<SingboxRouterRule | null>(null);
	let deletingBusy = $state(false);

	function ruleKey(r: SingboxRouterRule): string {
		return JSON.stringify({
			action: r.action,
			outbound: r.outbound ?? '',
			protocol: r.protocol ?? '',
			domain_suffix: r.domain_suffix ?? [],
			ip_cidr: r.ip_cidr ?? [],
			source_ip_cidr: r.source_ip_cidr ?? [],
			port: r.port ?? [],
			rule_set: r.rule_set ?? [],
		});
	}

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
		deleteSnapshot = rules[i] ? { ...rules[i] } : null;
	}

	async function confirmDelete(): Promise<void> {
		if (deleteIndex === null) return;
		deletingBusy = true;
		try {
			await api.singboxRouterDeleteRule(deleteIndex);
			deleteIndex = null;
			deleteSnapshot = null;
			await refresh();
		} catch (e) {
			// Rules can shift (concurrent edits / system rule reconciliation).
			// Retry once by matching the captured rule snapshot.
			if (deleteSnapshot) {
				try {
					await refresh();
					const targetKey = ruleKey(deleteSnapshot);
					const fallbackIndex = rules.findIndex((r) => ruleKey(r) === targetKey);
					if (fallbackIndex >= 0) {
						await api.singboxRouterDeleteRule(fallbackIndex);
						deleteIndex = null;
						deleteSnapshot = null;
						await refresh();
						return;
					}
				} catch {
					// fall through to notify original error below
				}
			}
			notifications.error((e as Error).message);
		} finally {
			deletingBusy = false;
		}
	}

	function canMoveRule(index: number, direction: -1 | 1): boolean {
		const current = rules[index];
		const target = rules[index + direction];
		if (!current || !target) return false;
		if (isSystem(current) || isSystem(target)) return false;
		return true;
	}

	async function moveRule(index: number, to: number): Promise<void> {
		if (to < 0 || to >= rules.length) return;
		try {
			await api.singboxRouterMoveRule(index, to);
			await refresh();
		} catch (e) {
			notifications.error((e as Error).message);
		}
	}
</script>

{#if status}
	<div class="stat-row-wrap">
		<StatRow tiles={statTiles} columns={3} />
	</div>

	<div class="action-row">
		<div class="hint">first-match-wins · final: <strong>{finalLabel}</strong></div>
		<div class="actions">
			<Button variant="primary" size="sm" onclick={openAdd}>+ Правило</Button>
		</div>
	</div>

	<div class="table">
		<div class="t-head">
			<div class="col-idx">#</div>
			<div class="col-action">Действие</div>
			<div class="col-match">Matchers</div>
			<div class="col-out">Outbound</div>
			<div class="col-order">Порядок</div>
			<div class="col-edit"></div>
		</div>

		{#if rules.length === 0}
			<div class="empty">Нет правил</div>
		{:else}
			{#each rules as r, i (i)}
				{@const sys = isSystem(r)}
				<div class="t-row" class:system={sys}>
					<div class="col-idx mono">{i}</div>
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
					<div class="order">
						<button
							class="arrow"
							onclick={() => moveRule(i, i - 1)}
							disabled={!canMoveRule(i, -1)}
							aria-label="Выше"
							type="button">↑</button
						>
						<button
							class="arrow"
							onclick={() => moveRule(i, i + 1)}
							disabled={!canMoveRule(i, 1)}
							aria-label="Ниже"
							type="button">↓</button
						>
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
		{outboundOptions}
		onClose={() => (addMode = false)}
		onSave={async (rule) => {
			await api.singboxRouterAddRule(rule);
			addMode = false;
			await refresh();
		}}
	/>
{/if}

{#if editIndex !== null}
	{@const idx = editIndex}
	<RuleEditModal
		rule={rules[idx]}
		{outboundOptions}
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
		if (!deletingBusy) {
			deleteIndex = null;
			deleteSnapshot = null;
		}
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
		grid-template-columns: 36px 80px 1fr 160px 64px 72px;
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
	.col-order,
	.order {
		display: flex;
		gap: 0.25rem;
		justify-content: center;
	}
	.arrow {
		background: var(--color-bg);
		border: 1px solid var(--color-border);
		color: var(--color-text-muted);
		width: 26px;
		height: 26px;
		border-radius: 6px;
		cursor: pointer;
		font-size: 0.75rem;
	}
	.arrow:disabled {
		opacity: 0.3;
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
			grid-template-columns: 28px 70px 1fr 60px 64px;
		}
		.col-out,
		.t-head > :nth-child(4),
		.t-row > :nth-child(4) {
			display: none;
		}
	}
</style>
