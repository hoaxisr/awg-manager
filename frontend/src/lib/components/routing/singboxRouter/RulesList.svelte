<script lang="ts">
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import type { SingboxRouterRule } from '$lib/types';
	import type { OutboundGroup } from './outboundOptions';
	import RuleEditModal from './RuleEditModal.svelte';
	import ConfirmModal from '$lib/components/ui/ConfirmModal.svelte';

	interface Props {
		rules: SingboxRouterRule[];
		outboundOptions: OutboundGroup[];
		finalLabel: string;
		onChange: () => Promise<void> | void;
	}
	let { rules, outboundOptions, finalLabel, onChange }: Props = $props();

	let editIndex = $state<number | null>(null);
	let addMode = $state(false);
	let expanded = $state<Set<number>>(new Set());
	let deleteIndex = $state<number | null>(null);
	let deletingBusy = $state(false);

	function isSystem(r: SingboxRouterRule): boolean {
		return r.action === 'sniff' || r.action === 'hijack-dns';
	}

	function actionBadge(r: SingboxRouterRule): { label: string; cls: string } {
		if (isSystem(r)) return { label: 'SYSTEM', cls: 'system' };
		if (r.action === 'reject') return { label: 'REJECT', cls: 'reject' };
		return { label: 'ROUTE', cls: 'route' };
	}

	function matcherSummary(r: SingboxRouterRule): string {
		if (r.action === 'sniff') return 'sniff';
		if (r.action === 'hijack-dns') return 'hijack-dns (protocol=dns)';
		const parts: string[] = [];
		if (r.domain_suffix?.length) parts.push(`domain_suffix: ${r.domain_suffix[0]}${r.domain_suffix.length > 1 ? ` +${r.domain_suffix.length - 1}` : ''}`);
		if (r.ip_cidr?.length) parts.push(`ip_cidr: ${r.ip_cidr[0]}${r.ip_cidr.length > 1 ? ` +${r.ip_cidr.length - 1}` : ''}`);
		if (r.source_ip_cidr?.length) parts.push(`src: ${r.source_ip_cidr[0]}`);
		if (r.rule_set?.length) parts.push(`${r.rule_set.join(', ')}`);
		if (r.port?.length) parts.push(`port: ${r.port.join(',')}`);
		return parts.join(' · ') || '—';
	}

	function requestDelete(index: number): void {
		deleteIndex = index;
	}

	async function confirmDelete(): Promise<void> {
		if (deleteIndex === null) return;
		deletingBusy = true;
		try {
			await api.singboxRouterDeleteRule(deleteIndex);
			deleteIndex = null;
			await onChange();
		} catch (e) {
			notifications.error((e as Error).message);
		} finally {
			deletingBusy = false;
		}
	}

	async function moveRule(index: number, to: number): Promise<void> {
		if (to < 0 || to >= rules.length) return;
		try {
			await api.singboxRouterMoveRule(index, to);
			await onChange();
		} catch (e) {
			notifications.error((e as Error).message);
		}
	}

	function toggleExpand(i: number): void {
		const next = new Set(expanded);
		if (next.has(i)) next.delete(i);
		else next.add(i);
		expanded = next;
	}
</script>

<div class="header">
	<div class="hint">{rules.length} правил · first-match-wins</div>
	<button class="btn btn-primary" onclick={() => { addMode = true; editIndex = null; }}>+ Добавить правило</button>
</div>

<div class="col-header">
	<div></div>
	<div>#</div>
	<div>Action</div>
	<div>Matchers</div>
	<div>Outbound</div>
	<div class="center">Порядок</div>
	<div></div>
	<div></div>
</div>

<div class="rows">
	{#each rules as r, i (i)}
		{@const b = actionBadge(r)}
		{@const sys = isSystem(r)}
		{@const isOpen = expanded.has(i)}
		<div class="row" class:system={sys} class:expanded={isOpen}>
			<div class="grid">
				<div class="handle" aria-hidden="true">{sys ? '' : '⋮⋮'}</div>
				<div class="idx mono">{i}</div>
				<span class="badge badge-{b.cls}">{b.label}</span>
				{#if sys}
					<div class="matcher mono">{matcherSummary(r)}</div>
				{:else}
					<button type="button" class="matcher mono matcher-btn" onclick={() => toggleExpand(i)}>
						{matcherSummary(r)}
					</button>
				{/if}
				<div class="outbound mono">
					{#if r.action === 'route' && r.outbound}{r.outbound}{:else}—{/if}
				</div>
				<div class="order">
					{#if !sys}
						<button class="arrow" onclick={() => moveRule(i, i - 1)} disabled={i === 0 || isSystem(rules[i - 1])} aria-label="Выше">↑</button>
						<button class="arrow" onclick={() => moveRule(i, i + 1)} disabled={i === rules.length - 1} aria-label="Ниже">↓</button>
					{/if}
				</div>
				<button class="icon-btn" onclick={() => !sys && (editIndex = i)} disabled={sys} aria-label="Редактировать">✎</button>
				<button class="icon-btn danger" onclick={() => !sys && requestDelete(i)} disabled={sys} aria-label="Удалить">✕</button>
			</div>
			{#if isOpen && !sys}
				<div class="expansion">
					{#if r.domain_suffix?.length}
						<div><span class="key">domain_suffix:</span> {r.domain_suffix.join(', ')}</div>
					{/if}
					{#if r.ip_cidr?.length}
						<div><span class="key">ip_cidr:</span> {r.ip_cidr.join(', ')}</div>
					{/if}
					{#if r.source_ip_cidr?.length}
						<div><span class="key">source_ip_cidr:</span> {r.source_ip_cidr.join(', ')}</div>
					{/if}
					{#if r.rule_set?.length}
						<div><span class="key">rule_set:</span> {r.rule_set.join(', ')}</div>
					{/if}
					{#if r.port?.length}
						<div><span class="key">port:</span> {r.port.join(', ')}</div>
					{/if}
				</div>
			{/if}
		</div>
	{/each}
</div>

<div class="final-info mono">
	final: <strong>{finalLabel}</strong> — если ни одно правило не совпало
</div>

{#if addMode}
	<RuleEditModal
		{outboundOptions}
		onClose={() => (addMode = false)}
		onSave={async (rule) => {
			await api.singboxRouterAddRule(rule);
			addMode = false;
			await onChange();
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
			await onChange();
		}}
	/>
{/if}

<ConfirmModal
	open={deleteIndex !== null}
	title="Удалить правило"
	message={deleteIndex !== null ? `Удалить правило #${deleteIndex}?` : ''}
	busy={deletingBusy}
	onConfirm={confirmDelete}
	onClose={() => { if (!deletingBusy) deleteIndex = null; }}
/>

<style>
	.header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 0.75rem;
	}
	.hint {
		color: var(--muted-text);
		font-size: 0.85rem;
	}
	.col-header {
		display: grid;
		grid-template-columns: 20px 28px 82px 1fr 140px 60px 24px 24px;
		gap: 0.4rem;
		padding: 0.25rem 0.75rem;
		font-size: 0.65rem;
		letter-spacing: 0.5px;
		text-transform: uppercase;
		color: var(--muted-text);
	}
	.col-header .center {
		text-align: center;
	}
	.rows {
		display: grid;
		gap: 0.2rem;
	}
	.row {
		background: var(--surface-bg);
		padding: 0.5rem 0.75rem;
		border-radius: 4px;
	}
	.row.expanded {
		border-left: 2px solid var(--accent, #3b82f6);
	}
	.row.system {
		opacity: 0.75;
	}
	.grid {
		display: grid;
		grid-template-columns: 20px 28px 82px 1fr 140px 60px 24px 24px;
		gap: 0.4rem;
		align-items: center;
	}
	.handle {
		color: var(--muted-text);
		text-align: center;
		cursor: grab;
		font-size: 0.85rem;
	}
	.idx {
		color: var(--muted-text);
	}
	.mono {
		font-family: ui-monospace, monospace;
		font-size: 0.8rem;
	}
	.badge {
		padding: 0.15rem 0.45rem;
		border-radius: 3px;
		font-size: 0.7rem;
		font-weight: 600;
		text-align: center;
		color: white;
	}
	.badge-route {
		background: var(--accent, #3b82f6);
	}
	.badge-reject {
		background: var(--danger, #dc2626);
	}
	.badge-system {
		background: var(--muted, #64748b);
	}
	.matcher {
		color: var(--text);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.matcher-btn {
		background: transparent;
		border: none;
		padding: 0;
		text-align: left;
		cursor: pointer;
		color: inherit;
		font: inherit;
		width: 100%;
	}
	.matcher-btn:hover {
		color: var(--accent, #3b82f6);
	}
	.outbound {
		color: var(--success, #22c55e);
	}
	.order {
		display: flex;
		gap: 2px;
		justify-content: center;
	}
	.arrow {
		background: var(--bg);
		border: 1px solid var(--border);
		color: var(--muted-text);
		width: 26px;
		height: 26px;
		border-radius: 3px;
		cursor: pointer;
		padding: 0;
		font-size: 0.75rem;
	}
	.arrow:disabled {
		opacity: 0.3;
		cursor: not-allowed;
	}
	.icon-btn {
		background: transparent;
		border: none;
		color: var(--muted-text);
		cursor: pointer;
		font-size: 0.9rem;
		padding: 0.15rem;
	}
	.icon-btn:disabled {
		opacity: 0.3;
		cursor: not-allowed;
	}
	.icon-btn.danger {
		color: var(--danger, #dc2626);
	}
	.expansion {
		margin-top: 0.4rem;
		padding: 0.35rem 0.5rem 0.35rem 3.4rem;
		font-family: ui-monospace, monospace;
		font-size: 0.75rem;
		color: var(--muted-text);
		line-height: 1.6;
	}
	.expansion .key {
		color: var(--muted-text-strong, #64748b);
	}
	.final-info {
		margin-top: 0.75rem;
		padding: 0.5rem 0.75rem;
		background: var(--bg);
		border-radius: 4px;
		color: var(--muted-text);
		font-size: 0.8rem;
	}
	.final-info strong {
		color: var(--success, #22c55e);
	}
</style>
