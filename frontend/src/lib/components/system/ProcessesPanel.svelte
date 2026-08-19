<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { api, type SystemProcSnapshot, type SystemProcessItem } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Button, Card } from '$lib/components/ui';
	import { errorMessage } from '$lib/utils/errorMessage';
	import { Power, Play } from 'lucide-svelte';
	import {
		ProcessesToolbar,
		HardwareDashboard,
		ProcessTable,
		KillProcessModal,
		type SortField,
	} from './processes';

	// Master on/off state (persisted)
	let enabled = $state<boolean>(true);

	let snapshot = $state<SystemProcSnapshot | null>(null);
	let loading = $state(false);
	let initialLoaded = $state(false);
	let autoRefreshInterval = $state<number>(2); // seconds, 0 = paused
	let timer = $state<ReturnType<typeof setInterval> | null>(null);

	// Search and filter
	let searchQuery = $state('');
	let showKernelThreads = $state(false);

	// Sorting
	let sortField = $state<SortField>('cpu');
	let sortAsc = $state(false);

	// Process Kill modal state
	let killTarget = $state<SystemProcessItem | null>(null);
	let killSignal = $state<'SIGTERM' | 'SIGKILL'>('SIGTERM');
	let killing = $state(false);

	onMount(() => {
		const saved = localStorage.getItem('awgm.proctop.enabled');
		if (saved !== null) {
			enabled = saved === 'true';
		}
		if (enabled) {
			void fetchSnapshot(true);
			setupTimer();
		}

		document.addEventListener('visibilitychange', onVisibilityChange);
		return () => {
			document.removeEventListener('visibilitychange', onVisibilityChange);
			if (timer) clearInterval(timer);
		};
	});

	onDestroy(() => {
		if (timer) clearInterval(timer);
	});

	function onVisibilityChange() {
		if (document.hidden) {
			if (timer) clearInterval(timer);
		} else if (enabled) {
			setupTimer();
			void fetchSnapshot(false);
		}
	}

	function toggleMasterEnabled() {
		enabled = !enabled;
		localStorage.setItem('awgm.proctop.enabled', String(enabled));
		if (enabled) {
			void fetchSnapshot(true);
			setupTimer();
		} else {
			if (timer) clearInterval(timer);
		}
	}

	function setupTimer() {
		if (timer) clearInterval(timer);
		if (enabled && autoRefreshInterval > 0) {
			timer = setInterval(() => {
				if (!document.hidden) {
					void fetchSnapshot(false);
				}
			}, autoRefreshInterval * 1000);
		}
	}

	function setRefreshInterval(sec: number) {
		autoRefreshInterval = sec;
		setupTimer();
	}

	async function fetchSnapshot(showSpinner = false) {
		if (!enabled && !showSpinner) return;
		if (showSpinner) loading = true;
		try {
			const res = await api.systemProcSnapshot();
			snapshot = res;
			initialLoaded = true;
		} catch (e) {
			if (showSpinner) {
				notifications.error(errorMessage(e, 'Не удалось получить данные о процессах'));
			}
		} finally {
			if (showSpinner) loading = false;
		}
	}

	function toggleSort(field: SortField) {
		if (sortField === field) {
			sortAsc = !sortAsc;
		} else {
			sortField = field;
			sortAsc = field === 'name' || field === 'user' || field === 'pid';
		}
	}

	const filteredProcesses = $derived.by(() => {
		if (!snapshot?.processes) return [];
		let list = snapshot.processes;

		// Filter kernel threads if not requested
		if (!showKernelThreads) {
			list = list.filter((p: SystemProcessItem) => !p.isKernel && !p.cmdline.startsWith('[') && p.pid > 2);
		}

		// Filter by search query
		const q = searchQuery.trim().toLowerCase();
		if (q) {
			list = list.filter((p: SystemProcessItem) => {
				return (
					p.name.toLowerCase().includes(q) ||
					p.cmdline.toLowerCase().includes(q) ||
					p.user.toLowerCase().includes(q) ||
					String(p.pid).includes(q)
				);
			});
		}

		// Sort
		return [...list].sort((a, b) => {
			let res = 0;
			switch (sortField) {
				case 'cpu':
					res = a.cpuPercent - b.cpuPercent;
					break;
				case 'mem':
					res = a.memoryRss - b.memoryRss;
					break;
				case 'pid':
					res = a.pid - b.pid;
					break;
				case 'name':
					res = a.name.localeCompare(b.name);
					break;
				case 'user':
					res = a.user.localeCompare(b.user);
					break;
				case 'threads':
					res = a.threads - b.threads;
					break;
				case 'state':
					res = a.state.localeCompare(b.state);
					break;
			}
			return sortAsc ? res : -res;
		});
	});

	async function handleKillProcess() {
		if (!killTarget) return;
		killing = true;
		const isSelf = killTarget.isSelf;
		try {
			await api.systemProcKill({ pid: killTarget.pid, signal: killSignal });
			notifications.success(`Сигнал ${killSignal} отправлен процессу PID ${killTarget.pid}`);
			killTarget = null;
			if (!isSelf) {
				await fetchSnapshot(false);
			}
		} catch (e) {
			notifications.error(errorMessage(e, 'Ошибка завершения процесса'));
		} finally {
			killing = false;
		}
	}
</script>

<div class="proctop-root">
	<ProcessesToolbar
		{enabled}
		{loading}
		interval={autoRefreshInterval}
		{showKernelThreads}
		{searchQuery}
		processCount={filteredProcesses.length}
		ontoggleenabled={toggleMasterEnabled}
		onrefresh={() => fetchSnapshot(true)}
		onintervalchange={setRefreshInterval}
		ontogglekernelthreads={() => (showKernelThreads = !showKernelThreads)}
		onsearchchange={(value) => (searchQuery = value)}
	/>

	{#if !enabled}
		<!-- Disabled Banner -->
		<Card padding="md">
			<div class="disabled-placeholder">
				<Power size={36} class="muted-icon" />
				<div class="disabled-text">
					<h3>Мониторинг процессов отключен</h3>
					<p>Для экономии вычислительных ресурсов роутера фоновый сбор метрик и опрос процессов остановлен.</p>
				</div>
				<Button variant="primary" onclick={toggleMasterEnabled}>
					{#snippet iconBefore()}<Play size={14} />{/snippet}
					Включить мониторинг
				</Button>
			</div>
		</Card>
	{:else}
		<HardwareDashboard {snapshot} />

		<ProcessTable
			processes={filteredProcesses}
			{loading}
			{initialLoaded}
			{sortField}
			{sortAsc}
			onsort={toggleSort}
			onkill={(proc) => {
				killTarget = proc;
				killSignal = 'SIGTERM';
			}}
		/>
	{/if}
</div>

<!-- Terminate Process Confirmation Modal -->
<KillProcessModal
	target={killTarget}
	bind:signal={killSignal}
	{killing}
	onclose={() => (killTarget = null)}
	onconfirm={handleKillProcess}
/>

<style>
	.proctop-root {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
	}


	.disabled-placeholder {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		text-align: center;
		padding: 3rem 1rem;
		gap: 0.75rem;
	}
	:global(.muted-icon) {
		color: var(--color-text-muted);
		opacity: 0.5;
	}
	.disabled-text h3 {
		margin: 0;
		font-size: 1.05rem;
		color: var(--color-text-primary);
	}
	.disabled-text p {
		margin: 0.25rem 0 0 0;
		font-size: 0.85rem;
		color: var(--color-text-muted);
		max-width: 450px;
	}

</style>
