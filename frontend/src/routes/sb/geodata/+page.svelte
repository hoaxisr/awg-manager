<script lang="ts">
	// Страница «Sing-box · Гео-данные» — вынесена из вкладки /routing?tab=geodata
	// (навигация v3). Содержимое перенесено как есть; счётчик гео-файлов,
	// питавший бейдж вкладки, умер вместе с ней.
	import { api } from '$lib/api/client';
	import { PageContainer, PageHeader, EmptyState } from '$lib/components/layout';
	import type { GeoFileEntry, GeoFileSettings, Settings } from '$lib/types';
	import { HrNeoGeoDataView, HrNeoGeoRefreshSettings } from '$lib/components/hrneo';
	import { Button } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import { hydrarouteStatusStore } from '$lib/stores/routing';
	import { systemInfo } from '$lib/stores/system';

	// Гео-файлы нужны обоим движкам: sing-box и HydraRoute Neo. Если не
	// установлен ни один — страница пустая по существу, показываем заглушку
	// (по образцу «Sing-box не установлен» на /sb/routing).
	let hydrarouteInstalled = $derived($hydrarouteStatusStore.data?.installed ?? false);
	let singboxInstalled = $derived($systemInfo.data?.singbox?.installed ?? false);
	let enginesKnown = $derived(
		($hydrarouteStatusStore.lastFetchedAt > 0 || $hydrarouteStatusStore.status === 'error') &&
			($systemInfo.lastFetchedAt > 0 || $systemInfo.status === 'error'),
	);
	let noEngine = $derived(enginesKnown && !hydrarouteInstalled && !singboxInstalled);

	let geoFiles = $state<GeoFileEntry[]>([]);
	let settings = $state<Settings | null>(null);
	let saving = $state(false);
	let updatingAll = $state(false);

	async function loadGeoFiles() {
		try { await api.rescanGeoFiles(); } catch { /* HR не установлен — не ломаем вкладку */ }
		try { geoFiles = (await api.getGeoFiles()) ?? []; } catch { geoFiles = []; }
	}

	async function loadSettings() {
		try { settings = await api.getSettings(); } catch { settings = null; }
	}

	$effect(() => {
		void loadGeoFiles();
		void loadSettings();
	});

	async function toggleAutoRefresh(enabled: boolean) {
		if (!settings) return;
		saving = true;
		try {
			settings = await api.updateSettings({
				...settings,
				geoFile: {
					...settings.geoFile,
					autoRefreshEnabled: enabled,
					refreshIntervalHours:
						enabled && settings.geoFile.refreshIntervalHours === 0 ? 6 : settings.geoFile.refreshIntervalHours,
					refreshMode: settings.geoFile.refreshMode || 'interval',
				},
			});
			notifications.success(enabled ? 'Автообновление гео-файлов включено' : 'Автообновление гео-файлов отключено');
		} catch {
			notifications.error('Ошибка сохранения настроек');
		} finally {
			saving = false;
		}
	}

	async function saveGeo(next: GeoFileSettings) {
		if (!settings) return;
		saving = true;
		try {
			settings = await api.updateSettings({ ...settings, geoFile: next });
			notifications.success('Настройки автообновления сохранены');
		} catch {
			notifications.error('Ошибка сохранения настроек');
		} finally {
			saving = false;
		}
	}

	async function updateAllNow() {
		updatingAll = true;
		try {
			await api.updateGeoFile('');
			await loadGeoFiles();
			notifications.success('Гео-файлы обновлены');
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка обновления');
		} finally {
			updatingAll = false;
		}
	}
</script>

<svelte:head>
	<title>Гео-данные - AWGM</title>
</svelte:head>

<PageContainer width="full">
	<PageHeader title="Гео-данные" />

	{#if noEngine}
		<EmptyState
			title="Гео-данные не используются"
			description="Гео-файлы нужны sing-box и HydraRoute Neo — ни один из них не установлен."
		/>
	{:else}
		<HrNeoGeoDataView files={geoFiles} onrefresh={loadGeoFiles} />

		{#if settings}
			<div class="geo-settings">
				<HrNeoGeoRefreshSettings value={settings.geoFile} saving={saving} onToggle={toggleAutoRefresh} onSave={saveGeo} />
			</div>
		{/if}

		<div class="geo-actions">
			<Button variant="secondary" size="sm" onclick={updateAllNow} loading={updatingAll}>
				Запустить обновление сейчас
			</Button>
		</div>
	{/if}
</PageContainer>

<style>
	.geo-settings { display: flex; flex-direction: column; gap: 0.75rem; margin-top: 1rem; }
	.geo-actions { margin-top: 0.75rem; }
</style>
