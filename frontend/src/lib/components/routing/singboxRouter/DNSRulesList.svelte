<script lang="ts">
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import type { SingboxRouterDNSRule, SingboxRouterDNSServer } from '$lib/types';
	import DNSRuleEditModal from './DNSRuleEditModal.svelte';
	import ConfirmModal from '$lib/components/ui/ConfirmModal.svelte';

	interface Props {
		rules: SingboxRouterDNSRule[];
		servers: SingboxRouterDNSServer[];
		finalLabel: string;
		onChange: () => Promise<void> | void;
	}
	let { rules, servers, finalLabel, onChange }: Props = $props();

	let editIndex = $state<number | null>(null);
	let addMode = $state(false);
	let deleteIndex = $state<number | null>(null);
	let busy = $state(false);

	function matcherSummary(r: SingboxRouterDNSRule): string {
		const parts: string[] = [];
		if (r.rule_set?.length) parts.push(`rule_set: ${r.rule_set.join(', ')}`);
		if (r.domain_suffix?.length) parts.push(`suffix: ${r.domain_suffix[0]}${r.domain_suffix.length > 1 ? ` +${r.domain_suffix.length - 1}` : ''}`);
		if (r.domain?.length) parts.push(`domain: ${r.domain[0]}${r.domain.length > 1 ? ` +${r.domain.length - 1}` : ''}`);
		if (r.domain_keyword?.length) parts.push(`keyword: ${r.domain_keyword[0]}`);
		if (r.query_type?.length) parts.push(`type: ${r.query_type.join(',')}`);
		return parts.join(' · ') || '—';
	}

	function actionBadge(r: SingboxRouterDNSRule): { label: string; cls: string } {
		if (r.action === 'reject') return { label: 'REJECT', cls: 'reject' };
		return { label: 'RESOLVE', cls: 'route' };
	}

	function requestDelete(index: number): void {
		deleteIndex = index;
	}

	async function confirmDelete(): Promise<void> {
		if (deleteIndex === null) return;
		busy = true;
		try {
			await api.singboxRouterDeleteDNSRule(deleteIndex);
			deleteIndex = null;
			await onChange();
		} catch (e) {
			notifications.error((e as Error).message);
		} finally {
			busy = false;
		}
	}

	async function moveRule(index: number, to: number): Promise<void> {
		if (to < 0 || to >= rules.length) return;
		try {
			await api.singboxRouterMoveDNSRule(index, to);
			await onChange();
		} catch (e) {
			notifications.error((e as Error).message);
		}
	}
</script>

<div class="header">
	<div class="hint">{rules.length} правил · first-match-wins</div>
	<button class="btn btn-primary" onclick={() => { addMode = true; editIndex = null; }} disabled={servers.length === 0}>
		+ Добавить правило
	</button>
</div>

{#if servers.length === 0}
	<div class="empty">
		Сначала добавьте хотя бы один DNS сервер — правила ссылаются на tag сервера.
	</div>
{:else if rules.length === 0}
	<div class="empty-mild">
		Правил нет. Все запросы идут на <code>final: {finalLabel || '—'}</code>.
	</div>
{:else}
	<div class="col-header">
		<div>#</div>
		<div>Action</div>
		<div>Matchers</div>
		<div>Server</div>
		<div class="center">Порядок</div>
		<div></div>
		<div></div>
	</div>

	<div class="rows">
		{#each rules as r, i (i)}
			{@const b = actionBadge(r)}
			<div class="row">
				<div class="idx mono">{i}</div>
				<span class="badge badge-{b.cls}">{b.label}</span>
				<div class="matcher mono">{matcherSummary(r)}</div>
				<div class="server mono">{r.server || (r.action === 'reject' ? '—' : '?')}</div>
				<div class="order">
					<button class="arrow" onclick={() => moveRule(i, i - 1)} disabled={i === 0} aria-label="Выше">↑</button>
					<button class="arrow" onclick={() => moveRule(i, i + 1)} disabled={i === rules.length - 1} aria-label="Ниже">↓</button>
				</div>
				<button class="icon-btn" onclick={() => (editIndex = i)} aria-label="Редактировать">✎</button>
				<button class="icon-btn danger" onclick={() => requestDelete(i)} aria-label="Удалить">✕</button>
			</div>
		{/each}
	</div>
{/if}

<div class="final-info mono">
	final: <strong>{finalLabel || '—'}</strong> — если ни одно правило не совпало
</div>

{#if addMode}
	<DNSRuleEditModal
		{servers}
		onClose={() => (addMode = false)}
		onSave={async (rule) => {
			await api.singboxRouterAddDNSRule(rule);
			addMode = false;
			await onChange();
		}}
	/>
{/if}

{#if editIndex !== null}
	{@const idx = editIndex}
	<DNSRuleEditModal
		rule={rules[idx]}
		{servers}
		onClose={() => (editIndex = null)}
		onSave={async (rule) => {
			await api.singboxRouterUpdateDNSRule(idx, rule);
			editIndex = null;
			await onChange();
		}}
	/>
{/if}

<ConfirmModal
	open={deleteIndex !== null}
	title="Удалить DNS правило"
	message={deleteIndex !== null ? `Удалить DNS правило #${deleteIndex}?` : ''}
	{busy}
	onConfirm={confirmDelete}
	onClose={() => { if (!busy) deleteIndex = null; }}
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
	.empty {
		padding: 0.75rem 0.9rem;
		background: rgba(224, 175, 104, 0.12);
		border-left: 3px solid var(--warning, #e0af68);
		border-radius: 4px;
		color: var(--muted-text);
		font-size: 0.85rem;
		line-height: 1.5;
	}
	.empty-mild {
		padding: 0.6rem 0.9rem;
		background: var(--surface-bg);
		border-radius: 4px;
		color: var(--muted-text);
		font-size: 0.85rem;
	}
	.empty-mild code {
		background: var(--bg);
		padding: 0.05rem 0.25rem;
		border-radius: 2px;
		font-family: ui-monospace, monospace;
		color: var(--text);
	}
	.col-header {
		display: grid;
		grid-template-columns: 28px 82px 1fr 140px 60px 24px 24px;
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
		display: grid;
		grid-template-columns: 28px 82px 1fr 140px 60px 24px 24px;
		gap: 0.4rem;
		align-items: center;
		background: var(--surface-bg);
		padding: 0.5rem 0.75rem;
		border-radius: 4px;
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
	.badge-route { background: var(--accent, #3b82f6); }
	.badge-reject { background: var(--danger, #dc2626); }
	.matcher {
		color: var(--text);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.server {
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
	.icon-btn.danger {
		color: var(--danger, #dc2626);
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
