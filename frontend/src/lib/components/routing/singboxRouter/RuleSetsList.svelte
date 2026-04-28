<script lang="ts">
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import type { SingboxRouterRuleSet } from '$lib/types';
	import type { OutboundGroup } from './outboundOptions';
	import RuleSetAddModal from './RuleSetAddModal.svelte';
	import ConfirmModal from '$lib/components/ui/ConfirmModal.svelte';

	interface Props {
		ruleSets: SingboxRouterRuleSet[];
		outboundOptions: OutboundGroup[];
		onChange: () => Promise<void> | void;
	}
	let { ruleSets, outboundOptions, onChange }: Props = $props();

	let addMode = $state(false);
	let refreshing = $state<Set<string>>(new Set());
	let deleteTag = $state<string | null>(null);
	let forceDeleteTag = $state<string | null>(null);
	let forceDeleteMessage = $state('');
	let busy = $state(false);

	async function refresh(tag: string): Promise<void> {
		const next = new Set(refreshing);
		next.add(tag);
		refreshing = next;
		try {
			await api.singboxRouterRefreshRuleSet(tag);
			await onChange();
		} catch (e) {
			notifications.error((e as Error).message);
		} finally {
			const cleaned = new Set(refreshing);
			cleaned.delete(tag);
			refreshing = cleaned;
		}
	}

	function requestDelete(tag: string): void {
		deleteTag = tag;
	}

	async function confirmDelete(): Promise<void> {
		if (deleteTag === null) return;
		const tag = deleteTag;
		busy = true;
		try {
			await api.singboxRouterDeleteRuleSet(tag, false);
			deleteTag = null;
			await onChange();
		} catch (e) {
			const msg = (e as Error).message;
			deleteTag = null;
			if (msg.includes('referenced')) {
				// Two-step: surface the reference info and ask for force-delete.
				forceDeleteMessage = msg;
				forceDeleteTag = tag;
			} else {
				notifications.error(msg);
			}
		} finally {
			busy = false;
		}
	}

	async function confirmForceDelete(): Promise<void> {
		if (forceDeleteTag === null) return;
		const tag = forceDeleteTag;
		busy = true;
		try {
			await api.singboxRouterDeleteRuleSet(tag, true);
			forceDeleteTag = null;
			forceDeleteMessage = '';
			await onChange();
		} catch (e) {
			notifications.error((e as Error).message);
		} finally {
			busy = false;
		}
	}
</script>

<div class="header">
	<div class="hint">{ruleSets.length} списков · обновляются автоматически</div>
	<button class="btn btn-primary" onclick={() => (addMode = true)}>+ Добавить rule set</button>
</div>

<div class="col-header">
	<div>Tag</div>
	<div>Type</div>
	<div>Источник</div>
	<div>Detour</div>
	<div></div>
</div>

<div class="rows">
	{#each ruleSets as rs (rs.tag)}
		<div class="row">
			<div class="tag mono">{rs.tag}</div>
			<div>
				<span class="badge badge-{rs.type}">{rs.type.toUpperCase()}</span>
			</div>
			<div class="src mono" title={rs.type === 'remote' ? rs.url : rs.path}>
				{rs.type === 'remote' ? (rs.url ?? '') : (rs.path ?? '')}
				{#if rs.format === 'source'}<span class="format-tag">[source]</span>{/if}
			</div>
			<div class="detour mono">
				{#if rs.type === 'local'}<span class="muted">—</span>{:else if rs.download_detour}{rs.download_detour}{:else}<em class="muted">автоматически</em>{/if}
			</div>
			<div class="actions">
				{#if rs.type === 'remote'}
					<button class="btn btn-sm" onclick={() => refresh(rs.tag)} disabled={refreshing.has(rs.tag)}>
						{refreshing.has(rs.tag) ? '...' : 'Обновить'}
					</button>
				{/if}
				<button class="icon-btn danger" onclick={() => requestDelete(rs.tag)} aria-label="Удалить">✕</button>
			</div>
		</div>
	{/each}
</div>

{#if ruleSets.length === 0}
	<div class="empty">Пусто. Нажмите "+ Добавить rule set" или примените пресет.</div>
{/if}

{#if addMode}
	<RuleSetAddModal
		{outboundOptions}
		onClose={() => (addMode = false)}
		onSave={async (rs) => {
			await api.singboxRouterAddRuleSet(rs);
			addMode = false;
			await onChange();
		}}
	/>
{/if}

<ConfirmModal
	open={deleteTag !== null}
	title="Удалить rule set"
	message={deleteTag !== null ? `Удалить rule set "${deleteTag}"?` : ''}
	{busy}
	onConfirm={confirmDelete}
	onClose={() => { if (!busy) deleteTag = null; }}
/>

<ConfirmModal
	open={forceDeleteTag !== null}
	title="Удалить с потерей ссылок?"
	message={forceDeleteMessage}
	secondary="Удалить всё равно? Правила, ссылающиеся на этот rule set, станут orphan."
	confirmLabel="Удалить принудительно"
	{busy}
	onConfirm={confirmForceDelete}
	onClose={() => { if (!busy) { forceDeleteTag = null; forceDeleteMessage = ''; } }}
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
		grid-template-columns: 1fr 60px 1.5fr 110px 140px;
		gap: 0.5rem;
		padding: 0.25rem 0.75rem;
		font-size: 0.65rem;
		letter-spacing: 0.5px;
		text-transform: uppercase;
		color: var(--muted-text);
	}
	.rows {
		display: grid;
		gap: 0.2rem;
	}
	.row {
		background: var(--surface-bg);
		padding: 0.5rem 0.75rem;
		border-radius: 4px;
		display: grid;
		grid-template-columns: 1fr 60px 1.5fr 110px 140px;
		gap: 0.5rem;
		align-items: center;
	}
	.mono {
		font-family: ui-monospace, monospace;
		font-size: 0.8rem;
	}
	.tag {
		color: var(--text);
	}
	.src {
		color: var(--muted-text);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.format-tag {
		color: var(--muted-text);
		font-size: 0.7rem;
		margin-left: 0.25rem;
	}
	.muted {
		color: var(--muted-text);
	}
	.detour {
		color: var(--success, #22c55e);
	}
	.detour em {
		font-style: italic;
		color: var(--muted-text);
	}
	.badge {
		padding: 0.15rem 0.45rem;
		border-radius: 3px;
		font-size: 0.7rem;
		font-weight: 600;
		color: white;
		display: inline-block;
	}
	.badge-remote {
		background: var(--accent, #3b82f6);
	}
	.badge-local {
		background: #8b5cf6;
	}
	.actions {
		display: flex;
		gap: 0.3rem;
		justify-content: flex-end;
		align-items: center;
	}
	.icon-btn {
		background: transparent;
		border: none;
		color: var(--muted-text);
		cursor: pointer;
		font-size: 0.9rem;
		padding: 0.15rem 0.35rem;
	}
	.icon-btn.danger {
		color: var(--danger, #dc2626);
	}
	.empty {
		padding: 1rem;
		text-align: center;
		color: var(--muted-text);
		font-size: 0.85rem;
	}
</style>
