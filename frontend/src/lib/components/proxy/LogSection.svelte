<script lang="ts">
	// EX-49..54 — журнал процесса: автоскролл, «К последним строкам», счётчик,
	// «Копировать», тумблер отладки (только FreeTurn-клиент: у wdtt флага -debug нет).
	import { tick } from 'svelte';
	import { ArrowDown } from 'lucide-svelte';
	import { Button, FieldHint, Toggle } from '$lib/components/ui';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { notifications } from '$lib/stores/notifications';
	import DetailSection from './DetailSection.svelte';

	interface Props {
		log?: string;
		/** Часы роутера — чтобы сверять с метками строк. */
		routerClock?: string;
		/** Пояснение секции под (i): SH-73 у «Раздачи». */
		hint?: string;
		/**
		 * EX-53: тумблер отладки есть у FreeTurn-клиента и FreeTurn-сервера —
		 * `-debug` знают только их бинари (у wdtt поле мёртвое).
		 */
		showDebug?: boolean;
		debug?: boolean;
		/** (i) у тумблера: EX-54 у клиента, SH-86 у сервера. */
		debugHint?: string;
		ondebug?: (on: boolean) => void;
	}

	let {
		log = '',
		routerClock = '',
		hint = '',
		showDebug = false,
		debug = false,
		debugHint = 'Отладочный вывод включится при следующем запуске процесса.',
		ondebug,
	}: Props = $props();

	let logEl = $state<HTMLPreElement | undefined>();
	let stickToBottom = $state(true);

	const text = $derived(log.trim());
	const lineCount = $derived(text ? text.split('\n').length : 0);

	$effect(() => {
		void text;
		if (!logEl || !stickToBottom) return;
		void tick().then(() => {
			if (logEl && stickToBottom) logEl.scrollTop = logEl.scrollHeight;
		});
	});

	function onScroll() {
		if (!logEl) return;
		stickToBottom = logEl.scrollHeight - logEl.scrollTop - logEl.clientHeight < 32;
	}

	function scrollToBottom() {
		stickToBottom = true;
		if (logEl) logEl.scrollTop = logEl.scrollHeight;
	}

	async function copyLog() {
		if (!text) return;
		if (!(await copyToClipboard(text))) notifications.error('Не удалось скопировать');
	}
</script>

<DetailSection title="Журнал" {hint} aside={toolbar}>
	<pre bind:this={logEl} class="log" onscroll={onScroll}>{text}</pre>
	{#if showDebug}
		<div class="debug-row">
			<Toggle label="Отладочный вывод" checked={debug} onchange={(v) => ondebug?.(v)} />
			<FieldHint text={debugHint} ariaLabel="Подсказка: отладочный вывод" />
		</div>
	{/if}
</DetailSection>

{#snippet toolbar()}
	{#if !stickToBottom}
		<Button variant="ghost" size="sm" onclick={scrollToBottom}>
			{#snippet iconBefore()}<ArrowDown size={12} />{/snippet}
			К последним строкам
		</Button>
	{/if}
	{#if routerClock}<span class="meta">{routerClock}</span>{/if}
	<span class="meta">строк: {lineCount}</span>
	<Button variant="ghost" size="sm" disabled={!text} onclick={copyLog}>Копировать</Button>
{/snippet}

<style>
	.log {
		margin: 0;
		padding: 0.75rem;
		background: var(--color-bg-tertiary);
		border-radius: var(--radius);
		font-family: var(--font-mono);
		font-size: 0.75rem;
		color: var(--color-text-secondary);
		max-height: 220px;
		overflow: auto;
		white-space: pre-wrap;
		word-break: break-word;
	}

	.meta {
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}

	.debug-row {
		display: flex;
		align-items: center;
		gap: 0.35rem;
		margin-top: 0.625rem;
	}
</style>
