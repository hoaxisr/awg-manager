<script lang="ts">
	import { onDestroy } from 'svelte';
	import { api } from '$lib/api/client';
	import { diagnosticsStore } from '$lib/stores/diagnostics';
	import {
		ChecksToolbar,
		ChecksGroup,
		ClientDnsCheck,
	} from '$lib/components/diagnostics';
	import {
		GLOBAL_TARGET_ID,
		type DiagTestEvent,
		type TunnelListItem,
		type TargetSummary,
	} from '$lib/types';
	import type { GroupLed } from '$lib/components/diagnostics/ChecksGroup.svelte';

	interface Props {
		tunnels: TunnelListItem[];
	}

	let { tunnels }: Props = $props();

	let includeRestart = $state(false);
	let downloadingReport = $state(false);
	let eventSource: EventSource | null = null;

	// Hide stopped tunnels — diagnostics only run on running ones.
	let visibleTunnels = $derived(tunnels.filter((t) => t.status === 'running'));

	$effect(() => {
		diagnosticsStore.seedTargets(visibleTunnels);
	});

	let running = $derived($diagnosticsStore.running);
	let currentPhase = $derived($diagnosticsStore.currentPhase);
	let errorMessage = $derived($diagnosticsStore.errorMessage);
	let hasReport = $derived(!!$diagnosticsStore.summary?.hasReport);
	let targets = $derived($diagnosticsStore.targets);
	let tests = $derived($diagnosticsStore.tests);
	let hasResults = $derived(tests.length > 0);

	let summaryCounts = $derived.by(() => {
		let pass = 0, warn = 0, fail = 0;
		for (const t of tests) {
			if (t.status === 'pass') pass++;
			else if (t.status === 'warn' || t.status === 'skip') warn++;
			else if (t.status === 'fail' || t.status === 'error') fail++;
		}
		return { pass, warn, fail };
	});

	// Per-group expanded state. Auto-expand groups with non-pass results.
	let expandedMap = $state<Record<string, boolean>>({});

	$effect(() => {
		for (const t of targets) {
			if (
				(t.overallLed === 'red' || t.overallLed === 'yellow') &&
				expandedMap[t.id] === undefined
			) {
				expandedMap[t.id] = true;
			}
		}
	});

	function toggle(id: string) {
		expandedMap[id] = !expandedMap[id];
	}

	function targetTests(target: TargetSummary): DiagTestEvent[] {
		if (target.id === GLOBAL_TARGET_ID) {
			return tests.filter((t) => !t.tunnelId);
		}
		return tests.filter((t) => t.tunnelId === target.id);
	}

	function targetLed(target: TargetSummary, currentlyRunning: boolean): GroupLed {
		if (currentlyRunning && target.counts.total === 0) return 'running';
		return target.overallLed;
	}

	function targetSummary(target: TargetSummary, currentlyRunning: boolean): string {
		const c = target.counts;
		if (currentlyRunning && c.total === 0) return '⟳';
		if (c.total === 0) {
			return target.isGlobal ? '— · 0/5' : '—';
		}
		const fails = c.fail + c.error;
		const warns = c.warn + c.skip;
		if (fails > 0) return `${c.pass}/${c.total} ✕`;
		if (warns > 0) return `${c.pass}/${c.total} △`;
		return `${c.pass}/${c.total} ✓`;
	}

	function start() {
		diagnosticsStore.start(visibleTunnels);
		expandedMap = {};
		cleanup();

		eventSource = api.streamDiagnostics(
			includeRestart,
			(event) => {
				switch (event.type) {
					case 'phase':
						diagnosticsStore.setPhase(event.label ?? '');
						break;
					case 'test':
						if (event.test) diagnosticsStore.addTest(event.test, visibleTunnels);
						break;
					case 'done':
						if (event.summary) diagnosticsStore.finish(event.summary);
						cleanup();
						break;
					case 'error':
						diagnosticsStore.fail(event.message ?? 'Ошибка диагностики');
						cleanup();
						break;
				}
			},
			() => diagnosticsStore.fail('Соединение потеряно'),
		);
	}

	async function downloadReport() {
		downloadingReport = true;
		try {
			await api.downloadDiagnosticsReport();
		} catch (e) {
			diagnosticsStore.fail((e as Error).message);
		} finally {
			downloadingReport = false;
		}
	}

	function cleanup() {
		if (eventSource) {
			eventSource.close();
			eventSource = null;
		}
	}

	onDestroy(cleanup);
</script>

<div class="container">
	<ChecksToolbar
		{includeRestart}
		{running}
		{currentPhase}
		{hasReport}
		{downloadingReport}
		{hasResults}
		{summaryCounts}
		{errorMessage}
		onChangeIncludeRestart={(v) => (includeRestart = v)}
		onStart={start}
		onDownloadReport={downloadReport}
	/>

	<!-- DNS-маршрутизация — первая группа, отдельный flow -->
	<ClientDnsCheck />

	<!-- Глобальные + per-tunnel группы -->
	{#each targets as target (target.id)}
		<div class="divider"></div>
		<ChecksGroup
			name={target.name}
			led={targetLed(target, running)}
			summary={targetSummary(target, running)}
			tests={targetTests(target)}
			expanded={!!expandedMap[target.id]}
			onToggle={() => toggle(target.id)}
		/>
	{/each}

	{#if visibleTunnels.length === 0}
		<div class="divider"></div>
		<div class="empty-tunnels">
			Нет запущенных туннелей. Per-tunnel проверки будут пропущены.
		</div>
	{/if}
</div>

<style>
	.container {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		overflow: hidden;
	}

	.divider {
		height: 1px;
		background: var(--color-border);
		opacity: 0.5;
	}

	.empty-tunnels {
		padding: 14px;
		text-align: center;
		font-size: 12px;
		color: var(--color-text-muted);
		font-style: italic;
	}
</style>
