<script lang="ts">
	import { onDestroy, onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { PageContainer, PageHeader, LoadingSpinner } from '$lib/components/layout';
	import { Card, ConfirmModal, Tabs } from '$lib/components/ui';
	import { BinaryStrip, InstanceList, RunBar, exitRows, shareRows } from '$lib/components/proxy';
	import type { ProxyInstanceRow } from '$lib/components/proxy';
	import { createSelfReschedulingPoll } from '$lib/utils/selfReschedulingPoll';
	import { formatUptime } from '$lib/components/freeturn/uptime';
	import { errText } from '$lib/utils/errorMessage';
	import type { FreeTurnConfig, FreeTurnStatus, WdttConfig, WdttStatus } from '$lib/types';

	type TabId = 'exit' | 'share';

	let activeTab = $state<TabId>('exit');
	let loading = $state(true);
	let loadError = $state('');

	let wdttConfig = $state<WdttConfig | null>(null);
	let ftConfig = $state<FreeTurnConfig | null>(null);
	let wdttStatus = $state<WdttStatus | null>(null);
	let ftStatus = $state<FreeTurnStatus | null>(null);

	let selectedExitKey = $state<string | null>(null);
	let selectedShareKey = $state<string | null>(null);
	let exitWizard = $state(false);
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
	const selectedShare = $derived(shares.find((r) => r.key === selectedShareKey) ?? null);
	const selected = $derived(activeTab === 'exit' ? selectedExit : selectedShare);
	const wizardOpen = $derived(activeTab === 'exit' ? exitWizard : shareWizard);

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

	const binaries = $derived([
		{
			name: 'wdtt',
			binaryPresent: wdttStatus?.client.binaryPresent === true,
			installAvailable: wdttStatus?.installAvailable === true,
			installing: installing === 'wdtt' || wdttStatus?.installing === true,
			updateAvailable: wdttStatus?.updateAvailable === true,
			installedVersion: wdttStatus?.installedVersion,
			installVersion: wdttStatus?.installVersion,
			oninstall: () => install('wdtt'),
		},
		{
			name: 'freeturn',
			binaryPresent: ftStatus?.client.binaryPresent === true,
			installAvailable: ftStatus?.installAvailable === true,
			installing: installing === 'freeturn' || ftStatus?.installing === true,
			updateAvailable: ftStatus?.updateAvailable === true,
			installedVersion: ftStatus?.installedVersion,
			installVersion: ftStatus?.installVersion,
			oninstall: () => install('freeturn'),
		},
	]);

	// RB-06 / RB-07: порты раздачи приезжают вместе с деталью «Раздача».
	const detailMeta = $derived.by(() => {
		const row = selected;
		if (!row) return '';
		const listen =
			row.role === 'client' && row.protocol === 'wdtt'
				? wdttConfig?.clients.find((x) => x.id === row.id)?.config.listen
				: row.role === 'client'
					? ftConfig?.clients.find((x) => x.id === row.id)?.config.listen
					: undefined;
		return [listen, formatUptime(row.startedAt), row.pid ? `PID ${row.pid}` : '']
			.filter(Boolean)
			.join(' · ');
	});

	// ─── Загрузка и поллинг.

	async function loadStatuses() {
		const [w, f] = await Promise.all([api.getWdttStatus(), api.getFreeTurnStatus()]);
		wdttStatus = w;
		ftStatus = f;
	}

	async function loadConfigs() {
		const [w, f] = await Promise.all([api.getWdttConfig(), api.getFreeTurnConfig()]);
		wdttConfig = w;
		ftConfig = f;
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
			await Promise.all([loadConfigs(), loadStatuses()]);
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
			if (row.protocol === 'wdtt' && row.role === 'client') {
				if (on) await api.startWdttClientInstance(row.id);
				else await api.stopWdttClientInstance(row.id);
			} else if (row.protocol === 'wdtt') {
				if (on) await api.startWdttServerInstance(row.id);
				else await api.stopWdttServerInstance(row.id);
			} else if (row.role === 'client') {
				if (on) await api.startFreeTurnClient(row.id);
				else await api.stopFreeTurnClient(row.id);
			} else {
				if (on) await api.startFreeTurnServer(row.id);
				else await api.stopFreeTurnServer(row.id);
			}
			await Promise.all([loadConfigs(), loadStatuses()]);
		} catch (e) {
			notifications.error(errText(e));
		} finally {
			withBusy(row.key, false);
		}
	}

	async function renameInstance(row: ProxyInstanceRow, name: string) {
		try {
			if (row.protocol === 'wdtt' && row.role === 'client') await api.renameWdttClient(row.id, name);
			else if (row.protocol === 'wdtt') await api.renameWdttServer(row.id, name);
			else if (row.role === 'client') await api.renameFreeTurnClient(row.id, name);
			else await api.renameFreeTurnServer(row.id, name);
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
			if (row.protocol === 'wdtt' && row.role === 'client') {
				const res = await api.deleteWdttClient(row.id);
				reportDeletedTunnels(res.deletedTunnels, res.tunnelErrors);
			} else if (row.protocol === 'wdtt') {
				await api.deleteWdttServer(row.id);
			} else if (row.role === 'client') {
				const res = await api.deleteFreeTurnClient(row.id);
				reportDeletedTunnels(res.deletedTunnels, res.tunnelErrors);
			} else {
				await api.deleteFreeTurnServer(row.id);
			}
			deleteTarget = null;
			await Promise.all([loadConfigs(), loadStatuses()]);
		} catch (e) {
			notifications.error(errText(e));
		} finally {
			deleting = false;
		}
	}

	function reportDeletedTunnels(deleted?: string[], errors?: string[]) {
		if (deleted?.length) {
			notifications.success(`AWG-туннелей удалено: ${deleted.length} — перезапустите клиент`);
		}
		if (errors?.length) {
			notifications.error(`Не удалось удалить туннели: ${tunnelErrorNames(errors).join(', ')}`);
		}
	}

	// TS-03 просит имена туннелей, а бэкенд отдаёт строку «Имя (id): ошибка»
	// (`internal/api/wdtt_linked.go:95`) — отрезаем хвост с id и текстом ошибки.
	// Строка без этого хвоста (сбой чтения хранилища) остаётся как есть.
	function tunnelErrorNames(errors: string[]): string[] {
		return errors.map((e) => e.replace(/ \([^)]*\): [\s\S]*$/, '').trim()).filter(Boolean);
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
				{#if activeTab === 'exit'}
					<InstanceList
						title="Инстансы выхода"
						rows={exits}
						selectedKey={selectedExitKey}
						addLabel="Вывести трафик"
						emptyText="Выведите трафик роутера наружу"
						{busyKeys}
						onselect={(r) => {
							selectedExitKey = r.key;
							exitWizard = false;
						}}
						onadd={() => (exitWizard = true)}
						ontoggle={toggleInstance}
						onrename={renameInstance}
						ondelete={(r) => (deleteTarget = r)}
					/>
				{:else}
					<InstanceList
						title="Инстансы раздачи"
						rows={shares}
						selectedKey={selectedShareKey}
						addLabel="Настроить раздачу"
						emptyText="Раздайте выход другим устройствам"
						{busyKeys}
						onselect={(r) => {
							selectedShareKey = r.key;
							shareWizard = false;
						}}
						onadd={() => (shareWizard = true)}
						ontoggle={toggleInstance}
						onrename={renameInstance}
						ondelete={(r) => (deleteTarget = r)}
					/>
				{/if}
			</aside>

			<main class="detail">
				{#if wizardOpen}
					<!-- Мастера — задачи 4 и 7; здесь только оболочка с выходом к списку. -->
					<Card>
						{#snippet header()}
							<div class="detail-head">
								<h2 class="detail-title">
									{activeTab === 'exit' ? 'Вывести трафик' : 'Настроить раздачу'}
								</h2>
							</div>
						{/snippet}
						<button
							type="button"
							class="wizard-back"
							onclick={() => {
								if (activeTab === 'exit') exitWizard = false;
								else shareWizard = false;
							}}>← К списку</button
						>
					</Card>
				{:else if selected}
					<!-- Секции детали — задачи 3 и 5; каркас несёт только строку состояния. -->
					<Card>
						{#snippet header()}
							<div class="detail-head">
								<h2 class="detail-title">{selected.name}</h2>
							</div>
						{/snippet}
						<RunBar
							state={selected.state}
							meta={detailMeta}
							busy={busyKeys.includes(selected.key)}
							onstart={() => toggleInstance(selected, true)}
							onstop={() => toggleInstance(selected, false)}
							onwizard={() => {
								if (activeTab === 'exit') exitWizard = true;
								else shareWizard = true;
							}}
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
