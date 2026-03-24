<script lang="ts">
	import { Modal } from '$lib/components/ui';
	import { parseStaticRouteImport, type PortableStaticRoute } from '$lib/utils/staticroute-export';

	interface Props {
		open: boolean;
		existingNames: string[];
		onclose: () => void;
		onimport: (routes: PortableStaticRoute[]) => void;
	}

	let {
		open = $bindable(false),
		existingNames,
		onclose,
		onimport,
	}: Props = $props();

	let parsed = $state<PortableStaticRoute[] | null>(null);
	let selectedFlags = $state<boolean[]>([]);
	let parseError = $state('');
	let importing = $state(false);
	let wasOpen = $state(false);
	let dragging = $state(false);
	let fileInput: HTMLInputElement;

	// Reset on open
	$effect(() => {
		if (open && !wasOpen) {
			parsed = null;
			selectedFlags = [];
			parseError = '';
			importing = false;
		}
		wasOpen = open;
	});

	let selectedCount = $derived(selectedFlags.filter(Boolean).length);
	let existingLower = $derived(existingNames.map(n => n.toLowerCase()));

	function isDuplicate(name: string): boolean {
		return existingLower.includes(name.toLowerCase());
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
		const selected = parsed.filter((_, i) => selectedFlags[i]);
		importing = true;
		onimport(selected);
	}
</script>

<Modal {open} title="Загрузить набор маршрутов" size="md" {onclose}>
	{#if !parsed}
		<div class="import-upload">
			<p class="import-description">
				Загрузка конфигурации IP-маршрутов, <span class="import-accent">ранее сохранённых в AWG Manager</span>.
			</p>
			<!-- svelte-ignore a11y_no_static_element_interactions -->
			<div
				class="drop-zone"
				class:dragging
				onclick={() => fileInput.click()}
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
		<!-- Preview list -->
		<p class="import-hint">Найдено {parsed.length} маршрутов:</p>
		<div class="import-list">
			{#each parsed as route, i}
				<label class="import-item" class:duplicate={isDuplicate(route.name)}>
					<input type="checkbox" bind:checked={selectedFlags[i]} disabled={importing} />
					<span class="import-name">{route.name}</span>
					<span class="import-meta">
						{route.subnets.length} подсетей
					</span>
					{#if isDuplicate(route.name)}
						<span class="import-dup">(дубликат)</span>
					{/if}
				</label>
			{/each}
		</div>
	{/if}

	{#snippet actions()}
		<button class="btn btn-ghost" onclick={onclose} disabled={importing}>Отмена</button>
		{#if parsed}
			<button class="btn btn-primary" onclick={handleImport} disabled={importing || selectedCount === 0}>
				{importing ? 'Импорт...' : `Импортировать (${selectedCount})`}
			</button>
		{/if}
	{/snippet}
</Modal>

<style>
	.import-upload {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 1rem;
		padding: 2rem;
	}

	.drop-zone {
		border: 2px dashed var(--border);
		border-radius: 10px;
		padding: 2rem 1.5rem;
		cursor: pointer;
		text-align: center;
		color: var(--text-muted);
		transition: border-color 0.2s, background 0.2s;
		width: 100%;
	}

	.drop-zone:hover, .drop-zone.dragging {
		border-color: var(--accent);
		background: rgba(59, 130, 246, 0.05);
	}

	.drop-text {
		font-size: 0.8125rem;
		color: var(--text-secondary);
		margin: 0.5rem 0 0;
	}

	.drop-hint {
		font-size: 0.6875rem;
		color: var(--text-muted);
		margin: 0.25rem 0 0;
	}

	.hidden-input {
		display: none;
	}

	.import-description {
		color: var(--text-secondary);
		font-size: 0.8125rem;
		text-align: center;
		margin-bottom: 0.5rem;
	}

	.import-accent {
		color: var(--error);
		font-weight: 500;
	}

	.import-error {
		color: var(--error);
		font-size: 0.8125rem;
	}

	.import-hint {
		color: var(--text-secondary);
		font-size: 0.875rem;
		margin-bottom: 0.75rem;
	}

	.import-list {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		max-height: 400px;
		overflow-y: auto;
	}

	.import-item {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		padding: 0.5rem 0.75rem;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: 6px;
		cursor: pointer;
		font-size: 0.8125rem;
	}

	.import-item.duplicate {
		opacity: 0.5;
	}

	.import-name {
		font-weight: 500;
		color: var(--text-primary);
	}

	.import-meta {
		color: var(--text-muted);
		font-size: 0.75rem;
	}

	.import-dup {
		color: var(--warning);
		font-size: 0.6875rem;
		font-style: italic;
	}
</style>
