<script lang="ts">
	import { onMount } from 'svelte';
	import { api, type SystemOpkgPackage } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Button, Card, ConfirmModal, SegmentedControl } from '$lib/components/ui';
	import { errorMessage } from '$lib/utils/errorMessage';
	import { RefreshCw, Download, ArrowUpCircle, Trash2 } from 'lucide-svelte';

	type PkgTab = 'available' | 'installed' | 'updates';

	let tab = $state<PkgTab>('available');
	let installed = $state<SystemOpkgPackage[]>([]);
	let upgradable = $state<SystemOpkgPackage[]>([]);
	let available = $state<SystemOpkgPackage[]>([]);
	let availableTotal = $state(0);
	let availableOffset = $state(0);
	const pageSize = 50;
	let searchQuery = $state('');
	let installedQuery = $state('');
	let loading = $state(false);
	let busy = $state(false);
	let confirmUpgradeAll = $state(false);
	let removeTarget = $state<SystemOpkgPackage | null>(null);
	let output = $state('');

	const tabOptions = [
		{ value: 'available' as PkgTab, label: 'Доступные' },
		{ value: 'installed' as PkgTab, label: 'Установленные' },
		{ value: 'updates' as PkgTab, label: 'Обновления' },
	];

	const filteredInstalled = $derived.by(() => {
		const q = installedQuery.trim().toLowerCase();
		if (!q) return installed;
		return installed.filter(
			(p) => p.name.toLowerCase().includes(q) || (p.description ?? '').toLowerCase().includes(q),
		);
	});

	onMount(async () => {
		await loadAvailable(true);
	});

	async function loadInstalled() {
		loading = true;
		try {
			installed = await api.systemOpkgInstalled();
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось загрузить установленные пакеты'));
		} finally {
			loading = false;
		}
	}

	async function loadUpgradable() {
		loading = true;
		try {
			upgradable = await api.systemOpkgUpgradable();
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось загрузить обновления'));
		} finally {
			loading = false;
		}
	}

	async function loadAvailable(reset = false) {
		if (reset) availableOffset = 0;
		loading = true;
		try {
			const res = await api.systemOpkgAvailable({
				q: searchQuery.trim(),
				offset: availableOffset,
				limit: pageSize,
			});
			available = res.items;
			availableTotal = res.total;
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось загрузить каталог пакетов'));
		} finally {
			loading = false;
		}
	}

	async function onTabChange(next: PkgTab) {
		tab = next;
		if (next === 'installed') await loadInstalled();
		else if (next === 'updates') await loadUpgradable();
		else await loadAvailable(true);
	}

	async function runUpdate() {
		busy = true;
		output = '';
		try {
			const res = await api.systemOpkgUpdate();
			output = res.output;
			notifications.success('Списки пакетов обновлены');
			if (tab === 'available') await loadAvailable(true);
			if (tab === 'installed') await loadInstalled();
			if (tab === 'updates') await loadUpgradable();
		} catch (e) {
			notifications.error(errorMessage(e, 'opkg update не удался'));
		} finally {
			busy = false;
		}
	}

	async function upgradeAll() {
		busy = true;
		output = '';
		try {
			const res = await api.systemOpkgUpgrade();
			output = res.output;
			notifications.success('Обновление завершено');
			await loadUpgradable();
			await loadInstalled();
		} catch (e) {
			notifications.error(errorMessage(e, 'opkg upgrade не удался'));
		} finally {
			busy = false;
			confirmUpgradeAll = false;
		}
	}

	async function upgradeOne(pkg: SystemOpkgPackage) {
		busy = true;
		try {
			const res = await api.systemOpkgUpgrade([pkg.name]);
			output = res.output;
			notifications.success(`${pkg.name} обновлён`);
			await loadUpgradable();
			await loadInstalled();
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось обновить пакет'));
		} finally {
			busy = false;
		}
	}

	async function installOne(pkg: SystemOpkgPackage) {
		busy = true;
		try {
			const res = await api.systemOpkgInstall([pkg.name]);
			output = res.output;
			notifications.success(`${pkg.name} установлен`);
			await loadInstalled();
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось установить пакет'));
		} finally {
			busy = false;
		}
	}

	async function removeOne() {
		if (!removeTarget) return;
		busy = true;
		try {
			const res = await api.systemOpkgRemove([removeTarget.name]);
			output = res.output;
			notifications.success(`${removeTarget.name} удалён`);
			removeTarget = null;
			await loadInstalled();
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось удалить пакет'));
		} finally {
			busy = false;
		}
	}

	function nextPage() {
		if (availableOffset + pageSize >= availableTotal) return;
		availableOffset += pageSize;
		void loadAvailable(false);
	}

	function prevPage() {
		availableOffset = Math.max(0, availableOffset - pageSize);
		void loadAvailable(false);
	}
</script>

<div class="packages">
	<div class="head-row">
		<SegmentedControl value={tab} options={tabOptions} ariaLabel="Раздел пакетов opkg" onchange={onTabChange} />
		<div class="toolbar">
			<Button variant="secondary" loading={busy} onclick={runUpdate}>
				{#snippet iconBefore()}<RefreshCw size={14} />{/snippet}
				opkg update
			</Button>
			{#if tab === 'updates'}
				<Button variant="primary" disabled={upgradable.length === 0} onclick={() => (confirmUpgradeAll = true)}>
					{#snippet iconBefore()}<ArrowUpCircle size={14} />{/snippet}
					Обновить всё ({upgradable.length})
				</Button>
			{/if}
		</div>
	</div>

	{#if tab === 'available'}
		<Card padding="sm">
			<div class="section-head">
				<h3>Доступные пакеты ({availableTotal})</h3>
				<div class="search-row">
					<input bind:value={searchQuery} placeholder="Поиск по названию…" onkeydown={(e) => e.key === 'Enter' && loadAvailable(true)} />
					<Button variant="secondary" loading={loading} onclick={() => loadAvailable(true)}>Найти</Button>
				</div>
			</div>
			{#if loading && available.length === 0}
				<p class="muted">Загрузка каталога…</p>
			{:else}
				<div class="table-wrap">
					<table class="pkg">
						<thead>
							<tr>
								<th>Пакет</th>
								<th>Версия</th>
								<th>Описание</th>
								<th></th>
							</tr>
						</thead>
						<tbody>
							{#each available as pkg (pkg.name + pkg.version)}
								<tr>
									<td>{pkg.name}</td>
									<td><code>{pkg.version}</code></td>
									<td class="desc">{pkg.description || '—'}</td>
									<td class="act">
										<Button size="sm" variant="primary" disabled={busy} onclick={() => installOne(pkg)}>
											{#snippet iconBefore()}<Download size={14} />{/snippet}
											Установить
										</Button>
									</td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
				<div class="pager">
					<Button variant="ghost" disabled={availableOffset <= 0} onclick={prevPage}>Назад</Button>
					<span>{availableOffset + 1}–{Math.min(availableOffset + pageSize, availableTotal)} из {availableTotal}</span>
					<Button variant="ghost" disabled={availableOffset + pageSize >= availableTotal} onclick={nextPage}>Вперёд</Button>
				</div>
			{/if}
		</Card>
	{:else if tab === 'installed'}
		<Card padding="sm">
			<div class="section-head">
				<h3>Установленные ({installed.length})</h3>
				<input bind:value={installedQuery} placeholder="Поиск по названию…" />
			</div>
			<div class="table-wrap">
				<table class="pkg">
					<thead>
						<tr>
							<th>Пакет</th>
							<th>Версия</th>
							<th>Установлен</th>
							<th></th>
						</tr>
					</thead>
					<tbody>
						{#each filteredInstalled as pkg (pkg.name)}
							<tr>
								<td>{pkg.name}</td>
								<td><code>{pkg.version}</code></td>
								<td>{pkg.installedAt || '—'}</td>
								<td class="act">
									<Button size="sm" variant="outline-danger" disabled={busy} onclick={() => (removeTarget = pkg)}>
										{#snippet iconBefore()}<Trash2 size={14} />{/snippet}
										Удалить
									</Button>
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		</Card>
	{:else}
		<Card padding="sm">
			<h3>Доступны обновления ({upgradable.length})</h3>
			{#if loading}
				<p class="muted">Загрузка…</p>
			{:else if upgradable.length === 0}
				<p class="muted">Все установленные пакеты актуальны.</p>
			{:else}
				<table class="pkg">
					<thead>
						<tr>
							<th>Пакет</th>
							<th>Текущая</th>
							<th>Новая</th>
							<th></th>
						</tr>
					</thead>
					<tbody>
						{#each upgradable as pkg (pkg.name)}
							<tr>
								<td>{pkg.name}</td>
								<td><code>{pkg.version}</code></td>
								<td><code>{pkg.upgradeVersion}</code></td>
								<td class="act">
									<Button size="sm" variant="secondary" disabled={busy} onclick={() => upgradeOne(pkg)}>Обновить</Button>
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			{/if}
		</Card>
	{/if}

	{#if output}
		<Card padding="sm">
			<h3>Вывод opkg</h3>
			<pre class="output">{output}</pre>
		</Card>
	{/if}
</div>

{#if confirmUpgradeAll}
	<ConfirmModal
		open={confirmUpgradeAll}
		title="Обновить все пакеты?"
		message={`Будет выполнено opkg upgrade для ${upgradable.length} пакет(ов).`}
		confirmLabel="Обновить"
		variant="primary"
		busy={busy}
		onClose={() => (confirmUpgradeAll = false)}
		onConfirm={upgradeAll}
	/>
{/if}

{#if removeTarget}
	<ConfirmModal
		open={!!removeTarget}
		title="Удалить пакет?"
		message={`${removeTarget.name} (${removeTarget.version})`}
		confirmLabel="Удалить"
		variant="danger"
		busy={busy}
		onClose={() => (removeTarget = null)}
		onConfirm={removeOne}
	/>
{/if}

<style>
	.packages { display: flex; flex-direction: column; gap: 0.75rem; }
	.head-row { display: flex; flex-wrap: wrap; gap: 0.75rem; justify-content: space-between; align-items: center; }
	.toolbar { display: flex; flex-wrap: wrap; gap: 0.35rem; }
	.section-head { display: flex; flex-wrap: wrap; gap: 0.5rem; justify-content: space-between; align-items: center; margin-bottom: 0.5rem; }
	h3 { margin: 0; font-size: 0.95rem; }
	.search-row { display: flex; gap: 0.35rem; flex: 1; min-width: 220px; }
	.search-row input, .section-head input { flex: 1; min-width: 180px; padding: 0.4rem 0.5rem; }
	.pkg { width: 100%; border-collapse: collapse; font-size: 0.88rem; table-layout: fixed; }
	.pkg th, .pkg td { text-align: left; padding: 0.45rem 0.5rem; border-bottom: 1px solid var(--border-subtle, #333); vertical-align: top; }
	.pkg th:nth-child(1) { width: 16%; }
	.pkg th:nth-child(2) { width: 12%; }
	.pkg th:nth-child(3) { width: 52%; }
	.pkg th:nth-child(4) { width: 20%; }
	.desc { font-size: 0.82rem; opacity: 0.85; word-break: break-word; }
	.act { text-align: right; white-space: nowrap; }
	.table-wrap { max-height: 420px; overflow: auto; }
	.pager { display: flex; gap: 0.75rem; align-items: center; justify-content: flex-end; margin-top: 0.5rem; font-size: 0.85rem; }
	.output { margin: 0; white-space: pre-wrap; font-size: 0.8rem; max-height: 200px; overflow: auto; }
	.muted { opacity: 0.7; }
</style>
