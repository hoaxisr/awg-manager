<script lang="ts">
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import type { SingboxRouterOutbound, SingboxProxyGroup } from '$lib/types';
	import type { OutboundGroup } from './outboundOptions';
	import CompositeOutboundEditModal from './CompositeOutboundEditModal.svelte';
	import ConfirmModal from '$lib/components/ui/ConfirmModal.svelte';
	import { LatencySparkline } from '$lib/components/ui';
	import Button from '$lib/components/ui/Button.svelte';
	import { latencyTier } from '$lib/utils/latencyTier';

	interface Props {
		outbounds: SingboxRouterOutbound[];
		outboundOptions: OutboundGroup[];
		onChange: () => Promise<void> | void;
		proxies?: SingboxProxyGroup[];
		latencyHistory?: Map<string, number[]>;
	}
	let {
		outbounds,
		outboundOptions,
		onChange,
		proxies = [],
		latencyHistory = new Map(),
	}: Props = $props();

	let addMode = $state(false);
	let editTag = $state<string | null>(null);
	let deleteTag = $state<string | null>(null);
	let forceDeleteTag = $state<string | null>(null);
	let forceDeleteMessage = $state('');
	let busy = $state(false);

	let expanded = $state<Record<string, boolean>>({});
	let testingGroup = $state<string | null>(null);

	function badgeCls(type: string): string {
		return `badge badge-${type}`;
	}

	function toggleExpand(tag: string): void {
		expanded[tag] = !expanded[tag];
	}

	function liveGroup(tag: string): SingboxProxyGroup | null {
		return proxies.find((g) => g.tag === tag) ?? null;
	}

	function delayClass(d: number): string {
		switch (latencyTier(d)) {
			case 'success': return 'delay-good';
			case 'warning': return 'delay-warn';
			case 'error':   return 'delay-bad';
			default:        return 'delay-muted';
		}
	}

	async function testGroup(group: string): Promise<void> {
		testingGroup = group;
		try {
			await api.singboxRouterTestProxy({ group });
			// Polling will pick up new delays within 5s; no client-side merge.
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : String(e));
		} finally {
			testingGroup = null;
		}
	}

	async function selectMember(group: string, member: string): Promise<void> {
		try {
			await api.singboxRouterSelectProxy({ group, member });
			// Optimistic update: mutate the in-memory `now` so UI reflects
			// the change before the next polling tick.
			const g = proxies.find((p) => p.tag === group);
			if (g) g.now = member;
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : String(e));
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
	<Button variant="primary" size="sm" onclick={() => (addMode = true)}>
		+ Создать outbound
	</Button>
</div>

<div class="cards">
	{#each outbounds as o (o.tag)}
		{@const live = liveGroup(o.tag)}
		{@const isOpen = !!expanded[o.tag]}
		<div class="card">
			<div class="card-header">
				<button
					class="row-toggle"
					type="button"
					onclick={() => toggleExpand(o.tag)}
					aria-expanded={isOpen}
				>
					<span class="chevron" class:open={isOpen}>›</span>
					<span class={badgeCls(o.type)}>{o.type.toUpperCase()}</span>
					<span class="tag mono">{o.tag}</span>
					{#if o.source === 'subscription'}
						<span class="badge-managed" title="Управляется подпиской — редактирование заблокировано">из подписки</span>
					{/if}
					{#if live}
						<span class="now">now: <span class="now-tag mono">{live.now}</span></span>
						{#if !isOpen}
							{@const m = live.members.find((x) => x.tag === live.now)}
							{#if m && m.lastDelay && m.lastDelay > 0}
								<span class="now-delay {delayClass(m.lastDelay)}">{m.lastDelay}ms</span>
							{/if}
						{/if}
					{:else}
						<span class="muted">offline</span>
					{/if}
				</button>
				<div class="card-actions">
					{#if o.source !== 'subscription'}
						<button class="icon-btn" onclick={() => (editTag = o.tag)} aria-label="Редактировать">✎</button>
						<button class="icon-btn danger" onclick={() => requestDelete(o.tag)} aria-label="Удалить">✕</button>
					{/if}
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

			{#if isOpen && live}
				<div class="member-grid">
					<div class="grid-toolbar">
						{#if live.type !== 'selector'}
							<button
								class="btn btn-sm"
								type="button"
								disabled={testingGroup === o.tag}
								onclick={() => testGroup(o.tag)}
							>
								{testingGroup === o.tag ? 'Тест…' : 'Тест всех'}
							</button>
						{/if}
					</div>
					<div class="member-cards">
						{#each live.members as m (m.tag)}
							{@const isNow = m.tag === live.now}
							{@const hist = latencyHistory.get(m.tag) ?? []}
							{@const hasDelay = !!(m.lastDelay && m.lastDelay > 0)}
							<button
								class="member-chip"
								class:active={isNow}
								type="button"
								disabled={live.type !== 'selector'}
								onclick={() => live.type === 'selector' && selectMember(o.tag, m.tag)}
							>
								<div class="chip-row">
									<span class="chip-tag mono">{m.tag}</span>
									{#if isNow}
										<span class="chip-mark">●</span>
									{/if}
								</div>
								<div class="chip-row">
									<span class="chip-delay {hasDelay ? delayClass(m.lastDelay!) : 'delay-muted'}">
										{hasDelay ? `${m.lastDelay}ms` : '—'}
									</span>
									<LatencySparkline history={hist} />
								</div>
							</button>
						{/each}
					</div>
				</div>
			{/if}
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
		gap: 0.5rem;
		margin-bottom: 0.5rem;
	}
	.row-toggle {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex: 1;
		min-width: 0;
		background: transparent;
		border: none;
		padding: 0;
		cursor: pointer;
		color: var(--color-text);
		text-align: left;
	}
	.chevron {
		display: inline-block;
		font-family: ui-monospace, monospace;
		transition: transform 0.15s;
		color: var(--color-text-secondary, var(--muted-text));
	}
	.chevron.open {
		transform: rotate(90deg);
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
	.now {
		font-size: 0.8rem;
		color: var(--color-text-secondary, var(--muted-text));
	}
	.now-tag {
		color: var(--color-text, inherit);
	}
	.now-delay {
		font-size: 0.75rem;
		margin-left: 0.25rem;
		font-family: ui-monospace, monospace;
	}
	.muted {
		font-size: 0.75rem;
		color: var(--color-text-muted, var(--muted-text));
	}
	.card-actions {
		display: flex;
		gap: 0.25rem;
		flex-shrink: 0;
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

	.member-grid {
		margin-top: 0.5rem;
		padding: 0.5rem;
		background: var(--color-bg-primary, var(--bg));
		border: 1px solid var(--color-border, var(--surface-bg));
		border-radius: 8px;
	}
	.grid-toolbar {
		display: flex;
		justify-content: flex-end;
		margin-bottom: 0.5rem;
	}
	.member-cards {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
		gap: 0.5rem;
	}
	.member-chip {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		padding: 0.5rem 0.6rem;
		background: var(--color-bg-secondary, var(--surface-bg));
		border: 1px solid var(--color-border, transparent);
		border-radius: 8px;
		cursor: pointer;
		color: var(--color-text, inherit);
		text-align: left;
	}
	.member-chip:hover:not(:disabled) {
		border-color: var(--color-accent);
	}
	.member-chip:disabled {
		cursor: default;
	}
	.member-chip.active {
		background: color-mix(in srgb, var(--color-accent) 12%, var(--color-bg-secondary, var(--surface-bg)));
		border-color: var(--color-accent);
	}
	.chip-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
	}
	.chip-tag {
		font-size: 0.78rem;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.chip-mark {
		color: var(--color-accent);
		font-size: 0.85rem;
	}
	.chip-delay {
		font-size: 0.72rem;
		font-family: ui-monospace, monospace;
	}
	.delay-good {
		color: var(--color-success, #22c55e);
	}
	.delay-warn {
		color: var(--color-warning, #f59e0b);
	}
	.delay-bad {
		color: var(--color-error, #dc2626);
	}
	.delay-muted {
		color: var(--color-text-muted, var(--muted-text));
	}
	.badge-managed {
		background: rgba(120, 130, 200, 0.18);
		color: var(--text-muted);
		font-size: 0.6875rem;
		padding: 0.125rem 0.5rem;
		border-radius: var(--radius-pill);
		margin-left: 0.375rem;
	}
</style>
