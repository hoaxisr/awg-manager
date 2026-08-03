<script lang="ts">
	import { Button } from '$lib/components/ui';
	import { Bell } from 'lucide-svelte';
	import SideDrawer from '$lib/components/ui/SideDrawer.svelte';
	import {
		notificationCenter,
		unreadCount,
		unreadSeverity,
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

	const badgeLabel = $derived($unreadCount > 99 ? '99+' : String($unreadCount));
	const bellAria = $derived(
		$unreadCount > 0 ? `Уведомления, непрочитанных: ${$unreadCount}` : 'Уведомления',
	);

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
	<button
		type="button"
		class="notif-trigger"
		class:has-unread={$unreadCount > 0}
		class:is-error={$unreadSeverity === 'error'}
		class:is-warning={$unreadSeverity === 'warning'}
		aria-label={bellAria}
		onclick={() => (open = true)}
	>
		<span class="notif-chip">
			<Bell size={16} aria-hidden="true" />
			{#if $unreadCount > 0}
				<span class="notif-count" aria-hidden="true">{badgeLabel}</span>
				<span class="notif-pip" aria-hidden="true"></span>
			{/if}
		</span>
	</button>

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
					<div class="notif-list">
						{#each groups[key] as e (e.id)}
							<div
								class="notif-row"
								class:unread={!e.read}
								class:is-error={e.type === 'error'}
								class:is-warning={e.type === 'warning'}
							>
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
					</div>
				{/if}
			{/each}
		{/if}

		{#snippet footer()}
			<div class="notif-footer">
				<span class="notif-retention">Хранится 7 дней · до 100</span>
				<a class="notif-journal" href="/tools?tab=logs" onclick={() => (open = false)}>Открыть журнал →</a>
			</div>
		{/snippet}
	</SideDrawer>
{/if}

<style>
	.notif-trigger {
		display: inline-flex;
		align-items: center;
		padding: 0;
		margin: 0;
		background: transparent;
		border: none;
		color: var(--color-text-muted);
		cursor: pointer;
		font: inherit;
	}

	.notif-trigger:focus-visible .notif-chip {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
	}

	.notif-chip {
		position: relative;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		gap: 0.3rem;
		min-width: 28px;
		height: 28px;
		padding: 0 0.35rem;
		box-sizing: border-box;
		border: 1px solid transparent;
		border-radius: var(--radius-sm);
		color: inherit;
		transition:
			background var(--t-fast) ease,
			color var(--t-fast) ease,
			border-color var(--t-fast) ease;
	}

	.notif-trigger:not(.has-unread) .notif-chip {
		padding: 0;
		width: 28px;
	}

	.notif-trigger:hover .notif-chip {
		background: var(--color-bg-hover);
		color: var(--color-accent);
	}

	.notif-trigger.has-unread.is-error .notif-chip {
		background: var(--color-error-tint);
		border-color: var(--color-error-border);
		color: var(--color-error);
		padding-inline: 0.45rem 0.55rem;
	}

	.notif-trigger.has-unread.is-error:hover .notif-chip {
		background: color-mix(in srgb, var(--color-error) 28%, transparent);
		color: var(--color-error);
	}

	.notif-trigger.has-unread.is-warning .notif-chip {
		background: var(--color-warning-tint);
		border-color: var(--color-warning-border);
		color: var(--color-warning);
		padding-inline: 0.45rem 0.55rem;
	}

	.notif-trigger.has-unread.is-warning:hover .notif-chip {
		background: color-mix(in srgb, var(--color-warning) 28%, transparent);
		color: var(--color-warning);
	}

	.notif-count {
		font-size: 12px;
		font-weight: 700;
		font-variant-numeric: tabular-nums;
		letter-spacing: -0.02em;
		line-height: 1;
		color: currentColor;
	}

	/* Empty ring sitting on the chip's top-right border corner */
	.notif-pip {
		position: absolute;
		top: -3px;
		right: -3px;
		width: 8px;
		height: 8px;
		box-sizing: border-box;
		border-radius: 999px;
		background: var(--color-bg-secondary);
		border: 1.5px solid currentColor;
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
		align-items: center;
		justify-content: flex-end;
		gap: 0.25rem;
		/* body padding 1rem — тянем к хедеру; сверху/снизу до полоски одинаково */
		margin: -1rem 0 0.375rem;
		padding: 0.375rem 0;
		border-bottom: 1px solid var(--color-border);
	}

	.notif-toolbar :global(.btn) {
		height: 1.75rem;
		min-height: 1.75rem;
		max-height: 1.75rem;
		padding-inline: 0.5rem;
	}

	.notif-group {
		font-size: 11px;
		text-transform: uppercase;
		letter-spacing: 0.5px;
		color: var(--color-text-muted);
		padding: 0.625rem 0.25rem 0.375rem;
	}

	.notif-list {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}

	.notif-row {
		display: flex;
		align-items: stretch;
		gap: 0.25rem;
		border-radius: var(--radius-sm);
		border: 1px solid transparent;
	}

	.notif-row:not(.unread) {
		border-color: color-mix(in srgb, var(--color-border) 65%, transparent);
		opacity: 0.62;
	}

	.notif-row.unread.is-error {
		background: var(--color-error-tint);
		border-color: var(--color-error-border);
	}

	.notif-row.unread.is-warning {
		background: var(--color-warning-tint);
		border-color: var(--color-warning-border);
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

	.notif-row.is-error .notif-dot {
		background: var(--color-error);
	}

	.notif-row.is-warning .notif-dot {
		background: var(--color-warning);
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
		color: var(--color-error);
	}

	.notif-row.is-warning .notif-msg {
		color: var(--color-warning);
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
