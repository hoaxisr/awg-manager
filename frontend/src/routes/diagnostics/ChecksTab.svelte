<script lang="ts">
	import { onDestroy } from 'svelte';
	import { api } from '$lib/api/client';
	import { Button, Modal } from '$lib/components/ui';
	import { diagnosticsStore } from '$lib/stores/diagnostics';
	import { developFeedbackIncidentPending } from '$lib/stores/developFeedbackIncident';
	import type { DiagnosticsTargetSeed } from '$lib/stores/diagnostics';
	import { notifications } from '$lib/stores/notifications';
	import type { DiagEvent } from '$lib/types';
	import {
		ChecksToolbar,
		ChecksGroup,
		ClientDnsCheck,
	} from '$lib/components/diagnostics';
	import { collectDiagnosticsEnvironmentSnapshot } from '$lib/utils/diagnostics-environment';
	import {
		GLOBAL_TARGET_ID,
		type DiagTestEvent,
		type TargetSummary,
	} from '$lib/types';
	import type { GroupLed } from '$lib/components/diagnostics/ChecksGroup.svelte';
	import { GITHUB_BUG_REPORT_URL } from '$lib/utils/githubFeedback';

	interface Props {
		tunnels: DiagnosticsTargetSeed[];
	}

	let { tunnels }: Props = $props();

	const DIAGNOSTICS_REPORT_FILENAME = 'diagnostics.json';

	let includeRestart = $state(false);
	let downloadingReport = $state(false);
	let creatingIncident = $state(false);
	let incidentModalOpen = $state(false);
	let awaitingIncidentModal = $state(false);
	let eventSource: EventSource | null = null;

	// Hide stopped tunnels — diagnostics only run on running ones.
	let visibleTunnels = $derived(tunnels.filter((t) => t.status === 'running'));

	$effect(() => {
		diagnosticsStore.seedTargets(visibleTunnels);
	});

	let dnsCheckTrigger = $state(0);
	/** ID туннеля, который сейчас проверяется кнопкой ▷. Пусто = полный прогон. */
	let activeTunnelId = $state('');

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

	function makeEventHandler(single: boolean) {
		return (event: DiagEvent) => {
			switch (event.type) {
				case 'phase':
					diagnosticsStore.setPhase(event.label ?? '');
					break;
				case 'test':
					if (event.test) diagnosticsStore.addTest(event.test, visibleTunnels);
					break;
				case 'done':
					activeTunnelId = '';
					if (single) {
						diagnosticsStore.finishSingle();
					} else if (event.summary) {
						diagnosticsStore.finish(event.summary);
					}
					cleanup();
					break;
				case 'error':
					activeTunnelId = '';
					diagnosticsStore.fail(event.message ?? 'Ошибка диагностики');
					cleanup();
					break;
			}
		};
	}

	function start() {
		activeTunnelId = '';
		diagnosticsStore.start(visibleTunnels);
		expandedMap = {};
		cleanup();
		eventSource = api.streamDiagnostics(
			includeRestart,
			makeEventHandler(false),
			() => diagnosticsStore.fail('Соединение потеряно'),
		);
		// Let the SSE connection register first so DNS/NDMS work does not race the same browser slot.
		queueMicrotask(() => {
			dnsCheckTrigger++;
		});
	}

	function startSingle(tunnelId: string) {
		diagnosticsStore.startSingleTunnel(tunnelId, visibleTunnels);
		expandedMap = { ...expandedMap, [tunnelId]: true };
		cleanup();
		activeTunnelId = tunnelId;
		eventSource = api.streamDiagnostics(
			false,
			makeEventHandler(true),
			() => { activeTunnelId = ''; diagnosticsStore.fail('Соединение потеряно'); },
			tunnelId,
		);
	}

	async function downloadCurrentReport() {
		let environment: unknown;
		try {
			environment = await collectDiagnosticsEnvironmentSnapshot();
		} catch (e) {
			environment = {
				source: 'frontend',
				partial: true,
				errors: [{ scope: 'environment', message: e instanceof Error ? e.message : String(e) }],
			};
		}
		await api.downloadDiagnosticsReport(environment);
	}

	async function downloadReport() {
		downloadingReport = true;
		try {
			await downloadCurrentReport();
		} catch (e) {
			diagnosticsStore.fail((e as Error).message);
		} finally {
			downloadingReport = false;
		}
	}

	function openIncidentModal() {
		incidentModalOpen = true;
	}

	$effect(() => {
		if (!$developFeedbackIncidentPending) return;
		developFeedbackIncidentPending.set(false);
		awaitingIncidentModal = true;
		if (!running) {
			start();
		}
	});

	$effect(() => {
		if (!awaitingIncidentModal || running) return;
		awaitingIncidentModal = false;
		openIncidentModal();
	});

	function buildIncidentIssueBody(): string {
		const pagePath = typeof window !== 'undefined'
			? `${window.location.pathname}${window.location.search}${window.location.hash}`
			: '/diagnostics';
		return [
			'## Что произошло',
			'',
			'<!-- Опишите проблему своими словами -->',
			'',
			'## Что ожидалось',
			'',
			'<!-- Что должно было произойти -->',
			'',
			'## Контекст',
			'',
			`- Страница: \`${pagePath}\``,
			`- Создано: \`${new Date().toISOString()}\``,
			`- Отчёт диагностики: прикрепите скачанный файл \`${DIAGNOSTICS_REPORT_FILENAME}\``,
			'',
			'## Важно',
			'',
			'Это публичный issue. Не прикладывайте приватные ключи, пароли, токены, реальные адреса и домены, если не хотите их раскрывать.',
		].join('\n');
	}

	async function copyText(text: string): Promise<boolean> {
		if (typeof navigator !== 'undefined' && navigator.clipboard?.writeText) {
			try {
				await navigator.clipboard.writeText(text);
				return true;
			} catch {
				// Fall back to execCommand below.
			}
		}
		if (typeof document === 'undefined') return false;
		const textarea = document.createElement('textarea');
		textarea.value = text;
		textarea.setAttribute('readonly', '');
		textarea.style.position = 'fixed';
		textarea.style.left = '-9999px';
		document.body.appendChild(textarea);
		textarea.select();
		let copied = false;
		try {
			copied = document.execCommand('copy');
		} catch {
			copied = false;
		}
		document.body.removeChild(textarea);
		return copied;
	}

	function openPendingIssueWindow(): Window | null {
		if (typeof window === 'undefined') return null;
		const issueWindow = window.open('', '_blank');
		if (issueWindow) {
			issueWindow.opener = null;
		}
		return issueWindow;
	}

	async function confirmCreateIncident() {
		if (!hasReport) {
			incidentModalOpen = false;
			return;
		}

		creatingIncident = true;
		downloadingReport = true;
		const issueWindow = openPendingIssueWindow();
		try {
			await downloadCurrentReport();
			const body = buildIncidentIssueBody();
			const copied = await copyText(body);
			if (copied) {
				notifications.success('Текст issue скопирован. Прикрепите скачанный отчёт на GitHub.');
			} else {
				notifications.warning('Отчёт скачан. Текст issue не удалось скопировать автоматически.');
			}
			const url = GITHUB_BUG_REPORT_URL;
			if (issueWindow) {
				issueWindow.location.href = url;
			} else if (typeof window !== 'undefined') {
				window.open(url, '_blank', 'noopener,noreferrer');
			}
			incidentModalOpen = false;
		} catch (e) {
			issueWindow?.close();
			diagnosticsStore.fail((e as Error).message);
		} finally {
			creatingIncident = false;
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
		onCreateIncident={openIncidentModal}
		{creatingIncident}
	/>

	<!-- DNS-маршрутизация — первая группа, отдельный flow -->
	<ClientDnsCheck triggerRun={dnsCheckTrigger} />

	<!-- Глобальные + per-tunnel группы -->
	{#each targets as target (target.id)}
		<div class="divider"></div>
		<ChecksGroup
			name={target.name}
			kind={target.kind}
			isGlobal={target.isGlobal}
			led={targetLed(target, running)}
			summary={targetSummary(target, running)}
			tests={targetTests(target)}
			expanded={!!expandedMap[target.id]}
			onToggle={() => toggle(target.id)}
			onRun={() => startSingle(target.id)}
			groupRunning={running && activeTunnelId === target.id}
			anyRunning={running}
		/>
	{/each}

	{#if visibleTunnels.length === 0}
		<div class="divider"></div>
		<div class="empty-tunnels">
			Нет запущенных туннелей. Per-tunnel проверки будут пропущены.
		</div>
	{/if}
</div>

<Modal
	open={incidentModalOpen}
	title={hasReport ? 'Создать инцидент на GitHub' : 'Сначала выполните диагностику'}
	size="md"
	onclose={() => (incidentModalOpen = false)}
>
	<div class="incident-modal-body">
		{#if hasReport}
			<p>
				AWG Manager — open-source проект без службы поддержки и SLA. Инцидент попадёт
				в публичный GitHub issue; ответ и исправление не гарантируются.
			</p>
			<p>
				Будет скачан диагностический отчёт. Проверьте файл перед публикацией и
				<strong>прикрепите его к issue вручную</strong>.
			</p>
			<p class="incident-warning">
				Не публикуйте приватные ключи, пароли, токены, реальные адреса и домены,
				если не хотите их раскрывать.
			</p>
		{:else}
			<p>
				Инцидент лучше создавать после полного прогона диагностики, чтобы к нему
				можно было приложить отчёт.
			</p>
			<p class="incident-warning">
				Запустите проверки, дождитесь завершения и вернитесь к кнопке «Инцидент».
			</p>
		{/if}
	</div>

	{#snippet actions()}
		{#if hasReport}
			<Button variant="secondary" size="md" onclick={() => (incidentModalOpen = false)} disabled={creatingIncident}>
				Отмена
			</Button>
			<Button variant="primary" size="md" onclick={confirmCreateIncident} loading={creatingIncident}>
				Скачать и открыть GitHub
			</Button>
		{:else}
			<Button variant="primary" size="md" onclick={() => (incidentModalOpen = false)}>
				Понятно
			</Button>
		{/if}
	{/snippet}
</Modal>

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

	.incident-modal-body {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
		font-size: 0.875rem;
		line-height: 1.5;
		color: var(--color-text-secondary);
	}

	.incident-modal-body p {
		margin: 0;
	}

	.incident-warning {
		padding: 0.75rem;
		border: 1px solid var(--color-warning-border);
		border-radius: var(--radius-sm);
		background: var(--color-warning-tint);
		color: var(--color-warning);
	}
</style>
