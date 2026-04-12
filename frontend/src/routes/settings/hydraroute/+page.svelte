<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { PageContainer, LoadingSpinner } from '$lib/components/layout';
	import { Toggle } from '$lib/components/ui';
	import { GeoTagBrowserModal } from '$lib/components/hydraroute';
	import type { HydraRouteStatus, HydraRouteConfig, GeoFileEntry, GeoTag, IpsetUsage } from '$lib/types';

	// ── State ──────────────────────────────────────────
	let status = $state<HydraRouteStatus | null>(null);
	let config = $state<HydraRouteConfig | null>(null);
	let geoFiles = $state<GeoFileEntry[]>([]);
	let loading = $state(true);
	let saving = $state(false);
	let hydraLoading = $state(false);

	// Tag browser modal
	let geoTags = $state<GeoTag[]>([]);
	let ipsetUsage = $state<IpsetUsage | null>(null);
	let tagModalOpen = $state(false);
	let tagModalFile = $state<GeoFileEntry | null>(null);

	// Add file form
	let addType = $state<'geosite' | 'geoip'>('geosite');
	let addUrl = $state('');
	let downloading = $state(false);

	// ── Init ───────────────────────────────────────────
	onMount(async () => {
		try {
			const [s, c, f] = await Promise.all([
				api.getHydraRouteStatus(),
				api.getHydraRouteConfig(),
				api.getGeoFiles(),
			]);
			status = s;
			config = c;
			geoFiles = f;
		} catch (e) {
			notifications.error('Не удалось загрузить данные HydraRoute');
		} finally {
			loading = false;
		}
	});

	// ── HydraRoute daemon control ──────────────────────
	async function controlHydraRoute(action: 'start' | 'stop' | 'restart') {
		hydraLoading = true;
		try {
			status = await api.controlHydraRoute(action);
			const msgs = { start: 'HydraRoute запущен', stop: 'HydraRoute остановлен', restart: 'HydraRoute перезапущен' };
			notifications.success(msgs[action]);
		} catch (e) {
			notifications.error('Ошибка управления HydraRoute');
		} finally {
			hydraLoading = false;
		}
	}

	// ── Config save ────────────────────────────────────
	async function saveConfig() {
		if (!config) return;
		saving = true;
		try {
			config = await api.updateHydraRouteConfig(config);
			notifications.success('Настройки сохранены');
		} catch (e) {
			notifications.error('Ошибка сохранения настроек');
		} finally {
			saving = false;
		}
	}

	// ── Geo file actions ───────────────────────────────
	async function addGeoFile() {
		if (!addUrl.trim()) return;
		downloading = true;
		try {
			const entry = await api.addGeoFile(addType, addUrl.trim());
			geoFiles = [...geoFiles, entry];
			addUrl = '';
			notifications.success('Файл добавлен');
		} catch (e) {
			notifications.error('Ошибка добавления файла');
		} finally {
			downloading = false;
		}
	}

	async function deleteGeoFile(file: GeoFileEntry) {
		try {
			await api.deleteGeoFile(file.path);
			geoFiles = geoFiles.filter((f) => f.path !== file.path);
			notifications.success('Файл удалён');
		} catch (e) {
			notifications.error('Ошибка удаления файла');
		}
	}

	async function updateGeoFile(file: GeoFileEntry) {
		try {
			await api.updateGeoFile(file.path);
			// Refresh file list
			geoFiles = await api.getGeoFiles();
			notifications.success('Файл обновлён');
		} catch (e) {
			notifications.error('Ошибка обновления файла');
		}
	}

	async function updateAllGeoFiles() {
		try {
			await api.updateGeoFile();
			geoFiles = await api.getGeoFiles();
			notifications.success('Все файлы обновлены');
		} catch (e) {
			notifications.error('Ошибка обновления файлов');
		}
	}

	async function openTagBrowser(file: GeoFileEntry) {
		tagModalFile = file;
		geoTags = [];
		tagModalOpen = true;
		try {
			const [tags, usage] = await Promise.all([
				api.getGeoTags(file.path),
				file.type === 'geoip' ? api.getIpsetUsage() : Promise.resolve(null),
			]);
			geoTags = tags;
			ipsetUsage = usage;
		} catch (e) {
			notifications.error('Не удалось загрузить теги');
		}
	}

	// ── Helpers ────────────────────────────────────────
	function formatSize(bytes: number): string {
		if (bytes < 1024) return `${bytes} Б`;
		if (bytes < 1048576) return `${(bytes / 1024).toFixed(1)} КБ`;
		return `${(bytes / 1048576).toFixed(1)} МБ`;
	}

	const ipsetUsageForFile = $derived(
		tagModalFile && ipsetUsage
			? (ipsetUsage.usage[tagModalFile.path] ?? 0)
			: 0
	);

	const ipsetMaxElem = $derived(ipsetUsage?.maxElem ?? 65536);
</script>

<svelte:head>
	<title>HydraRoute Neo — AWG Manager</title>
</svelte:head>

<PageContainer>
	<div class="breadcrumb">
		<a href="/settings">Настройки</a>
		<span class="breadcrumb-sep">/</span>
		<span>HydraRoute Neo</span>
	</div>

	{#if loading}
		<div class="flex justify-center py-8">
			<LoadingSpinner size="md" />
		</div>
	{:else}
		<div class="settings-stack">

			<!-- ── Статус и управление ─────────────────── -->
			<div>
				<div class="section-label">Статус и управление</div>
				<div class="card">
					<div class="setting-row">
						<div class="flex flex-col gap-1">
							<span class="font-medium">Демон HydraRoute</span>
							<span class="setting-description">
								{#if !status || !status.installed}
									<span style="color: var(--text-muted)">Не обнаружен</span>
								{:else if status.running}
									<span style="color: var(--success)">Работает</span>
									{#if status.version}
										<span style="color: var(--text-muted)"> · v{status.version}</span>
									{/if}
								{:else}
									<span style="color: var(--text-muted)">Остановлен</span>
								{/if}
							</span>
						</div>
						{#if status?.installed}
							<div style="display: flex; gap: 0.5rem;">
								{#if !status.running}
									<button class="btn btn-ghost btn-sm" onclick={() => controlHydraRoute('start')} disabled={hydraLoading}>Запустить</button>
								{:else}
									<button class="btn btn-ghost btn-sm" onclick={() => controlHydraRoute('stop')} disabled={hydraLoading}>Остановить</button>
								{/if}
								<button class="btn btn-ghost btn-sm" onclick={() => controlHydraRoute('restart')} disabled={hydraLoading}>Перезапустить</button>
							</div>
						{/if}
					</div>
				</div>
			</div>

			<!-- ── Базы данных GeoIP/GeoSite ──────────── -->
			<div>
				<div class="section-label">Базы данных GeoIP / GeoSite</div>
				<div class="card">
					{#if geoFiles.length > 0}
						<div class="geo-table-wrap">
							<table class="geo-table">
								<thead>
									<tr>
										<th>Тип</th>
										<th>Файл</th>
										<th>Источник</th>
										<th>Теги</th>
										<th>Действия</th>
									</tr>
								</thead>
								<tbody>
									{#each geoFiles as file (file.path)}
										<tr>
											<td>
												<span class="badge {file.type === 'geosite' ? 'badge-geosite' : 'badge-geoip'}">
													{file.type === 'geosite' ? 'GeoSite' : 'GeoIP'}
												</span>
											</td>
											<td>
												<div class="file-info">
													<span class="file-name" title={file.path}>{file.path.split('/').pop()}</span>
													<span class="file-size">{formatSize(file.size)}</span>
												</div>
											</td>
											<td>
												<span class="file-url" title={file.url}>{file.url}</span>
											</td>
											<td>
												<button
													class="btn btn-ghost btn-sm"
													onclick={() => openTagBrowser(file)}
												>
													{file.tagCount} тегов
												</button>
											</td>
											<td>
												<div style="display: flex; gap: 0.25rem;">
													<button class="btn btn-ghost btn-sm" onclick={() => updateGeoFile(file)} title="Обновить">Обновить</button>
													<button class="btn btn-icon" onclick={() => deleteGeoFile(file)} title="Удалить">×</button>
												</div>
											</td>
										</tr>
									{/each}
								</tbody>
							</table>
						</div>
					{/if}

					<!-- Add file row -->
					<div class="add-file-row">
						<select class="type-select" bind:value={addType}>
							<option value="geosite">GeoSite</option>
							<option value="geoip">GeoIP</option>
						</select>
						<input
							class="url-input"
							type="text"
							placeholder="URL файла .dat (формат v2ray)"
							bind:value={addUrl}
							onkeydown={(e) => { if (e.key === 'Enter') addGeoFile(); }}
						/>
						<button class="btn btn-ghost btn-sm" onclick={addGeoFile} disabled={downloading || !addUrl.trim()}>
							{downloading ? 'Загрузка...' : 'Скачать'}
						</button>
					</div>

					<div class="geo-hints">
						<span>Поддерживаются файлы .dat в формате v2ray (geoip.dat, geosite.dat и аналоги). Лимит: 16 файлов.</span>
					</div>

					{#if geoFiles.length > 0}
						<div class="geo-actions">
							<button class="btn btn-ghost btn-sm" onclick={updateAllGeoFiles}>Обновить все</button>
						</div>
					{/if}
				</div>
			</div>

			<!-- ── Параметры ──────────────────────────── -->
			{#if config}
				<div>
					<div class="section-label">Параметры</div>
					<div class="card">
						<div class="setting-row">
							<div class="flex flex-col gap-1">
								<span class="font-medium">Автозапуск</span>
								<span class="setting-description">Запускать HydraRoute при старте роутера</span>
							</div>
							<Toggle
								checked={config.autoStart}
								onchange={(v) => { config!.autoStart = v; saveConfig(); }}
								disabled={saving}
							/>
						</div>

						<div class="setting-row">
							<div class="flex flex-col gap-1">
								<span class="font-medium">Время жизни IP</span>
								<span class="setting-description">Через сколько часов удалять обнаруженные IP</span>
							</div>
							<input
								class="param-input"
								type="number"
								min="0"
								value={Math.round(config.ipsetTimeout / 3600)}
								onchange={(e) => {
									config!.ipsetTimeout = Number((e.target as HTMLInputElement).value) * 3600;
									saveConfig();
								}}
							/>
						</div>

						<div class="setting-row">
							<div class="flex flex-col gap-1">
								<span class="font-medium">Макс. размер таблицы IP</span>
								<span class="setting-description">Лимит записей. По умолчанию 65536 — максимум для ipset</span>
							</div>
							<input
								class="param-input"
								type="number"
								min="1"
								bind:value={config.ipsetMaxElem}
								onchange={() => saveConfig()}
							/>
						</div>

						<div class="setting-row">
							<div class="flex flex-col gap-1">
								<span class="font-medium">Очищать IP при запуске</span>
								<span class="setting-description">Удалять все IP при каждом перезапуске</span>
							</div>
							<Toggle
								checked={config.clearIPSet}
								onchange={(v) => { config!.clearIPSet = v; saveConfig(); }}
								disabled={saving}
							/>
						</div>

						<div class="setting-row">
							<div class="flex flex-col gap-1">
								<span class="font-medium">Сброс соединений</span>
								<span class="setting-description">Сбросить соединения при обнаружении нового IP</span>
							</div>
							<Toggle
								checked={config.conntrackFlush}
								onchange={(v) => { config!.conntrackFlush = v; saveConfig(); }}
								disabled={saving}
							/>
						</div>

						<div class="setting-row">
							<div class="flex flex-col gap-1">
								<span class="font-medium">Глобальная маршрутизация</span>
								<span class="setting-description">Маршрутизировать трафик всех устройств</span>
							</div>
							<Toggle
								checked={config.globalRouting}
								onchange={(v) => { config!.globalRouting = v; saveConfig(); }}
								disabled={saving}
							/>
						</div>

						<div class="setting-row">
							<div class="flex flex-col gap-1">
								<span class="font-medium">Журнал</span>
								<span class="setting-description">Режим записи событий</span>
							</div>
							<select
								class="param-select"
								bind:value={config.log}
								onchange={() => saveConfig()}
							>
								<option value="off">Отключён</option>
								<option value="console">Консоль</option>
								<option value="file">Файл</option>
							</select>
						</div>
					</div>
				</div>
			{/if}
		</div>
	{/if}
</PageContainer>

{#if tagModalFile}
	<GeoTagBrowserModal
		open={tagModalOpen}
		title="Теги: {tagModalFile.path.split('/').pop() ?? tagModalFile.path}"
		tags={geoTags}
		fileType={tagModalFile.type}
		ipsetMaxElem={ipsetMaxElem}
		ifaceUsage={ipsetUsageForFile}
		ifaceName={tagModalFile.path.split('/').pop() ?? tagModalFile.path}
		onclose={() => { tagModalOpen = false; tagModalFile = null; }}
	/>
{/if}

<style>
	.breadcrumb {
		display: flex;
		align-items: center;
		gap: 0.375rem;
		font-size: 0.8125rem;
		color: var(--text-muted);
		margin-bottom: 1rem;
	}

	.breadcrumb a {
		color: var(--accent);
		text-decoration: none;
	}

	.breadcrumb a:hover {
		text-decoration: underline;
	}

	.breadcrumb-sep {
		color: var(--border);
	}

	/* Geo table */
	.geo-table-wrap {
		overflow-x: auto;
		margin-bottom: 0.75rem;
	}

	.geo-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.8125rem;
	}

	.geo-table th {
		text-align: left;
		padding: 0.375rem 0.5rem;
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--text-muted);
		border-bottom: 1px solid var(--border);
	}

	.geo-table td {
		padding: 0.5rem;
		border-bottom: 1px solid var(--border);
		vertical-align: middle;
	}

	.geo-table tbody tr:last-child td {
		border-bottom: none;
	}

	.badge-geosite {
		background: rgba(59, 130, 246, 0.15);
		color: var(--accent, #3b82f6);
	}

	.badge-geoip {
		background: rgba(16, 185, 129, 0.15);
		color: var(--success, #10b981);
	}

	.file-info {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
	}

	.file-name {
		font-family: var(--font-mono, monospace);
		font-size: 0.8rem;
		color: var(--text-primary);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		max-width: 180px;
	}

	.file-size {
		font-size: 0.7rem;
		color: var(--text-muted);
	}

	.file-url {
		font-size: 0.75rem;
		color: var(--text-secondary);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		display: block;
		max-width: 220px;
	}

	/* Add file */
	.add-file-row {
		display: flex;
		gap: 0.5rem;
		align-items: center;
		margin-top: 0.5rem;
	}

	.type-select {
		flex-shrink: 0;
		padding: 0.375rem 0.5rem;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm, 4px);
		color: var(--text-primary);
		font-size: 0.875rem;
		cursor: pointer;
	}

	.url-input {
		flex: 1;
		padding: 0.375rem 0.625rem;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm, 4px);
		color: var(--text-primary);
		font-size: 0.875rem;
		outline: none;
		min-width: 0;
	}

	.url-input:focus {
		border-color: var(--accent);
	}

	.geo-hints {
		margin-top: 0.5rem;
		font-size: 0.75rem;
		color: var(--text-muted);
	}

	.geo-actions {
		display: flex;
		justify-content: flex-end;
		margin-top: 0.75rem;
		padding-top: 0.75rem;
		border-top: 1px solid var(--border);
	}

	/* Param controls */
	.param-input {
		width: 7rem;
		padding: 0.375rem 0.5rem;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm, 4px);
		color: var(--text-primary);
		font-size: 0.875rem;
		text-align: right;
		outline: none;
	}

	.param-input:focus {
		border-color: var(--accent);
	}

	.param-select {
		padding: 0.375rem 0.5rem;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm, 4px);
		color: var(--text-primary);
		font-size: 0.875rem;
		cursor: pointer;
		outline: none;
	}

	.param-select:focus {
		border-color: var(--accent);
	}

	.py-8 {
		padding-top: 2rem;
		padding-bottom: 2rem;
	}

	.justify-center {
		justify-content: center;
	}

	.font-medium {
		font-weight: 500;
	}
</style>
