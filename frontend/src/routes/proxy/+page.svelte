<script lang="ts">
	import { onDestroy, onMount, tick } from 'svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { PageContainer, PageHeader, LoadingSpinner } from '$lib/components/layout';
	import { Card, ConfirmModal, Tabs } from '$lib/components/ui';
	import {
		BinaryStrip,
		InstanceList,
		ProxyDetailPane,
		binaryStripItems,
		deleteProxyInstance,
		exitRows,
		normalizeExitConfigs,
		normalizeShareConfigs,
		renameProxyInstance,
		reportDeletedTunnels,
		seedGateWarning,
		shareRows,
		toggleProxyInstance,
	} from '$lib/components/proxy';
	import type { ExitProtocol, ProxyInstanceRow } from '$lib/components/proxy';
	import { createSelfReschedulingPoll } from '$lib/utils/selfReschedulingPoll';
	import { errText } from '$lib/utils/errorMessage';
	import type { ProxySeedView } from '$lib/api/proxyInstances';
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
	/**
	 * Посев прокси-подсистемы. Признака два: `seeded` — она поднялась,
	 * `certified` — посев подтверждён реестру и уборка разрешена. Слить их
	 * нельзя: при seeded без certified инстансы работают, а осиротевшие
	 * интерфейсы прошлой жизни никто не уберёт — это обязано быть видно.
	 */
	let seed = $state<ProxySeedView | null>(null);
	let tunnels = $state<TunnelListItem[]>([]);
	let selectedExitKey = $state<string | null>(null);
	let selectedShareKey = $state<string | null>(null);
	/** Мастер «Выхода», открытый явно: кнопкой списка (новый) или «Мастер». */
	let exitWizard = $state<'new' | 'instance' | null>(null);
	/** Авто-мастер ненастроенного инстанса закрыт пользователем. */
	let exitWizardClosed = $state(false);
	/** Мастер «Раздачи», открытый явно: кнопкой списка (новый) или «Мастер». */
	let shareWizard = $state<'new' | 'instance' | null>(null);
	/** Авто-мастер ненастроенного сервера закрыт пользователем. */
	let shareWizardClosed = $state(false);
	let deleteTarget = $state<ProxyInstanceRow | null>(null);
	let deleting = $state(false);
	let busyKeys = $state<string[]>([]);
	let installing = $state<'wdtt' | 'freeturn' | null>(null);

	// Строки собираются из статуса (жизнь процесса) и конфига (автоподключение, режим).
	const sources = $derived({ wdttStatus, wdttConfig, ftStatus, ftConfig });
	const exits = $derived(exitRows(sources));
	const shares = $derived(shareRows(sources));

	const selectedExit = $derived(exits.find((r) => r.key === selectedExitKey) ?? null);
	const selectedShare = $derived(shares.find((r) => r.key === selectedShareKey) ?? null);

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
	const seedWarning = $derived(seedGateWarning(seed));

	// ─── Загрузка и поллинг.

	async function loadStatuses() {
		const [w, f, s] = await Promise.all([
			api.getWdttStatus(),
			api.getFreeTurnStatus(),
			api.getProxySeed(),
		]);
		wdttStatus = w;
		ftStatus = f;
		seed = s;
	}

	// Конфиги страницы — это состояние сервера: их правит только загрузка и
	// ответ на сохранение. Правки пользователя живут в копии внутри детали.
	async function loadConfigs() {
		const [w, f] = await Promise.all([api.getWdttConfig(), api.getFreeTurnConfig()]);
		normalizeExitConfigs(w, f);
		normalizeShareConfigs(w, f);
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

	/** Мастер раздачи довёл сервер до запуска — уводим в его деталь, к абонентам. */
	async function shareWizardDone(protocol: ExitProtocol, id: string) {
		shareWizard = null;
		shareWizardClosed = false;
		await reloadAll();
		selectedShareKey = `${protocol}:server:${id}`;
		await tick();
		document.getElementById('share-clients')?.scrollIntoView({ block: 'start' });
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
		{#if seedWarning}
			<Card><p class="seed-warning">{seedWarning}</p></Card>
		{/if}

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
							shareWizard = null;
							shareWizardClosed = false;
						}
					}}
					onadd={() => {
						if (activeTab === 'exit') exitWizard = 'new';
						else shareWizard = 'new';
					}}
					ontoggle={toggleInstance}
					onrename={renameInstance}
					ondelete={(r) => (deleteTarget = r)}
				/>
			</aside>

			<main class="detail">
				<ProxyDetailPane
					{activeTab}
					exitRow={selectedExit}
					exitKey={selectedExitKey}
					shareRow={selectedShare}
					shareKey={selectedShareKey}
					{wdttConfig}
					{ftConfig}
					{wdttStatus}
					{ftStatus}
					{policies}
					{tunnels}
					{busyKeys}
					bind:exitWizard
					bind:exitWizardClosed
					bind:shareWizard
					bind:shareWizardClosed
					ontoggle={toggleInstance}
					onstatuses={loadStatuses}
					onconfigs={loadConfigs}
					onreload={reloadAll}
					onexitdone={exitWizardDone}
					onsharedone={shareWizardDone}
				/>
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

	.seed-warning {
		margin: 0;
		color: var(--color-warning);
		font-size: 0.8125rem;
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

	/* Узкий экран: список схлопывается над деталью (ia.md §1). */
	@media (max-width: 900px) {
		.split {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
