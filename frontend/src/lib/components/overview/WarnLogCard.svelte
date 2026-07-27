<script lang="ts">
	// Журнал ≥WARN на «Обзоре». Клиентские SSE-буферы при холодном заходе
	// пусты, поэтому при маунте делаем два one-shot запроса к существующей
	// ручке (`level=warn` отдаёт только warn+error) и сливаем их со сторами
	// по времени последней активности. Новых ручек не добавлялось.
	import { onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import { appLogEntries, singboxLogEntries } from '$lib/stores/logs';
	import { formatRelativeTime } from '$lib/utils/format';
	import type { LogEntry } from '$lib/types';
	import { mergeWarnEntries, effectiveTime, WARN_LOG_FETCH_LIMIT } from './warnLog';

	let fetched = $state<LogEntry[]>([]);
	let loading = $state(true);

	onMount(async () => {
		try {
			const [app, singbox] = await Promise.all([
				api.getLogs({ bucket: 'app', level: 'warn', limit: WARN_LOG_FETCH_LIMIT }),
				api.getLogs({ bucket: 'singbox', level: 'warn', limit: WARN_LOG_FETCH_LIMIT }),
			]);
			fetched = [...app.logs, ...singbox.logs];
		} catch {
			// Молча: SSE-сторы всё равно наполнят карточку живыми строками.
		} finally {
			loading = false;
		}
	});

	const entries = $derived(
		mergeWarnEntries([fetched, $appLogEntries, $singboxLogEntries]),
	);

	const source = (e: LogEntry) => [e.group, e.subgroup].filter(Boolean).join(' · ');
</script>

<div class="log">
	<div class="head">
		<span class="title">Журнал · WARN / ERROR</span>
		<a class="link" href="/tools?tab=logs">весь журнал →</a>
	</div>

	{#if loading && entries.length === 0}
		<p class="empty">Загрузка…</p>
	{:else if entries.length === 0}
		<p class="empty">Журнал пуст — предупреждений нет.</p>
	{:else}
		{#each entries as entry (entry.timestamp + entry.message)}
			<div class="row">
				<span class="level" class:err={entry.level === 'error'}>{entry.level.toUpperCase()}</span>
				<span class="body">
					<span class="message">{entry.message}</span>
					<span class="meta">
						{source(entry)} · {formatRelativeTime(new Date(effectiveTime(entry)))}
						{#if (entry.repeats ?? 0) > 0}· ×{(entry.repeats ?? 0) + 1}{/if}
					</span>
				</span>
			</div>
		{/each}
	{/if}
</div>

<style>
	.log {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		overflow: hidden;
	}

	.head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
		padding: 0.75rem 0.9375rem;
		border-bottom: 1px solid var(--color-border);
	}

	.title {
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.link {
		font-size: 0.6875rem;
		font-weight: 500;
		color: var(--color-accent);
		text-decoration: none;
	}

	.link:hover { text-decoration: underline; }

	.empty {
		margin: 0;
		padding: 0.875rem 0.9375rem;
		font-size: 0.8125rem;
		color: var(--color-text-muted);
	}

	.row {
		display: flex;
		align-items: flex-start;
		gap: 0.5625rem;
		padding: 0.5625rem 0.875rem;
		border-top: 1px solid var(--color-border);
	}

	.row:first-of-type { border-top: none; }

	.level {
		flex: none;
		font-family: var(--font-mono);
		font-size: 0.5625rem;
		font-weight: 600;
		letter-spacing: 0.3px;
		padding: 0.125rem 0.3125rem;
		border-radius: 4px;
		background: var(--color-warning-tint);
		color: var(--color-warning);
	}

	.level.err {
		background: var(--color-error-tint);
		color: var(--color-error);
	}

	.body {
		display: flex;
		flex-direction: column;
		gap: 0.0625rem;
		min-width: 0;
	}

	.message {
		font-size: 0.75rem;
		line-height: 1.4;
		color: var(--color-text-secondary);
		overflow-wrap: anywhere;
	}

	.meta {
		font-family: var(--font-mono);
		font-size: 0.6875rem;
		color: var(--color-text-muted);
	}
</style>
