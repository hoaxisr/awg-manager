<script lang="ts">
	import { onDestroy, onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { PageContainer, PageHeader, LoadingSpinner } from '$lib/components/layout';
	import { Card, ConfirmModal, Tabs } from '$lib/components/ui';
	import {
		BinaryStrip,
		ExitDetail,
		ExitWizard,
		InstanceList,
		RunBar,
		binaryStripItems,
		deleteProxyInstance,
		exitInstance,
		exitRows,
		exitConfigSetupComplete,
		normalizeExitConfigs,
		renameProxyInstance,
		reportDeletedTunnels,
		saveExitInstance,
		shareRows,
		toggleProxyInstance,
	} from '$lib/components/proxy';
	import type { ExitConfig, ExitProtocol, ProxyInstanceRow } from '$lib/components/proxy';
	import { createSelfReschedulingPoll } from '$lib/utils/selfReschedulingPoll';
	import { proxyClientOpsMode, proxyInOpsMode } from '$lib/utils/proxyOpsMode';
	import { formatUptime } from '$lib/components/freeturn/uptime';
	import { errText } from '$lib/utils/errorMessage';
	import type {
		AccessPolicy,
		FreeTurnConfig,
		FreeTurnStatus,
		TunnelListItem,
		WdttConfig,
		WdttStatus,
	} from '$lib/types';

	type TabId = 'exit' | 'share';

	let activeTab = $state<TabId>('exit');
	let loading = $state(true);
	let loadError = $state('');

	let wdttConfig = $state<WdttConfig | null>(null);
	let ftConfig = $state<FreeTurnConfig | null>(null);
	let wdttStatus = $state<WdttStatus | null>(null);
	let ftStatus = $state<FreeTurnStatus | null>(null);
	let policies = $state<AccessPolicy[]>([]);
	let tunnels = $state<TunnelListItem[]>([]);
	let savingExit = $state(false);

	let selectedExitKey = $state<string | null>(null);
	let selectedShareKey = $state<string | null>(null);
	/** Мастер «Выхода», открытый явно: кнопкой списка (новый) или «Мастер». */
	let exitWizard = $state<'new' | 'instance' | null>(null);
	/** Авто-мастер ненастроенного инстанса закрыт пользователем. */
	let exitWizardClosed = $state(false);
	let shareWizard = $state(false);
	let deleteTarget = $state<ProxyInstanceRow | null>(null);
	let deleting = $state(false);
	let busyKeys = $state<string[]>([]);
	let installing = $state<'wdtt' | 'freeturn' | null>(null);

	// Строки собираются из статуса (жизнь процесса) и конфига (автоподключение, режим).
	const sources = $derived({ wdttStatus, wdttConfig, ftStatus, ftConfig });
	const exits = $derived(exitRows(sources));
	const shares = $derived(shareRows(sources));

	const selectedExit = $derived(exits.find((r) => r.key === selectedExitKey) ?? null);
	const exitWdttClient = $derived(
		selectedExit?.protocol === 'wdtt'
			? wdttConfig?.clients.find((c) => c.id === selectedExit.id)?.config
			: undefined,
	);
	const exitFtClient = $derived(
		selectedExit?.protocol === 'freeturn'
			? ftConfig?.clients.find((c) => c.id === selectedExit.id)?.config
			: undefined,
	);
	const exitStatus = $derived(
		selectedExit?.protocol === 'wdtt'
			? wdttStatus?.clients?.find((c) => c.id === selectedExit.id)?.status
			: ftStatus?.clients?.find((c) => c.id === selectedExit?.id)?.status,
	);
	const selectedShare = $derived(shares.find((r) => r.key === selectedShareKey) ?? null);
	const selected = $derived(activeTab === 'exit' ? selectedExit : selectedShare);

	// Конфиг настроен ровно по критерию шага 2 мастера — это и есть setupComplete
	// панелей, по которому инстанс уходит из мастера в деталь (решение Q12).
	const exitSetupComplete = $derived(exitConfigSetupComplete(exitWdttClient, exitFtClient));
	const exitLife = $derived({
		running: selectedExit?.state === 'running',
		startedAt: selectedExit?.startedAt,
		enabled: selectedExit?.autostart,
	});
	const exitOpsMode = $derived(
		proxyClientOpsMode({ ...exitLife, setupComplete: exitSetupComplete }),
	);
	/** Инстанс ни разу не поднимался — только тогда возврат в мастер осмыслен (RB-08). */
	const exitNeverRan = $derived(!proxyInOpsMode(exitLife));
	// Мастер подменяет деталь, пока инстанс не настроен (ia.md §1).
	const exitWizardOpen = $derived(
		exitWizard !== null || (!!selectedExit && !exitOpsMode && !exitWizardClosed),
	);
	const wizardOpen = $derived(activeTab === 'exit' ? exitWizardOpen : shareWizard);
	/** Занятые локальные порты: подсказка порта новому клиенту. */
	const usedListens = $derived({
		wdtt: (wdttConfig?.clients ?? []).map((c) => c.config.listen),
		freeturn: (ftConfig?.clients ?? []).map((c) => c.config.listen),
	});

	// Выбор не переживает удаление инстанса — уводим на первую строку вкладки.
	$effect(() => {
		if (exits.length > 0 && !exits.some((r) => r.key === selectedExitKey)) {
			selectedExitKey = exits[0].key;
		}
		if (shares.length > 0 && !shares.some((r) => r.key === selectedShareKey)) {
			selectedShareKey = shares[0].key;
		}
	});

	const tabs = [
		{ id: 'exit', label: 'Выход' },
		{ id: 'share', label: 'Раздача' },
	];

	const binaries = $derived(binaryStripItems(wdttStatus, ftStatus, installing, install));

	// RB-07: порты раздачи приезжают вместе с деталью «Раздача» (задача 5).
	const shareMeta = $derived(
		[formatUptime(selectedShare?.startedAt), selectedShare?.pid ? `PID ${selectedShare.pid}` : '']
			.filter(Boolean)
			.join(' · '),
	);

	// ─── Загрузка и поллинг.

	async function loadStatuses() {
		const [w, f] = await Promise.all([api.getWdttStatus(), api.getFreeTurnStatus()]);
		wdttStatus = w;
		ftStatus = f;
	}

	// Конфиги страницы — это состояние сервера: их правит только загрузка и
	// ответ на сохранение. Правки пользователя живут в копии внутри детали.
	async function loadConfigs() {
		const [w, f] = await Promise.all([api.getWdttConfig(), api.getFreeTurnConfig()]);
		normalizeExitConfigs(w, f);
		wdttConfig = w;
		ftConfig = f;
	}

	// Каталог для секции «Куда идёт трафик»: политики (обратное членство) и
	// туннели (карточка связанного AWG). Вторичен — сбой не гасит страницу.
	async function loadCatalog() {
		try {
			const [p, t] = await Promise.all([api.listAccessPolicies(), api.getTunnelsAll()]);
			policies = p;
			tunnels = t.tunnels ?? [];
		} catch {
			/* каталог вторичен */
		}
	}

	async function reloadAll() {
		await Promise.all([loadConfigs(), loadStatuses(), loadCatalog()]);
	}

	const statusPoll = createSelfReschedulingPoll(async () => {
		try {
			await loadStatuses();
		} catch {
			/* поллинг молчит: разовый сбой сети не должен засыпать экран ошибками */
		}
	});

	onMount(async () => {
		try {
			await Promise.all([loadConfigs(), loadStatuses(), loadCatalog()]);
		} catch (e) {
			loadError = errText(e);
		} finally {
			loading = false;
		}
		statusPoll.start();
	});

	onDestroy(() => statusPoll.stop());

	// ─── Мутации.

	function withBusy(key: string, on: boolean) {
		busyKeys = on ? [...busyKeys, key] : busyKeys.filter((k) => k !== key);
	}

	async function install(kind: 'wdtt' | 'freeturn') {
		installing = kind;
		try {
			if (kind === 'wdtt') await api.installWdttClient();
			else await api.installFreeTurn();
			await loadStatuses();
		} catch (e) {
			notifications.error(errText(e));
		} finally {
			installing = null;
		}
	}

	async function toggleInstance(row: ProxyInstanceRow, on: boolean) {
		withBusy(row.key, true);
		try {
			await toggleProxyInstance(row, on);
			await Promise.all([loadConfigs(), loadStatuses()]);
		} catch (e) {
			notifications.error(errText(e));
		} finally {
			withBusy(row.key, false);
		}
	}

	/**
	 * Сохранение параметров клиента: смена адреса сервера останавливает клиент
	 * (W-27), работающему после сохранения — просьба перезапустить (TS-04).
	 * Возвращает конфиг сервера после записи; null — не сохранилось, и деталь
	 * по нему не запускает клиента (EX-32).
	 */
	async function saveExit(config: ExitConfig): Promise<ExitConfig | null> {
		const row = selectedExit;
		const inst = exitInstance(row, wdttConfig, ftConfig);
		if (!row || !inst) return null;
		savingExit = true;
		try {
			const res = await saveExitInstance(row, inst, config);
			reportDeletedTunnels(res.deletedTunnels, res.tunnelErrors);
			if (row.state === 'running' && res.peerChanged) await toggleInstance(row, false);
			else if (row.state === 'running') {
				notifications.info('Перезапустите клиент, чтобы изменения применились');
			}
			await loadStatuses();
			return res.config;
		} catch (e) {
			notifications.error(errText(e));
			return null;
		} finally {
			savingExit = false;
		}
	}

	/** Мастер довёл инстанс до запуска — «Готово» уводит в его деталь (ia.md §2.3). */
	async function exitWizardDone(protocol: ExitProtocol, id: string) {
		exitWizard = null;
		exitWizardClosed = false;
		await reloadAll();
		selectedExitKey = `${protocol}:client:${id}`;
	}

	async function renameInstance(row: ProxyInstanceRow, name: string) {
		try {
			await renameProxyInstance(row, name);
			await Promise.all([loadConfigs(), loadStatuses()]);
		} catch (e) {
			notifications.error(errText(e));
		}
	}

	async function deleteInstance() {
		const row = deleteTarget;
		if (!row) return;
		deleting = true;
		try {
			const res = await deleteProxyInstance(row);
			reportDeletedTunnels(res.deletedTunnels, res.tunnelErrors);
			deleteTarget = null;
			await Promise.all([loadConfigs(), loadStatuses()]);
		} catch (e) {
			notifications.error(errText(e));
		} finally {
			deleting = false;
		}
	}
</script>

<PageContainer>
	<PageHeader title="Прокси" description="Выход трафика роутера и раздача другим" />

	{#if loading}
		<LoadingSpinner />
	{:else if loadError}
		<Card><p class="load-error">{loadError}</p></Card>
	{:else}
		<BinaryStrip {binaries} />

		<Tabs
			{tabs}
			active={activeTab}
			urlParam="tab"
			defaultTab="exit"
			onchange={(id) => (activeTab = id as TabId)}
		/>

		<div class="split">
			<aside class="rail">
				<InstanceList
					title={activeTab === 'exit' ? 'Инстансы выхода' : 'Инстансы раздачи'}
					rows={activeTab === 'exit' ? exits : shares}
					selectedKey={activeTab === 'exit' ? selectedExitKey : selectedShareKey}
					addLabel={activeTab === 'exit' ? 'Вывести трафик' : 'Настроить раздачу'}
					emptyText={activeTab === 'exit'
						? 'Выведите трафик роутера наружу'
						: 'Раздайте выход другим устройствам'}
					{busyKeys}
					onselect={(r) => {
						if (activeTab === 'exit') {
							selectedExitKey = r.key;
							exitWizard = null;
							exitWizardClosed = false;
						} else {
							selectedShareKey = r.key;
							shareWizard = false;
						}
					}}
					onadd={() => {
						if (activeTab === 'exit') exitWizard = 'new';
						else shareWizard = true;
					}}
					ontoggle={toggleInstance}
					onrename={renameInstance}
					ondelete={(r) => (deleteTarget = r)}
				/>
			</aside>

			<main class="detail">
				{#if activeTab === 'exit' && exitWizardOpen}
					<!-- Мастер живёт от источника до запуска: {#key} сбрасывает его
					     состояние при смене инстанса и при заходе за новым. -->
					{#key exitWizard === 'new' ? 'new' : selectedExitKey}
						<ExitWizard
							{policies}
							{usedListens}
							row={exitWizard === 'new' ? null : selectedExit}
							wdttClient={exitWizard === 'new' ? undefined : exitWdttClient}
							ftClient={exitWizard === 'new' ? undefined : exitFtClient}
							onclose={() => {
								exitWizard = null;
								exitWizardClosed = true;
							}}
							ondone={exitWizardDone}
						/>
					{/key}
				{:else if wizardOpen}
					<!-- Мастер «Раздачи» — задача 7; здесь только оболочка с выходом к списку. -->
					<Card>
						{#snippet header()}
							<div class="detail-head">
								<h2 class="detail-title">Настроить раздачу</h2>
							</div>
						{/snippet}
						<button type="button" class="wizard-back" onclick={() => (shareWizard = false)}
							>← К списку</button
						>
					</Card>
				{:else if activeTab === 'exit' && selectedExit}
					<!-- Локальное состояние детали не переживает смену инстанса (W-13). -->
					{#key selectedExit.key}
						<ExitDetail
							row={selectedExit}
							status={exitStatus}
							wdttClient={exitWdttClient}
							ftClient={exitFtClient}
							routerClock={wdttStatus?.routerClock ?? ftStatus?.routerClock}
							{policies}
							{tunnels}
							saving={savingExit}
							busy={busyKeys.includes(selectedExit.key)}
							onstart={() => selectedExit && toggleInstance(selectedExit, true)}
							onstop={() => selectedExit && toggleInstance(selectedExit, false)}
							onwizard={exitNeverRan ? () => (exitWizard = 'instance') : undefined}
							onsave={saveExit}
							onreload={reloadAll}
						/>
					{/key}
				{:else if selected}
					<!-- Секции детали «Раздача» — задача 5; каркас несёт строку состояния. -->
					<Card>
						{#snippet header()}
							<div class="detail-head">
								<h2 class="detail-title">{selected.name}</h2>
							</div>
						{/snippet}
						<RunBar
							state={selected.state}
							meta={shareMeta}
							busy={busyKeys.includes(selected.key)}
							onstart={() => toggleInstance(selected, true)}
							onstop={() => toggleInstance(selected, false)}
							onwizard={() => (shareWizard = true)}
						/>
					</Card>
				{/if}
			</main>
		</div>
	{/if}
</PageContainer>

<ConfirmModal
	open={deleteTarget !== null}
	title={deleteTarget?.role === 'server'
		? `Удалить раздачу «${deleteTarget?.name}»?`
		: `Удалить инстанс «${deleteTarget?.name}»?`}
	message={deleteTarget?.role === 'server' ? '' : 'Связанные с ним AWG-туннели будут удалены.'}
	busy={deleting}
	onConfirm={deleteInstance}
	onClose={() => (deleteTarget = null)}
/>

<style>
	.load-error {
		margin: 0;
		color: var(--color-error);
		font-size: 0.875rem;
	}

	.split {
		display: grid;
		grid-template-columns: 300px minmax(0, 1fr);
		gap: 1rem;
		align-items: start;
	}

	.rail,
	.detail {
		min-width: 0;
	}

	.detail-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		flex-wrap: wrap;
	}

	.detail-title {
		margin: 0;
		font-size: 1rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.wizard-back {
		background: none;
		border: none;
		padding: 0;
		font-size: 0.8125rem;
		color: var(--color-accent);
		cursor: pointer;
	}

	/* Узкий экран: список схлопывается над деталью (ia.md §1). */
	@media (max-width: 900px) {
		.split {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
