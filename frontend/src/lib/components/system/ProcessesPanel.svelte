<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { api, type SystemProcSnapshot, type SystemProcessItem } from '$lib/api/client';
	import type { SystemCpuCore } from '$lib/api/clientSystem';
	import { notifications } from '$lib/stores/notifications';
	import { Button, Card, Modal } from '$lib/components/ui';
	import { formatBytes } from '$lib/utils/format';
	import { errorMessage } from '$lib/utils/errorMessage';
	import {
		RefreshCw,
		Cpu,
		HardDrive,
		Activity,
		Clock,
		Layers,
		Search,
		Square,
		AlertTriangle,
		ArrowUp,
		ArrowDown,
		Pause,
		Play,
		Power,
		Plus,
		Check,
	} from 'lucide-svelte';

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
	type SortField = 'cpu' | 'mem' | 'pid' | 'name' | 'user' | 'threads' | 'state';
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

	function formatUptime(sec: number): string {
		if (!sec) return '—';
		const days = Math.floor(sec / 86400);
		const hours = Math.floor((sec % 86400) / 3600);
		const minutes = Math.floor((sec % 3600) / 60);
		if (days > 0) return `${days}д ${hours}ч ${minutes}м`;
		if (hours > 0) return `${hours}ч ${minutes}м`;
		return `${minutes}м ${sec % 60}с`;
	}

	function getCpuClass(pct: number): 'low' | 'med' | 'high' {
		if (pct >= 80) return 'high';
		if (pct >= 45) return 'med';
		return 'low';
	}
</script>

<div class="proctop-root">
	<!-- Master Control Bar -->
	<Card padding="sm">
		<div class="proctop-master-bar">
			<div class="master-left">
				<button
					type="button"
					class="master-switch-btn"
					class:active={enabled}
					onclick={toggleMasterEnabled}
				>
					<Power size={14} />
					<span>{enabled ? 'Мониторинг активен' : 'Мониторинг выключен'}</span>
				</button>

				{#if enabled}
					<Button size="sm" variant="ghost" onclick={() => fetchSnapshot(true)} disabled={loading}>
						{#snippet iconBefore()}<RefreshCw size={14} class={loading ? 'spin' : ''} />{/snippet}
						Обновить
					</Button>

					<!-- Auto refresh interval picker -->
					<div class="interval-picker">
						<span class="picker-label">Интервал:</span>
						<button
							type="button"
							class="interval-btn"
							class:active={autoRefreshInterval === 1}
							onclick={() => setRefreshInterval(1)}
						>
							1с
						</button>
						<button
							type="button"
							class="interval-btn"
							class:active={autoRefreshInterval === 2}
							onclick={() => setRefreshInterval(2)}
						>
							2с
						</button>
						<button
							type="button"
							class="interval-btn"
							class:active={autoRefreshInterval === 5}
							onclick={() => setRefreshInterval(5)}
						>
							5с
						</button>
						<button
							type="button"
							class="interval-btn"
							class:active={autoRefreshInterval === 0}
							onclick={() => setRefreshInterval(0)}
							title="Приостановить опрос"
						>
							{#if autoRefreshInterval === 0}
								<Pause size={12} /> Пауза
							{:else}
								Пауза
							{/if}
						</button>
					</div>
				{/if}
			</div>

			{#if enabled}
				<div class="master-right">
					<button
						type="button"
						class="filter-threads-btn"
						class:active={showKernelThreads}
						onclick={() => (showKernelThreads = !showKernelThreads)}
					>
						{#if showKernelThreads}
							<Check size={14} class="icon-inline" /> Все потоки ядра
						{:else}
							<Plus size={14} class="icon-inline" /> Показать потоки ядра
						{/if}
					</button>

					<div class="search-box">
						<Search size={13} class="search-icon" />
						<input
							type="text"
							placeholder="Поиск по PID, имени, аргументам…"
							bind:value={searchQuery}
						/>
					</div>
					<span class="counter-badge">{filteredProcesses.length} процессов</span>
				</div>
			{/if}
		</div>
	</Card>

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
		<!-- 1. HTOP Style Hardware Dashboard -->
		<Card padding="md">
			<div class="dashboard-grid">
				<!-- Left: CPU Cores Bars -->
				<div class="dash-col cpu-col">
					<div class="col-head">
						<div class="col-title">
							<Cpu size={16} class="text-accent" />
							<span>Процессор (CPU)</span>
							{#if snapshot?.cpuModel}
								<span class="chip-model">{snapshot.cpuModel}</span>
							{/if}
						</div>
						{#if snapshot?.cores && snapshot.cores.length > 0}
							{@const totalCore = snapshot.cores.find((c: SystemCpuCore) => c.id === 'total') ?? snapshot.cores[0]}
							{@const level = getCpuClass(totalCore.usage)}
							<div class="pill-cpu-total level-{level}">
								<span class="dot"></span>
								<span>Всего: <strong>{totalCore.usage.toFixed(1)}%</strong></span>
							</div>
						{/if}
					</div>

					<div class="cores-list">
						{#if snapshot?.cores}
							{#each snapshot.cores.filter((c: SystemCpuCore) => c.id !== 'total') as core, i}
								{@const level = getCpuClass(core.usage)}
								<div class="core-card">
									<div class="core-header-line">
										<div class="core-id-group">
											<span class="core-idx">CPU {i + 1}</span>
											<span class="core-details-text">
												usr {core.user.toFixed(1)}% · sys {core.system.toFixed(1)}% {#if core.iowait > 0.5}· io {core.iowait.toFixed(1)}%{/if}
											</span>
										</div>
										<span class="core-percentage level-{level}">
											{core.usage.toFixed(1)}%
										</span>
									</div>

									<!-- Multi-segment htop bar -->
									<div class="htop-bar-track">
										<!-- User (emerald) -->
										<div
											class="bar-seg seg-user"
											style="width: {Math.min(100, Math.max(0, core.user))}%"
											title="Пользователь (User): {core.user.toFixed(1)}%"
										></div>
										<!-- System (amber) -->
										<div
											class="bar-seg seg-sys"
											style="width: {Math.min(100 - core.user, Math.max(0, core.system))}%"
											title="Система (Kernel): {core.system.toFixed(1)}%"
										></div>
										<!-- IOWait (orange/red) -->
										{#if core.iowait > 0}
											<div
												class="bar-seg seg-iowait"
												style="width: {Math.min(100 - core.user - core.system, Math.max(0, core.iowait))}%"
												title="Ожидание ввода-вывода: {core.iowait.toFixed(1)}%"
											></div>
										{/if}
									</div>
								</div>
							{/each}
						{:else}
							<div class="loading-hint">Сбор данных CPU…</div>
						{/if}
					</div>
				</div>

				<!-- Right: RAM & System Info -->
				<div class="dash-col mem-col">
					<div class="col-head">
						<div class="col-title">
							<HardDrive size={16} class="text-accent" />
							<span>Оперативная память</span>
						</div>
						{#if snapshot?.memory}
							<div class="pill-mem-total">
								<span>{formatBytes(snapshot.memory.used)} / {formatBytes(snapshot.memory.total)}</span>
								<strong>({snapshot.memory.usagePercent.toFixed(1)}%)</strong>
							</div>
						{/if}
					</div>

					{#if snapshot?.memory}
						<div class="mem-bars">
							<!-- RAM Bar -->
							<div class="core-card">
								<div class="core-header-line">
									<span class="core-idx">ОЗУ</span>
									<span class="core-details-text">
										Занято: {formatBytes(snapshot.memory.used)} · Кэш: {formatBytes(snapshot.memory.cached + snapshot.memory.buffers)} · Свободно: {formatBytes(snapshot.memory.available)}
									</span>
									<span class="core-percentage mem-pct-txt">
										{snapshot.memory.usagePercent.toFixed(1)}%
									</span>
								</div>
								<div class="htop-bar-track">
									<div
										class="bar-seg mem-used-seg"
										style="width: {Math.min(100, (snapshot.memory.used / snapshot.memory.total) * 100)}%"
										title="Занято приложениями: {formatBytes(snapshot.memory.used)}"
									></div>
									<div
										class="bar-seg mem-cached-seg"
										style="width: {Math.min(100 - (snapshot.memory.used / snapshot.memory.total) * 100, (snapshot.memory.cached / snapshot.memory.total) * 100)}%"
										title="Кэш и буферы: {formatBytes(snapshot.memory.cached + snapshot.memory.buffers)}"
									></div>
								</div>
							</div>

							<!-- Swap Bar (if configured) -->
							{#if snapshot.memory.swapTotal > 0}
								<div class="core-card">
									<div class="core-header-line">
										<span class="core-idx">Swap</span>
										<span class="core-details-text">
											{formatBytes(snapshot.memory.swapUsed)} / {formatBytes(snapshot.memory.swapTotal)}
										</span>
										<span class="core-percentage mem-pct-txt">
											{((snapshot.memory.swapUsed / snapshot.memory.swapTotal) * 100).toFixed(1)}%
										</span>
									</div>
									<div class="htop-bar-track">
										<div
											class="bar-seg swap-seg"
											style="width: {Math.min(100, (snapshot.memory.swapUsed / snapshot.memory.swapTotal) * 100)}%"
										></div>
									</div>
								</div>
							{/if}
						</div>
					{/if}

					<!-- System Meta Metrics -->
					{#if snapshot}
						{@const numCores = snapshot.cores.filter((c: SystemCpuCore) => c.id !== 'total').length || 2}
						{@const load1Pct = Math.round((snapshot.loadAvg[0] / numCores) * 100)}
						{@const loadStatusClass = load1Pct >= 80 ? 'high' : load1Pct >= 45 ? 'med' : 'low'}
						{@const loadStatusText = load1Pct >= 100 ? 'Перегрузка' : load1Pct >= 70 ? 'Высокая' : load1Pct >= 30 ? 'Умеренная' : 'Низкая'}

						<div class="sys-meta-grid">
							<div class="meta-item">
								<div class="meta-k" title="Load Average — среднее число задач в очереди за 1, 5 и 15 минут. Норма для вашего {numCores}-ядерного процессора — до {numCores}.00 (100%).">
									<Activity size={13} />
									<span>Средняя нагрузка:</span>
								</div>
								<div class="meta-v">
									<span class="load-status-pill level-{loadStatusClass}" title="Текущий уровень общей нагрузки за 1 минуту: {load1Pct}% от емкости {numCores} ядер">
										<span class="dot"></span>
										<span>{loadStatusText} ({load1Pct}%)</span>
									</span>
									<div class="load-badges-group" title="Load Average: 1 мин · 5 мин · 15 мин">
										<span class="load-badge">1м: <strong>{snapshot.loadAvg[0].toFixed(2)}</strong></span>
										<span class="load-badge">5м: <strong>{snapshot.loadAvg[1].toFixed(2)}</strong></span>
										<span class="load-badge">15м: <strong>{snapshot.loadAvg[2].toFixed(2)}</strong></span>
									</div>
								</div>
							</div>
							<div class="meta-item">
								<div class="meta-k"><Clock size={13} /> Аптайм роутера:</div>
								<div class="meta-v"><strong>{formatUptime(snapshot.uptimeSeconds)}</strong></div>
							</div>
							<div class="meta-item">
								<div class="meta-k"><Layers size={13} /> Задачи:</div>
								<div class="meta-v">
									<strong>{snapshot.processSummary.total}</strong> всего (<strong>{snapshot.processSummary.running}</strong> активных, <strong>{snapshot.processSummary.threads}</strong> потоков)
								</div>
							</div>
						</div>
					{/if}
				</div>
			</div>
		</Card>

		<!-- 2. Process Table (100% responsive, no bottom scrollbar) -->
		<Card padding="sm">
			<div class="table-container">
				{#if !initialLoaded && loading}
					<div class="empty-state">
						<RefreshCw size={24} class="spin" />
						<p>Сбор списка процессов роутера…</p>
					</div>
				{:else if filteredProcesses.length === 0}
					<div class="empty-state">
						<Search size={24} class="muted" />
						<p>Процессы не найдены по текущему запросу</p>
					</div>
				{:else}
					<table class="proc-table">
						<thead>
							<tr>
								<th class="th-sortable col-th-pid" onclick={() => toggleSort('pid')}>
									<div class="th-wrap">
										<span>PID</span>
										{#if sortField === 'pid'}
											{#if sortAsc}<ArrowUp size={11} />{:else}<ArrowDown size={11} />{/if}
										{/if}
									</div>
								</th>
								<th class="th-sortable col-th-user" onclick={() => toggleSort('user')}>
									<div class="th-wrap">
										<span>Польз.</span>
										{#if sortField === 'user'}
											{#if sortAsc}<ArrowUp size={11} />{:else}<ArrowDown size={11} />{/if}
										{/if}
									</div>
								</th>
								<th class="th-sortable col-th-state" onclick={() => toggleSort('state')}>
									<div class="th-wrap">
										<span>Сост.</span>
										{#if sortField === 'state'}
											{#if sortAsc}<ArrowUp size={11} />{:else}<ArrowDown size={11} />{/if}
										{/if}
									</div>
								</th>
								<th class="th-sortable col-th-threads" onclick={() => toggleSort('threads')}>
									<div class="th-wrap">
										<span>Потоки</span>
										{#if sortField === 'threads'}
											{#if sortAsc}<ArrowUp size={11} />{:else}<ArrowDown size={11} />{/if}
										{/if}
									</div>
								</th>
								<th class="th-sortable col-th-cpu" onclick={() => toggleSort('cpu')}>
									<div class="th-wrap">
										<span>CPU %</span>
										{#if sortField === 'cpu'}
											{#if sortAsc}<ArrowUp size={11} />{:else}<ArrowDown size={11} />{/if}
										{/if}
									</div>
								</th>
								<th class="th-sortable col-th-mem" onclick={() => toggleSort('mem')}>
									<div class="th-wrap">
										<span>Память</span>
										{#if sortField === 'mem'}
											{#if sortAsc}<ArrowUp size={11} />{:else}<ArrowDown size={11} />{/if}
										{/if}
									</div>
								</th>
								<th class="th-sortable col-th-cmd" onclick={() => toggleSort('name')}>
									<div class="th-wrap">
										<span>Команда / Процесс</span>
										{#if sortField === 'name'}
											{#if sortAsc}<ArrowUp size={11} />{:else}<ArrowDown size={11} />{/if}
										{/if}
									</div>
								</th>
								<th class="col-th-act">Стоп</th>
							</tr>
						</thead>
						<tbody>
							{#each filteredProcesses as proc (proc.pid)}
								{@const cpuLvl = getCpuClass(proc.cpuPercent)}
								<tr class:is-self={proc.isSelf} class:is-high-cpu={proc.cpuPercent > 30}>
									<!-- PID -->
									<td class="col-td-pid">
										<code>{proc.pid}</code>
									</td>

									<!-- User -->
									<td class="col-td-user">
										<span class="user-pill" class:root={proc.user === 'root'}>{proc.user}</span>
									</td>

									<!-- State -->
									<td class="col-td-state">
										<span class="state-badge state-{proc.state.toLowerCase()}" title={
											proc.state === 'R' ? 'Выполняется (Running)' :
											proc.state === 'S' ? 'Ожидание (Sleeping)' :
											proc.state === 'D' ? 'Ожидание диска (Disk sleep)' :
											proc.state === 'Z' ? 'Зомби (Zombie)' : 'Остановлен'
										}>
											{proc.state}
										</span>
									</td>

									<!-- Threads -->
									<td class="col-td-threads">
										{proc.threads}
									</td>

									<!-- CPU % -->
									<td class="col-td-cpu">
										<div class="cpu-cell-wrap">
											<span class="cpu-val-text level-{cpuLvl}">
												{proc.cpuPercent.toFixed(1)}%
											</span>
											{#if proc.cpuPercent > 0.5}
												<div class="mini-bar">
													<div
														class="mini-bar-fill bar-level-{cpuLvl}"
														style="width: {Math.min(100, proc.cpuPercent)}%"
													></div>
												</div>
											{/if}
										</div>
									</td>

									<!-- Memory -->
									<td class="col-td-mem">
										<div class="mem-cell-wrap">
											<span class="mem-rss">{formatBytes(proc.memoryRss)}</span>
											{#if proc.memoryPercent > 0.1}
												<span class="mem-pct">({proc.memoryPercent.toFixed(1)}%)</span>
											{/if}
										</div>
									</td>

									<!-- Command / Process -->
									<td class="col-td-cmd">
										<div class="cmd-wrap">
											<span class="proc-name">{proc.name}</span>
											{#if proc.isSelf}
												<span class="badge-self">AWG Manager</span>
											{/if}
											{#if proc.isCritical}
												<span class="badge-critical">Системный</span>
											{/if}
											<span class="proc-cmdline" title={proc.cmdline}>{proc.cmdline}</span>
										</div>
									</td>

									<!-- Actions -->
									<td class="col-td-act">
										<button
											type="button"
											class="btn-kill"
											class:btn-kill-self={proc.isSelf}
											title={proc.isSelf ? 'Остановить сервис AWG Manager' : 'Завершить процесс'}
											onclick={() => {
												killTarget = proc;
												killSignal = 'SIGTERM';
											}}
										>
											<Square size={11} />
										</button>
									</td>
								</tr>
							{/each}
						</tbody>
					</table>
				{/if}
			</div>
		</Card>
	{/if}
</div>

<!-- Terminate Process Confirmation Modal -->
<Modal
	open={killTarget !== null}
	title="Завершение процесса"
	size="md"
	onclose={() => (killTarget = null)}
>
	{#if killTarget}
		<div class="kill-modal-content">
			<div class="kill-warning-box">
				<AlertTriangle size={24} class="warning-icon" />
				<div>
					<p>Вы действительно хотите отправить сигнал завершения процессу?</p>
					<strong>PID {killTarget.pid} — {killTarget.name}</strong>
				</div>
			</div>

			<div class="kill-info-table">
				<div class="kill-row">
					<span>Команда:</span>
					<code>{killTarget.cmdline}</code>
				</div>
				<div class="kill-row">
					<span>Пользователь:</span>
					<span>{killTarget.user}</span>
				</div>
				<div class="kill-row">
					<span>Использование:</span>
					<span>CPU: {killTarget.cpuPercent.toFixed(1)}% | RAM: {formatBytes(killTarget.memoryRss)}</span>
				</div>
			</div>

			{#if killTarget.isSelf}
				<div class="self-kill-notice">
					<AlertTriangle size={16} class="danger-icon" />
					<div>
						<strong>Внимание: Это текущий процесс веб-панели AWG Manager!</strong>
						<p>При завершении процесса веб-интерфейс будет немедленно остановлен и станет недоступен. Чтобы снова его включить, потребуется зайти по SSH и выполнить: <code>/opt/etc/init.d/S99awg-manager start</code>.</p>
					</div>
				</div>
			{:else if killTarget.isCritical}
				<div class="danger-notice">
					<AlertTriangle size={15} />
					<span>Внимание: Это критически важный системный процесс роутера! Его завершение может нарушить работу сети или доступ к устройству.</span>
				</div>
			{/if}

			<div class="signal-selector">
				<span class="signal-label">Тип сигнала:</span>
				<label class="signal-option">
					<input type="radio" bind:group={killSignal} value="SIGTERM" />
					<span><strong>SIGTERM</strong> (Мягкое корректное завершение)</span>
				</label>
				<label class="signal-option">
					<input type="radio" bind:group={killSignal} value="SIGKILL" />
					<span><strong>SIGKILL</strong> (Принудительное немедленное убийство)</span>
				</label>
			</div>
		</div>
	{/if}

	{#snippet actions()}
		<div class="modal-footer-btns">
			<Button variant="ghost" onclick={() => (killTarget = null)}>Отмена</Button>
			<Button variant="danger" loading={killing} onclick={handleKillProcess}>
				{#snippet iconBefore()}<Square size={13} />{/snippet}
				Завершить (PID {killTarget?.pid})
			</Button>
		</div>
	{/snippet}
</Modal>

<style>
	.proctop-root {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
	}

	/* Master Bar */
	.proctop-master-bar {
		display: flex;
		justify-content: space-between;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.65rem;
	}

	.master-left, .master-right {
		display: flex;
		align-items: center;
		gap: 0.55rem;
		flex-wrap: wrap;
	}

	.master-switch-btn {
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
		padding: 0.3rem 0.65rem;
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-muted);
		font-size: 0.8rem;
		font-weight: 600;
		cursor: pointer;
		transition: all 0.15s ease;
	}
	.master-switch-btn:hover {
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
	}
	.master-switch-btn.active {
		background: rgba(16, 185, 129, 0.12);
		border-color: rgba(16, 185, 129, 0.35);
		color: #059669;
	}
	:global(.dark) .master-switch-btn.active {
		background: rgba(16, 185, 129, 0.15);
		border-color: rgba(16, 185, 129, 0.35);
		color: #34d399;
	}

	.filter-threads-btn {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		color: var(--color-text-secondary);
		padding: 0.25rem 0.55rem;
		border-radius: var(--radius-sm, 6px);
		font-size: 0.75rem;
		cursor: pointer;
		white-space: nowrap;
	}
	.filter-threads-btn:hover {
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
	}
	.filter-threads-btn.active {
		background: var(--color-accent-tint, rgba(96, 165, 250, 0.15));
		border-color: var(--color-accent);
		color: var(--color-accent);
		font-weight: 600;
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

	/* 1. Hardware Dashboard */
	.dashboard-grid {
		display: grid;
		grid-template-columns: 1.15fr 1fr;
		gap: 1.25rem;
	}

	.dash-col {
		display: flex;
		flex-direction: column;
		gap: 0.65rem;
	}

	.col-head {
		display: flex;
		justify-content: space-between;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.4rem;
	}

	.col-title {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		font-size: 0.9rem;
		font-weight: 700;
		color: var(--color-text-primary);
	}

	.chip-model {
		font-size: 0.72rem;
		font-weight: 600;
		color: var(--color-text-muted);
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		padding: 0.05rem 0.4rem;
		border-radius: 4px;
	}

	/* Badges */
	.pill-cpu-total {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		font-size: 0.75rem;
		font-weight: 600;
		padding: 0.15rem 0.55rem;
		border-radius: 999px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		color: var(--color-text-primary);
	}
	.pill-cpu-total .dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		flex-shrink: 0;
	}
	.pill-cpu-total.level-low .dot { background: #10b981; }
	.pill-cpu-total.level-med .dot { background: #f59e0b; }
	.pill-cpu-total.level-high .dot { background: #ef4444; }

	.pill-mem-total {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		font-size: 0.75rem;
		padding: 0.15rem 0.55rem;
		border-radius: 999px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		color: var(--color-text-primary);
	}

	.cores-list, .mem-bars {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.core-card {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		padding: 0.45rem 0.6rem;
		background: var(--color-bg-secondary);
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
	}

	.core-header-line {
		display: flex;
		justify-content: space-between;
		align-items: center;
		font-size: 0.78rem;
		gap: 0.5rem;
	}

	.core-id-group {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.core-idx {
		font-weight: 700;
		color: var(--color-text-primary);
	}

	.core-details-text {
		font-size: 0.72rem;
		color: var(--color-text-muted);
		font-family: var(--font-mono, monospace);
	}

	.core-percentage {
		font-weight: 700;
		font-family: var(--font-mono, monospace);
		font-size: 0.82rem;
	}

	/* Text color levels */
	.level-low {
		color: #059669;
	}
	:global(.dark) .level-low {
		color: #34d399;
	}

	.level-med {
		color: #d97706;
	}
	:global(.dark) .level-med {
		color: #fbbf24;
	}

	.level-high {
		color: #dc2626;
	}
	:global(.dark) .level-high {
		color: #f87171;
	}

	.mem-pct-txt {
		color: #2563eb;
	}
	:global(.dark) .mem-pct-txt {
		color: #60a5fa;
	}

	/* Multi-segment HTOP Progress bar */
	.htop-bar-track {
		height: 8px;
		background: var(--color-bg-tertiary);
		border-radius: 999px;
		overflow: hidden;
		display: flex;
		border: 1px solid var(--color-border);
	}

	.bar-seg {
		height: 100%;
		transition: width 0.3s ease;
	}

	.seg-user {
		background: #10b981;
	}
	.seg-sys {
		background: #f59e0b;
	}
	.seg-iowait {
		background: #ef4444;
	}

	.mem-used-seg {
		background: #3b82f6;
	}
	.mem-cached-seg {
		background: #06b6d4;
	}
	.swap-seg {
		background: #a855f7;
	}

	/* System Metadata */
	.sys-meta-grid {
		display: grid;
		grid-template-columns: 1fr;
		gap: 0.35rem;
		margin-top: 0.2rem;
		padding: 0.5rem 0.65rem;
		background: var(--color-bg-secondary);
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		font-size: 0.8rem;
	}

	.meta-item {
		display: flex;
		justify-content: space-between;
		align-items: center;
	}

	.meta-k {
		display: flex;
		align-items: center;
		gap: 0.35rem;
		color: var(--color-text-muted);
	}

	.meta-v {
		display: flex;
		align-items: center;
		gap: 0.35rem;
		color: var(--color-text-primary);
	}

	.load-status-pill {
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		font-size: 0.72rem;
		font-weight: 600;
		padding: 0.1rem 0.45rem;
		border-radius: 999px;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
	}
	.load-status-pill .dot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		flex-shrink: 0;
	}
	.load-status-pill.level-low {
		background: rgba(16, 185, 129, 0.12);
		border-color: rgba(16, 185, 129, 0.3);
		color: #059669;
	}
	:global(.dark) .load-status-pill.level-low {
		background: rgba(16, 185, 129, 0.15);
		color: #34d399;
	}
	.load-status-pill.level-low .dot { background: #10b981; }

	.load-status-pill.level-med {
		background: rgba(245, 158, 11, 0.12);
		border-color: rgba(245, 158, 11, 0.3);
		color: #d97706;
	}
	:global(.dark) .load-status-pill.level-med {
		background: rgba(245, 158, 11, 0.15);
		color: #fbbf24;
	}
	.load-status-pill.level-med .dot { background: #f59e0b; }

	.load-status-pill.level-high {
		background: rgba(239, 68, 68, 0.12);
		border-color: rgba(239, 68, 68, 0.3);
		color: #dc2626;
	}
	:global(.dark) .load-status-pill.level-high {
		background: rgba(239, 68, 68, 0.15);
		color: #f87171;
	}
	.load-status-pill.level-high .dot { background: #ef4444; }

	.load-badges-group {
		display: flex;
		align-items: center;
		gap: 0.25rem;
	}

	.load-badge {
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		padding: 0.08rem 0.35rem;
		border-radius: 4px;
		font-size: 0.74rem;
		font-family: var(--font-mono, monospace);
		color: var(--color-text-secondary);
	}
	.load-badge strong {
		color: var(--color-text-primary);
	}

	/* Interval Picker */
	.interval-picker {
		display: flex;
		align-items: center;
		gap: 0.2rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm, 6px);
		padding: 0.15rem;
	}

	.picker-label {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		padding: 0 0.35rem;
	}

	.interval-btn {
		background: none;
		border: none;
		padding: 0.2rem 0.45rem;
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--color-text-secondary);
		border-radius: 4px;
		cursor: pointer;
		display: inline-flex;
		align-items: center;
		gap: 0.2rem;
	}

	.interval-btn:hover {
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
	}

	.interval-btn.active {
		background: var(--color-accent);
		color: #ffffff;
	}

	.search-box {
		position: relative;
		display: flex;
		align-items: center;
	}

	:global(.search-icon) {
		position: absolute;
		left: 0.55rem;
		color: var(--color-text-muted);
	}

	.search-box input {
		padding: 0.3rem 0.55rem 0.3rem 1.65rem;
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-primary);
		font-size: 0.8rem;
		width: 200px;
	}

	.search-box input:focus {
		border-color: var(--color-accent);
		outline: none;
	}

	.counter-badge {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		white-space: nowrap;
	}

	/* 3. Table Responsive Styles (No horizontal scrollbar) */
	.table-container {
		max-height: 540px;
		overflow-y: auto;
		overflow-x: hidden;
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
	}

	.proc-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.82rem;
		table-layout: fixed;
	}

	.proc-table th, .proc-table td {
		padding: 0.45rem 0.5rem;
		border-bottom: 1px solid var(--color-border);
		text-align: left;
		vertical-align: middle;
	}

	.proc-table th {
		position: sticky;
		top: 0;
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
		font-size: 0.72rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.02em;
		z-index: 1;
	}

	/* Fixed column widths */
	.col-th-pid, .col-td-pid { width: 56px; }
	.col-th-user, .col-td-user { width: 68px; }
	.col-th-state, .col-td-state { width: 48px; text-align: center; }
	.col-th-threads, .col-td-threads { width: 56px; text-align: center; }
	.col-th-cpu, .col-td-cpu { width: 76px; }
	.col-th-mem, .col-td-mem { width: 110px; }
	.col-th-cmd, .col-td-cmd { width: auto; overflow: hidden; }
	.col-th-act, .col-td-act { width: 48px; text-align: center; }

	.th-sortable {
		cursor: pointer;
		user-select: none;
	}

	.th-sortable:hover {
		background: var(--color-bg-hover, rgba(255,255,255,0.05));
	}

	.th-wrap {
		display: flex;
		align-items: center;
		gap: 0.25rem;
	}

	.proc-table tr:hover {
		background: var(--color-bg-hover, rgba(255,255,255,0.03));
	}

	.proc-table tr.is-self {
		background: var(--color-accent-tint, rgba(96, 165, 250, 0.08));
	}

	.col-td-pid code {
		font-weight: 700;
		color: var(--color-text-primary);
		font-size: 0.78rem;
	}

	.user-pill {
		font-size: 0.72rem;
		padding: 0.05rem 0.25rem;
		border-radius: 3px;
		background: var(--color-bg-tertiary);
		color: var(--color-text-secondary);
	}

	.user-pill.root {
		color: var(--color-accent);
		font-weight: 600;
	}

	.state-badge {
		display: inline-block;
		font-size: 0.7rem;
		font-weight: 700;
		padding: 0.05rem 0.3rem;
		border-radius: 3px;
		text-align: center;
	}
	.state-r {
		background: rgba(16, 185, 129, 0.15);
		color: #10b981;
	}
	.state-s {
		background: rgba(148, 163, 184, 0.15);
		color: #94a3b8;
	}
	.state-d {
		background: rgba(245, 158, 11, 0.15);
		color: #f59e0b;
	}
	.state-z {
		background: rgba(239, 68, 68, 0.15);
		color: #ef4444;
	}

	.cpu-cell-wrap {
		display: flex;
		flex-direction: column;
		gap: 0.15rem;
	}

	.cpu-val-text {
		font-weight: 700;
		font-family: var(--font-mono, monospace);
		font-size: 0.78rem;
	}

	.mini-bar {
		height: 3px;
		background: var(--color-bg-tertiary);
		border-radius: 999px;
		overflow: hidden;
		width: 40px;
	}

	.mini-bar-fill {
		height: 100%;
	}
	.bar-level-low { background: #10b981; }
	.bar-level-med { background: #f59e0b; }
	.bar-level-high { background: #ef4444; }

	.mem-cell-wrap {
		display: flex;
		align-items: baseline;
		gap: 0.2rem;
		white-space: nowrap;
	}
	.mem-rss {
		font-weight: 600;
		color: var(--color-text-primary);
		font-size: 0.78rem;
	}

	.mem-pct {
		font-size: 0.7rem;
		color: var(--color-text-muted);
	}

	.cmd-wrap {
		display: flex;
		align-items: center;
		gap: 0.35rem;
		width: 100%;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.proc-name {
		font-weight: 700;
		color: var(--color-text-primary);
		flex-shrink: 0;
	}

	.badge-self {
		font-size: 0.65rem;
		font-weight: 700;
		padding: 0.05rem 0.3rem;
		border-radius: 3px;
		background: rgba(96, 165, 250, 0.2);
		color: #60a5fa;
		flex-shrink: 0;
	}

	.badge-critical {
		font-size: 0.65rem;
		font-weight: 700;
		padding: 0.05rem 0.3rem;
		border-radius: 3px;
		background: rgba(245, 158, 11, 0.15);
		color: #f59e0b;
		flex-shrink: 0;
	}

	.proc-cmdline {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		font-family: var(--font-mono, monospace);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		flex: 1;
		min-width: 0;
	}

	.btn-kill {
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		color: var(--color-text-secondary);
		border-radius: 4px;
		padding: 0.25rem 0.35rem;
		cursor: pointer;
		display: inline-flex;
		align-items: center;
		justify-content: center;
	}

	.btn-kill:hover {
		background: var(--color-error-tint, rgba(239, 68, 68, 0.15));
		color: var(--color-error, #f87171);
		border-color: var(--color-error);
	}

	.btn-kill.btn-kill-self:hover {
		background: rgba(245, 158, 11, 0.2);
		color: #f59e0b;
		border-color: #f59e0b;
	}

	.empty-state {
		padding: 3rem;
		text-align: center;
		color: var(--color-text-muted);
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.5rem;
	}

	.loading-hint {
		font-size: 0.8rem;
		color: var(--color-text-muted);
		padding: 1rem;
		text-align: center;
	}

	/* Kill Modal */
	.kill-modal-content {
		display: flex;
		flex-direction: column;
		gap: 0.85rem;
	}

	.kill-warning-box {
		display: flex;
		gap: 0.75rem;
		align-items: center;
		padding: 0.75rem;
		background: var(--color-bg-tertiary);
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
	}

	:global(.warning-icon) {
		color: #f59e0b;
		flex-shrink: 0;
	}

	.kill-info-table {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
		padding: 0.6rem;
		background: var(--color-bg-secondary);
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		font-size: 0.82rem;
	}

	.kill-row {
		display: flex;
		justify-content: space-between;
		gap: 0.5rem;
	}

	.kill-row code {
		word-break: break-all;
	}

	.self-kill-notice {
		display: flex;
		gap: 0.6rem;
		padding: 0.65rem;
		background: rgba(245, 158, 11, 0.12);
		border: 1px solid rgba(245, 158, 11, 0.35);
		border-radius: var(--radius-sm, 6px);
		color: #d97706;
		font-size: 0.8rem;
	}
	:global(.dark) .self-kill-notice {
		color: #fbbf24;
	}
	.self-kill-notice p {
		margin: 0.25rem 0 0 0;
	}
	.self-kill-notice code {
		background: var(--color-bg-tertiary);
		padding: 0.1rem 0.3rem;
		border-radius: 3px;
	}

	.danger-notice {
		display: flex;
		gap: 0.5rem;
		padding: 0.5rem 0.65rem;
		background: rgba(239, 68, 68, 0.12);
		border: 1px solid rgba(239, 68, 68, 0.3);
		border-radius: var(--radius-sm, 6px);
		color: #f87171;
		font-size: 0.8rem;
	}

	.signal-selector {
		display: flex;
		flex-direction: column;
		gap: 0.4rem;
		padding: 0.6rem;
		background: var(--color-bg-secondary);
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		font-size: 0.82rem;
	}

	.signal-label {
		font-weight: 600;
		color: var(--color-text-secondary);
	}

	.signal-option {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		cursor: pointer;
	}

	.modal-footer-btns {
		display: flex;
		justify-content: flex-end;
		gap: 0.5rem;
		width: 100%;
	}

	@media (max-width: 900px) {
		.dashboard-grid {
			grid-template-columns: 1fr;
		}
	}
</style>
