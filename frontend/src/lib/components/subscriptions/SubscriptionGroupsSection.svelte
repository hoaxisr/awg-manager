<script lang="ts">
	import { m } from '$lib/i18n';
	import RichText from '$lib/components/ui/RichText.svelte';
	import type { Subscription, SubscriptionGroup } from '$lib/types';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Button, Modal } from '$lib/components/ui';
	import { Pencil, Trash2 } from 'lucide-svelte';
	import CreateIcon from '$lib/components/ui/icons/CreateIcon.svelte';
	import SubscriptionGroupModal from './SubscriptionGroupModal.svelte';

	interface Props {
		subscriptions: Subscription[];
	}
	let { subscriptions }: Props = $props();

	let groups = $state<SubscriptionGroup[]>([]);
	let loaded = $state(false);
	let modalOpen = $state(false);
	let editing = $state<SubscriptionGroup | null>(null);
	let pendingDelete = $state<SubscriptionGroup | null>(null);
	let deleting = $state(false);

	// Монотонный счётчик запросов: ответ применяется только если за время
	// полёта не стартовал более свежий load (ignore-stale-response) — заодно
	// схлопывает перекрывающиеся вызовы из $effect ниже.
	let loadSeq = 0;

	async function load(): Promise<void> {
		const seq = ++loadSeq;
		try {
			const fresh = await api.listSubscriptionGroups();
			if (seq !== loadSeq) return;
			groups = fresh;
		} catch {
			// Секция не должна ронять страницу подписок (старый бекенд без
			// групп, сетевой сбой) — просто остаёмся с пустым списком.
			if (seq !== loadSeq) return;
			groups = [];
		} finally {
			if (seq === loadSeq) loaded = true;
		}
	}

	// Перезагрузка при каждом изменении subscriptions (store опрашивается
	// каждые 30 с и меняется при действиях пользователя): состав групп
	// (memberCount/members) считается на сервере из членов подписок и без
	// этого протухал бы до ручного действия. Срабатывает и на mount.
	$effect(() => {
		void subscriptions;
		void load();
	});

	function openCreate(): void {
		editing = null;
		modalOpen = true;
	}

	function openEdit(g: SubscriptionGroup): void {
		editing = g;
		modalOpen = true;
	}

	async function confirmDelete(): Promise<void> {
		if (!pendingDelete || deleting) return;
		deleting = true;
		try {
			await api.deleteSubscriptionGroup(pendingDelete.id);
			notifications.success(m.subscriptions_groups_deleted());
			pendingDelete = null;
			await load();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.subscriptions_groups_delete_failed());
		} finally {
			deleting = false;
		}
	}

	function subLabels(g: SubscriptionGroup): string {
		const byId = new Map(subscriptions.map((s) => [s.id, s.label || s.url || s.id]));
		const names = g.useSubscriptionIds.map((id) => byId.get(id) ?? id);
		return names.length > 0 ? names.join(', ') : m.subscriptions_groups_no_subs();
	}
</script>

{#snippet createIcon()}
	<CreateIcon />
{/snippet}

{#if loaded}
	<section class="groups-section">
		<div class="groups-head">
			<div>
				<h2 class="section-title">{m.subscriptions_groups_title()}</h2>
				<p class="section-hint">
					{m.subscriptions_groups_hint()}
				</p>
			</div>
			<Button variant="primary" size="md" iconBefore={createIcon} onclick={openCreate}>
				{m.subscriptions_groups_create()}
			</Button>
		</div>
		{#if groups.length > 0}
			<div class="groups-grid">
				{#each groups as g (g.id)}
					<div class="group-card" class:off={!g.enabled}>
						<div class="group-main">
							<div class="group-title-row">
								<span class="group-title">{g.label}</span>
								{#if !g.enabled}<span class="group-off-badge">{m.subscriptions_groups_off()}</span>{/if}
							</div>
							<div class="group-meta">
								{m.subscriptions_groups_meta({
									mode: g.mode === 'urltest' ? m.subscriptions_groups_mode_urltest() : m.subscriptions_groups_mode_selector(),
									count: g.memberCount,
								})}
								{#if g.listenPort}
									· {m.subscriptions_groups_port()} <span class="mono">{g.listenPort}</span>
								{/if}
							</div>
							<div class="group-subs" title={subLabels(g)}>{m.subscriptions_groups_from({ names: subLabels(g) })}</div>
						</div>
						<div class="group-actions">
							<button
								type="button"
								class="icon-btn"
								title={m.subscriptions_groups_edit()}
								aria-label={m.subscriptions_groups_edit_aria({ label: g.label })}
								onclick={() => openEdit(g)}
							>
								<Pencil size={14} strokeWidth={2} aria-hidden="true" />
							</button>
							<button
								type="button"
								class="icon-btn danger"
								title={m.subscriptions_groups_delete()}
								aria-label={m.subscriptions_groups_delete_aria({ label: g.label })}
								onclick={() => (pendingDelete = g)}
							>
								<Trash2 size={14} strokeWidth={2} aria-hidden="true" />
							</button>
						</div>
					</div>
				{/each}
			</div>
		{:else}
			<div class="groups-empty">
				{m.subscriptions_groups_empty()}
			</div>
		{/if}
	</section>
{/if}

<SubscriptionGroupModal
	open={modalOpen}
	group={editing}
	{subscriptions}
	onclose={() => (modalOpen = false)}
	onsaved={() => void load()}
/>

<Modal
	open={pendingDelete !== null}
	title={m.subscriptions_groups_delete_title()}
	size="md"
	onclose={() => {
		if (deleting) return;
		pendingDelete = null;
	}}
>
	{#if pendingDelete}
		<p>
			<RichText text={m.subscriptions_groups_delete_message({ label: pendingDelete.label })} />
		</p>
	{/if}
	{#snippet actions()}
		<Button variant="ghost" disabled={deleting} onclick={() => (pendingDelete = null)}>
			{m.common_cancel()}
		</Button>
		<Button variant="danger" disabled={deleting} loading={deleting} onclick={confirmDelete}>
			{deleting ? m.subscriptions_groups_deleting() : m.common_delete()}
		</Button>
	{/snippet}
</Modal>

<style>
	.groups-section {
		margin-top: 2rem;
		padding-top: 1.25rem;
		border-top: 1px solid var(--color-border);
	}
	.groups-head {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		gap: 1rem;
		margin-bottom: 1rem;
	}
	.section-title {
		margin: 0 0 0.25rem;
		font-size: 1rem;
		color: var(--color-text-primary);
	}
	.section-hint {
		margin: 0;
		font-size: 0.8rem;
		color: var(--color-text-muted);
		max-width: 60ch;
	}
	.groups-grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(min(100%, 300px), 1fr));
		gap: 0.8rem;
	}
	.group-card {
		display: flex;
		align-items: flex-start;
		gap: 0.6rem;
		padding: 12px 14px;
		border: 1px solid var(--color-border);
		border-radius: 10px;
		background: var(--color-bg-secondary, var(--color-bg-primary));
	}
	.group-card.off {
		opacity: 0.7;
	}
	.group-main {
		flex: 1 1 auto;
		min-width: 0;
	}
	.group-title-row {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		min-width: 0;
	}
	.group-title {
		font-size: 0.9rem;
		font-weight: 600;
		color: var(--color-text-primary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.group-off-badge {
		flex-shrink: 0;
		font-size: 0.65rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		padding: 0.1rem 0.4rem;
		border-radius: 999px;
		background: var(--color-bg-tertiary);
		color: var(--color-text-muted);
	}
	.group-meta {
		font-size: 0.78rem;
		color: var(--color-text-muted);
		margin-top: 0.25rem;
	}
	.group-subs {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		margin-top: 0.25rem;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.group-actions {
		display: flex;
		gap: 0.25rem;
		flex-shrink: 0;
	}
	.icon-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 28px;
		height: 28px;
		padding: 0;
		border: none;
		border-radius: var(--radius-sm, 4px);
		background: transparent;
		color: var(--color-text-muted);
		cursor: pointer;
	}
	.icon-btn:hover {
		color: var(--color-text-primary);
		background: var(--color-bg-tertiary);
	}
	.icon-btn.danger:hover {
		color: var(--color-danger, #f85149);
		background: color-mix(in srgb, var(--color-danger, #f85149) 12%, transparent);
	}
	.icon-btn:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
	}
	.groups-empty {
		padding: 1.25rem;
		text-align: center;
		font-size: 0.82rem;
		color: var(--color-text-muted);
		border: 1px dashed var(--color-border);
		border-radius: 8px;
	}
	.mono {
		font-family: var(--font-mono, ui-monospace, monospace);
	}
	@media (max-width: 640px) {
		.groups-head {
			flex-direction: column;
			align-items: stretch;
		}
		.groups-grid {
			grid-template-columns: 1fr;
		}
	}
</style>
