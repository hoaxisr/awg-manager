<script lang="ts">
	import { tick } from 'svelte';
	import { ChevronRight, ChevronDown, ArrowDown } from 'lucide-svelte';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { notifications } from '$lib/stores/notifications';
	import LogInstanceSwitcher, { type LogInstanceItem } from './LogInstanceSwitcher.svelte';

	interface Props {
		log?: string;
		title?: string;
		maxHeight?: string;
		/** Текущее время роутера — для сверки с метками в логе freeturn. */
		routerClock?: string;
		/** Внутри SettingRow — без внешней рамки секции. */
		embedded?: boolean;
		/** Показать тихий переключатель -debug рядом с логом. */
		showDebugToggle?: boolean;
		debug?: boolean;
		/** Свёрнут ли лог по умолчанию. */
		defaultCollapsed?: boolean;
		/** Компактный переключатель клиентов/серверов в шапке лога. */
		instances?: LogInstanceItem[];
		selectedInstanceId?: string;
		onSelectInstance?: (id: string) => void;
	}

	let {
		log = '',
		title = 'Лог процесса',
		maxHeight = '320px',
		routerClock = '',
		embedded = false,
		showDebugToggle = false,
		debug = $bindable(false),
		defaultCollapsed = false,
		instances = [],
		selectedInstanceId = '',
		onSelectInstance
	}: Props = $props();

	let collapsed = $state(defaultCollapsed);

	let logEl: HTMLPreElement | undefined = $state();
	let stickToBottom = $state(true);

	const lineCount = $derived(log?.trim() ? log.trim().split('\n').length : 0);
	const displayLog = $derived(log?.trim() || 'лог пуст — запустите процесс');

	$effect(() => {
		void displayLog;
		if (!logEl || !stickToBottom) return;
		void tick().then(() => {
			if (logEl && stickToBottom) {
				logEl.scrollTop = logEl.scrollHeight;
			}
		});
	});

	function onScroll() {
		if (!logEl) return;
		const dist = logEl.scrollHeight - logEl.scrollTop - logEl.clientHeight;
		stickToBottom = dist < 32;
	}

	function scrollToBottom() {
		stickToBottom = true;
		if (logEl) {
			logEl.scrollTop = logEl.scrollHeight;
		}
	}

	async function copyLog() {
		const text = log?.trim();
		if (!text) return;
		if (await copyToClipboard(text)) {
			notifications.success('Лог скопирован');
		} else {
			notifications.error('Не удалось скопировать');
		}
	}
</script>

{#snippet logToolbar(header = false)}
	<div class="ft-log-toolbar" class:ft-log-toolbar--header={header}>
		{#if header}
			<button type="button" class="ft-log-toggle" onclick={() => (collapsed = !collapsed)} aria-expanded={!collapsed}>
				<span class="ft-log-chevron">
					{#if collapsed}<ChevronRight size={14} />{:else}<ChevronDown size={14} />{/if}
				</span>
				<span class="section-label">{title}</span>
				{#if lineCount && collapsed}
					<span class="ft-log-meta">({lineCount} строк)</span>
				{/if}
			</button>
		{/if}
		{#if onSelectInstance}
			<LogInstanceSwitcher items={instances} selectedId={selectedInstanceId} onSelect={onSelectInstance} />
		{/if}
		<div class="ft-log-toolbar-actions">
			{#if !stickToBottom}
				<button type="button" class="ft-log-follow" onclick={scrollToBottom}>
					<ArrowDown size={12} /> К последним строкам
				</button>
			{/if}
			{#if lineCount}
				<span class="ft-log-meta">{lineCount} строк</span>
				<button type="button" class="ft-log-copy" onclick={copyLog} title="Скопировать лог в буфер">
					Копировать
				</button>
			{/if}
			{#if routerClock}
				<span class="ft-log-meta" title="Время роутера — сверяйте с метками в строках лога">
					{routerClock}
				</span>
			{/if}
			{#if showDebugToggle}
				<label class="ft-log-debug" title="Подробный лог freeturn (-debug). Нужен перезапуск процесса.">
					<input type="checkbox" bind:checked={debug} />
					<span>debug</span>
				</label>
			{/if}
		</div>
	</div>
{/snippet}

{#if embedded}
	<div class="ft-log-wrap ft-span">
		{@render logToolbar(false)}
		{#if !collapsed}
			<pre
				bind:this={logEl}
				class="ft-log-box"
				style:max-height={maxHeight}
				onscroll={onScroll}
			>{displayLog}</pre>
		{/if}
	</div>
{:else}
	<section class="ft-log-panel">
		{@render logToolbar(true)}
		{#if !collapsed}
			<pre
				bind:this={logEl}
				class="ft-log-box"
				style:max-height={maxHeight}
				onscroll={onScroll}
			>{displayLog}</pre>
		{/if}
	</section>
{/if}

<style>
	.ft-log-toggle {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		padding: 0;
		border: none;
		background: none;
		cursor: pointer;
		color: inherit;
		font: inherit;
	}

	.ft-log-chevron {
		display: inline-flex;
		align-items: center;
		color: var(--color-text-secondary);
	}

	.ft-log-panel {
		margin-top: 0.75rem;
		padding: 0.75rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
	}

	.ft-log-wrap {
		min-width: 0;
	}

	.ft-log-toolbar {
		display: flex;
		align-items: center;
		justify-content: flex-end;
		gap: 0.5rem;
		min-height: 1.25rem;
		margin-bottom: 0.375rem;
	}

	.ft-log-toolbar-actions {
		display: flex;
		align-items: center;
		justify-content: flex-end;
		gap: 0.5rem;
		flex-shrink: 0;
		width: 100%;
	}

	.ft-log-toolbar--header {
		justify-content: flex-start;
		flex-wrap: nowrap;
		margin-bottom: 0.5rem;
	}

	.ft-log-toolbar--header .ft-log-toggle {
		flex-shrink: 0;
	}

	.ft-log-toolbar--header .ft-log-toolbar-actions {
		width: auto;
		margin-left: auto;
	}

	.ft-log-debug {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
		font-size: 0.625rem;
		font-family: var(--font-mono);
		color: var(--color-text-secondary);
		opacity: 0.55;
		cursor: pointer;
		user-select: none;
		flex-shrink: 0;
	}

	.ft-log-debug:hover {
		opacity: 0.85;
	}

	.ft-log-debug:has(input:checked) {
		opacity: 0.8;
	}

	.ft-log-debug input {
		width: 0.75rem;
		height: 0.75rem;
		margin: 0;
		cursor: pointer;
		accent-color: var(--color-text-secondary);
	}

	.ft-log-follow,
	.ft-log-copy {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
		padding: 0.125rem 0.5rem;
		border: 1px solid var(--color-accent-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
		color: var(--color-accent);
		font-size: 0.6875rem;
		cursor: pointer;
	}

	.ft-log-follow:hover,
	.ft-log-copy:hover {
		background: var(--color-bg-secondary);
	}

	.ft-log-meta {
		font-size: 0.6875rem;
		color: var(--color-text-secondary);
		white-space: nowrap;
	}

	.ft-log-box {
		overflow-y: auto;
		padding: 0.5rem 0.625rem;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-secondary);
		font-family: var(--font-mono);
		font-size: 0.75rem;
		line-height: 1.45;
		white-space: pre-wrap;
		word-break: break-all;
		margin: 0;
	}

	.ft-span {
		grid-column: 1 / -1;
	}
</style>
