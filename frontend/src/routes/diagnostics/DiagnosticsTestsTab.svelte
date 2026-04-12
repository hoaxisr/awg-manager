<script lang="ts">
	import { onDestroy } from 'svelte';
	import { api } from '$lib/api/client';
	import type { DiagTestEvent, DiagDoneSummary, DiagMode, DiagRouteMode, TunnelListItem } from '$lib/types';
	import {
		DiagnosticsControls,
		DiagnosticsTestList,
		DiagnosticsSummary
	} from '$lib/components/diagnostics';
	import { notifications } from '$lib/stores/notifications';

	interface Props {
		tunnels: TunnelListItem[];
	}

	let { tunnels }: Props = $props();

	type PageState = 'idle' | 'running' | 'done' | 'error';

	let pageState = $state<PageState>('idle');
	let tests = $state<DiagTestEvent[]>([]);
	let currentPhase = $state('');
	let summary = $state<DiagDoneSummary | null>(null);
	let errorMessage = $state('');
	let lastMode = $state<DiagMode>('quick');
	let eventSource: EventSource | null = null;

	function cleanup() {
		if (eventSource) {
			eventSource.close();
			eventSource = null;
		}
	}

	function startDiagnostics(mode: DiagMode, restart: boolean, routeMode: DiagRouteMode, routeTunnelId: string) {
		cleanup();
		tests = [];
		summary = null;
		errorMessage = '';
		currentPhase = '';
		lastMode = mode;
		pageState = 'running';

		eventSource = api.streamDiagnostics(
			mode,
			restart,
			routeMode,
			routeTunnelId,
			(event) => {
				switch (event.type) {
					case 'phase':
						currentPhase = event.label ?? '';
						break;
					case 'test':
						if (event.test) {
							tests = [...tests, event.test];
						}
						break;
					case 'done':
						if (event.summary) {
							summary = event.summary;
						}
						pageState = 'done';
						currentPhase = '';
						cleanup();
						break;
					case 'error':
						errorMessage = event.message ?? 'Неизвестная ошибка';
						pageState = 'error';
						cleanup();
						break;
				}
			},
			() => {
				if (pageState === 'running') {
					errorMessage = 'Соединение потеряно';
					pageState = 'error';
				}
			}
		);
	}

	async function downloadReport() {
		try {
			await api.downloadDiagnosticsReport();
		} catch {
			notifications.error('Ошибка скачивания отчёта');
		}
	}

	onDestroy(cleanup);
</script>

<div class="settings-stack">
	<div class="card">
		<div class="card-body">
			{#if pageState === 'idle'}
				<DiagnosticsControls
					onStart={startDiagnostics}
					disabled={false}
					{tunnels}
				/>

			{:else if pageState === 'running'}
				<DiagnosticsTestList {tests} {currentPhase} />

			{:else if pageState === 'done' && summary}
				<DiagnosticsSummary
					{summary}
					onRestart={() => pageState = 'idle'}
					onDownload={summary.hasReport ? downloadReport : null}
				/>
				<DiagnosticsTestList {tests} />

			{:else if pageState === 'error'}
				<div class="error-box">
					<p>{errorMessage}</p>
				</div>
				{#if tests.length > 0}
					<DiagnosticsTestList {tests} />
				{/if}
				<button class="btn btn-primary" onclick={() => pageState = 'idle'}>
					Попробовать снова
				</button>
			{/if}
		</div>
	</div>
</div>

<style>
	.error-box {
		background: rgba(239, 68, 68, 0.1);
		border: 1px solid rgba(239, 68, 68, 0.3);
		border-radius: 8px;
		padding: 12px 16px;
		margin-bottom: 16px;
		color: var(--text-secondary);
	}
</style>
