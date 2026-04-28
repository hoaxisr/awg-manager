<script lang="ts">
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import type { SingboxRouterOutbound } from '$lib/types';
	import type { OutboundGroup } from './outboundOptions';
	import CompositeOutboundEditModal from './CompositeOutboundEditModal.svelte';
	import ConfirmModal from '$lib/components/ui/ConfirmModal.svelte';

	interface Props {
		outbounds: SingboxRouterOutbound[];
		outboundOptions: OutboundGroup[];
		onChange: () => Promise<void> | void;
	}
	let { outbounds, outboundOptions, onChange }: Props = $props();

	let addMode = $state(false);
	let editTag = $state<string | null>(null);
	let deleteTag = $state<string | null>(null);
	let forceDeleteTag = $state<string | null>(null);
	let forceDeleteMessage = $state('');
	let busy = $state(false);

	function badgeCls(type: string): string {
		return `badge badge-${type}`;
	}

	function requestDelete(tag: string): void {
		deleteTag = tag;
	}

	async function confirmDelete(): Promise<void> {
		if (deleteTag === null) return;
		const tag = deleteTag;
		busy = true;
		try {
			await api.singboxRouterDeleteOutbound(tag, false);
			deleteTag = null;
			await onChange();
		} catch (e) {
			const msg = (e as Error).message;
			deleteTag = null;
			if (msg.includes('referenced')) {
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
			await api.singboxRouterDeleteOutbound(tag, true);
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
	<div class="hint">{outbounds.length} composite outbound'ов</div>
	<button class="btn btn-primary" onclick={() => (addMode = true)}>+ Создать outbound</button>
</div>

<div class="cards">
	{#each outbounds as o (o.tag)}
		<div class="card">
			<div class="card-header">
				<div class="head-left">
					<span class={badgeCls(o.type)}>{o.type.toUpperCase()}</span>
					<span class="tag mono">{o.tag}</span>
				</div>
				<div class="card-actions">
					<button class="icon-btn" onclick={() => (editTag = o.tag)} aria-label="Редактировать">✎</button>
					<button class="icon-btn danger" onclick={() => requestDelete(o.tag)} aria-label="Удалить">✕</button>
				</div>
			</div>
			<div class="card-body">
				<div class="detail">
					<div class="key">Members:</div>
					<div class="members">
						{#each o.outbounds ?? [] as m}
							<span class="chip mono">{m}</span>
						{/each}
					</div>
				</div>
				{#if o.type === 'urltest'}
					<div class="detail">
						<div class="key">Test URL:</div>
						<div class="mono">{o.url}</div>
					</div>
					<div class="detail">
						<div class="key">Interval:</div>
						<div class="mono">{o.interval} · tolerance {o.tolerance}ms</div>
					</div>
				{:else if o.type === 'selector'}
					<div class="detail">
						<div class="key">Default:</div>
						<div class="mono default">{o.default}</div>
					</div>
				{:else if o.type === 'loadbalance'}
					<div class="detail">
						<div class="key">Strategy:</div>
						<div class="mono">{o.strategy}</div>
					</div>
				{/if}
			</div>
		</div>
	{/each}
</div>

{#if outbounds.length === 0}
	<div class="empty">Composite outbound'ов пока нет. Создайте URLTest для автовыбора быстрейшего из набора туннелей.</div>
{/if}

{#if addMode}
	<CompositeOutboundEditModal
		{outboundOptions}
		onClose={() => (addMode = false)}
		onSave={async (o) => {
			await api.singboxRouterAddOutbound(o);
			addMode = false;
			await onChange();
		}}
	/>
{/if}

{#if editTag !== null}
	{@const current = outbounds.find((x) => x.tag === editTag)}
	{#if current}
		<CompositeOutboundEditModal
			outbound={current}
			{outboundOptions}
			onClose={() => (editTag = null)}
			onSave={async (o) => {
				await api.singboxRouterUpdateOutbound(editTag!, o);
				editTag = null;
				await onChange();
			}}
		/>
	{/if}
{/if}

<ConfirmModal
	open={deleteTag !== null}
	title="Удалить outbound"
	message={deleteTag !== null ? `Удалить outbound "${deleteTag}"?` : ''}
	{busy}
	onConfirm={confirmDelete}
	onClose={() => { if (!busy) deleteTag = null; }}
/>

<ConfirmModal
	open={forceDeleteTag !== null}
	title="Удалить с потерей ссылок?"
	message={forceDeleteMessage}
	secondary="Удалить всё равно? Правила, ссылающиеся на этот outbound, станут orphan."
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
	.cards {
		display: grid;
		gap: 0.5rem;
	}
	.card {
		background: var(--surface-bg);
		padding: 0.85rem 1rem;
		border-radius: 6px;
	}
	.card-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 0.5rem;
	}
	.head-left {
		display: flex;
		gap: 0.5rem;
		align-items: center;
	}
	.tag {
		font-weight: 600;
	}
	.mono {
		font-family: ui-monospace, monospace;
		font-size: 0.85rem;
	}
	.badge {
		padding: 0.2rem 0.5rem;
		border-radius: 3px;
		font-size: 0.7rem;
		font-weight: 600;
		color: white;
	}
	.badge-urltest {
		background: #f59e0b;
	}
	.badge-selector {
		background: #a855f7;
	}
	.badge-loadbalance {
		background: #ec4899;
	}
	.card-actions {
		display: flex;
		gap: 0.25rem;
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
	.card-body {
		display: grid;
		gap: 0.3rem;
		font-size: 0.8rem;
	}
	.detail {
		display: grid;
		grid-template-columns: 90px 1fr;
		gap: 0.5rem;
		align-items: start;
	}
	.key {
		color: var(--muted-text);
	}
	.members {
		display: flex;
		gap: 0.25rem;
		flex-wrap: wrap;
	}
	.chip {
		background: var(--bg);
		padding: 0.15rem 0.5rem;
		border-radius: 3px;
	}
	.default {
		color: var(--success, #22c55e);
	}
	.empty {
		padding: 1rem;
		text-align: center;
		color: var(--muted-text);
		font-size: 0.85rem;
	}
</style>
