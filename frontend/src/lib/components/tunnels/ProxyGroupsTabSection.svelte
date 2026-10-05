<script lang="ts">
	import { Badge, Button, Modal } from '$lib/components/ui';
	import { EmptyState } from '$lib/components/layout';
	import { Plus, Trash2, Pencil, Activity, Check, Zap, Search, Layers, Radio } from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { awgTags as awgTagsStore } from '$lib/stores/awgTags';
	import { subscriptionsStore } from '$lib/stores/subscriptions';
	import { singboxDelayHistory } from '$lib/stores/singbox';
	import { tunnels } from '$lib/stores/tunnels';
	import { formatOutboundHumanName } from '$lib/utils/outboundHumanName';

	function pluralGroups(n: number): string {
		const m100 = n % 100;
		if (m100 >= 11 && m100 <= 14) return 'групп';
		const m10 = n % 10;
		if (m10 === 1) return 'группа';
		if (m10 >= 2 && m10 <= 4) return 'группы';
		return 'групп';
	}
	import type {
		MihomoNativeGroup,
		MihomoNativeProxy,
		MihomoNativeSubscription,
		MihomoRuntimeProxy,
		MihomoRuntimeProvider,
	} from '$lib/types';
	import MihomoGroupEditModal from '$lib/components/sb-router/mihomo/MihomoGroupEditModal.svelte';

	interface Props {
		loading?: boolean;
		groups: MihomoNativeGroup[];
		proxies?: MihomoNativeProxy[];
		subscriptions?: MihomoNativeSubscription[];
		runtimeProxies?: Record<string, MihomoRuntimeProxy>;
		runtimeProviders?: Record<string, MihomoRuntimeProvider>;
		susaninEgressId?: string;
		onGroupChanged?: () => void;
	}

	let {
		loading = false,
		groups = [],
		proxies = [],
		subscriptions = [],
		runtimeProxies = {},
		runtimeProviders = {},
		susaninEgressId = '',
		onGroupChanged,
	}: Props = $props();

	let searchQuery = $state('');
	let editModalOpen = $state(false);
	let selectedGroup = $state<MihomoNativeGroup | null>(null);
	let deletingId = $state<string | null>(null);
	let switchingMember = $state<Record<string, boolean>>({});
	let testingDelay = $state<Record<string, boolean>>({});
	let delays = $state<Record<string, number>>({});

	const connectivityMap = tunnels.connectivityMap;

	const nameContext = $derived({
		awgTags: $awgTagsStore.data,
		subscriptions: $subscriptionsStore.data,
		mihomoSubscriptions: subscriptions,
	});

	function memberLabel(member: string): string {
		const proxy = proxies.find((item) => item.id === member || item.name === member);
		if (proxy?.name) return proxy.name;
		return formatOutboundHumanName(member, nameContext);
	}

	function findRuntimeProxy(name: string): MihomoRuntimeProxy | undefined {
		if (runtimeProxies[name]) return runtimeProxies[name];
		for (const provider of Object.values(runtimeProviders)) {
			const found = provider.proxies?.find((p) => p.name === name);
			if (found) return found;
		}
		return undefined;
	}

	function getMemberDelay(name: string): number | null {
		if (!name) return null;
		if (name in delays && typeof delays[name] === 'number') {
			return delays[name];
		}
		const p = findRuntimeProxy(name);
		if (p?.history && p.history.length > 0) {
			const last = p.history[p.history.length - 1];
			if (typeof last?.delay === 'number' && last.delay > 0) {
				return last.delay;
			}
		}
		if (p?.now) {
			const child = findRuntimeProxy(p.now);
			if (child?.history && child.history.length > 0) {
				const last = child.history[child.history.length - 1];
				if (typeof last?.delay === 'number' && last.delay > 0) {
					return last.delay;
				}
			}
			const sbHist = $singboxDelayHistory.get(p.now);
			if (sbHist && sbHist.length > 0 && sbHist[sbHist.length - 1] > 0) {
				return sbHist[sbHist.length - 1];
			}
		}
		const sbHist = $singboxDelayHistory.get(name);
		if (sbHist && sbHist.length > 0 && sbHist[sbHist.length - 1] > 0) {
			return sbHist[sbHist.length - 1];
		}
		const sbSubs = $subscriptionsStore.data ?? [];
		const matchedSub = sbSubs.find((s) =>
			s.id === name || (name.startsWith('sub-') && s.id.startsWith(name.slice(4))) || s.label === name
		);
		if (matchedSub?.activeMember) {
			const child = findRuntimeProxy(matchedSub.activeMember);
			if (child?.history && child.history.length > 0) {
				const last = child.history[child.history.length - 1];
				if (typeof last?.delay === 'number' && last.delay > 0) {
					return last.delay;
				}
			}
			const subHist = $singboxDelayHistory.get(matchedSub.activeMember);
			if (subHist && subHist.length > 0 && subHist[subHist.length - 1] > 0) {
				return subHist[subHist.length - 1];
			}
		}
		const conn = $connectivityMap.get(name);
		if (conn?.latency && conn.latency > 0) {
			return conn.latency;
		}
		for (const [tid, val] of $connectivityMap.entries()) {
			if ((tid === name || tid.toLowerCase() === name.toLowerCase()) && val?.latency && val.latency > 0) {
				return val.latency;
			}
		}
		const tagMatch = ($awgTagsStore.data ?? []).find(
			(t) => t.tag === name || t.label === name || t.iface === name
		);
		if (tagMatch) {
			const c = $connectivityMap.get(tagMatch.tag);
			if (c?.latency && c.latency > 0) return c.latency;
		}
		return null;
	}

	function getDelayTone(delay: number | null): 'good' | 'medium' | 'bad' | 'none' {
		if (delay === null || delay === undefined) return 'none';
		if (delay <= 0) return 'bad';
		if (delay < 200) return 'good';
		if (delay < 600) return 'medium';
		return 'bad';
	}

	const filteredGroups = $derived.by(() => {
		const q = searchQuery.trim().toLowerCase();
		if (!q) return groups;
		return groups.filter((g) =>
			g.name.toLowerCase().includes(q) ||
			g.type.toLowerCase().includes(q) ||
			(g.proxies || []).some((p) => p.toLowerCase().includes(q))
		);
	});

	function openCreate() {
		selectedGroup = null;
		editModalOpen = true;
	}

	function openEdit(group: MihomoNativeGroup) {
		selectedGroup = group;
		editModalOpen = true;
	}

	async function selectMember(groupName: string, memberName: string) {
		if (switchingMember[groupName]) return;
		switchingMember[groupName] = true;
		try {
			await api.mihomoSelectProxy(groupName, memberName);
			notifications.success(`Активный узел переключён на ${memberName}`);
			onGroupChanged?.();
		} catch (err) {
			notifications.error(`Не удалось переключить узел: ${err instanceof Error ? err.message : String(err)}`);
		} finally {
			switchingMember[groupName] = false;
		}
	}

	async function testGroupDelay(group: MihomoNativeGroup) {
		const groupName = group.name;
		if (testingDelay[groupName]) return;
		testingDelay = { ...testingDelay, [groupName]: true };
		try {
			const groupDelay = await api.mihomoProxyDelay(groupName).catch(() => 0);
			if (groupDelay > 0) {
				delays = { ...delays, [groupName]: groupDelay };
			}
			const members = group.proxies || [];
			if (members.length > 0) {
				const testUrl = group.url || 'https://www.gstatic.com/generate_204';
				await Promise.allSettled(
					members.map(async (name) => {
						try {
							const d = await api.mihomoRuntimeDelay(name, testUrl, 3000);
							delays = { ...delays, [name]: d };
						} catch {
							delays = { ...delays, [name]: 0 };
						}
					})
				);
			}
			notifications.success(`Задержки для «${groupName}» обновлены`);
		} catch (err) {
			const msg = err instanceof Error ? err.message : String(err);
			notifications.warning(`Тест группы ${groupName}: ошибка (${msg})`);
		} finally {
			testingDelay = { ...testingDelay, [groupName]: false };
		}
	}

	async function testMemberDelay(memberName: string, testUrl?: string) {
		if (testingDelay[memberName]) return;
		testingDelay = { ...testingDelay, [memberName]: true };
		try {
			const url = testUrl || 'https://www.gstatic.com/generate_204';
			const d = await api.mihomoRuntimeDelay(memberName, url, 3000);
			delays = { ...delays, [memberName]: d };
		} catch {
			delays = { ...delays, [memberName]: 0 };
		} finally {
			testingDelay = { ...testingDelay, [memberName]: false };
		}
	}

	async function handleDelete(id: string) {
		try {
			await api.mihomoNativeDeleteGroup(id);
			notifications.success('Группа удалена');
			deletingId = null;
			onGroupChanged?.();
		} catch (err) {
			notifications.error(`Ошибка при удалении: ${err instanceof Error ? err.message : String(err)}`);
		}
	}

	function groupTypeLabel(type: string): string {
		switch (type) {
			case 'select':
				return 'Ручной выбор (Select)';
			case 'url-test':
				return 'Авто (наименьшая задержка)';
			case 'fallback':
				return 'Отказоустойчивость (Fallback)';
			case 'load-balance':
				return 'Балансировка (Load Balance)';
			case 'relay':
				return 'Цепочка (Relay)';
			default:
				return type;
		}
	}

	function typeBadgeVariant(type: string): 'info' | 'success' | 'warning' | 'purple' | 'muted' {
		switch (type) {
			case 'select':
				return 'info';
			case 'url-test':
				return 'success';
			case 'fallback':
				return 'warning';
			case 'load-balance':
				return 'purple';
			default:
				return 'muted';
		}
	}
</script>

{#snippet createIcon()}
	<Plus size={16} />
{/snippet}

<div class="proxy-groups-page">
	<!-- Toolbar matching standard TunnelsToolbar across the app -->
	<div class="tunnels-toolbar">
		<span class="tunnel-count">
			{groups.length} {pluralGroups(groups.length)}
		</span>
		<div class="toolbar-actions">
			<div class="relative flex items-center">
				<Search class="absolute left-2.5 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-[var(--color-text-muted)] pointer-events-none" />
				<input
					type="text"
					bind:value={searchQuery}
					placeholder="Поиск групп или узлов..."
					class="w-48 sm:w-64 pl-8 pr-3 h-8 text-xs bg-[var(--color-bg-primary)] border border-[var(--color-border)] rounded-md text-[var(--color-text-primary)] placeholder:text-[var(--color-text-muted)] focus:outline-none focus:border-[var(--color-accent)]"
				/>
			</div>
			<Button variant="primary" size="md" onclick={openCreate} iconBefore={createIcon}>
				Создать группу
			</Button>
		</div>
	</div>

	<!-- Groups Grid -->
	{#if filteredGroups.length === 0}
		{#if searchQuery}
			<EmptyState
				title="Ничего не найдено"
				description="По запросу «{searchQuery}» группы не найдены."
			/>
		{:else}
			<EmptyState
				title="Нет прокси-групп"
				description="Создайте первую группу для объединения прокси, подписок и туннелей с автоматическим выбором или балансировкой."
			>
				{#snippet action()}
					<Button variant="primary" size="sm" onclick={openCreate}>
						<Plus class="w-3.5 h-3.5 mr-1" />
						Создать группу
					</Button>
				{/snippet}
			</EmptyState>
		{/if}
	{:else}
		<div class="groups-grid grid grid-cols-1 lg:grid-cols-2 gap-3">
			{#each filteredGroups as group (group.id || group.name)}
				{@const rt = runtimeProxies[group.name]}
				{@const activeNow = rt?.now || (group.proxies?.[0] ?? '')}
				{@const isSusaninTarget = susaninEgressId === group.id || susaninEgressId === group.name}
				<div class="group-card bg-[var(--color-bg-secondary)] rounded-xl border border-[var(--color-border)] flex flex-col justify-between hover:border-[var(--color-border-hover)] transition-all shadow-xs">
					<div class="group-card-body">
						<!-- Compact Header Row -->
						<div class="group-card-header flex items-start justify-between gap-2">
							<div class="flex items-center gap-1.5 flex-wrap min-w-0">
								<span class="w-2 h-2 rounded-full shrink-0 {group.enabled !== false ? 'bg-[var(--color-success)]' : 'bg-gray-400'}" title={group.enabled !== false ? 'Включена' : 'Отключена'}></span>
								<Layers class="w-4 h-4 text-[var(--color-accent)] shrink-0" />
								<h3 class="font-semibold text-sm text-[var(--color-text-primary)] leading-tight truncate">
									{group.name}
								</h3>
								<Badge variant={typeBadgeVariant(group.type)} size="sm">
									{groupTypeLabel(group.type)}
								</Badge>
								{#if isSusaninTarget}
									<Badge variant="purple" size="sm">
										<Radio class="w-3 h-3 mr-1" />
										Susanin Egress
									</Badge>
								{/if}
							</div>

							<!-- Action Buttons -->
							<div class="group-card-actions flex items-center gap-1 shrink-0">
								<button
									class="p-1.5 rounded-lg hover:bg-[var(--color-bg-tertiary)] text-[var(--color-text-muted)] hover:text-[var(--color-accent)] transition-colors cursor-pointer"
									title="Проверить задержки всех узлов группы"
									disabled={testingDelay[group.name]}
									onclick={() => testGroupDelay(group)}
								>
									<Activity class="w-3.5 h-3.5 {testingDelay[group.name] ? 'animate-spin text-[var(--color-accent)]' : ''}" />
								</button>
								<button
									class="p-1.5 rounded-lg hover:bg-[var(--color-bg-tertiary)] text-[var(--color-text-muted)] hover:text-[var(--color-accent)] transition-colors cursor-pointer"
									title="Редактировать группу"
									onclick={() => openEdit(group)}
								>
									<Pencil class="w-3.5 h-3.5" />
								</button>
								<button
									class="p-1.5 rounded-lg hover:bg-[var(--color-bg-tertiary)] text-[var(--color-text-muted)] hover:text-[var(--color-error)] transition-colors cursor-pointer"
									title="Удалить группу"
									onclick={() => (deletingId = group.id)}
								>
									<Trash2 class="w-3.5 h-3.5" />
								</button>
							</div>
						</div>

						<!-- Compact Active Node Bar -->
						<div class="group-active-node flex items-center justify-between gap-2 rounded-lg bg-[var(--color-bg-tertiary)] border border-[var(--color-border)] text-xs">
							<div class="flex items-center gap-1.5 min-w-0">
								<Zap class="w-3.5 h-3.5 text-amber-500 shrink-0" />
								<span class="text-[10px] uppercase tracking-wider font-semibold text-[var(--color-text-muted)] shrink-0">Активен:</span>
								<span class="font-mono text-xs text-[var(--color-text-primary)] font-medium truncate" title={activeNow}>
									{memberLabel(activeNow) || 'Не определён'}
								</span>
							</div>

							<div class="flex items-center gap-2 shrink-0">
								{#if delays[group.name] || getMemberDelay(activeNow)}
									{@const activeDelay = delays[group.name] ?? getMemberDelay(activeNow)}
									{@const activeTone = getDelayTone(activeDelay)}
									<span class="text-[11px] font-mono font-medium px-1.5 py-0.5 rounded {activeTone === 'good' ? 'text-[var(--color-success)] bg-[var(--color-success)]/10' : activeTone === 'medium' ? 'text-amber-500 bg-amber-500/10' : 'text-[var(--color-error)] bg-[var(--color-error)]/10'}">
										{activeDelay && activeDelay > 0 ? `${activeDelay} мс` : 'таймаут'}
									</span>
								{/if}
								{#if group.type === 'select'}
									<span class="text-[10px] text-[var(--color-accent)] hidden sm:inline">кликните узел для выбора</span>
								{/if}
							</div>
						</div>

						<!-- Member Chips -->
						{#if (group.proxies || []).length > 0}
							<div class="group-members">
								<div class="flex items-center justify-between text-[10px] uppercase tracking-wider font-semibold text-[var(--color-text-muted)]">
									<span>Узлы ({group.proxies?.length || 0})</span>
									{#if (group.use || []).length > 0}
										<span>+ {group.use.length} подписок</span>
									{/if}
								</div>
								<div class="group-member-list flex flex-wrap gap-1.5 max-h-36 overflow-y-auto">
									{#each group.proxies || [] as member}
										{@const isSelected = activeNow === member}
										{@const delay = getMemberDelay(member)}
										{@const tone = getDelayTone(delay)}
										{@const isTesting = Boolean(testingDelay[member])}
										{#if group.type === 'select'}
											<button
												type="button"
												class="px-2 py-1 text-xs rounded-md border transition-all flex items-center gap-1.5 cursor-pointer {isSelected ? 'bg-[var(--color-accent-tint)] border-[var(--color-accent)] text-[var(--color-accent)] font-medium shadow-xs' : 'bg-[var(--color-bg-tertiary)] border-[var(--color-border)] text-[var(--color-text-secondary)] hover:border-[var(--color-border-hover)] hover:text-[var(--color-text-primary)]'}"
												disabled={switchingMember[group.name]}
												onclick={() => selectMember(group.name, member)}
												title="Сделать узел активным"
											>
												{#if isSelected}
													<Check class="w-3 h-3 text-[var(--color-accent)] shrink-0" />
												{/if}
												<span class="truncate max-w-[180px]" title={member}>{memberLabel(member)}</span>
												<span
													role="button"
													tabindex={0}
													class="delay-badge {tone} text-[10px] font-mono px-1 py-0.5 rounded shrink-0 flex items-center gap-0.5 hover:opacity-80 transition-opacity border-0"
													class:animate-pulse={isTesting}
													title="Проверить задержку узла"
													onclick={(e) => {
														e.stopPropagation();
														void testMemberDelay(member, group.url);
													}}
													onkeydown={(e) => {
														if (e.key === 'Enter' || e.key === ' ') {
															e.stopPropagation();
															void testMemberDelay(member, group.url);
														}
													}}
												>
													{#if isTesting}
														<span class="text-[8px] animate-spin">↻</span>
													{/if}
													<span>{delay !== null ? (delay > 0 ? `${delay} мс` : 'таймаут') : '...'}</span>
												</span>
											</button>
										{:else}
											<span class="px-2 py-0.5 text-xs rounded-md border flex items-center gap-1.5 {isSelected ? 'bg-[var(--color-bg-tertiary)] border-[var(--color-success)] text-[var(--color-success)] font-medium' : 'bg-[var(--color-bg-tertiary)] border-[var(--color-border)] text-[var(--color-text-secondary)]'}">
												{#if isSelected}
													<Check class="w-3 h-3 text-[var(--color-success)] shrink-0" />
												{/if}
												<span class="truncate max-w-[180px]" title={member}>{memberLabel(member)}</span>
												<span
													role="button"
													tabindex={0}
													class="delay-badge {tone} text-[10px] font-mono px-1 py-0.5 rounded shrink-0 flex items-center gap-0.5 hover:opacity-80 transition-opacity border-0 cursor-pointer"
													class:animate-pulse={isTesting}
													title="Проверить задержку узла"
													onclick={(e) => {
														e.stopPropagation();
														void testMemberDelay(member, group.url);
													}}
													onkeydown={(e) => {
														if (e.key === 'Enter' || e.key === ' ') {
															e.stopPropagation();
															void testMemberDelay(member, group.url);
														}
													}}
												>
													{#if isTesting}
														<span class="text-[8px] animate-spin">↻</span>
													{/if}
													<span>{delay !== null ? (delay > 0 ? `${delay} мс` : 'таймаут') : '...'}</span>
												</span>
											</span>
										{/if}
									{/each}
								</div>
							</div>
						{/if}
					</div>

					<!-- Card Footer info -->
					{#if group.interval || group.tolerance || group.lazy}
					<div class="group-card-footer border-t border-[var(--color-border)] text-[11px] text-[var(--color-text-muted)] flex items-center justify-between">
							<div class="flex items-center gap-2">
								{#if group.interval}
									<span>Интервал: <strong class="text-[var(--color-text-primary)]">{group.interval} с</strong></span>
								{/if}
								{#if group.tolerance}
									<span>Допуск: <strong class="text-[var(--color-text-primary)]">{group.tolerance} мс</strong></span>
								{/if}
							</div>
							{#if group.lazy}
								<span class="text-[10px] font-mono bg-[var(--color-bg-tertiary)] px-1.5 py-0.5 rounded border border-[var(--color-border)]">lazy check</span>
							{/if}
						</div>
					{/if}
				</div>
			{/each}
		</div>
	{/if}
</div>

<!-- Edit / Create Modal -->
<MihomoGroupEditModal
	open={editModalOpen}
	group={selectedGroup}
	{groups}
	{proxies}
	{subscriptions}
	runtimeProxies={Object.values(runtimeProxies)}
	onClose={() => (editModalOpen = false)}
	onSaved={() => {
		editModalOpen = false;
		onGroupChanged?.();
	}}
	onDelete={(id) => {
		editModalOpen = false;
		handleDelete(id);
	}}
/>

<!-- Delete Confirm Modal -->
{#if deletingId}
	{@const target = groups.find((g) => g.id === deletingId)}
	<Modal
		open={Boolean(deletingId)}
		title="Удаление прокси-группы"
		onclose={() => (deletingId = null)}
	>
		<div class="p-4 space-y-4">
			<p class="text-sm text-[var(--color-text-primary)]">
				Вы действительно хотите удалить группу <strong class="text-[var(--color-error)]">{target?.name}</strong>?
			</p>
			<p class="text-xs text-[var(--color-text-muted)]">
				Правила маршрутизации, ссылавшиеся на эту группу, будут перенаправлены на DIRECT.
			</p>
			<div class="flex justify-end gap-2 pt-2">
				<Button variant="secondary" onclick={() => (deletingId = null)}>Отмена</Button>
				<Button variant="danger" onclick={() => deletingId && handleDelete(deletingId)}>
					Удалить
				</Button>
			</div>
		</div>
	</Modal>
{/if}

<style>
	.proxy-groups-page {
		width: 100%;
	}

	.tunnels-toolbar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		margin-bottom: 1rem;
	}

	.tunnel-count {
		font-size: 0.8125rem;
		color: var(--color-text-muted);
	}

	.toolbar-actions {
		display: flex;
		align-items: center;
		justify-content: flex-end;
		flex-wrap: wrap;
		gap: 0.5rem;
	}

	.toolbar-actions :global(.btn.size-md) {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		box-sizing: border-box;
		height: 32px;
		min-height: 32px;
		max-height: 32px;
		padding-block: 0;
	}

	.groups-grid { align-items: stretch; }
	.group-card { padding: 1rem; min-height: 210px; }
	.group-card-body > * + * { margin-top: 0.8rem; }
	.group-card-header { padding-bottom: 0.15rem; }
	.group-card-actions button { padding: 0.4rem; }
	.group-active-node { padding: 0.55rem 0.7rem; }
	.group-members > * + * { margin-top: 0.35rem; }
	.group-member-list { padding: 0.15rem 0; }
	.group-member-list button,
	.group-member-list > span { padding: 0.3rem 0.55rem; }
	.group-card-footer { margin-top: 0.85rem; padding-top: 0.65rem; }

	.delay-badge {
		user-select: none;
	}
	.delay-badge.good { background: rgba(34, 197, 94, 0.14); color: #16a34a; }
	.delay-badge.medium { background: rgba(234, 179, 8, 0.14); color: #d97706; }
	.delay-badge.bad { background: rgba(239, 68, 68, 0.14); color: #dc2626; }
	.delay-badge.none { background: var(--color-bg-primary); color: var(--color-text-muted); border: 1px solid var(--color-border); }

	@media (max-width: 760px) {
		.tunnels-toolbar {
			flex-direction: column;
			align-items: stretch;
			gap: 0.75rem;
		}
		.toolbar-actions {
			justify-content: stretch;
		}
		.toolbar-actions > * {
			flex: 1;
		}
		.group-card { padding: 0.8rem; min-height: 0; }
	}
</style>
