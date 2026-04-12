<script lang="ts">
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { logEntries } from '$lib/stores/logs';
	import { LoadingSpinner, EmptyState } from '$lib/components/layout';
	import { formatTime } from '$lib/utils/format';
	import type { LogEntry } from '$lib/types';

	const enabledStore = logEntries.enabled;
	const totalStore = logEntries.total;
	const loadedStore = logEntries.loaded;

	let filterGroup = $state('');
	let filterSubgroup = $state('');
	let filterLevel = $state('');
	let searchText = $state('');
	let loadingMore = $state(false);
	let clearing = $state(false);
	const LIMIT = 200;

	const subgroupsByGroup: Record<string, string[]> = {
		tunnel:  ['lifecycle', 'ops', 'state', 'firewall', 'pingcheck', 'connectivity'],
		routing: ['dns-route', 'static-route', 'access-policy'],
		server:  ['managed', 'system-tunnels'],
		system:  ['boot', 'wan', 'auth', 'settings', 'update'],
	};

	const levelStyle: Record<string, { badge: string; msg: string; label: string }> = {
		error: { badge: '#ef4444', msg: '#fca5a5', label: 'ERROR' },
		warn:  { badge: '#eab308', msg: '#fde047', label: 'WARN' },
		info:  { badge: '#60a5fa', msg: '#cbd5e1', label: 'INFO' },
		full:  { badge: '#a78bfa', msg: '#94a3b8', label: 'FULL' },
		debug: { badge: '#475569', msg: '#475569', label: 'DEBUG' },
	};

	const groupLabels: Record<string, string> = {
		tunnel: 'Tunnel', routing: 'Routing', server: 'Server', system: 'System',
	};

	async function loadMore() {
		loadingMore = true;
		try {
			const currentCount = $logEntries.length;
			const resp = await api.getLogs({
				limit: LIMIT,
				offset: currentCount,
			});
			logEntries.setEntries([...$logEntries, ...resp.logs]);
			logEntries.setTotal(resp.total);
		} catch { }
		finally { loadingMore = false; }
	}

	function setGroup(g: string) {
		filterGroup = filterGroup === g ? '' : g;
		filterSubgroup = '';
	}

	function setSubgroup(sg: string) {
		filterSubgroup = filterSubgroup === sg ? '' : sg;
	}

	function setLevel(l: string) {
		filterLevel = filterLevel === l ? '' : l;
	}

	let displayLogs = $derived.by(() => {
		let filtered = $logEntries;
		if (filterGroup) {
			filtered = filtered.filter(l => l.group === filterGroup);
		}
		if (filterSubgroup) {
			filtered = filtered.filter(l => l.subgroup === filterSubgroup);
		}
		if (filterLevel) {
			filtered = filtered.filter(l => l.level === filterLevel);
		}
		if (searchText) {
			const q = searchText.toLowerCase();
			filtered = filtered.filter(l =>
				l.message.toLowerCase().includes(q) ||
				l.target.toLowerCase().includes(q) ||
				l.action.toLowerCase().includes(q)
			);
		}
		return filtered;
	});

	async function clearLogs() {
		clearing = true;
		try {
			await api.clearLogs();
			logEntries.clear();
			notifications.success('Логи очищены');
		} catch (e) {
			notifications.error('Не удалось очистить логи');
		} finally {
			clearing = false;
		}
	}

	function formatLogLine(log: LogEntry): string {
		const time = formatTime(log.timestamp);
		const scope = log.subgroup ? `${log.group}/${log.subgroup}` : log.group;
		return `[${time}] [${(levelStyle[log.level]?.label ?? log.level).toUpperCase()}] [${scope}] ${log.action} ${log.target}: ${log.message}`;
	}

	async function copyToClipboard() {
		if (!displayLogs.length) return;

		const text = displayLogs.map(formatLogLine).join('\n');

		try {
			if (navigator.clipboard && window.isSecureContext) {
				await navigator.clipboard.writeText(text);
			} else {
				const textarea = document.createElement('textarea');
				textarea.value = text;
				textarea.style.position = 'fixed';
				textarea.style.opacity = '0';
				document.body.appendChild(textarea);
				textarea.select();
				document.execCommand('copy');
				document.body.removeChild(textarea);
			}
			notifications.success('Скопировано в буфер обмена');
		} catch (e) {
			notifications.error('Не удалось скопировать');
		}
	}

	let downloading = $state(false);

	async function downloadLogs() {
		downloading = true;
		try {
			// Fetch ALL logs from server in one request.
			const resp = await api.getLogs({
				group: filterGroup || undefined,
				subgroup: filterSubgroup || undefined,
				level: filterLevel || undefined,
				limit: $totalStore || 10000,
			});

			const text = resp.logs.map(formatLogLine).join('\n');
			const blob = new Blob([text], { type: 'text/plain;charset=utf-8' });
			const url = URL.createObjectURL(blob);

			const date = new Date().toISOString().slice(0, 10);
			const a = document.createElement('a');
			a.href = url;
			a.download = `awg-manager-logs-${date}.txt`;
			document.body.appendChild(a);
			a.click();
			document.body.removeChild(a);
			URL.revokeObjectURL(url);

			notifications.success(`Скачано ${resp.logs.length} записей`);
		} catch (e) {
			notifications.error('Не удалось скачать логи');
		} finally {
			downloading = false;
		}
	}
</script>

{#if !$loadedStore}
	<div class="flex justify-center py-12">
		<LoadingSpinner size="lg" message="Загрузка журнала..." />
	</div>
{:else if !$enabledStore}
	<div class="card">
		<EmptyState
			title="Логирование отключено"
			description="Включите логирование в настройках для записи событий."
		>
			{#snippet icon()}
				<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="48" height="48">
					<path d="M10.29 3.86L1.82 18a2 2 0 001.71 3h16.94a2 2 0 001.71-3L13.71 3.86a2 2 0 00-3.42 0z"/>
					<line x1="12" y1="9" x2="12" y2="13"/>
					<circle cx="12" cy="17" r="1" fill="currentColor"/>
				</svg>
			{/snippet}
			{#snippet action()}
				<a href="/settings" class="btn btn-primary">Открыть настройки</a>
			{/snippet}
		</EmptyState>
	</div>
{:else}
	<div class="log-container">
		<!-- Group chip bar -->
		<div class="chip-bar">
			{#each Object.entries(groupLabels) as [key, label]}
				<button
					class="chip"
					class:active={filterGroup === key}
					onclick={() => setGroup(key)}
				>
					{label}{#if filterGroup === key}&nbsp;&times;{/if}
				</button>
			{/each}

			{#if filterGroup && subgroupsByGroup[filterGroup]}
				<span class="chip-separator">|</span>
				{#each subgroupsByGroup[filterGroup] as sg}
					<button
						class="sub-chip"
						class:active={filterSubgroup === sg}
						onclick={() => setSubgroup(sg)}
					>
						{sg}
					</button>
				{/each}
			{/if}

			<div style="flex:1"></div>

			<input
				type="text"
				class="search-input"
				placeholder="Поиск..."
				bind:value={searchText}
			/>
		</div>

		<!-- Level + actions bar -->
		<div class="level-bar">
			<div class="level-chips">
				<span class="level-label">Level</span>
				<button class="level-chip" class:active={filterLevel === ''} onclick={() => setLevel('')}>Все</button>
				<button class="level-chip" class:active={filterLevel === 'error'} onclick={() => setLevel('error')}>ERROR</button>
				<button class="level-chip" class:active={filterLevel === 'warn'} onclick={() => setLevel('warn')}>WARN</button>
				<button class="level-chip" class:active={filterLevel === 'info'} onclick={() => setLevel('info')}>INFO</button>
				<button class="level-chip" class:active={filterLevel === 'full'} onclick={() => setLevel('full')}>FULL</button>
				<button class="level-chip" class:active={filterLevel === 'debug'} onclick={() => setLevel('debug')}>DEBUG</button>
			</div>

			<div class="level-actions">
				<span class="log-count">{displayLogs.length}{#if $totalStore > $logEntries.length} / {$totalStore}{/if}</span>
				<button class="btn btn-sm btn-ghost" onclick={downloadLogs} disabled={downloading || !$totalStore}>
					{downloading ? '...' : 'Download'}
				</button>
				<button class="btn btn-sm btn-ghost" onclick={copyToClipboard} disabled={!displayLogs.length}>Copy</button>
				<button class="btn btn-sm btn-danger" onclick={clearLogs} disabled={clearing || !$logEntries.length}>
					{clearing ? 'Очистка...' : 'Clear'}
				</button>
			</div>
		</div>

		<!-- Terminal feed -->
		<div class="log-feed">
			{#if displayLogs.length === 0}
				<div class="empty-feed">Нет записей в журнале</div>
			{:else}
				{#each displayLogs as log}
					<div class="log-entry">
						<div class="log-header">
							<span class="log-time">{formatTime(log.timestamp)}</span>
							<span class="log-level level-badge-{log.level}">[{levelStyle[log.level]?.label ?? log.level.toUpperCase()}]</span>
							<span class="log-scope">[{log.group}{log.subgroup ? '/' + log.subgroup : ''}]</span>
							<span class="log-action">{log.action}</span>
							<span class="log-target">{log.target}</span>
						</div>
						<div class="log-message level-msg-{log.level}">
							{log.message}
						</div>
					</div>
				{/each}

				{#if $logEntries.length < $totalStore}
					<div class="load-more">
						<button class="btn-load-more" onclick={loadMore} disabled={loadingMore}>
							{loadingMore ? 'Загрузка...' : `Загрузить ещё (${$totalStore - $logEntries.length} оставшихся)`}
						</button>
					</div>
				{/if}
			{/if}
		</div>
	</div>
{/if}

<style>
	.log-container {
		border: 1px solid var(--border);
		border-radius: var(--radius);
	}

	/* Terminal feed */
	.log-feed {
		background: var(--bg-primary);
		font-family: var(--font-mono, monospace);
		font-size: 12px;
		padding: 8px 16px;
		min-height: 400px;
		line-height: 1.7;
		border-radius: 0 0 var(--radius) var(--radius);
	}

	.log-entry { margin-bottom: 6px; }

	.log-header {
		display: flex;
		gap: 4px;
		flex-wrap: wrap;
	}

	.log-time { color: var(--text-muted); }
	.log-level { font-weight: 700; }
	.log-scope { color: var(--text-muted); }
	.log-action { color: var(--text-secondary); }
	.log-target { color: var(--text-primary); }

	/* Level badge colors (header line) */
	.level-badge-error { color: var(--error, #ef4444); }
	.level-badge-warn { color: var(--warning, #eab308); }
	.level-badge-info { color: var(--accent, #60a5fa); }
	.level-badge-full { color: #a78bfa; }
	.level-badge-debug { color: var(--text-muted); }

	.log-message {
		padding-left: 24px;
		word-break: break-word;
	}

	/* Level message colors (content line) — must meet WCAG contrast on both themes */
	.level-msg-error { color: var(--text-primary); }
	.level-msg-warn { color: var(--text-primary); }
	.level-msg-info { color: var(--text-primary); }
	.level-msg-full { color: var(--text-secondary); }
	.level-msg-debug { color: var(--text-muted); }

	.empty-feed {
		color: var(--text-muted);
		text-align: center;
		padding: 48px 0;
		font-family: sans-serif;
		font-size: 14px;
	}

	/* Chips */
	.chip-bar {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 12px 16px;
		border-bottom: 1px solid var(--border);
		flex-wrap: wrap;
	}

	.chip {
		padding: 4px 12px;
		border-radius: 12px;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		color: var(--text-muted);
		font-size: 11px;
		cursor: pointer;
		font-family: sans-serif;
		white-space: nowrap;
	}

	.chip:hover { border-color: var(--accent); }

	.chip.active {
		background: var(--accent);
		color: white;
		border-color: var(--accent);
	}

	.sub-chip {
		padding: 3px 8px;
		border-radius: 10px;
		background: rgba(59, 130, 246, 0.15);
		color: var(--accent);
		font-size: 10px;
		cursor: pointer;
		font-family: sans-serif;
		border: none;
	}

	.sub-chip.active {
		background: var(--accent);
		color: white;
	}

	.chip-separator {
		color: var(--border);
		margin: 0 2px;
	}

	.search-input {
		padding: 4px 10px;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: 6px;
		color: var(--text-secondary);
		font-size: 11px;
		font-family: monospace;
		width: 160px;
	}

	/* Level bar */
	.level-bar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 6px;
		padding: 6px 16px;
		border-bottom: 1px solid var(--border);
		font-family: sans-serif;
		flex-wrap: wrap;
	}

	.level-chips {
		display: flex;
		align-items: center;
		gap: 6px;
	}

	.level-actions {
		display: flex;
		align-items: center;
		gap: 6px;
	}

	.level-chip {
		padding: 2px 8px;
		border-radius: 8px;
		font-size: 10px;
		background: var(--bg-secondary);
		cursor: pointer;
		border: none;
		color: var(--text-muted);
	}

	.level-chip.active {
		background: var(--accent);
		color: white;
	}

	.level-label {
		font-size: 10px;
		color: var(--text-muted);
		text-transform: uppercase;
		margin-right: 4px;
	}

	.log-count {
		font-size: 10px;
		color: var(--text-muted);
		font-family: sans-serif;
	}

	/* Load more */
	.load-more {
		text-align: center;
		padding: 12px;
	}

	.btn-load-more {
		padding: 6px 16px;
		border: 1px solid var(--border);
		border-radius: 6px;
		color: var(--text-muted);
		font-size: 11px;
		cursor: pointer;
		background: none;
		font-family: sans-serif;
	}
</style>
