<script lang="ts">
	import { IconButton, Button } from '$lib/components/ui';
	import { Bell } from 'lucide-svelte';
	import SideDrawer from '$lib/components/ui/SideDrawer.svelte';
	import {
		notificationCenter,
		unreadCount,
		dayBucket,
		type CenterEntry,
		type DayBucket,
	} from '$lib/stores/notificationCenter';
	import { formatTime } from '$lib/utils/format';
	import { goto } from '$app/navigation';

	interface Props {
		authenticated: boolean;
	}

	let { authenticated }: Props = $props();

	let open = $state(false);

	const ORDER: DayBucket[] = ['today', 'yesterday', 'earlier'];
	const GROUP_LABELS: Record<DayBucket, string> = {
		today: 'Сегодня',
		yesterday: 'Вчера',
		earlier: 'Ранее',
	};

	const groups = $derived.by(() => {
		const now = Date.now();
		const buckets: Record<DayBucket, CenterEntry[]> = { today: [], yesterday: [], earlier: [] };
		for (const e of $notificationCenter) buckets[dayBucket(e.lastTs, now)].push(e);
		return buckets;
	});

	function clock(ts: number): string {
		return formatTime(new Date(ts).toISOString());
	}

	function meta(e: CenterEntry): string {
		const parts = [clock(e.lastTs)];
		if (e.count > 1) parts.push(`×${e.count}`);
		if (e.action) parts.push(e.action.label);
		return parts.join(' · ');
	}

	function onRowActivate(e: CenterEntry): void {
		notificationCenter.markRead(e.id);
		if (e.action) {
			open = false;
			goto(e.action.href);
		}
	}
</script>

{#if authenticated}
	<span class="notif-bell">
		<IconButton ariaLabel="Уведомления" onclick={() => (open = true)}>
			<Bell size={16} aria-hidden="true" />
		</IconButton>
		{#if $unreadCount > 0}
			<span class="notif-badge" aria-hidden="true">{$unreadCount}</span>
		{/if}
	</span>

	<SideDrawer {open} onClose={() => (open = false)} title="Уведомления">
		{#if $notificationCenter.length === 0}
			<p class="notif-empty">Уведомлений нет</p>
		{:else}
			<div class="notif-toolbar">
				<Button variant="ghost" onclick={() => notificationCenter.markAllRead()}>
					Прочитать всё
				</Button>
				<Button variant="ghost" onclick={() => notificationCenter.clearAll()}>
					Очистить
				</Button>
			</div>

			{#each ORDER as key (key)}
				{#if groups[key].length > 0}
					<div class="notif-group">{GROUP_LABELS[key]}</div>
					{#each groups[key] as e (e.id)}
						<div class="notif-row" class:unread={!e.read} class:is-error={e.type === 'error'}>
							<button type="button" class="notif-main" onclick={() => onRowActivate(e)}>
								<span class="notif-dot" class:hidden={e.read}></span>
								<span class="notif-body">
									<span class="notif-msg">{e.message}</span>
									<span class="notif-meta">{meta(e)}</span>
								</span>
							</button>
							<button
								type="button"
								class="notif-remove"
								aria-label="Удалить уведомление"
								onclick={() => notificationCenter.remove(e.id)}
							>
								×
							</button>
						</div>
					{/each}
				{/if}
			{/each}
		{/if}

		{#snippet footer()}
			<div class="notif-footer">
				<span class="notif-retention">Хранится 7 дней · до 100</span>
				<a class="notif-journal" href="/logs" onclick={() => (open = false)}>Открыть журнал →</a>
			</div>
		{/snippet}
	</SideDrawer>
{/if}

<style>
	.notif-bell {
		position: relative;
		display: inline-flex;
	}

	.notif-badge {
		position: absolute;
		top: -4px;
		right: -4px;
		min-width: 16px;
		height: 16px;
		padding: 0 4px;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		font-size: 10px;
		font-weight: 700;
		line-height: 1;
		color: #fff;
		background: var(--color-danger, #e5484d);
		border-radius: 999px;
		pointer-events: none;
	}

	.notif-empty {
		padding: 1.5rem 1rem;
		text-align: center;
		color: var(--color-text-muted);
		font-size: 0.875rem;
	}

	.notif-toolbar {
		display: flex;
		justify-content: flex-end;
		gap: 0.5rem;
		padding: 0 0 0.5rem;
		border-bottom: 1px solid var(--color-border);
		margin-bottom: 0.5rem;
	}

	.notif-group {
		font-size: 11px;
		text-transform: uppercase;
		letter-spacing: 0.5px;
		color: var(--color-text-muted);
		padding: 0.5rem 0.25rem 0.25rem;
	}

	.notif-row {
		display: flex;
		align-items: stretch;
		gap: 0.25rem;
		border-radius: var(--radius-sm);
	}

	.notif-row.unread {
		background: var(--color-bg-tertiary);
	}

	.notif-main {
		flex: 1;
		display: flex;
		align-items: flex-start;
		gap: 0.5rem;
		padding: 0.625rem 0.5rem;
		background: none;
		border: none;
		text-align: left;
		cursor: pointer;
		color: inherit;
	}

	.notif-dot {
		flex-shrink: 0;
		width: 8px;
		height: 8px;
		margin-top: 0.35rem;
		border-radius: 999px;
		background: var(--color-accent);
	}

	.notif-dot.hidden {
		visibility: hidden;
	}

	.notif-body {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
		min-width: 0;
	}

	.notif-msg {
		font-size: 0.875rem;
		color: var(--color-text-primary);
	}

	.notif-row.is-error .notif-msg {
		color: var(--color-danger, #e5484d);
	}

	.notif-meta {
		font-size: 11px;
		color: var(--color-text-muted);
	}

	.notif-remove {
		flex-shrink: 0;
		width: 28px;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		background: none;
		border: none;
		color: var(--color-text-muted);
		font-size: 18px;
		line-height: 1;
		cursor: pointer;
	}

	.notif-remove:hover {
		color: var(--color-text-primary);
	}

	.notif-footer {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
		font-size: 12px;
		color: var(--color-text-muted);
	}

	.notif-journal {
		color: var(--color-accent);
		text-decoration: none;
		white-space: nowrap;
	}
</style>
