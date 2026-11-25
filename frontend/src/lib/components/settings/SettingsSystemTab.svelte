<!-- Вкладка «Система»: сведения о роутере и резервное копирование. -->
<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { LoadingSpinner } from '$lib/components/layout';
	import { notifications } from '$lib/stores/notifications';
	import { systemInfo } from '$lib/stores/system';
	import type { SystemInfo } from '$lib/types';
	import { startVisiblePoll } from '$lib/utils/visiblePoll';
	import { History } from 'lucide-svelte';
	import BackupRestoreCard from './BackupRestoreCard.svelte';
	import SettingsSectionLabel from './SettingsSectionLabel.svelte';
	import SnapshotsSection from './SnapshotsSection.svelte';
	import SystemInfoGrid from './SystemInfoGrid.svelte';
	import type { SettingsEditor } from './settingsEditor.svelte';

	interface Props {
		editor: SettingsEditor;
	}

	let { editor }: Props = $props();

	const level = $derived(editor.settings.usageLevel);

	let fetched = $state.raw<SystemInfo | null>(null);
	// До первого ответа — то, что уже лежит в общем сторе.
	const info = $derived(fetched ?? $systemInfo.data);
	let refreshing = $state(false);
	let updatedAt = $state<string | null>(null);

	async function fetchSystemInfo() {
		refreshing = true;
		try {
			fetched = await api.getSystemInfo();
			updatedAt = new Date().toISOString();
		} catch (e) {
			// Есть прошлый снимок — остаёмся на нём, следующий шаг опроса повторит.
			if (!info) notifications.error(e instanceof Error ? e.message : m.settings_page_system_info_failed());
		} finally {
			refreshing = false;
		}
	}

	// Свой таймер мимо стора sysInfo. Данные тут почти статичные: версии,
	// возможности прошивки, состояние kernel-модуля — это страховка, а не
	// источник, и 30 с ей ни к чему. Фоновая вкладка не спрашивает вовсе.
	onMount(() => startVisiblePoll(fetchSystemInfo, 120_000));
</script>

<!-- Снимки: на «Базовом» добирают короткую левую колонку (сведений о роутере
     вдвое меньше), на «Расширенном» — под обеими, на «Продвинутом» — в карточке
     резервного копирования напротив полных сведений. -->
{#snippet snapshotsCard()}
	<div class="settings-block">
		<div class="card">
			<SettingsSectionLabel label={m.settings_backup_snapshots_label()} icon={History} tone="teal" header />
			<SnapshotsSection heading={false} />
		</div>
	</div>
{/snippet}

<div class="settings-columns">
	<div class="settings-column">
		{#if info}
			<SystemInfoGrid
				systemInfo={info}
				usageLevel={level}
				{refreshing}
				lastUpdated={updatedAt}
			/>
		{:else}
			<LoadingSpinner size="md" />
		{/if}
		{#if level === 'basic'}
			{@render snapshotsCard()}
		{/if}
	</div>
	<div class="settings-column">
		<div class="settings-block" id="settings-backup">
			<BackupRestoreCard>
				{#if level === 'expert'}
					<SnapshotsSection />
				{/if}
			</BackupRestoreCard>
		</div>
	</div>
	{#if level === 'advanced'}
		<div class="settings-wide">
			{@render snapshotsCard()}
		</div>
	{/if}
</div>
