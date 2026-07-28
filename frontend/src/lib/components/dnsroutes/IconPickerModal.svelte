<script lang="ts">
	import { Modal, Button } from '$lib/components/ui';
	import {
		QURE_ICONS,
		QURE_CDN_BASE,
		qureIconUrl,
	} from '$lib/generated/qureIcons';
	import { resolveIconSlug } from '$lib/utils/resolve-icon-slug';
	import {
		dataUrlToSvgMarkup,
		fileToIconDataUrl,
		formatIconUrlHint,
		isDataIconUrl,
		parseSvgMarkup,
		svgMarkupToDataUrl,
	} from '$lib/utils/custom-icon';
	import {
		DEFAULT_ICON_TILE_BG,
		readThemeIconTileHex,
	} from '$lib/utils/icon-tile-background';
	import { normalizeTileHex, parseIconUrl, withIconTileBg } from '$lib/utils/icon-url-meta';
	import IconTile from './IconTile.svelte';

	interface Props {
		open: boolean;
		iconUrl?: string;
		ruleName: string;
		onclose: () => void;
		onapply: (newUrl: string | null) => void;
	}

	let { open, iconUrl = '', ruleName, onclose, onapply }: Props = $props();

	type Tab = 'catalog' | 'custom';

	let tab = $state<Tab>('catalog');
	let search = $state('');
	let selectedQure = $state<string | null>(null);
	let customUrl = $state('');
	let customSvg = $state('');
	let uploadedDataUrl = $state<string | null>(null);
	let customError = $state<string | null>(null);
	let dropActive = $state(false);
	/** null = auto brand / hash color; string = user override (#rrggbb). */
	let userTileBg = $state<string | null>(null);

	let defaultSlug = $derived(iconUrl ? null : resolveIconSlug(ruleName));
	let trimmedUrl = $derived(customUrl.trim());
	let trimmedSvg = $derived(customSvg.trim());
	let parsedSvg = $derived(trimmedSvg ? parseSvgMarkup(trimmedSvg) : null);

	let customPreviewUrl = $derived.by(() => {
		if (uploadedDataUrl) return uploadedDataUrl;
		if (parsedSvg) {
			try {
				return svgMarkupToDataUrl(trimmedSvg);
			} catch {
				return null;
			}
		}
		if (trimmedUrl && !isDataIconUrl(trimmedUrl)) return trimmedUrl;
		if (trimmedUrl && isDataIconUrl(trimmedUrl)) return trimmedUrl;
		return null;
	});

	let customPreviewHint = $derived.by(() => {
		if (uploadedDataUrl) return formatIconUrlHint(uploadedDataUrl);
		if (parsedSvg) return 'Встроенный SVG';
		if (trimmedUrl) return formatIconUrlHint(trimmedUrl);
		return '';
	});

	// Initialize state when the modal opens, based on current iconUrl + ruleName.
	$effect(() => {
		if (!open) return;

		search = '';
		customError = null;
		uploadedDataUrl = null;

		const parsed = parseIconUrl(iconUrl || '');
		userTileBg = parsed.userTileBg ?? null;
		const src = parsed.src;

		if (src && src.startsWith(QURE_CDN_BASE)) {
			const match = src.slice(QURE_CDN_BASE.length + 1).replace(/\.png$/, '');
			tab = 'catalog';
			selectedQure = decodeURIComponent(match);
			customUrl = '';
			customSvg = '';
		} else if (src) {
			tab = 'custom';
			selectedQure = null;
			const svg = dataUrlToSvgMarkup(src);
			if (svg) {
				customSvg = svg;
				customUrl = '';
			} else if (isDataIconUrl(src)) {
				uploadedDataUrl = src;
				customUrl = '';
				customSvg = '';
			} else {
				customUrl = src;
				customSvg = '';
			}
		} else {
			tab = 'catalog';
			customUrl = '';
			customSvg = '';
			selectedQure = null;
			userTileBg = null;
		}
	});

	let filteredIcons = $derived.by(() => {
		const q = search.trim().toLowerCase();
		if (!q) return QURE_ICONS;
		return QURE_ICONS.filter((n) => n.toLowerCase().includes(q));
	});

	let hasCustomSource = $derived(
		uploadedDataUrl !== null || trimmedUrl !== '' || parsedSvg !== null
	);

	let catalogPreviewSrc = $derived(selectedQure ? qureIconUrl(selectedQure) : null);

	let themeAutoTileHex = $state('24283b');

	let effectiveTileBg = $derived(userTileBg ?? DEFAULT_ICON_TILE_BG);

	let colorInputValue = $derived(
		(userTileBg ? userTileBg.replace(/^#/, '') : themeAutoTileHex).replace(/^#/, '')
	);

	$effect(() => {
		if (!open) return;
		themeAutoTileHex = readThemeIconTileHex().replace(/^#/, '');
	});

	let showTileBgControls = $derived(
		(tab === 'catalog' && selectedQure !== null) ||
		(tab === 'custom' && customPreviewUrl !== null)
	);

	let canApply = $derived(
		(tab === 'catalog' && selectedQure !== null) ||
		(tab === 'custom' && hasCustomSource && !customError)
	);

	function resolveCustomUrl(): string | null {
		if (uploadedDataUrl) return uploadedDataUrl;
		if (parsedSvg) return svgMarkupToDataUrl(trimmedSvg);
		if (trimmedUrl) return trimmedUrl;
		return null;
	}

	function handleApply() {
		customError = null;
		let url: string | null = null;
		try {
			if (tab === 'catalog' && selectedQure) {
				url = withIconTileBg(qureIconUrl(selectedQure), userTileBg);
			} else if (tab === 'custom') {
				const base = resolveCustomUrl();
				url = base ? withIconTileBg(base, userTileBg) : null;
			}
		} catch (e) {
			customError = e instanceof Error ? e.message : 'Не удалось применить иконку';
			return;
		}
		onapply(url);
	}

	function onTileColorInput(e: Event) {
		const hex = normalizeTileHex((e.currentTarget as HTMLInputElement).value);
		if (hex) userTileBg = hex;
	}

	function resetTileBg() {
		userTileBg = null;
	}


	function handleReset() {
		onapply(null);
	}

	async function ingestFiles(files: FileList | File[] | null | undefined) {
		const file = files?.[0];
		if (!file) return;
		customError = null;
		try {
			uploadedDataUrl = await fileToIconDataUrl(file);
			customUrl = '';
			customSvg = '';
		} catch (e) {
			customError = e instanceof Error ? e.message : 'Не удалось загрузить файл';
		}
	}

	function onFileInputChange(e: Event) {
		const input = e.currentTarget as HTMLInputElement;
		void ingestFiles(input.files);
		input.value = '';
	}

	function onDrop(e: DragEvent) {
		e.preventDefault();
		dropActive = false;
		void ingestFiles(e.dataTransfer?.files);
	}

	function onDragOver(e: DragEvent) {
		e.preventDefault();
		dropActive = true;
	}

	function onDragLeave() {
		dropActive = false;
	}

	function onUrlInput() {
		if (trimmedUrl) {
			uploadedDataUrl = null;
			customSvg = '';
		}
		customError = null;
	}

	function onSvgInput() {
		if (trimmedSvg) {
			uploadedDataUrl = null;
			customUrl = '';
		}
		customError = null;
	}

	let defaultIconHint = $derived.by(() => {
		if (iconUrl || tab !== 'catalog' || selectedQure !== null) return null;
		return defaultSlug
			? `Без выбора используется встроенная иконка (${defaultSlug}), как в SingBox`
			: null;
	});

	let catalogTileBgHint = $derived(
		userTileBg
			? 'Свой цвет сохранится в настройках маршрута'
			: 'Авто — единый фон плитки, контрастный с карточкой маршрута'
	);
</script>

{#snippet tileBgBar(previewSrc: string, footnote?: string)}
	<div class="tile-bg-controls">
		<div class="tile-bg-preview-col">
			<IconTile src={previewSrc} background={effectiveTileBg} size={36} alt="" />
			{#if footnote}
				<span class="tile-bg-caption">{footnote}</span>
			{/if}
		</div>
		<div class="tile-bg-editor-col">
			<span class="field-label">Фон иконки</span>
			<div class="tile-bg-row">
				<label class="color-picker" title="Выбрать цвет">
					<span
						class="color-picker-swatch"
						style:background-color={`#${colorInputValue}`}
						aria-hidden="true"
					></span>
					<input
						type="color"
						class="color-picker-native"
						value={`#${colorInputValue}`}
						oninput={onTileColorInput}
						aria-label="Выбрать цвет фона плитки"
					/>
				</label>
				<input
					type="text"
					class="hex-input"
					value={`#${colorInputValue}`}
					oninput={onTileColorInput}
					spellcheck="false"
					maxlength={7}
					aria-label="HEX цвета фона"
				/>
				<button
					type="button"
					class="auto-bg-btn"
					disabled={userTileBg === null}
					onclick={resetTileBg}
				>
					Авто
				</button>
			</div>
		</div>
	</div>
{/snippet}

<Modal {open} {onclose} title="Выбрать иконку" size="lg">
	<div class="picker">
		<div class="tabs" role="tablist" aria-label="Источник иконки">
			<button
				class="tab"
				class:active={tab === 'catalog'}
				onclick={() => (tab = 'catalog')}
				type="button"
				role="tab"
				aria-selected={tab === 'catalog'}
			>
				Каталог Qure
			</button>
			<button
				class="tab"
				class:active={tab === 'custom'}
				onclick={() => (tab = 'custom')}
				type="button"
				role="tab"
				aria-selected={tab === 'custom'}
			>
				Своя иконка
			</button>
		</div>

		{#if tab === 'catalog'}
			<div class="search-row">
				<input
					type="text"
					class="search-input"
					placeholder="Поиск (telegram, netflix, github...)"
					aria-label="Поиск иконки"
					bind:value={search}
				/>
				<span class="count">
					{filteredIcons.length} иконок{search ? ' (отфильтровано)' : ''}
				</span>
			</div>

			{#if defaultIconHint}
				<p class="auto-hint">{defaultIconHint}</p>
			{/if}

			{#if showTileBgControls && catalogPreviewSrc}
				{@render tileBgBar(catalogPreviewSrc, catalogTileBgHint)}
			{/if}

			<div class="grid">
				{#each filteredIcons as name (name)}
					<button
						class="tile"
						class:selected={selectedQure === name}
						onclick={() => (selectedQure = name)}
						type="button"
						title={name}
					>
						<IconTile
							src={qureIconUrl(name)}
							background={DEFAULT_ICON_TILE_BG}
							size={44}
							alt={name}
						/>
						<span class="label">{name.replace(/_/g, ' ')}</span>
					</button>
				{/each}
			</div>
		{:else}
			<div class="custom-section">
				<label class="field-label" for="icon-url-input">URL картинки</label>
				<input
					id="icon-url-input"
					type="url"
					class="text-input"
					placeholder="https://example.com/icon.png"
					bind:value={customUrl}
					oninput={onUrlInput}
				/>

				<div class="or-divider" aria-hidden="true"><span>или</span></div>

				<label class="field-label" for="icon-svg-input">Код SVG</label>
				<textarea
					id="icon-svg-input"
					class="svg-input"
					placeholder="<svg viewBox=&quot;0 0 24 24&quot;>...</svg>"
					rows="5"
					bind:value={customSvg}
					oninput={onSvgInput}
					spellcheck="false"
				></textarea>
				<p class="field-hint">Вставьте фрагмент &lt;svg&gt;…&lt;/svg&gt; или целый файл.</p>

				<div class="or-divider" aria-hidden="true"><span>или</span></div>

				<!-- svelte-ignore a11y_no_static_element_interactions -->
				<div
					class="drop-zone"
					class:active={dropActive}
					ondrop={onDrop}
					ondragover={onDragOver}
					ondragleave={onDragLeave}
					role="region"
					aria-label="Загрузка файла иконки"
				>
					<input
						id="icon-file-input"
						type="file"
						class="file-input"
						accept="image/png,image/jpeg,image/webp,image/gif,image/svg+xml,.svg"
						onchange={onFileInputChange}
					/>
					<label for="icon-file-input" class="drop-label">
						<span class="drop-title">Перетащите файл сюда</span>
						<span class="drop-sub">или нажмите для выбора · PNG, JPG, WebP, SVG · до 96 КБ</span>
					</label>
					{#if uploadedDataUrl}
						<button type="button" class="clear-upload" onclick={() => (uploadedDataUrl = null)}>
							Убрать файл
						</button>
					{/if}
				</div>

				{#if customError}
					<p class="error-text" role="alert">{customError}</p>
				{/if}

				{#if showTileBgControls && customPreviewUrl}
					{@render tileBgBar(customPreviewUrl, customPreviewHint)}
				{:else if trimmedSvg && !parsedSvg}
					<p class="error-text">Некорректный SVG — нужен тег &lt;svg&gt; без скриптов</p>
				{/if}

				<p class="field-hint footer-hint">
					Иконка сохраняется в настройках маршрута. URL — ссылка; SVG и файлы — встроенные data URL.
				</p>
			</div>
		{/if}
	</div>

	{#snippet actions()}
		<div class="footer-left">
			{#if iconUrl}
				<Button variant="ghost" size="sm" onclick={handleReset}>&#x21BA; Сбросить (на авто)</Button>
			{/if}
		</div>
		<div class="footer-right">
			<Button variant="ghost" onclick={onclose}>Отмена</Button>
			<Button variant="primary" onclick={handleApply} disabled={!canApply}>Применить</Button>
		</div>
	{/snippet}
</Modal>

<style>
	.picker {
		display: flex;
		flex-direction: column;
		gap: 12px;
		min-height: 380px;
	}
	.tabs {
		display: flex;
		gap: 4px;
		border-bottom: 1px solid var(--border);
	}
	.tab {
		padding: 10px 14px;
		color: var(--text-muted);
		background: transparent;
		border: none;
		border-bottom: 2px solid transparent;
		margin-bottom: -1px;
		cursor: pointer;
		font-size: 0.875rem;
		font-weight: 500;
		font-family: inherit;
	}
	.tab:hover {
		color: var(--text-secondary);
	}
	.tab.active {
		color: var(--accent);
		border-bottom-color: var(--accent);
	}
	.search-row {
		display: flex;
		align-items: center;
		gap: 8px;
	}
	.search-input {
		flex: 1;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: 6px;
		padding: 8px 12px;
		color: var(--text-primary);
		font-size: 0.875rem;
		font-family: inherit;
	}
	.search-input:focus {
		outline: none;
		border-color: var(--accent);
	}
	.count {
		font-size: 0.75rem;
		color: var(--text-muted);
		flex-shrink: 0;
	}
	.auto-hint {
		font-size: 0.75rem;
		color: var(--accent);
		margin: 0;
	}
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(72px, 1fr));
		gap: 8px;
		max-height: 360px;
		overflow-y: auto;
	}
	.tile {
		aspect-ratio: 1;
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: 4px;
		padding: 6px;
		border: 1px solid transparent;
		border-radius: 8px;
		background: transparent;
		cursor: pointer;
		font-family: inherit;
		transition: background 0.12s, border-color 0.12s;
	}
	.tile:hover {
		background: var(--bg-hover);
		border-color: var(--border-hover);
	}
	.tile.selected {
		background: var(--bg-hover);
		border-color: var(--accent);
	}
	.tile .label {
		font-size: 0.625rem;
		color: var(--text-muted);
		max-width: 100%;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.tile.selected .label {
		color: var(--text-primary);
	}
	.custom-section {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}
	.field-label {
		font-size: 0.8125rem;
		color: var(--text-muted);
	}
	.text-input,
	.svg-input {
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: 6px;
		padding: 9px 12px;
		color: var(--text-primary);
		font-size: 0.875rem;
		font-family: inherit;
		width: 100%;
		box-sizing: border-box;
	}
	.svg-input {
		font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
		font-size: 0.8125rem;
		line-height: 1.45;
		resize: vertical;
		min-height: 96px;
	}
	.text-input:focus,
	.svg-input:focus {
		outline: none;
		border-color: var(--accent);
	}
	.field-hint {
		font-size: 0.75rem;
		color: var(--text-muted);
		margin: 0;
	}
	.footer-hint {
		margin-top: 4px;
	}
	.or-divider {
		display: flex;
		align-items: center;
		gap: 10px;
		color: var(--text-muted);
		font-size: 0.75rem;
		margin: 2px 0;
	}
	.or-divider::before,
	.or-divider::after {
		content: '';
		flex: 1;
		height: 1px;
		background: var(--border);
	}
	.drop-zone {
		position: relative;
		border: 1.5px dashed var(--border);
		border-radius: 8px;
		padding: 20px 16px;
		text-align: center;
		background: var(--bg-secondary);
		transition: border-color 0.12s, background 0.12s;
	}
	.drop-zone.active,
	.drop-zone:hover {
		border-color: var(--accent);
		background: var(--bg-hover);
	}
	.file-input {
		position: absolute;
		inset: 0;
		opacity: 0;
		cursor: pointer;
		width: 100%;
		height: 100%;
	}
	.drop-label {
		display: flex;
		flex-direction: column;
		gap: 4px;
		pointer-events: none;
	}
	.drop-title {
		font-size: 0.875rem;
		color: var(--text-primary);
		font-weight: 500;
	}
	.drop-sub {
		font-size: 0.75rem;
		color: var(--text-muted);
	}
	.clear-upload {
		position: relative;
		z-index: 1;
		margin-top: 10px;
		background: transparent;
		border: none;
		color: var(--accent);
		font-size: 0.75rem;
		cursor: pointer;
		font-family: inherit;
		text-decoration: underline;
	}
	.error-text {
		font-size: 0.75rem;
		color: var(--color-error);
		margin: 0;
	}
	.tile-bg-controls {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 8px 14px;
		align-items: center;
		padding: 8px 10px;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: 8px;
	}
	.tile-bg-preview-col {
		display: flex;
		align-items: center;
		gap: 8px;
		min-width: 0;
	}
	.tile-bg-caption,
	.tile-bg-editor-col .field-label {
		font-size: 0.75rem;
		line-height: 1.35;
	}

	.tile-bg-caption {
		color: var(--text-muted);
		min-width: 0;
	}
	.tile-bg-editor-col {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
		/* align-items: flex-end; */
	}
	.tile-bg-editor-col .field-label {
		margin: 0;
		color: var(--text-muted);
	}
	.tile-bg-row {
		display: flex;
		align-items: center;
		gap: 6px;
		--tile-bg-control-h: 28px;
	}
	.color-picker {
		position: relative;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: var(--tile-bg-control-h);
		height: var(--tile-bg-control-h);
		margin: 0;
		flex-shrink: 0;
		cursor: pointer;
		vertical-align: middle;
	}
	.color-picker-swatch {
		display: block;
		width: 100%;
		height: 100%;
		box-sizing: border-box;
		border-radius: 6px;
		border: 1px solid var(--border);
		box-shadow: inset 0 0 0 1px rgba(255, 255, 255, 0.08);
	}
	.color-picker-native {
		position: absolute;
		inset: 0;
		width: 100%;
		height: 100%;
		padding: 0;
		margin: 0;
		border: none;
		opacity: 0;
		cursor: pointer;
	}
	.color-picker:hover .color-picker-swatch,
	.color-picker:focus-within .color-picker-swatch {
		border-color: var(--accent);
	}
	.hex-input {
		width: 6rem !important;
		height: var(--tile-bg-control-h);
		margin: 0;
		flex-shrink: 0;
		box-sizing: border-box;
		background: var(--bg-tertiary);
		border: 1px solid var(--border);
		border-radius: 6px;
		padding: 0 8px !important;
		color: var(--text-primary);
		font-size: 0.75rem !important;
		line-height: calc(var(--tile-bg-control-h) - 2px);
		font-family: ui-monospace, monospace;
		vertical-align: middle;
	}
	.hex-input:focus {
		outline: none;
		border-color: var(--accent);
	}
	.auto-bg-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		height: var(--tile-bg-control-h);
		margin: 0;
		box-sizing: border-box;
		padding: 0 10px;
		border: 1px solid var(--border);
		border-radius: 6px;
		background: transparent;
		color: var(--text-secondary);
		font-size: 0.75rem;
		line-height: 1;
		white-space: nowrap;
		cursor: pointer;
		font-family: inherit;
		flex-shrink: 0;
	}
	.auto-bg-btn:hover:not(:disabled) {
		border-color: var(--accent);
		color: var(--accent);
	}
	.auto-bg-btn:disabled {
		opacity: 0.45;
		cursor: default;
	}
	.footer-left {
		flex: 1;
	}
	.footer-right {
		display: flex;
		gap: 8px;
	}
</style>
