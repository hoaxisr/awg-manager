<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type SystemServiceItem } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Button, Card, ConfirmModal } from '$lib/components/ui';
	import { errorMessage } from '$lib/utils/errorMessage';
	import { stripAnsi } from '$lib/utils/ansi';
	import { RefreshCw, Play, Square, RotateCw } from 'lucide-svelte';

	let items = $state<SystemServiceItem[]>([]);
	let loading = $state(false);
	let acting = $state<string | null>(null);
	let pendingAction = $state<{ item: SystemServiceItem; action: 'start' | 'stop' | 'restart' } | null>(
		null,
	);

	onMount(load);

	async function load() {
		loading = true;
		try {
			items = await api.systemServicesList();
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось загрузить службы'));
		} finally {
			loading = false;
		}
	}

	function requestAction(item: SystemServiceItem, action: 'start' | 'stop' | 'restart') {
		if (item.managed && action === 'stop') {
			pendingAction = { item, action };
			return;
		}
		void runAction(item, action);
	}

	async function runAction(item: SystemServiceItem, action: 'start' | 'stop' | 'restart') {
		acting = item.script;
		try {
			const res = await api.systemServicesAction(item.script, action);
			if (res.ok) notifications.success(`${item.name}: ${action}`);
			else notifications.error(stripAnsi(res.error || res.output || 'Ошибка'));
			await load();
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось выполнить действие'));
		} finally {
			acting = null;
			pendingAction = null;
		}
	}

	function statusHint(item: SystemServiceItem): string {
		return stripAnsi(item.statusText || '');
	}
</script>

<Card padding="sm">
	<div class="head">
		<h3>Службы Entware (init.d)</h3>
		<Button variant="ghost" onclick={load} disabled={loading}>
			{#snippet iconBefore()}<RefreshCw size={14} />{/snippet}
			Обновить
		</Button>
	</div>

	{#if loading && items.length === 0}
		<p class="muted">Загрузка…</p>
	{:else}
		<div class="svc-grid head-row">
			<div>Служба</div>
			<div>Статус</div>
			<div class="actions-head">Действия</div>
		</div>
		{#each items as item (item.script)}
			<div class="svc-grid row">
				<div class="name-cell">
					<div class="name">{item.name}</div>
					<div class="script">{item.script}</div>
					{#if item.managed && item.managedHint}
						<div class="hint">{item.managedHint}</div>
					{/if}
				</div>
				<div class="status-cell">
					<span class:badge-running={item.running} class:badge-stopped={!item.running}>
						{item.running ? 'работает' : 'остановлена'}
					</span>
					{#if statusHint(item)}
						<div class="status-hint" title={statusHint(item)}>{statusHint(item)}</div>
					{/if}
				</div>
				<div class="actions">
					<Button size="sm" variant="secondary" disabled={acting === item.script} onclick={() => requestAction(item, 'start')}>
						{#snippet iconBefore()}<Play size={14} />{/snippet}
						Start
					</Button>
					<Button size="sm" variant="secondary" disabled={acting === item.script} onclick={() => requestAction(item, 'stop')}>
						{#snippet iconBefore()}<Square size={14} />{/snippet}
						Stop
					</Button>
					<Button size="sm" variant="secondary" disabled={acting === item.script} onclick={() => requestAction(item, 'restart')}>
						{#snippet iconBefore()}<RotateCw size={14} />{/snippet}
						Restart
					</Button>
				</div>
			</div>
		{/each}
	{/if}
</Card>

{#if pendingAction}
	<ConfirmModal
		open={!!pendingAction}
		title="Остановить управляемую службу?"
		message={pendingAction.item.managedHint || pendingAction.item.name}
		confirmLabel="Остановить"
		variant="danger"
		busy={acting === pendingAction.item.script}
		onClose={() => (pendingAction = null)}
		onConfirm={() => {
			if (pendingAction) void runAction(pendingAction.item, pendingAction.action);
		}}
	/>
{/if}

<style>
	.head { display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.75rem; gap: 0.5rem; }
	.head h3 { margin: 0; font-size: 1rem; }
	.svc-grid {
		display: grid;
		grid-template-columns: minmax(160px, 1.2fr) minmax(120px, 0.9fr) minmax(220px, 1fr);
		gap: 0.5rem 0.75rem;
		align-items: center;
		padding: 0.45rem 0;
		border-bottom: 1px solid var(--border-subtle, #333);
	}
	.head-row { font-size: 0.75rem; font-weight: 600; text-transform: uppercase; letter-spacing: 0.04em; opacity: 0.65; }
	.name { font-weight: 600; }
	.script, .hint, .status-hint { font-size: 0.78rem; opacity: 0.75; margin-top: 0.15rem; word-break: break-word; }
	.status-hint { display: -webkit-box; -webkit-line-clamp: 2; line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
	.badge-running { color: var(--success, #22c55e); font-weight: 600; }
	.badge-stopped { color: var(--muted-text, #888); font-weight: 600; }
	.actions { display: flex; flex-wrap: nowrap; gap: 0.25rem; justify-content: flex-end; }
	.actions-head { text-align: right; }
	@media (max-width: 820px) {
		.svc-grid { grid-template-columns: 1fr; }
		.actions { justify-content: flex-start; flex-wrap: wrap; }
	}
	.muted { opacity: 0.7; }
</style>
