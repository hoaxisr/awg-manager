<script lang="ts">
	import { Modal } from '$lib/components/ui';
	import { parseStaticRouteImport, type PortableStaticRoute } from '$lib/utils/staticroute-export';
	import type { RoutingTunnel } from '$lib/types';

	interface Props {
		open: boolean;
		existingNames: string[];
		tunnels: RoutingTunnel[];
		onclose: () => void;
		onimport: (routes: (PortableStaticRoute & { tunnelID: string })[]) => void;
	}

	let {
		open = $bindable(false),
		existingNames,
		tunnels,
		onclose,
		onimport,
	}: Props = $props();

	let parsed = $state<PortableStaticRoute[] | null>(null);
	let selectedFlags = $state<boolean[]>([]);
	let parseError = $state('');
	let importing = $state(false);
	let wasOpen = $state(false);
	let dragging = $state(false);
	let fileInput = $state<HTMLInputElement>(null!);
	let defaultTunnelId = $state('');
	let tunnelOverrides = $state<Record<number, string>>({});
	let editingTunnelIdx = $state<number | null>(null);

	// Reset on open
	$effect(() => {
		if (open && !wasOpen) {
			parsed = null;
			selectedFlags = [];
			parseError = '';
			importing = false;
			defaultTunnelId = tunnels.find(t => t.available)?.id ?? '';
			tunnelOverrides = {};
			editingTunnelIdx = null;
		}
		wasOpen = open;
	});

	let selectedCount = $derived(selectedFlags.filter(Boolean).length);
	let existingLower = $derived(existingNames.map(n => n.toLowerCase()));
	let userTunnels = $derived(tunnels.filter(t => t.type === 'managed' && t.available));
	let systemTunnels = $derived(tunnels.filter(t => t.type === 'system' && t.available));
	let noTunnels = $derived(tunnels.filter(t => t.available).length === 0);

	function isDuplicate(name: string): boolean {
		return existingLower.includes(name.toLowerCase());
	}

	function effectiveTunnel(index: number): string {
		return tunnelOverrides[index] ?? defaultTunnelId;
	}

	function tunnelName(tunnelId: string): string {
		return tunnels.find(t => t.id === tunnelId)?.name ?? tunnelId;
	}

	async function processFile(file: File) {
		try {
			const text = await file.text();
			const routes = parseStaticRouteImport(text);
			if (routes.length === 0) {
				parseError = 'Не найдено валидных маршрутов в файле';
				return;
			}
			parsed = routes;
			selectedFlags = routes.map(r => !isDuplicate(r.name));
			tunnelOverrides = {};
			editingTunnelIdx = null;
		} catch (e) {
			parseError = e instanceof Error ? e.message : 'Ошибка чтения файла';
		}
	}

	function handleFile(e: Event) {
		const input = e.target as HTMLInputElement;
		const file = input.files?.[0];
		if (file) processFile(file);
	}

	function handleDrop(e: DragEvent) {
		e.preventDefault();
		dragging = false;
		const file = e.dataTransfer?.files?.[0];
		if (file) processFile(file);
	}

	function handleDragOver(e: DragEvent) {
		e.preventDefault();
		dragging = true;
	}

	function handleDragLeave() {
		dragging = false;
	}

	function handleImport() {
		if (!parsed) return;
		const selected = parsed
			.map((r, i) => ({ ...r, tunnelID: effectiveTunnel(i), _selected: selectedFlags[i] }))
			.filter(r => r._selected)
			.map(({ _selected, ...r }) => r);
		importing = true;
		onimport(selected);
	}
</script>

<Modal {open} title="Загрузить набор маршрутов" size="lg" {onclose}>
	{#if !parsed}
		<div class="import-upload">
			<p class="import-description">
				Загрузка конфигурации IP-маршрутов, <span class="import-accent">ранее сохранённых в AWG Manager</span>.
			</p>
			<div
				class="drop-zone"
				class:dragging
				role="button"
				tabindex="0"
				onclick={() => fileInput.click()}
				onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') fileInput.click(); }}
				ondrop={handleDrop}
				ondragover={handleDragOver}
				ondragleave={handleDragLeave}
			>
				<svg width="40" height="40" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
					<path d="M21 15v4a2 2 0 01-2 2H5a2 2 0 01-2-2v-4"/>
					<polyline points="17 8 12 3 7 8"/>
					<line x1="12" y1="3" x2="12" y2="15"/>
				</svg>
				<p class="drop-text">Перетащите .json файл сюда</p>
				<p class="drop-hint">или нажмите для выбора</p>
			</div>
			<input type="file" accept=".json" onchange={handleFile} bind:this={fileInput} class="hidden-input" />
			{#if parseError}
				<p class="import-error">{parseError}</p>
			{/if}
		</div>
	{:else}
		<!-- Default tunnel selector -->
		<div class="tunnel-default-bar">
			<span class="tunnel-default-label">Туннель для всех:</span>
			<select class="tunnel-select" bind:value={defaultTunnelId} disabled={importing}>
				{#if userTunnels.length > 0}
					<optgroup label="Пользовательские">
						{#each userTunnels as t}
							<option value={t.id}>{t.name}</option>
						{/each}
					</optgroup>
				{/if}
				{#if systemTunnels.length > 0}
					<optgroup label="Системные">
						{#each systemTunnels as t}
							<option value={t.id}>{t.name}</option>
						{/each}
					</optgroup>
				{/if}
			</select>
		</div>

		{#if noTunnels}
			<p class="import-error">Создайте хотя бы один туннель перед импортом</p>
		{/if}

		<!-- Preview list -->
		<p class="import-hint">Найдено {parsed.length} маршрутов:</p>
		<div class="import-list">
			{#each parsed as route, i}
				<label class="import-item" class:duplicate={isDuplicate(route.name)} class:overridden={tunnelOverrides[i] != null}>
					<input type="checkbox" bind:checked={selectedFlags[i]} disabled={importing} />
					<div class="import-item-info">
						<span class="import-name">{route.name}</span>
						<span class="import-meta">{route.subnets.length} подсетей</span>
					</div>
					{#if isDuplicate(route.name)}
						<span class="import-dup">(дубликат)</span>
					{/if}
					{#if editingTunnelIdx === i}
						<select
							class="tunnel-select-inline"
							value={effectiveTunnel(i)}
							onchange={(e) => {
								const val = (e.target as HTMLSelectElement).value;
								if (val === defaultTunnelId) {
									const next = { ...tunnelOverrides };
									delete next[i];
									tunnelOverrides = next;
								} else {
									tunnelOverrides = { ...tunnelOverrides, [i]: val };
								}
								editingTunnelIdx = null;
							}}
						>
							{#if userTunnels.length > 0}
								<optgroup label="Пользовательские">
									{#each userTunnels as t}
										<option value={t.id}>{t.name}</option>
									{/each}
								</optgroup>
							{/if}
							{#if systemTunnels.length > 0}
								<optgroup label="Системные">
									{#each systemTunnels as t}
										<option value={t.id}>{t.name}</option>
									{/each}
								</optgroup>
							{/if}
						</select>
					{:else}
						<button
							class="tunnel-name-btn"
							class:overridden={tunnelOverrides[i] != null}
							onclick={(e) => { e.stopPropagation(); editingTunnelIdx = i; }}
							disabled={importing}
						>
							{tunnelName(effectiveTunnel(i))}
						</button>
					{/if}
				</label>
			{/each}
		</div>
	{/if}

	{#snippet actions()}
		<button class="btn btn-ghost" onclick={onclose} disabled={importing}>Отмена</button>
		{#if parsed}
			<button class="btn btn-primary" onclick={handleImport} disabled={importing || selectedCount === 0 || noTunnels}>
				{importing ? 'Импорт...' : `Импортировать (${selectedCount})`}
			</button>
		{/if}
	{/snippet}
</Modal>
