<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import type { TrafficDevice, TrafficSnapshot, TrafficDomainGroup, TrafficSession, ItemRouteStatus } from '$lib/types';
	import {
		Button,
		SegmentedControl,
		Dropdown,
		type DropdownOption,
	} from '$lib/components/ui';
	import { LoadingSpinner, EmptyState } from '$lib/components/layout';
	import TrafficExportModal from './TrafficExportModal.svelte';
	import PresetIcon from '$lib/components/routing/singboxRouter/PresetIcon.svelte';
	import {
		Radio,
		Play,
		Pause,
		RefreshCw,
		Trash2,
		Search,
		Plus,
		Globe,
		List,
		Layers,
		Check,
		ChevronDown,
		ChevronUp,
		Tv,
		Smartphone,
		Laptop,
		Gamepad2,
		Server,
		Wifi,
		Activity,
		ArrowDown,
		ArrowUp,
		HardDrive,
		Filter,
		Shield,
		BookOpen,
		X,
		Info,
	} from 'lucide-svelte';

	// State
	let devices = $state<TrafficDevice[]>([]);
	let selectedDeviceIP = $state<string>('');
	let loadingDevices = $state(true);
	let snapshot = $state<TrafficSnapshot | null>(null);
	let loadingSnapshot = $state(false);

	// Polling / Recording
	let isRecording = $state(true);
	let refreshIntervalSec = $state(3);
	let timerId: ReturnType<typeof setInterval> | null = null;
	let secondsElapsed = $state(0);
	let elapsedTimerId: ReturnType<typeof setInterval> | null = null;

	// View mode & Filtering
	type ViewMode = 'domains' | 'sockets';
	let viewMode = $state<ViewMode>('domains');
	let searchQuery = $state('');
	let protocolFilter = $state<'all' | 'tcp' | 'udp'>('all');

	// Expanded domain cards
	let expandedDomains = $state<Record<string, boolean>>({});

	// Active route popover state
	let activeRoutePopoverKey = $state<string | null>(null);

	// Multi-select for export
	let selectedDomains = $state<string[]>([]);
	let selectedIPs = $state<string[]>([]);
	let showExportModal = $state(false);
	let exportDefaultService = $state('');

	interface RouteMatchEntry {
		target: string;
		targetLabel: string;
		ruleName: string;
		pattern?: string;
		item: string;
		isDomain: boolean;
	}

	interface GroupRouteSummary {
		activeRules: RouteMatchEntry[];
		catalogRules: RouteMatchEntry[];
		hasActiveRules: boolean;
		hasCatalog: boolean;
		engineNames: string[];
		summaryText: string;
		statusType: 'routed' | 'partial' | 'catalog' | 'new';
	}

	function analyzeGroupRoutes(group: TrafficDomainGroup): GroupRouteSummary {
		const activeList: RouteMatchEntry[] = [];
		const catalogList: RouteMatchEntry[] = [];
		const seenActive = new Set<string>();
		const seenCatalog = new Set<string>();

		for (const dom of group.domains || []) {
			const statuses = group.domainStatuses?.[dom] || [];
			for (const st of statuses) {
				const key = `${st.target}:${st.ruleName}:${st.matchedPattern || ''}:${dom}`;
				const entry: RouteMatchEntry = {
					target: st.target,
					targetLabel: st.targetLabel,
					ruleName: st.ruleName,
					pattern: st.matchedPattern,
					item: dom,
					isDomain: true,
				};
				if (st.target === 'catalog') {
					if (!seenCatalog.has(key)) {
						seenCatalog.add(key);
						catalogList.push(entry);
					}
				} else {
					if (!seenActive.has(key)) {
						seenActive.add(key);
						activeList.push(entry);
					}
				}
			}
		}

		for (const ip of group.ips || []) {
			const statuses = group.ipStatuses?.[ip] || [];
			for (const st of statuses) {
				const key = `${st.target}:${st.ruleName || st.matchedPattern || ''}:${ip}`;
				const entry: RouteMatchEntry = {
					target: st.target,
					targetLabel: st.targetLabel,
					ruleName: st.ruleName || st.matchedPattern || ip,
					pattern: st.matchedPattern,
					item: ip,
					isDomain: false,
				};
				if (st.target === 'catalog') {
					if (!seenCatalog.has(key)) {
						seenCatalog.add(key);
						catalogList.push(entry);
					}
				} else {
					if (!seenActive.has(key)) {
						seenActive.add(key);
						activeList.push(entry);
					}
				}
			}
		}

		const uniqueActiveEngines = Array.from(new Set(activeList.map((r) => r.targetLabel)));
		const hasActiveRules = activeList.length > 0;
		const hasCatalog = catalogList.length > 0;

		let statusType: 'routed' | 'partial' | 'catalog' | 'new' = 'new';
		let summaryText = 'Новый (нет в правилах)';

		const totalItems = (group.domains?.length || 0) + (group.ips?.length || 0);
		const routedItems =
			(group.domains?.filter((d) => (group.domainStatuses?.[d] || []).length > 0).length || 0) +
			(group.ips?.filter((ip) => (group.ipStatuses?.[ip] || []).length > 0).length || 0);

		if (hasActiveRules) {
			if (totalItems > 0 && routedItems >= totalItems) {
				statusType = 'routed';
				if (uniqueActiveEngines.length === 1) {
					summaryText = `✓ В правилах: ${uniqueActiveEngines[0]}: ${activeList[0].ruleName}`;
				} else {
					summaryText = `✓ В правилах: ${uniqueActiveEngines.join(', ')}`;
				}
			} else {
				statusType = 'partial';
				summaryText = `⚠️ Частично (${uniqueActiveEngines.join(', ')})`;
			}
			if (hasCatalog) {
				summaryText += ` (+ в каталоге)`;
			}
		} else if (hasCatalog) {
			statusType = 'catalog';
			const catName = catalogList[0].ruleName;
			summaryText = `📖 В каталоге: ${catName}`;
		}

		return {
			activeRules: activeList,
			catalogRules: catalogList,
			hasActiveRules,
			hasCatalog,
			engineNames: uniqueActiveEngines,
			summaryText,
			statusType,
		};
	}

	function analyzeItemRoutes(statuses: ItemRouteStatus[] = []) {
		const active = statuses.filter((s) => s.target !== 'catalog');
		const catalog = statuses.filter((s) => s.target === 'catalog');
		return { active, catalog, all: statuses };
	}

	// Devices dropdown options
	const deviceOptions = $derived.by((): DropdownOption[] => {
		return devices.map((d) => {
			const label = `${d.name || d.hostname || d.ip} (${d.ip})${d.activeSessions > 0 ? ` · ${d.activeSessions} сес.` : ''}`;
			return { value: d.ip, label };
		});
	});

	const selectedDevice = $derived.by(() => {
		return devices.find((d) => d.ip === selectedDeviceIP) || null;
	});

	function translateCategory(cat?: string): string {
		switch (cat) {
			case 'social':
				return 'Соцсети';
			case 'media':
				return 'Медиа / Видео';
			case 'communication':
				return 'Связь / Почта';
			case 'cloud':
				return 'Облако / Сервер';
			case 'gaming':
				return 'Игры';
			case 'dev':
				return 'Разработка';
			case 'isp':
				return 'Провайдер связи';
			case 'system':
				return 'Система / ОС';
			case 'software':
				return 'Программы';
			case 'block':
				return 'Реестр блокировок';
			default:
				return cat || 'Сервис';
		}
	}

	function pluralizeDomains(count: number): string {
		const n = Math.abs(count) % 100;
		const n1 = n % 10;
		if (n > 10 && n < 20) return `${count} доменов`;
		if (n1 > 1 && n1 < 5) return `${count} домена`;
		if (n1 === 1) return `${count} домен`;
		return `${count} доменов`;
	}

	// Filtered domain groups
	const filteredGroups = $derived.by(() => {
		if (!snapshot?.domainGroups) return [];
		const q = searchQuery.toLowerCase().trim();
		return snapshot.domainGroups.filter((g) => {
			if (q) {
				const matchKey = (g.groupKey || '').toLowerCase().includes(q);
				const matchTitle = (g.title || '').toLowerCase().includes(q);
				const matchDomain = (g.domain || '').toLowerCase().includes(q);
				const matchDomains = (g.domains || []).some((d) => d.toLowerCase().includes(q));
				const matchName = (g.serviceName || '').toLowerCase().includes(q);
				const matchDesc = (g.knowledge?.description || '').toLowerCase().includes(q);
				const matchOrg = (g.knowledge?.org || '').toLowerCase().includes(q);
				const matchCountry = (g.knowledge?.country || '').toLowerCase().includes(q);
				const matchIP = (g.ips || []).some((ip) => ip.includes(q));
				if (!matchKey && !matchTitle && !matchDomain && !matchDomains && !matchName && !matchDesc && !matchOrg && !matchCountry && !matchIP) return false;
			}
			if (protocolFilter !== 'all') {
				const hasProto = (g.sessions || []).some((s) => s.protocol === protocolFilter);
				if (!hasProto) return false;
			}
			return true;
		});
	});

	// Filtered sessions
	const filteredSessions = $derived.by(() => {
		if (!snapshot?.sessions) return [];
		const q = searchQuery.toLowerCase().trim();
		return snapshot.sessions.filter((s) => {
			if (q) {
				const matchDst = s.dstIp.includes(q) || String(s.dstPort).includes(q);
				const matchDomain = (s.domain || '').toLowerCase().includes(q);
				const matchName = (s.serviceName || '').toLowerCase().includes(q);
				const matchTitle = (s.knowledge?.title || '').toLowerCase().includes(q);
				const matchDesc = (s.knowledge?.description || '').toLowerCase().includes(q);
				const matchOrg = (s.knowledge?.org || '').toLowerCase().includes(q);
				const matchCountry = (s.knowledge?.country || '').toLowerCase().includes(q);
				if (!matchDst && !matchDomain && !matchName && !matchTitle && !matchDesc && !matchOrg && !matchCountry) return false;
			}
			if (protocolFilter !== 'all' && s.protocol !== protocolFilter) {
				return false;
			}
			return true;
		});
	});

	async function loadDevices(autoSelectFirst = true) {
		loadingDevices = true;
		try {
			const list = await api.systemTrafficDevices();
			devices = list || [];
			if (autoSelectFirst && devices.length > 0 && !selectedDeviceIP) {
				selectedDeviceIP = devices[0].ip;
			}
		} catch (err: any) {
			notifications.error(err?.message || 'Не удалось загрузить список устройств');
		} finally {
			loadingDevices = false;
		}
	}

	async function fetchSnapshot() {
		if (!selectedDeviceIP) return;
		loadingSnapshot = true;
		try {
			const data = await api.systemTrafficSnapshot(selectedDeviceIP);
			snapshot = data;
		} catch (err: any) {
			// silent fallback on interval
		} finally {
			loadingSnapshot = false;
		}
	}

	function startPolling() {
		stopPolling();
		if (isRecording) {
			fetchSnapshot();
			timerId = setInterval(fetchSnapshot, refreshIntervalSec * 1000);
			elapsedTimerId = setInterval(() => {
				secondsElapsed++;
			}, 1000);
		}
	}

	function stopPolling() {
		if (timerId) {
			clearInterval(timerId);
			timerId = null;
		}
		if (elapsedTimerId) {
			clearInterval(elapsedTimerId);
			elapsedTimerId = null;
		}
	}

	function toggleRecording() {
		isRecording = !isRecording;
		if (isRecording) {
			startPolling();
		} else {
			stopPolling();
		}
	}

	function clearData() {
		snapshot = null;
		selectedDomains = [];
		selectedIPs = [];
		secondsElapsed = 0;
		expandedDomains = {};
	}

	function formatBytes(bytes: number): string {
		if (!bytes || bytes === 0) return '0 B';
		const units = ['B', 'KB', 'MB', 'GB'];
		let i = 0;
		let val = bytes;
		while (val >= 1024 && i < units.length - 1) {
			val /= 1024;
			i++;
		}
		return `${val.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
	}

	function formatTime(sec: number): string {
		const m = Math.floor(sec / 60);
		const s = sec % 60;
		return `${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`;
	}

	function toggleDomainExpand(key: string) {
		expandedDomains[key] = !expandedDomains[key];
	}

	function toggleGroupSelect(group: TrafficDomainGroup) {
		const allDoms = group.domains && group.domains.length > 0 ? group.domains : (group.domain ? [group.domain] : []);
		const isAllSelected = allDoms.every((d) => selectedDomains.includes(d));

		if (isAllSelected) {
			selectedDomains = selectedDomains.filter((d) => !allDoms.includes(d));
			selectedIPs = selectedIPs.filter((ip) => !group.ips.includes(ip));
		} else {
			for (const d of allDoms) {
				if (!selectedDomains.includes(d)) {
					selectedDomains = [...selectedDomains, d];
				}
			}
			for (const ip of group.ips) {
				if (!selectedIPs.includes(ip)) {
					selectedIPs = [...selectedIPs, ip];
				}
			}
		}
	}

	function toggleSessionSelect(id: string, domain?: string, ip?: string) {
		if (domain) {
			if (selectedDomains.includes(domain)) {
				selectedDomains = selectedDomains.filter((d) => d !== domain);
			} else {
				selectedDomains = [...selectedDomains, domain];
			}
		} else if (ip) {
			if (selectedIPs.includes(ip)) {
				selectedIPs = selectedIPs.filter((x) => x !== ip);
			} else {
				selectedIPs = [...selectedIPs, ip];
			}
		}
	}

	let exportDomainStatuses = $state<Record<string, ItemRouteStatus[]>>({});
	let exportIPStatuses = $state<Record<string, ItemRouteStatus[]>>({});
	let exportExistingRules = $state<string[]>([]);

	function openSingleExport(group: TrafficDomainGroup) {
		const allDoms = group.domains && group.domains.length > 0 ? group.domains : (group.domain ? [group.domain] : []);
		exportDefaultService = group.title || group.serviceName || (allDoms.length > 0 ? allDoms[0] : group.ips[0]);
		selectedDomains = allDoms;
		selectedIPs = group.ips;
		exportDomainStatuses = group.domainStatuses || {};
		exportIPStatuses = group.ipStatuses || {};
		exportExistingRules = group.existingRules || [];
		showExportModal = true;
	}

	function openBulkExport() {
		exportDefaultService = 'Выбранные сервисы';
		exportDomainStatuses = {};
		exportIPStatuses = {};
		exportExistingRules = [];
		if (snapshot) {
			for (const g of snapshot.domainGroups) {
				if (g.domainStatuses) Object.assign(exportDomainStatuses, g.domainStatuses);
				if (g.ipStatuses) Object.assign(exportIPStatuses, g.ipStatuses);
				if (g.existingRules) {
					for (const r of g.existingRules) {
						if (!exportExistingRules.includes(r)) exportExistingRules.push(r);
					}
				}
			}
		}
		showExportModal = true;
	}

	let lastLoadedIP = '';

	$effect(() => {
		const ip = selectedDeviceIP;
		if (ip && ip !== lastLoadedIP) {
			lastLoadedIP = ip;
			snapshot = null;
			selectedDomains = [];
			selectedIPs = [];
			expandedDomains = {};
			fetchSnapshot();
		}
	});

	onMount(async () => {
		await loadDevices();
		if (isRecording) {
			startPolling();
		}
	});

	onDestroy(() => {
		stopPolling();
	});
</script>

<div class="traffic-panel">
	<!-- Control Bar -->
	<div class="top-controls card">
		<div class="device-select-area">
			<div class="label-with-icon">
				<Radio size={16} class="accent-icon" />
				<span class="control-title">Устройство сети:</span>
			</div>
			{#if loadingDevices}
				<div class="loading-devices"><LoadingSpinner size="sm" /> <span>Поиск устройств...</span></div>
			{:else}
				<div class="dropdown-wrapper">
					<Dropdown
						options={deviceOptions}
						value={selectedDeviceIP}
						onchange={(v) => (selectedDeviceIP = v)}
					/>
				</div>
			{/if}
		</div>

		<div class="actions-area">
			<div class="recording-indicator" class:active={isRecording}>
				<span class="pulse-dot"></span>
				<span>{isRecording ? 'Запись' : 'Пауза'} ({formatTime(secondsElapsed)})</span>
			</div>

			<Button
				variant={isRecording ? 'secondary' : 'primary'}
				onclick={toggleRecording}
			>
				{#if isRecording}
					<Pause size={16} /> Приостановить
				{:else}
					<Play size={16} /> Начать запись
				{/if}
			</Button>

			<Button variant="secondary" title="Обновить вручную" onclick={fetchSnapshot}>
				<RefreshCw size={16} class={loadingSnapshot ? 'spin' : ''} />
			</Button>

			<Button variant="secondary" title="Очистить снимок" onclick={clearData}>
				<Trash2 size={16} />
			</Button>
		</div>
	</div>

	<!-- Summary Stats Bar -->
	{#if snapshot}
		<div class="stats-row">
			<div class="stat-card">
				<div class="stat-icon"><Activity size={18} /></div>
				<div class="stat-body">
					<span class="stat-val">{snapshot.activeCount} <small>/ {snapshot.totalSessions}</small></span>
					<span class="stat-lbl">Активных сессий</span>
				</div>
			</div>

			<div class="stat-card">
				<div class="stat-icon"><Globe size={18} /></div>
				<div class="stat-body">
					<span class="stat-val">{snapshot.domainGroups.length}</span>
					<span class="stat-lbl">Сервисов и доменов</span>
				</div>
			</div>

			<div class="stat-card">
				<div class="stat-icon in"><ArrowDown size={18} /></div>
				<div class="stat-body">
					<span class="stat-val">{formatBytes(snapshot.totalBytesIn)}</span>
					<span class="stat-lbl">Входящий трафик</span>
				</div>
			</div>

			<div class="stat-card">
				<div class="stat-icon out"><ArrowUp size={18} /></div>
				<div class="stat-body">
					<span class="stat-val">{formatBytes(snapshot.totalBytesOut)}</span>
					<span class="stat-lbl">Исходящий трафик</span>
				</div>
			</div>
		</div>
	{/if}

	<!-- View Mode & Search Filter Bar -->
	<div class="filter-row">
		<SegmentedControl
			options={[
				{ value: 'domains', label: 'Домены и сервисы' },
				{ value: 'sockets', label: 'Все сокеты' },
			]}
			value={viewMode}
			onchange={(v) => (viewMode = v as ViewMode)}
		/>

		<div class="search-input-wrap">
			<Search size={16} class="search-icon" />
			<input
				type="text"
				placeholder="Поиск по домену, сервису, названию или IP..."
				bind:value={searchQuery}
			/>
			{#if searchQuery}
				<button class="clear-search-btn" onclick={() => (searchQuery = '')}>×</button>
			{/if}
		</div>

		<div class="protocol-filter">
			<SegmentedControl
				options={[
					{ value: 'all', label: 'Все' },
					{ value: 'tcp', label: 'TCP' },
					{ value: 'udp', label: 'UDP' },
				]}
				value={protocolFilter}
				onchange={(v) => (protocolFilter = v as 'all' | 'tcp' | 'udp')}
			/>
		</div>
	</div>

	<!-- Content Area -->
	{#if loadingSnapshot && !snapshot}
		<div class="loading-state card">
			<LoadingSpinner size="lg" />
			<p>Анализ сетевого трафика устройства...</p>
		</div>
	{:else if !snapshot || (viewMode === 'domains' && filteredGroups.length === 0) || (viewMode === 'sockets' && filteredSessions.length === 0)}
		<div class="card empty-container">
			<EmptyState
				title="Нет активных соединений"
				description={isRecording
					? 'Ожидание сетевой активности устройства... Запустите приложение или откройте сайт на выбранном устройстве.'
					: 'Нажмите «Начать запись», чтобы начать перехват сетевых сессий устройства.'}
			/>
		</div>
	{:else if viewMode === 'domains'}
		<!-- Mode 1: Domain & Services Cards -->
		<div class="domain-groups-list">
			{#each filteredGroups as group}
				{@const isExpanded = !!expandedDomains[group.groupKey || group.domain]}
				{@const allDoms = group.domains && group.domains.length > 0 ? group.domains : (group.domain ? [group.domain] : [])}
				{@const isSelected = allDoms.some((d) => selectedDomains.includes(d)) || group.ips.some((ip) => selectedIPs.includes(ip))}
				{@const rInfo = analyzeGroupRoutes(group)}
				{@const popKey = group.groupKey || group.domain}
				<div class="domain-card card" class:selected={isSelected}>
					<div class="card-main-row">
						<label class="checkbox-container">
							<input
								type="checkbox"
								checked={isSelected}
								onchange={() => toggleGroupSelect(group)}
							/>
							<span class="custom-checkbox"></span>
						</label>

						<div class="service-icon-tile">
							<PresetIcon
								slug={group.knowledge?.icon || group.serviceName}
								size={26}
								label={group.title || group.domain}
							/>
						</div>

						<div class="domain-info">
							<div class="domain-header-line">
								<span class="domain-title" title={group.title || group.domain}>
									{group.title || group.domain}
								</span>

								<div class="route-tag-wrapper">
									<button
										type="button"
										class="route-status-tag {rInfo.statusType}"
										class:interactive={rInfo.hasActiveRules || rInfo.hasCatalog}
										title={rInfo.hasActiveRules || rInfo.hasCatalog ? 'Нажмите, чтобы увидеть где и как настроен сервис' : ''}
										onclick={(e) => {
											if (rInfo.hasActiveRules || rInfo.hasCatalog) {
												e.stopPropagation();
												activeRoutePopoverKey = activeRoutePopoverKey === popKey ? null : popKey;
											}
										}}
									>
										<span>{rInfo.summaryText}</span>
										{#if rInfo.hasActiveRules || rInfo.hasCatalog}
											<ChevronDown size={11} class="tag-chevron" />
										{/if}
									</button>

									{#if activeRoutePopoverKey === popKey}
										<div class="route-popover-menu" onclick={(e) => e.stopPropagation()}>
											<div class="popover-header">
												<span class="popover-title">Где задействован сервис</span>
												<button
													type="button"
													class="popover-close-btn"
													title="Закрыть"
													onclick={() => (activeRoutePopoverKey = null)}
												>
													<X size={12} />
												</button>
											</div>

											<div class="popover-content">
												{#if rInfo.activeRules.length > 0}
													<div class="popover-section">
														<div class="popover-section-title">
															<Shield size={13} />
															<span>Маршрутизация ({rInfo.activeRules.length}):</span>
														</div>
														<div class="popover-items-list">
															{#each rInfo.activeRules as item}
																<div class="popover-row">
																	<span class="popover-engine-badge {item.target}">{item.targetLabel}</span>
																	<div class="popover-row-main">
																		<span class="popover-rule-name">{item.ruleName}</span>
																		{#if item.pattern}
																			<span class="popover-pattern font-mono">{item.pattern}</span>
																		{/if}
																		<span class="popover-target-item">для <code>{item.item}</code></span>
																	</div>
																</div>
															{/each}
														</div>
													</div>
												{/if}

												{#if rInfo.catalogRules.length > 0}
													<div class="popover-section catalog">
														<div class="popover-section-title">
															<BookOpen size={13} />
															<span>Каталог пресетов ({rInfo.catalogRules.length}):</span>
														</div>
														<div class="popover-items-list">
															{#each rInfo.catalogRules as item}
																<div class="popover-row">
																	<span class="popover-engine-badge catalog">Каталог</span>
																	<div class="popover-row-main">
																		<span class="popover-rule-name">{item.ruleName}</span>
																		{#if item.pattern}
																			<span class="popover-pattern font-mono">{item.pattern}</span>
																		{/if}
																		<span class="popover-target-item">для <code>{item.item}</code></span>
																	</div>
																</div>
															{/each}
														</div>
													</div>
												{/if}

												{#if !rInfo.hasActiveRules && !rInfo.hasCatalog}
													<div class="popover-empty">Сервис пока не настроен ни в одном правиле.</div>
												{/if}
											</div>
										</div>
									{/if}
								</div>

								{#if group.domains && group.domains.length > 1}
									<span class="domains-count-badge">
										{pluralizeDomains(group.domains.length)}
									</span>
								{/if}

								{#if group.knowledge?.country}
									<span class="country-tag" title={group.knowledge.country}>
										{group.knowledge.country}
									</span>
								{/if}

								{#if group.knowledge?.category || group.serviceCategory}
									<span class="category-tag">
										{translateCategory(group.knowledge?.category || group.serviceCategory)}
									</span>
								{/if}
							</div>

							<div class="domain-sub-line">
								<span class="domain-desc-text">
									{#if group.knowledge?.description}
										{group.knowledge.description}
									{:else if group.domains && group.domains.length > 0}
										{group.domains.slice(0, 3).join(', ')}{group.domains.length > 3 ? '...' : ''}
									{:else}
										{group.domain}
									{/if}
								</span>
								<span class="meta-dot">·</span>
								<span class="meta-item"><strong>{group.sessionCount}</strong> {group.sessionCount === 1 ? 'сес.' : 'сес.'}</span>
								<span class="meta-dot">·</span>
								<span class="meta-item">{group.ips.length} IP</span>
								<span class="meta-dot">·</span>
								<span class="meta-item bytes">↓ {formatBytes(group.bytesIn)} ↑ {formatBytes(group.bytesOut)}</span>
								{#if group.knowledge?.org && group.knowledge.org !== group.title}
									<span class="meta-dot">·</span>
									<span class="meta-item meta-org">{group.knowledge.org}</span>
								{/if}
							</div>
						</div>

						<div class="card-actions">
							<Button
								variant="secondary"
								size="sm"
								onclick={() => openSingleExport(group)}
							>
								<Plus size={14} /> В правила
							</Button>

							<button
								class="expand-btn"
								title={isExpanded ? 'Свернуть детали' : 'Показать сокеты и IP'}
								onclick={() => toggleDomainExpand(group.groupKey || group.domain)}
							>
								{#if isExpanded}
									<ChevronUp size={16} />
								{:else}
									<ChevronDown size={16} />
								{/if}
							</button>
						</div>
					</div>

					{#if isExpanded}
						<div class="expanded-details">
							{#if group.domains && group.domains.length > 0}
								<div class="details-section">
									<span class="details-label">Входящие домены сервиса:</span>
									<div class="domains-chips">
										{#each group.domains as dom}
											{@const domRoutes = group.domainStatuses?.[dom] || []}
											{@const itemInfo = analyzeItemRoutes(domRoutes)}
											<div class="chip-item-with-status">
												<span class="dom-badge">{dom}</span>
												{#if itemInfo.active.length > 0}
													{#each itemInfo.active as st}
														<span class="route-mini-badge routed {st.target}" title="{st.targetLabel}: {st.ruleName}">
															✓ {st.targetLabel}: {st.ruleName}
														</span>
													{/each}
												{/if}
												{#if itemInfo.catalog.length > 0}
													{#each itemInfo.catalog as st}
														<span class="route-mini-badge catalog" title="В каталоге пресетов: {st.ruleName}">
															📖 Каталог: {st.ruleName}
														</span>
													{/each}
												{/if}
												{#if domRoutes.length === 0}
													<span class="route-mini-badge new">+ Новый</span>
												{/if}
											</div>
										{/each}
									</div>
								</div>
							{/if}

							<div class="details-section">
								<span class="details-label">Целевые IP-адреса и порты:</span>
								<div class="ips-chips">
									{#each group.ips as ip}
										{@const ipRoutes = group.ipStatuses?.[ip] || []}
										{@const itemInfo = analyzeItemRoutes(ipRoutes)}
										<div class="chip-item-with-status">
											<span class="ip-badge">{ip}</span>
											{#if itemInfo.active.length > 0}
												{#each itemInfo.active as st}
													<span class="route-mini-badge routed {st.target}" title="{st.targetLabel}: {st.ruleName} ({st.matchedPattern})">
														✓ {st.targetLabel}: {st.matchedPattern || st.ruleName}
													</span>
												{/each}
											{/if}
											{#if itemInfo.catalog.length > 0}
												{#each itemInfo.catalog as st}
													<span class="route-mini-badge catalog" title="В каталоге пресетов: {st.ruleName} ({st.matchedPattern})">
														📖 Каталог: {st.matchedPattern || st.ruleName}
													</span>
												{/each}
											{/if}
											{#if ipRoutes.length === 0}
												<span class="route-mini-badge new">+ Новый IP</span>
											{/if}
										</div>
									{/each}
									<span class="ports-badge">Порты: {group.ports.join(', ')}</span>
								</div>
							</div>

							{#if group.sessions && group.sessions.length > 0}
								<div class="sessions-micro-table">
									<div class="micro-header">
										<span>Протокол</span>
										<span>Сокет назначения</span>
										<span>Домен</span>
										<span>Состояние</span>
										<span>Передано</span>
									</div>
									{#each group.sessions as s}
										<div class="micro-row">
											<span class="proto-tag {s.protocol}">{s.protocol.toUpperCase()}</span>
											<span class="socket-txt font-mono">{s.dstIp}:{s.dstPort}</span>
											<span class="sub-dom-txt">{s.domain || '—'}</span>
											<span class="state-txt">{s.state || 'ACTIVE'}</span>
											<span class="bytes-txt">↓ {formatBytes(s.bytesIn)} ↑ {formatBytes(s.bytesOut)}</span>
										</div>
									{/each}
								</div>
							{/if}
						</div>
					{/if}
				</div>
			{/each}
		</div>
	{:else}
		<!-- Mode 2: All Sockets / Raw Sessions Table -->
		<div class="table-container card">
			<table class="sockets-table">
				<thead>
					<tr>
						<th style="width: 40px;"></th>
						<th>Протокол</th>
						<th>Порт устр.</th>
						<th>Назначение (IP:Порт)</th>
						<th>Домен / Сервис</th>
						<th>Состояние</th>
						<th>Трафик</th>
						<th>TTL</th>
						<th>Действие</th>
					</tr>
				</thead>
				<tbody>
					{#each filteredSessions as s}
						{@const isSelected = (s.domain && selectedDomains.includes(s.domain)) || selectedIPs.includes(s.dstIp)}
						<tr class:selected-row={isSelected}>
							<td>
								<input
									type="checkbox"
									checked={isSelected}
									onchange={() => toggleSessionSelect(s.id, s.domain, s.dstIp)}
								/>
							</td>
							<td>
								<span class="proto-tag {s.protocol}">{s.protocol.toUpperCase()}</span>
							</td>
							<td class="font-mono">:{s.srcPort}</td>
							<td class="font-mono"><strong>{s.dstIp}</strong>:{s.dstPort}</td>
							<td>
								<div class="table-domain-col">
									{#if s.knowledge?.title || s.serviceName}
										<span class="service-pill-sm">{s.knowledge?.title || s.serviceName}</span>
									{/if}
									<span class="table-domain-text">{s.domain || s.dstIp}</span>
									{#if s.knowledge?.country}
										<span class="table-country-text">({s.knowledge.country})</span>
									{/if}
								</div>
							</td>
							<td>
								<span class="state-badge state-{s.state.toLowerCase()}">{s.state || 'ACTIVE'}</span>
							</td>
							<td>↓ {formatBytes(s.bytesIn)} / ↑ {formatBytes(s.bytesOut)}</td>
							<td>{s.ttl}s</td>
							<td>
								<Button
									variant="secondary"
									size="sm"
									onclick={() => {
										selectedDomains = s.domain ? [s.domain] : [];
										selectedIPs = [s.dstIp];
										exportDefaultService = s.knowledge?.title || s.serviceName || s.domain || s.dstIp;
										exportDomainStatuses = s.domain && s.domainRoutes ? { [s.domain]: s.domainRoutes } : {};
										exportIPStatuses = s.ipRoutes ? { [s.dstIp]: s.ipRoutes } : {};
										exportExistingRules = [];
										if (s.domainRoutes) {
											for (const r of s.domainRoutes) exportExistingRules.push(`${r.targetLabel}: ${r.ruleName}`);
										}
										if (s.ipRoutes) {
											for (const r of s.ipRoutes) exportExistingRules.push(`${r.targetLabel}: ${r.ruleName}`);
										}
										showExportModal = true;
									}}
								>
									<Plus size={13} />
								</Button>
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}

	<!-- Floating Multi-select Export Bar -->
	{#if selectedDomains.length > 0 || selectedIPs.length > 0}
		<div class="floating-action-bar">
			<div class="selected-summary">
				<Check size={16} class="check-icon" />
				<span>Выбрано: <strong>{selectedDomains.length}</strong> доменов, <strong>{selectedIPs.length}</strong> IP</span>
			</div>
			<div class="floating-actions">
				<Button variant="secondary" size="sm" onclick={() => { selectedDomains = []; selectedIPs = []; }}>
					Снять выбор
				</Button>
				<Button variant="primary" size="sm" onclick={openBulkExport}>
					<Plus size={15} /> Экспортировать в правила / каталог
				</Button>
			</div>
		</div>
	{/if}

	<!-- Export Modal -->
	{#if showExportModal}
		<TrafficExportModal
			domains={selectedDomains}
			ips={selectedIPs}
			defaultServiceName={exportDefaultService}
			activeEngines={snapshot?.activeEngines || []}
			domainStatuses={exportDomainStatuses}
			ipStatuses={exportIPStatuses}
			existingRules={exportExistingRules}
			onclose={() => (showExportModal = false)}
			onsuccess={() => {
				showExportModal = false;
				selectedDomains = [];
				selectedIPs = [];
				fetchSnapshot();
			}}
		/>
	{/if}
</div>

<style>
	.traffic-panel {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.top-controls {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: 1rem;
		padding: 0.85rem 1.15rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
	}

	.device-select-area {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		flex: 1;
		min-width: 280px;
	}

	.label-with-icon {
		display: flex;
		align-items: center;
		gap: 0.45rem;
		font-weight: 500;
		color: var(--color-text-primary);
		font-size: 0.88rem;
		white-space: nowrap;
	}

	.accent-icon {
		color: var(--color-accent);
	}

	.dropdown-wrapper {
		flex: 1;
		max-width: 380px;
	}

	.actions-area {
		display: flex;
		align-items: center;
		gap: 0.65rem;
	}

	.recording-indicator {
		display: flex;
		align-items: center;
		gap: 0.45rem;
		padding: 0.3rem 0.65rem;
		border-radius: var(--radius-pill);
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		font-size: 0.8rem;
		color: var(--color-text-secondary);
	}

	.recording-indicator.active {
		background: var(--color-success-tint);
		border-color: var(--color-success-border);
		color: var(--color-success);
		font-weight: 500;
	}

	.pulse-dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--color-text-muted);
	}

	.recording-indicator.active .pulse-dot {
		background: var(--color-success);
		box-shadow: 0 0 6px var(--color-success);
		animation: pulse 1.5s infinite;
	}

	@keyframes pulse {
		0% { opacity: 1; }
		50% { opacity: 0.4; }
		100% { opacity: 1; }
	}

	.stats-row {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
		gap: 0.65rem;
	}

	.stat-card {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		padding: 0.75rem 0.95rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
	}

	.stat-icon {
		display: flex;
		align-items: center;
		justify-content: center;
		width: 34px;
		height: 34px;
		border-radius: 8px;
		background: var(--color-accent-tint);
		color: var(--color-accent);
	}

	.stat-icon.in {
		background: var(--color-info-tint);
		color: var(--color-info);
	}

	.stat-icon.out {
		background: var(--color-warning-tint);
		color: var(--color-warning);
	}

	.stat-body {
		display: flex;
		flex-direction: column;
	}

	.stat-val {
		font-size: 1.1rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.stat-val small {
		font-size: 0.82rem;
		font-weight: 400;
		color: var(--color-text-muted);
	}

	.stat-lbl {
		font-size: 0.75rem;
		color: var(--color-text-secondary);
	}

	.filter-row {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.65rem;
	}

	.search-input-wrap {
		position: relative;
		display: flex;
		align-items: center;
		flex: 1;
		min-width: 220px;
	}

	.search-input-wrap input {
		width: 100%;
		padding: 0.4rem 1.8rem 0.4rem 2rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		color: var(--color-text-primary);
		font-size: 0.85rem;
	}

	.search-input-wrap input:focus {
		border-color: var(--color-accent);
		outline: none;
	}

	.search-input-wrap :global(.search-icon) {
		position: absolute;
		left: 0.6rem;
		color: var(--color-text-muted);
		pointer-events: none;
	}

	.clear-search-btn {
		position: absolute;
		right: 0.5rem;
		background: none;
		border: none;
		color: var(--color-text-muted);
		cursor: pointer;
		font-size: 1.1rem;
	}

	.domain-groups-list {
		display: flex;
		flex-direction: column;
		gap: 0.45rem;
	}

	.domain-card {
		padding: 0.65rem 0.85rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		transition: border-color 0.15s ease, background-color 0.15s ease;
	}

	.domain-card:hover {
		border-color: var(--color-border-hover);
	}

	.domain-card.selected {
		border-color: var(--color-accent);
		background: var(--color-accent-tint);
	}

	.card-main-row {
		display: flex;
		align-items: center;
		gap: 0.75rem;
	}

	.checkbox-container {
		display: flex;
		align-items: center;
		cursor: pointer;
	}

	.service-icon-tile {
		display: flex;
		align-items: center;
		justify-content: center;
		width: 32px;
		height: 32px;
		border-radius: 6px;
		background: var(--color-bg-tertiary);
		flex-shrink: 0;
	}

	.domain-info {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 0.15rem;
	}

	.domain-header-line {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.45rem;
	}

	.domain-title {
		font-weight: 600;
		font-size: 0.92rem;
		color: var(--color-text-primary);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.route-tag-wrapper {
		position: relative;
		display: inline-flex;
		align-items: center;
	}

	.route-status-tag {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
		font-size: 0.72rem;
		font-weight: 500;
		padding: 0.08rem 0.45rem;
		border-radius: 4px;
		white-space: nowrap;
		cursor: default;
		transition: all 0.15s ease;
	}

	.route-status-tag.interactive {
		cursor: pointer;
	}

	.route-status-tag.interactive:hover {
		filter: brightness(0.92);
		box-shadow: 0 1px 4px rgba(0, 0, 0, 0.1);
	}

	.route-status-tag.routed {
		background: var(--color-success-tint, rgba(16, 185, 129, 0.08));
		border: 1px solid var(--color-success-border, rgba(16, 185, 129, 0.2));
		color: var(--color-success, #10b981);
	}

	.route-status-tag.partial {
		background: var(--color-warning-tint, rgba(245, 158, 11, 0.08));
		border: 1px solid var(--color-warning-border, rgba(245, 158, 11, 0.2));
		color: var(--color-warning, #f59e0b);
	}

	.route-status-tag.catalog {
		background: rgba(139, 92, 246, 0.08);
		border: 1px solid rgba(139, 92, 246, 0.25);
		color: #8b5cf6;
	}

	.route-status-tag.new {
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		color: var(--color-text-muted);
	}

	.tag-chevron {
		opacity: 0.65;
	}

	/* Route Popover Dropdown */
	.route-popover-menu {
		position: absolute;
		top: calc(100% + 6px);
		left: 0;
		z-index: 100;
		min-width: 320px;
		max-width: 440px;
		background: var(--color-bg-primary, #1e2029);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md, 8px);
		box-shadow: 0 8px 24px rgba(0, 0, 0, 0.25);
		padding: 0.75rem;
		display: flex;
		flex-direction: column;
		gap: 0.6rem;
		animation: popIn 0.15s ease-out;
	}

	@keyframes popIn {
		from {
			opacity: 0;
			transform: translateY(-4px);
		}
		to {
			opacity: 1;
			transform: translateY(0);
		}
	}

	.popover-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding-bottom: 0.4rem;
		border-bottom: 1px solid var(--color-border);
	}

	.popover-title {
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--color-text-primary);
		text-transform: uppercase;
		letter-spacing: 0.03em;
	}

	.popover-close-btn {
		display: flex;
		align-items: center;
		justify-content: center;
		background: transparent;
		border: none;
		color: var(--color-text-muted);
		cursor: pointer;
		padding: 2px;
		border-radius: 3px;
	}

	.popover-close-btn:hover {
		color: var(--color-text-primary);
		background: var(--color-bg-tertiary);
	}

	.popover-content {
		display: flex;
		flex-direction: column;
		gap: 0.6rem;
		max-height: 280px;
		overflow-y: auto;
	}

	.popover-section {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
	}

	.popover-section-title {
		display: flex;
		align-items: center;
		gap: 0.35rem;
		font-size: 0.72rem;
		font-weight: 600;
		color: var(--color-text-secondary);
	}

	.popover-items-list {
		display: flex;
		flex-direction: column;
		gap: 0.3rem;
	}

	.popover-row {
		display: flex;
		align-items: flex-start;
		gap: 0.5rem;
		padding: 0.35rem 0.5rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 5px;
		font-size: 0.74rem;
	}

	.popover-engine-badge {
		font-size: 0.65rem;
		font-weight: 600;
		padding: 0.08rem 0.35rem;
		border-radius: 3px;
		white-space: nowrap;
	}

	.popover-engine-badge.mihomo {
		background: rgba(59, 130, 246, 0.12);
		border: 1px solid rgba(59, 130, 246, 0.3);
		color: #3b82f6;
	}

	.popover-engine-badge.hydraroute {
		background: rgba(16, 185, 129, 0.12);
		border: 1px solid rgba(16, 185, 129, 0.3);
		color: #10b981;
	}

	.popover-engine-badge.singbox {
		background: rgba(245, 158, 11, 0.12);
		border: 1px solid rgba(245, 158, 11, 0.3);
		color: #f59e0b;
	}

	.popover-engine-badge.ndms,
	.popover-engine-badge.static_route {
		background: rgba(99, 102, 241, 0.12);
		border: 1px solid rgba(99, 102, 241, 0.3);
		color: #6366f1;
	}

	.popover-engine-badge.catalog {
		background: rgba(139, 92, 246, 0.12);
		border: 1px solid rgba(139, 92, 246, 0.3);
		color: #8b5cf6;
	}

	.popover-row-main {
		display: flex;
		flex-direction: column;
		gap: 0.1rem;
		flex: 1;
		min-width: 0;
	}

	.popover-rule-name {
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.popover-pattern {
		font-size: 0.68rem;
		color: var(--color-text-muted);
	}

	.popover-target-item {
		font-size: 0.67rem;
		color: var(--color-text-secondary);
	}

	.popover-empty {
		font-size: 0.74rem;
		color: var(--color-text-muted);
		padding: 0.5rem 0;
		text-align: center;
	}

	.chip-item-with-status {
		display: inline-flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.35rem;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: 4px;
		padding: 0.1rem 0.35rem;
	}

	.route-mini-badge {
		font-size: 0.68rem;
		font-weight: 500;
		padding: 0.04rem 0.28rem;
		border-radius: 3px;
		white-space: nowrap;
	}

	.route-mini-badge.routed {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		color: var(--color-text-secondary);
	}

	.route-mini-badge.catalog {
		background: rgba(139, 92, 246, 0.08);
		border: 1px solid rgba(139, 92, 246, 0.25);
		color: #8b5cf6;
	}

	.route-mini-badge.new {
		background: transparent;
		color: var(--color-text-muted);
	}

	.domains-count-badge {
		padding: 0.08rem 0.38rem;
		background: var(--color-accent-tint);
		border: 1px solid var(--color-accent-border);
		border-radius: 4px;
		font-size: 0.7rem;
		color: var(--color-accent);
		font-weight: 500;
	}

	.country-tag {
		padding: 0.08rem 0.4rem;
		background: var(--color-success-tint);
		border: 1px solid var(--color-success-border);
		border-radius: 4px;
		font-size: 0.7rem;
		color: var(--color-success);
		font-weight: 600;
	}

	.category-tag {
		padding: 0.08rem 0.38rem;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: 4px;
		font-size: 0.7rem;
		color: var(--color-text-secondary);
	}

	.domain-sub-line {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.4rem;
		font-size: 0.78rem;
		color: var(--color-text-secondary);
	}

	.domain-desc-text {
		color: var(--color-text-secondary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		max-width: 480px;
	}

	.meta-dot {
		opacity: 0.4;
	}

	.bytes {
		color: var(--color-text-primary);
		font-weight: 500;
	}

	.meta-org {
		color: var(--color-accent);
		font-weight: 500;
	}

	.card-actions {
		display: flex;
		align-items: center;
		gap: 0.45rem;
	}

	.expand-btn {
		display: flex;
		align-items: center;
		justify-content: center;
		width: 28px;
		height: 28px;
		background: transparent;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		color: var(--color-text-secondary);
		cursor: pointer;
		transition: background-color 0.15s ease, color 0.15s ease;
	}

	.expand-btn:hover {
		color: var(--color-text-primary);
		background: var(--color-bg-hover);
	}

	.expanded-details {
		margin-top: 0.65rem;
		padding-top: 0.65rem;
		border-top: 1px solid var(--color-border);
		display: flex;
		flex-direction: column;
		gap: 0.6rem;
	}

	.details-section {
		display: flex;
		flex-direction: column;
		gap: 0.3rem;
	}

	.details-label {
		font-size: 0.72rem;
		color: var(--color-text-muted);
		text-transform: uppercase;
		letter-spacing: 0.03em;
		font-weight: 500;
	}

	.domains-chips, .ips-chips {
		display: flex;
		flex-wrap: wrap;
		gap: 0.35rem;
	}

	.dom-badge {
		padding: 0.12rem 0.45rem;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: 4px;
		font-size: 0.74rem;
		font-family: var(--font-mono, monospace);
		color: var(--color-text-primary);
	}

	.ip-badge {
		padding: 0.12rem 0.45rem;
		background: var(--color-success-tint);
		border: 1px solid var(--color-success-border);
		border-radius: 4px;
		font-size: 0.74rem;
		font-family: var(--font-mono, monospace);
		color: var(--color-success);
	}

	.ports-badge {
		padding: 0.12rem 0.45rem;
		background: var(--color-bg-tertiary);
		border-radius: 4px;
		font-size: 0.74rem;
		color: var(--color-text-secondary);
	}

	.sessions-micro-table {
		display: flex;
		flex-direction: column;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		overflow: hidden;
		font-size: 0.76rem;
	}

	.micro-header {
		display: grid;
		grid-template-columns: 60px 150px 1fr 100px 160px;
		gap: 0.5rem;
		padding: 0.4rem 0.65rem;
		background: var(--color-bg-tertiary);
		font-weight: 600;
		color: var(--color-text-muted);
		border-bottom: 1px solid var(--color-border);
	}

	.micro-row {
		display: grid;
		grid-template-columns: 60px 150px 1fr 100px 160px;
		gap: 0.5rem;
		padding: 0.35rem 0.65rem;
		border-bottom: 1px solid var(--color-border);
		align-items: center;
		color: var(--color-text-primary);
	}

	.micro-row:last-child {
		border-bottom: none;
	}

	.proto-tag {
		display: inline-block;
		padding: 0.08rem 0.35rem;
		border-radius: 3px;
		font-size: 0.68rem;
		font-weight: 600;
		text-align: center;
	}

	.proto-tag.tcp {
		background: var(--color-accent-tint);
		color: var(--color-accent);
	}

	.proto-tag.udp {
		background: var(--color-warning-tint);
		color: var(--color-warning);
	}

	.sub-dom-txt {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		color: var(--color-text-secondary);
	}

	.state-txt {
		font-size: 0.72rem;
		color: var(--color-text-muted);
	}

	.bytes-txt {
		font-weight: 500;
	}

	/* Sockets Table */
	.table-container {
		overflow-x: auto;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
	}

	.sockets-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.82rem;
	}

	.sockets-table th {
		padding: 0.55rem 0.75rem;
		text-align: left;
		background: var(--color-bg-tertiary);
		color: var(--color-text-muted);
		font-weight: 600;
		border-bottom: 1px solid var(--color-border);
	}

	.sockets-table td {
		padding: 0.5rem 0.75rem;
		border-bottom: 1px solid var(--color-border);
		color: var(--color-text-primary);
	}

	.sockets-table tr:hover {
		background: var(--color-bg-hover);
	}

	.selected-row {
		background: var(--color-accent-tint) !important;
	}

	.table-domain-col {
		display: flex;
		align-items: center;
		gap: 0.4rem;
	}

	.service-pill-sm {
		padding: 0.08rem 0.35rem;
		background: var(--color-accent-tint);
		border-radius: 4px;
		color: var(--color-accent);
		font-size: 0.72rem;
		font-weight: 500;
	}

	.table-country-text {
		font-size: 0.72rem;
		color: var(--color-text-muted);
	}

	.state-badge {
		display: inline-block;
		padding: 0.1rem 0.4rem;
		border-radius: 4px;
		font-size: 0.72rem;
		background: var(--color-bg-tertiary);
		color: var(--color-text-muted);
	}

	.state-badge.state-established {
		background: var(--color-success-tint);
		color: var(--color-success);
	}

	/* Floating Bar */
	.floating-action-bar {
		position: fixed;
		bottom: 1.5rem;
		left: 50%;
		transform: translateX(-50%);
		display: flex;
		align-items: center;
		gap: 1.5rem;
		padding: 0.65rem 1.25rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-accent);
		border-radius: var(--radius-pill);
		box-shadow: 0 10px 25px rgba(0, 0, 0, 0.4);
		z-index: var(--z-fab, 95);
		color: var(--color-text-primary);
	}

	.selected-summary {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		font-size: 0.85rem;
	}

	.check-icon {
		color: var(--color-accent);
	}

	.floating-actions {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.empty-container, .loading-state {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		padding: 3rem 1.5rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		color: var(--color-text-secondary);
	}

	.loading-state p {
		margin-top: 0.75rem;
		font-size: 0.9rem;
	}

	.spin {
		animation: spin 1s linear infinite;
	}

	@keyframes spin {
		100% { transform: rotate(360deg); }
	}
</style>
