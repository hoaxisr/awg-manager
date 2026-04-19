<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/stores';
	import { tunnels } from '$lib/stores/tunnels';
	import { systemInfo as systemInfoStore } from '$lib/stores/system';
	import { notifications } from '$lib/stores/notifications';
	import { api } from '$lib/api/client';
	import { TunnelCard, ExternalTunnelCard, AdoptTunnelDialog, SystemTunnelCard } from '$lib/components/tunnels';
	import TunnelTabs from '$lib/components/tunnels/TunnelTabs.svelte';
	import { PageContainer, LoadingSpinner } from '$lib/components/layout';
	import { Modal } from '$lib/components/ui';
	import { singbox } from '$lib/stores/singbox';
	import { SingboxInstallBanner, SingboxTunnelCard, SingboxGhostTerminal } from '$lib/components/singbox';

	type TunnelTab = 'awg' | 'singbox';

	let sysInfo = $derived($systemInfoStore);
	let loading = $derived(!sysInfo);

	const goArch = $derived(sysInfo?.goArch ?? '');

	let showUnsupportedBlock = $derived(
		sysInfo !== null &&
		!sysInfo.kernelModuleExists &&
		!sysInfo.kernelModuleLoaded &&
		!sysInfo.backendAvailability?.nativewg
	);

	const externalTunnels = tunnels.externalTunnels;
	const systemTunnelsList = tunnels.systemTunnels;

	let toggleLoading = $state<Record<string, boolean>>({});
	let deleteLoading = $state<Record<string, boolean>>({});
	let deleteConfirmId = $state<string | null>(null);

	async function hideSystemTunnel(id: string) {
		try {
			await api.hideSystemTunnel(id);
			// List refresh comes via SSE tunnels:list + server:updated
			notifications.success(`Туннель ${id} скрыт. Вернуть можно в настройках.`);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка скрытия туннеля');
		}
	}

	async function markAsServer(id: string) {
		try {
			await api.markServerInterface(id);
			// List refresh comes via SSE tunnels:list + server:updated
			notifications.success(`Туннель ${id} перенесён в серверы.`);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка переноса в серверы');
		}
	}

	async function handleToggleOnOff(id: string) {
		const tunnel = $tunnels.find(t => t.id === id);
		if (!tunnel) return;
		// needs_start is NOT "on" — it means "intent up but not actually running",
		// so the toggle should show OFF and the click should fire Start, not Stop.
		const isOn = ['running', 'starting', 'broken'].includes(tunnel.status);
		toggleLoading = { ...toggleLoading, [id]: true };
		try {
			if (isOn) {
				await tunnels.stop(id);
				notifications.success('Туннель остановлен');
			} else {
				await tunnels.start(id);
				notifications.success('Туннель запущен');
			}
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка');
		} finally {
			const { [id]: _, ...rest } = toggleLoading;
			toggleLoading = rest;
		}
	}

	function requestDelete(id: string) {
		deleteConfirmId = id;
	}

	async function handleDelete(id: string) {
		deleteConfirmId = null;
		deleteLoading = { ...deleteLoading, [id]: true };
		try {
			const result = await tunnels.remove(id);
			if (result.success && result.verified) {
				notifications.success('Туннель удалён');
			} else {
				notifications.error('Не удалось верифицировать удаление');
			}
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Не удалось удалить туннель');
		} finally {
			const { [id]: _, ...rest } = deleteLoading;
			deleteLoading = rest;
		}
	}

	const singboxStatus = singbox.status;
	const singboxTunnels = singbox.tunnels;

	// Tabs
	let activeTab = $state<TunnelTab>('awg');

	onMount(() => {
		// URL query wins over sessionStorage — lets other pages
		// (e.g. /singbox/new) land the user on the right tab after an action.
		const fromQuery = $page.url.searchParams.get('tab');
		if (fromQuery === 'awg' || fromQuery === 'singbox') {
			activeTab = fromQuery;
			return;
		}
		const stored = sessionStorage.getItem('tunnelsTab');
		if (stored === 'awg' || stored === 'singbox') {
			activeTab = stored;
		}
	});

	$effect(() => {
		sessionStorage.setItem('tunnelsTab', activeTab);
	});

	$effect(() => {
		if (activeTab === 'singbox' && sysInfo && !sysInfo.singbox?.installed) {
			activeTab = 'awg';
		}
	});

	// External tunnels
	let adoptDialogOpen = $state(false);
	let adoptingInterface = $state('');
	let adoptError = $state('');
	let adoptLoading = $state(false);

	function handleAdoptClick(interfaceName: string): void {
		adoptingInterface = interfaceName;
		adoptDialogOpen = true;
	}

	async function handleAdopt(data: { content: string; name: string }): Promise<void> {
		adoptLoading = true;
		adoptError = '';
		try {
			const adopted = await tunnels.adoptExternal(adoptingInterface, data.content, data.name);
			if (adopted.warnings?.length) {
				adopted.warnings.forEach(w => notifications.warning(w));
			}
			notifications.success('Туннель успешно импортирован');
			adoptDialogOpen = false;
		} catch (e) {
			adoptError = e instanceof Error ? e.message : 'Не удалось импортировать туннель';
		} finally {
			adoptLoading = false;
		}
	}

	// Empty state: inline drag-and-drop import
	let dragOver = $state(false);
	let importing = $state(false);

	let exporting = $state(false);

	async function handleExportAll() {
		exporting = true;
		try {
			const blob = await api.exportAllTunnels();
			const { downloadBlob } = await import('$lib/utils/download');
			downloadBlob(blob, 'awg-tunnels.zip');
		} catch (e) {
			notifications.error('Не удалось экспортировать конфиги');
		} finally {
			exporting = false;
		}
	}

	function handleDrop(event: DragEvent) {
		event.preventDefault();
		dragOver = false;
		if (event.dataTransfer?.files?.[0]) {
			readAndImport(event.dataTransfer.files[0]);
		}
	}

	function handleDragOver(event: DragEvent) {
		event.preventDefault();
		dragOver = true;
	}

	function handleDragLeave() {
		dragOver = false;
	}

	let selectedBackend = $state<'nativewg' | 'kernel'>('nativewg');

	// Auto-select backend based on availability
	$effect(() => {
		if (sysInfo?.backendAvailability && !sysInfo.backendAvailability.nativewg && sysInfo.backendAvailability.kernel) {
			selectedBackend = 'kernel';
		}
	});

	let fileInput = $state<HTMLInputElement>();

	function handleFileSelect(event: Event) {
		const input = event.target as HTMLInputElement;
		if (input.files?.[0]) {
			readAndImport(input.files[0]);
		}
	}

	function readAndImport(file: File) {
		const reader = new FileReader();
		reader.onload = async (e) => {
			const content = e.target?.result as string;
			if (!content?.trim()) return;
			importing = true;
			try {
				const name = file.name.replace(/\.conf$/i, '');
				const tunnel = await tunnels.importConfig(content, name, selectedBackend);
				if (tunnel.warnings?.length) {
					tunnel.warnings.forEach(w => notifications.warning(w));
				}
				notifications.success('Туннель импортирован');
				goto(`/tunnels/${tunnel.id}`);
			} catch (err) {
				notifications.error(err instanceof Error ? err.message : 'Ошибка импорта');
			} finally {
				importing = false;
			}
		};
		reader.readAsText(file);
	}

	// Terminal status line
	let statusLine = $derived.by(() => {
		if (!sysInfo) return '';
		const count = $tunnels.length;
		const word = count === 0 ? 'туннелей' : count === 1 ? 'туннель' : count < 5 ? 'туннеля' : 'туннелей';
		return `${sysInfo.version}  ·  ${sysInfo.goArch}  ·  ${count} ${word}`;
	});


</script>

<svelte:head>
	<title>Туннели - AWG Manager</title>
</svelte:head>

<PageContainer>
	<SingboxInstallBanner />

	{#if loading}
		<div class="py-12">
			<LoadingSpinner size="lg" message="Загрузка туннелей..." />
		</div>
	{:else}
		<TunnelTabs
			bind:active={activeTab}
			awgCount={$tunnels.length + $systemTunnelsList.length}
			singboxCount={$singboxTunnels.length}
		/>

		{#if activeTab === 'awg'}
		{#if $tunnels.length === 0 && $systemTunnelsList.length === 0}
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="ghost-terminal"
			class:drag-over={dragOver}
			ondrop={handleDrop}
			ondragover={handleDragOver}
			ondragleave={handleDragLeave}
		>
			{#if dragOver}
				<div class="drop-overlay">
					<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" width="40" height="40">
						<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/>
						<polyline points="17 8 12 3 7 8"/>
						<line x1="12" y1="3" x2="12" y2="15"/>
					</svg>
					<span class="drop-text">Отпустите для импорта</span>
				</div>
			{:else if importing}
				<div class="drop-overlay">
					<div class="spinner"></div>
					<span class="drop-text">Импорт...</span>
				</div>
			{:else}
				<div class="term-status">
					<span class="term-prompt">$ awg status</span>
					{#if statusLine}
						<span class="term-info">{statusLine}</span>
					{/if}
				</div>

				<div class="term-action-group">
					<div class="term-drop-hint">
						<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" width="28" height="28">
							<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/>
							<polyline points="17 8 12 3 7 8"/>
							<line x1="12" y1="3" x2="12" y2="15"/>
						</svg>
						<span>Перетащите .conf сюда</span>
					</div>

					<div class="term-backend-selector">
						<button
							type="button"
							class="term-backend-btn"
							class:selected={selectedBackend === 'nativewg'}
							class:disabled={sysInfo !== null && !sysInfo.backendAvailability?.nativewg}
							disabled={sysInfo !== null && !sysInfo.backendAvailability?.nativewg}
							onclick={() => selectedBackend = 'nativewg'}
						>
							NativeWG
						</button>
						<button
							type="button"
							class="term-backend-btn"
							class:selected={selectedBackend === 'kernel'}
							class:disabled={sysInfo !== null && !sysInfo.backendAvailability?.kernel}
							disabled={sysInfo !== null && !sysInfo.backendAvailability?.kernel}
							onclick={() => selectedBackend = 'kernel'}
						>
							Kernel
						</button>
					</div>

					<div class="term-commands">
						{#if $externalTunnels.length > 0}
							<span class="term-found">
								найдено {$externalTunnels.length} внешних интерфейс{$externalTunnels.length === 1 ? '' : 'а'}
							</span>
							<button class="term-cmd term-cmd-primary" onclick={() => {
								adoptingInterface = $externalTunnels[0].interfaceName;
								adoptDialogOpen = true;
							}}>
								<span class="term-arrow">{'>'}</span> подхватить интерфейсы
							</button>
						{/if}
						<button class="term-cmd" onclick={() => fileInput?.click()}>
							<span class="term-arrow">{'>'}</span> импортировать файл
						</button>
						<button class="term-cmd" onclick={() => goto('/tunnels/new?tab=link')}>
							<span class="term-arrow">{'>'}</span> импортировать ссылку
						</button>
					</div>
				</div>

				<input
					type="file"
					accept=".conf"
					bind:this={fileInput}
					onchange={handleFileSelect}
					style="display: none"
				/>
			{/if}
		</div>

		<div class="info-card">
			<h3 class="info-title">Об AmneziaWG</h3>
			<p class="info-section-desc">
				Форк WireGuard с обфускацией трафика. Три поколения протокола:
			</p>
			<div class="info-versions">
				<div class="info-version">
					<span class="info-version-tag">AWG 1.0</span>
					<span class="info-version-desc">Базовая обфускация: модификация заголовков (H1–H4), junk-пакеты (Jc/Jmin/Jmax), размеры сообщений (S1–S2).</span>
				</div>
				<div class="info-version">
					<span class="info-version-tag tag-15">AWG 1.5</span>
					<span class="info-version-desc">Мимикрия протоколов: initiation-пакеты (I1–I5) маскируют соединение под QUIC, DTLS, STUN, DNS.</span>
				</div>
				<div class="info-version">
					<span class="info-version-tag tag-20">AWG 2.0</span>
					<span class="info-version-desc">Рандомизация заголовков: H1–H4 задаются диапазонами, генерируются при каждом хэндшейке.</span>
				</div>
			</div>
			<p class="info-text info-kernel">
				Работает через <strong>модуль ядра</strong> — трафик обрабатывается напрямую в ядре Linux, что снижает нагрузку на CPU.
			</p>
		</div>

		{:else}
			{@const totalCount = $tunnels.length + $systemTunnelsList.length}
			<div class="tunnels-toolbar">
				<span class="tunnel-count">{totalCount} {totalCount === 1 ? 'туннель' : totalCount < 5 ? 'туннеля' : 'туннелей'}</span>
				<div class="toolbar-actions">
					<button class="btn btn-secondary" onclick={handleExportAll} disabled={exporting}>
						<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
							<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/>
							<polyline points="7 10 12 15 17 10"/>
							<line x1="12" y1="15" x2="12" y2="3"/>
						</svg>
						Экспорт
					</button>
					<a href="/tunnels/new" class="btn btn-primary">+ Создать</a>
				</div>
			</div>
			<div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
				{#each $tunnels as tunnel (tunnel.id)}
					<TunnelCard
						{tunnel}
						toggleLoading={toggleLoading[tunnel.id] ?? false}
						deleteLoading={deleteLoading[tunnel.id] ?? false}
						onToggleOnOff={() => handleToggleOnOff(tunnel.id)}
						ondelete={() => requestDelete(tunnel.id)}
					/>
				{/each}
				{#each $systemTunnelsList.filter((st) =>
					// Defense against backend dedup races: if a managed tunnel
					// already claims this NDMS name, don't render the system
					// card (it would be a ghost duplicate). System tunnel id
					// is the NDMS name ("WireguardN"), so we compare against
					// the managed tunnel's ndmsName.
					!$tunnels.some((mt) =>
						(mt.ndmsName && mt.ndmsName === st.id) ||
						(mt.interfaceName && mt.interfaceName === st.id)
					)
				) as tunnel (tunnel.id)}
					<SystemTunnelCard {tunnel} onHide={hideSystemTunnel} onMarkServer={markAsServer} />
				{/each}
			</div>

			{#if $externalTunnels.length > 0}
				<h2 class="text-lg font-semibold mt-6 mb-4">Внешние туннели</h2>
				<div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
					{#each $externalTunnels as extTunnel (extTunnel.interfaceName)}
						<ExternalTunnelCard
							tunnel={extTunnel}
							onadopt={(name) => handleAdoptClick(name)}
						/>
					{/each}
				</div>
			{/if}
		{/if}
		{:else}
			{#if $singboxTunnels.length === 0}
				<SingboxGhostTerminal />
				<div class="info-card">
					<h3 class="info-title">О Sing-box</h3>
					<p class="info-section-desc">
						Универсальный прокси с поддержкой современных протоколов:
					</p>
					<div class="info-versions">
						<div class="info-version">
							<span class="info-version-tag tag-vless">VLESS</span>
							<span class="info-version-desc">Лёгкий протокол без шифрования на уровне протокола. Поддерживает <strong>Reality</strong> (маскировка под настоящий TLS-сервер) и транспорт gRPC для обхода DPI.</span>
						</div>
						<div class="info-version">
							<span class="info-version-tag tag-hy2">Hysteria2</span>
							<span class="info-version-desc">QUIC-based, устойчив к потерям пакетов и работает поверх UDP. Паролевая аутентификация, обфускация salamander.</span>
						</div>
						<div class="info-version">
							<span class="info-version-tag tag-naive">NaiveProxy</span>
							<span class="info-version-desc">HTTP/2 с полноценным TLS-маскированием под обычный HTTPS-сервер. Сложно отличим от браузерного трафика.</span>
						</div>
					</div>
				</div>
			{:else}
				<div class="tunnels-toolbar">
					<span class="tunnel-count">
						{$singboxTunnels.length}
						{$singboxTunnels.length === 1 ? 'туннель' : $singboxTunnels.length < 5 ? 'туннеля' : 'туннелей'}
					</span>
					<div class="toolbar-actions">
						<a href="/singbox/new" class="btn btn-primary">+ Добавить</a>
					</div>
				</div>
				<div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
					{#each $singboxTunnels as tunnel (tunnel.tag)}
						<SingboxTunnelCard {tunnel} />
					{/each}
				</div>
			{/if}
		{/if}
	{/if}
</PageContainer>

<AdoptTunnelDialog
	interfaceName={adoptingInterface}
	bind:open={adoptDialogOpen}
	bind:error={adoptError}
	bind:loading={adoptLoading}
	onclose={() => adoptDialogOpen = false}
	onadopt={handleAdopt}
/>

{#if deleteConfirmId}
	{@const tunnelName = $tunnels.find(t => t.id === deleteConfirmId)?.name ?? deleteConfirmId}
	<Modal
		open={true}
		title="Удалить туннель"
		size="sm"
		onclose={() => deleteConfirmId = null}
	>
		<p class="confirm-text">Удалить туннель <strong>{tunnelName}</strong>?</p>
		{#snippet actions()}
			<button class="btn btn-ghost" onclick={() => deleteConfirmId = null}>Отмена</button>
			<button class="btn btn-danger" onclick={() => handleDelete(deleteConfirmId!)}>Удалить</button>
		{/snippet}
	</Modal>
{/if}

{#if showUnsupportedBlock}
	<div class="unsupported-overlay">
		<div class="unsupported-card">
			<div class="unsupported-icon">
				<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" width="48" height="48">
					<path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/>
					<line x1="12" y1="9" x2="12" y2="13"/>
					<line x1="12" y1="17" x2="12.01" y2="17"/>
				</svg>
			</div>
			<h2 class="unsupported-title">Модуль ядра недоступен</h2>
			<p class="unsupported-text">
				Модель роутера <strong>{sysInfo?.kernelModuleModel || '(неизвестна)'}</strong> не имеет скомпилированный модуль ядра в настоящий момент.
			</p>
			<div class="unsupported-actions">
				<a href="https://t.me/awgmanager" target="_blank" rel="noopener" class="unsupported-link unsupported-link-primary">
					Написать в @awgmanager
				</a>
				<a href="https://gitlab.com/AmneziaVPN/amneziawg/amneziawg-linux-kernel-module" target="_blank" rel="noopener" class="unsupported-link">
					Установить вручную
				</a>
			</div>
		</div>
	</div>
{/if}

<style>
	.confirm-text {
		font-size: 0.875rem;
		color: var(--text-secondary);
		margin: 0;
	}

	.tunnels-toolbar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		margin-bottom: 1rem;
	}

	.tunnel-count {
		font-size: 0.8125rem;
		color: var(--text-muted);
	}

	.toolbar-actions {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.toolbar-actions .btn svg {
		vertical-align: middle;
		margin-right: 4px;
	}

	.ghost-terminal {
		margin: 3rem 0;
		border: 2px dashed var(--border);
		border-radius: 12px;
		padding: 2rem 2rem 1.5rem;
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 1.5rem;
		transition: border-color 0.2s, background 0.2s;
	}

	.ghost-terminal.drag-over {
		border-color: var(--accent);
		border-style: solid;
		background: rgba(122, 162, 247, 0.06);
	}

	/* Terminal status line */
	.term-status {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.25rem;
		font-family: ui-monospace, SFMono-Regular, 'SF Mono', Menlo, monospace;
	}

	.term-prompt {
		font-size: 0.8125rem;
		color: var(--text-muted);
	}

	.term-info {
		font-size: 0.75rem;
		color: var(--text-muted);
		opacity: 0.7;
	}

	/* Group: drop hint + commands */
	.term-action-group {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 1.5rem;
	}

	/* Drop hint — the visual accent */
	.term-drop-hint {
		display: flex;
		align-items: center;
		gap: 0.625rem;
		color: var(--accent);
		font-size: 1.0625rem;
		font-weight: 500;
	}

	.term-drop-hint svg {
		flex-shrink: 0;
		opacity: 0.8;
	}

	.term-commands {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.125rem;
		font-family: ui-monospace, SFMono-Regular, 'SF Mono', Menlo, monospace;
	}

	.term-found {
		font-size: 0.8125rem;
		color: var(--accent);
		margin-bottom: 0.375rem;
	}

	.term-cmd {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		background: none;
		border: none;
		color: var(--text-secondary);
		font-family: inherit;
		font-size: 0.875rem;
		padding: 0.375rem 0.5rem;
		border-radius: 6px;
		cursor: pointer;
		transition: color 0.15s, background 0.15s;
		text-decoration: none;
	}

	.term-cmd:hover {
		color: var(--text-primary);
		background: var(--bg-hover);
	}

	.term-cmd-primary {
		color: var(--accent);
	}

	.term-cmd-primary:hover {
		color: var(--accent-hover);
	}

	.term-arrow {
		color: var(--text-muted);
	}

	/* Backend selector */
	.term-backend-selector {
		display: flex;
		gap: 8px;
	}

	.term-backend-btn {
		font-family: ui-monospace, SFMono-Regular, 'SF Mono', Menlo, monospace;
		font-size: 0.8125rem;
		padding: 0.375rem 1rem;
		border: 1px solid var(--border);
		border-radius: 6px;
		background: transparent;
		color: var(--text-muted);
		cursor: pointer;
		transition: all 0.15s;
	}

	.term-backend-btn:hover:not(.disabled) {
		border-color: var(--accent);
		color: var(--text-secondary);
	}

	.term-backend-btn.selected {
		border-color: var(--accent);
		color: var(--accent);
		background: rgba(122, 162, 247, 0.08);
	}

	.term-backend-btn.disabled {
		opacity: 0.4;
		cursor: not-allowed;
	}

	/* Drop overlay (drag-over & importing states) */
	.drop-overlay {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.75rem;
		padding: 2rem 0;
		color: var(--accent);
	}

	.drop-text {
		font-size: 1.0625rem;
		font-weight: 500;
	}

	/* Info card */
	.info-card {
		border-left: 3px solid var(--accent);
		background: var(--bg-secondary);
		border-radius: 0 8px 8px 0;
		padding: 1.25rem 1.5rem;
		margin-top: 1.5rem;
	}

	.info-title {
		font-size: 1rem;
		font-weight: 600;
		margin-bottom: 0.75rem;
	}

	.info-text {
		font-size: 0.8125rem;
		color: var(--text-secondary);
		line-height: 1.6;
		margin: 0;
	}

	.info-versions {
		display: flex;
		flex-direction: column;
		gap: 0.625rem;
		margin: 0.75rem 0;
	}

	.info-version {
		display: flex;
		gap: 0.75rem;
		align-items: baseline;
	}

	.info-version-tag {
		flex-shrink: 0;
		font-size: 0.6875rem;
		font-weight: 600;
		font-family: ui-monospace, SFMono-Regular, 'SF Mono', Menlo, monospace;
		padding: 0.2rem 0.5rem;
		border-radius: 4px;
		background: rgba(122, 162, 247, 0.12);
		color: var(--accent);
		white-space: nowrap;
	}

	.info-version-tag.tag-15 {
		background: rgba(125, 207, 255, 0.12);
		color: var(--info);
	}

	.info-version-tag.tag-20 {
		background: rgba(158, 206, 106, 0.12);
		color: var(--success);
	}

	.info-version-desc {
		font-size: 0.8125rem;
		color: var(--text-secondary);
		line-height: 1.5;
	}

	.info-kernel {
		margin-top: 0.75rem;
		padding-top: 0.75rem;
		border-top: 1px solid var(--border);
	}

	.info-kernel strong {
		color: var(--text-primary);
	}

	/* Unsupported overlay */
	.unsupported-overlay {
		position: fixed;
		inset: 0;
		z-index: 100;
		background: rgba(0, 0, 0, 0.85);
		display: flex;
		align-items: center;
		justify-content: center;
		padding: 1rem;
	}

	.unsupported-card {
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: 12px;
		padding: 2rem;
		max-width: 420px;
		width: 100%;
		text-align: center;
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 1rem;
	}

	.unsupported-icon {
		color: var(--warning, #e0af68);
	}

	.unsupported-title {
		font-size: 1.25rem;
		font-weight: 600;
		margin: 0;
	}

	.unsupported-text {
		font-size: 0.875rem;
		color: var(--text-secondary);
		line-height: 1.6;
		margin: 0;
	}

	.unsupported-text strong {
		color: var(--text-primary);
	}

	.unsupported-actions {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		width: 100%;
		margin-top: 0.5rem;
	}

	.unsupported-link {
		display: block;
		padding: 0.625rem 1rem;
		border-radius: 8px;
		font-size: 0.875rem;
		font-weight: 500;
		text-decoration: none;
		text-align: center;
		transition: opacity 0.15s;
		border: 1px solid var(--border);
		color: var(--text-secondary);
		background: var(--bg-secondary);
	}

	.unsupported-link:hover {
		opacity: 0.85;
	}

	.unsupported-link-primary {
		background: var(--accent);
		color: #fff;
		border-color: var(--accent);
	}

	.info-section-desc {
		font-size: 0.85rem;
		color: var(--text-muted);
		margin: 0 0 0.75rem 0;
	}

	.tag-vless {
		background: rgba(59, 130, 246, 0.15);
		color: #60a5fa;
	}
	.tag-hy2 {
		background: rgba(245, 158, 11, 0.15);
		color: #fbbf24;
	}
	.tag-naive {
		background: rgba(34, 211, 238, 0.15);
		color: #22d3ee;
	}

</style>
