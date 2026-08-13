<script lang="ts">
	import { onMount, tick } from 'svelte';
	import { api, type SystemFileEntry, type SystemFileRoot } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Button, Card, ConfirmModal } from '$lib/components/ui';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { errorMessage } from '$lib/utils/errorMessage';
	import SystemTerminal from './SystemTerminal.svelte';
	import {
		ChevronDown,
		ChevronRight,
		Folder,
		FileText,
		RefreshCw,
		Save,
		Trash2,
		FolderPlus,
		Download,
		Upload,
		Copy,
		Terminal,
		FilePlus,
		ArrowUp,
	} from 'lucide-svelte';

	type TreeDir = {
		path: string;
		name: string;
		expanded: boolean;
		loading: boolean;
		children: TreeDir[];
	};

	type CtxMenu = {
		x: number;
		y: number;
		entry: SystemFileEntry | null;
	};

	let roots = $state<SystemFileRoot[]>([]);
	let currentPath = $state('');
	let entries = $state<SystemFileEntry[]>([]);
	let treeRoots = $state<TreeDir[]>([]);
	let loading = $state(false);
	let editorPath = $state<string | null>(null);
	let editorContent = $state('');
	let editorDirty = $state(false);
	let saving = $state(false);
	let selected = $state<SystemFileEntry | null>(null);
	let checked = $state<Set<string>>(new Set());
	let newDirName = $state('');
	let showMkdir = $state(false);
	let deleteTarget = $state<SystemFileEntry | null>(null);
	let deleting = $state(false);
	let showTerminal = $state(true);
	let uploadInput: HTMLInputElement | undefined = $state();
	let ctxMenu = $state<CtxMenu | null>(null);
	let ctxMenuEl = $state<HTMLDivElement | undefined>(undefined);
	let propsEntry = $state<SystemFileEntry | null>(null);
	let renameEntry = $state<SystemFileEntry | null>(null);
	let renameName = $state('');
	let copyTarget = $state('');
	let moveTarget = $state('');
	let chmodEntry = $state<SystemFileEntry | null>(null);
	let chmodMode = $state('');
	let checksumResult = $state('');
	let newFileName = $state('');
	let showNewFile = $state(false);

	const currentRoot = $derived(
		roots.find((r) => currentPath === r.path || currentPath.startsWith(r.path + '/')) ?? roots[0],
	);
	const readOnly = $derived(currentRoot?.readOnly ?? false);

	function parentPath(): string | null {
		if (!currentPath || currentPath === '/') return null;
		const p = currentPath.replace(/\/$/, '');
		const i = p.lastIndexOf('/');
		return i <= 0 ? '/' : p.slice(0, i);
	}

	onMount(() => {
		void loadRoots();
		document.addEventListener('pointerdown', onDocPointerDown, true);
		window.addEventListener('keydown', onKeyDown);
		return () => {
			document.removeEventListener('pointerdown', onDocPointerDown, true);
			window.removeEventListener('keydown', onKeyDown);
		};
	});

	function onDocPointerDown(ev: PointerEvent) {
		if (!ctxMenu) return;
		const t = ev.target as HTMLElement | null;
		if (t?.closest('.file-ctx-menu')) return;
		ctxMenu = null;
	}

	function onKeyDown(ev: KeyboardEvent) {
		if (ev.target instanceof HTMLInputElement || ev.target instanceof HTMLTextAreaElement) return;
		if (ev.key === 'F5') {
			ev.preventDefault();
			void loadDir(currentPath);
		}
		if (ev.key === 'F2' && selected) {
			ev.preventDefault();
			startRename(selected);
		}
		if (ev.key === 'F7') {
			ev.preventDefault();
			showMkdir = true;
		}
		if (ev.key === 'F8' && selected && selected.name !== '..') {
			ev.preventDefault();
			deleteTarget = selected;
		}
		if ((ev.ctrlKey || ev.metaKey) && ev.key.toLowerCase() === 'a') {
			ev.preventDefault();
			selectAll();
		}
		if (ev.key === 'Enter' && selected) {
			ev.preventDefault();
			void openEntry(selected);
		}
		if (ev.key === 'Backspace' && parentPath()) {
			ev.preventDefault();
			void loadDir(parentPath()!);
		}
	}

	async function loadRoots() {
		try {
			roots = await api.systemFilesRoots();
			treeRoots = roots.map((r) => ({
				path: r.path,
				name: r.label,
				expanded: false,
				loading: false,
				children: [],
			}));
			if (!currentPath && roots.length > 0) {
				currentPath = roots[0].path;
				await loadDir(currentPath);
			}
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось загрузить корни'));
		}
	}

	async function loadDir(path: string) {
		loading = true;
		selected = null;
		checked = new Set();
		try {
			const res = await api.systemFilesList(path);
			currentPath = res.path;
			entries = res.entries;
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось прочитать каталог'));
		} finally {
			loading = false;
		}
	}

	async function expandTree(node: TreeDir) {
		if (node.expanded) {
			node.expanded = false;
			return;
		}
		node.expanded = true;
		if (node.children.length > 0) {
			await loadDir(node.path);
			return;
		}
		node.loading = true;
		try {
			const res = await api.systemFilesList(node.path);
			node.children = res.entries
				.filter((e) => e.isDir && e.name !== '..')
				.map((e) => ({
					path: e.path,
					name: e.name,
					expanded: false,
					loading: false,
					children: [],
				}));
			await loadDir(node.path);
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось открыть каталог'));
		} finally {
			node.loading = false;
		}
	}

	async function openEntry(entry: SystemFileEntry) {
		selected = entry;
		if (entry.isDir) {
			await loadDir(entry.path);
			return;
		}
		try {
			const res = await api.systemFilesRead(entry.path);
			editorPath = res.path;
			editorContent = res.content;
			editorDirty = false;
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось открыть файл'));
		}
	}

	function toggleCheck(path: string, ev?: Event) {
		ev?.stopPropagation();
		const next = new Set(checked);
		if (next.has(path)) next.delete(path);
		else next.add(path);
		checked = next;
	}

	function selectAll() {
		checked = new Set(entries.filter((e) => e.name !== '..').map((e) => e.path));
	}

	function invertSelection() {
		const next = new Set<string>();
		for (const e of entries) {
			if (e.name === '..') continue;
			if (!checked.has(e.path)) next.add(e.path);
		}
		checked = next;
	}

	function selectedEntries(): SystemFileEntry[] {
		if (checked.size === 0 && selected) return [selected];
		return entries.filter((e) => checked.has(e.path));
	}

	async function showContextMenu(ev: MouseEvent, entry: SystemFileEntry | null) {
		ev.preventDefault();
		ev.stopPropagation();
		if (entry) selected = entry;
		const ax = ev.clientX;
		const ay = ev.clientY;
		// Off-screen first so we can measure real height before placing.
		ctxMenu = { x: -10000, y: -10000, entry };
		await tick();
		await new Promise<void>((resolve) => {
			requestAnimationFrame(() => requestAnimationFrame(() => resolve()));
		});
		if (!ctxMenuEl) {
			ctxMenu = { x: ax, y: ay, entry };
			return;
		}
		const pad = 8;
		const rect = ctxMenuEl.getBoundingClientRect();
		const vw = window.innerWidth;
		const vh = window.innerHeight;
		let x = ax;
		let y = ay;
		if (x + rect.width > vw - pad) x = vw - rect.width - pad;
		if (y + rect.height > vh - pad) y = ay - rect.height;
		if (x < pad) x = pad;
		if (y < pad) y = pad;
		if (y + rect.height > vh - pad) y = vh - rect.height - pad;
		ctxMenu = { x, y, entry };
	}

	async function saveFile() {
		if (!editorPath) return;
		saving = true;
		try {
			await api.systemFilesWrite(editorPath, editorContent);
			editorDirty = false;
			notifications.success('Файл сохранён');
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось сохранить'));
		} finally {
			saving = false;
		}
	}

	async function createDir() {
		const name = newDirName.trim();
		if (!name) return;
		const path = `${currentPath.replace(/\/$/, '')}/${name}`;
		try {
			await api.systemFilesMkdir(path);
			showMkdir = false;
			newDirName = '';
			await loadDir(currentPath);
			notifications.success('Каталог создан');
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось создать каталог'));
		}
	}

	async function createFile() {
		const name = newFileName.trim();
		if (!name) return;
		const path = `${currentPath.replace(/\/$/, '')}/${name}`;
		try {
			await api.systemFilesWrite(path, '');
			showNewFile = false;
			newFileName = '';
			await loadDir(currentPath);
			notifications.success('Файл создан');
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось создать файл'));
		}
	}

	async function confirmDelete() {
		if (!deleteTarget) return;
		deleting = true;
		try {
			await api.systemFilesRemove(deleteTarget.path);
			if (editorPath === deleteTarget.path) editorPath = null;
			if (selected?.path === deleteTarget.path) selected = null;
			deleteTarget = null;
			await loadDir(currentPath);
			notifications.success('Удалено');
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось удалить'));
		} finally {
			deleting = false;
		}
	}

	async function copyPath(path?: string) {
		const p = path ?? selected?.path ?? currentPath;
		if (!p) return;
		const ok = await copyToClipboard(p);
		if (ok) notifications.success('Путь скопирован');
		else notifications.error('Не удалось скопировать');
	}

	async function terminalInFolder(path?: string) {
		const p = path ?? (selected?.isDir ? selected.path : currentPath);
		const cmd = `cd ${JSON.stringify(p)}`;
		const ok = await copyToClipboard(cmd);
		if (ok) notifications.success('Команда cd скопирована — вставьте в терминал');
		else notifications.error('Не удалось скопировать');
		showTerminal = true;
	}

	function downloadEntry(entry?: SystemFileEntry | null) {
		const e = entry ?? selected;
		if (!e || e.isDir) {
			notifications.error('Выберите файл для скачивания');
			return;
		}
		window.open(api.systemFilesDownloadUrl(e.path), '_blank');
	}

	function triggerUpload() {
		uploadInput?.click();
	}

	async function onUpload(ev: Event) {
		const input = ev.target as HTMLInputElement;
		const file = input.files?.[0];
		input.value = '';
		if (!file || readOnly) return;
		try {
			const res = await api.systemFilesUpload(currentPath, file);
			notifications.success(`Загружено: ${res.path}`);
			await loadDir(currentPath);
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось загрузить'));
		}
	}

	function startRename(entry: SystemFileEntry) {
		renameEntry = entry;
		renameName = entry.name;
		ctxMenu = null;
	}

	async function confirmRename() {
		if (!renameEntry) return;
		const name = renameName.trim();
		if (!name || name === renameEntry.name) {
			renameEntry = null;
			return;
		}
		const base = renameEntry.path.replace(/\/[^/]+$/, '');
		const to = `${base}/${name}`;
		try {
			await api.systemFilesRename(renameEntry.path, to);
			renameEntry = null;
			await loadDir(currentPath);
			notifications.success('Переименовано');
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось переименовать'));
		}
	}

	async function confirmCopy() {
		const from = copyTarget.trim();
		if (!from) return;
		const sel = selectedEntries()[0];
		if (!sel) return;
		try {
			await api.systemFilesCopy(sel.path, from);
			copyTarget = '';
			await loadDir(currentPath);
			notifications.success('Скопировано');
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось скопировать'));
		}
	}

	async function confirmMove() {
		const to = moveTarget.trim();
		if (!to) return;
		const sel = selectedEntries()[0];
		if (!sel) return;
		try {
			await api.systemFilesRename(sel.path, to);
			moveTarget = '';
			await loadDir(currentPath);
			notifications.success('Перемещено');
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось переместить'));
		}
	}

	async function confirmChmod() {
		if (!chmodEntry) return;
		try {
			await api.systemFilesChmod(chmodEntry.path, chmodMode.trim());
			chmodEntry = null;
			await loadDir(currentPath);
			notifications.success('Права изменены');
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось изменить права'));
		}
	}

	async function runChecksum(entry: SystemFileEntry, algo: 'md5' | 'sha256') {
		try {
			const res = await api.systemFilesChecksum(entry.path, algo);
			checksumResult = `${algo.toUpperCase()}: ${res.checksum}\n${res.path}`;
			propsEntry = entry;
			ctxMenu = null;
		} catch (e) {
			notifications.error(errorMessage(e, 'Checksum'));
		}
	}

	function formatSize(n: number): string {
		if (n < 1024) return `${n} B`;
		if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
		return `${(n / (1024 * 1024)).toFixed(1)} MB`;
	}

	function formatTime(iso: string): string {
		if (!iso) return '—';
		const d = new Date(iso);
		return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
	}

	function ctxTarget(): SystemFileEntry | null {
		return ctxMenu?.entry ?? selected;
	}

	function canMutate(entry?: SystemFileEntry | null): boolean {
		const e = entry ?? ctxTarget();
		return !readOnly && !!e && e.name !== '..';
	}
</script>

<input bind:this={uploadInput} type="file" class="hidden-upload" onchange={onUpload} />

<div class="file-manager">
	<div class="toolbar">
		<div class="toolbar-left">
			<Button variant="ghost" onclick={() => loadDir(currentPath)} disabled={loading}>
				{#snippet iconBefore()}<RefreshCw size={14} />{/snippet}
				Обновить
			</Button>
			{#if parentPath()}
				<Button variant="ghost" onclick={() => loadDir(parentPath()!)}>
					{#snippet iconBefore()}<ArrowUp size={14} />{/snippet}
					Вверх
				</Button>
			{/if}
			{#if !readOnly}
				<Button variant="secondary" onclick={() => (showMkdir = true)}>
					{#snippet iconBefore()}<FolderPlus size={14} />{/snippet}
					Каталог
				</Button>
				<Button variant="secondary" onclick={() => (showNewFile = true)}>
					{#snippet iconBefore()}<FilePlus size={14} />{/snippet}
					Файл
				</Button>
				<Button variant="secondary" onclick={triggerUpload}>
					{#snippet iconBefore()}<Upload size={14} />{/snippet}
					Загрузить
				</Button>
			{/if}
			<Button variant="ghost" onclick={() => copyPath()}>
				{#snippet iconBefore()}<Copy size={14} />{/snippet}
				Копировать путь
			</Button>
			<Button variant="ghost" onclick={() => downloadEntry()} disabled={!selected || selected.isDir}>
				{#snippet iconBefore()}<Download size={14} />{/snippet}
				Скачать
			</Button>
			<Button variant="ghost" onclick={() => (showTerminal = !showTerminal)}>
				{#snippet iconBefore()}<Terminal size={14} />{/snippet}
				{showTerminal ? 'Скрыть терминал' : 'Терминал'}
			</Button>
		</div>
		<code class="path">{currentPath}</code>
	</div>

	<div class="body">
		<aside class="tree-panel">
			<div class="tree-title">Папки</div>
			<ul class="tree">
				{#each treeRoots as node (node.path)}
					<li>
						<button type="button" class="tree-row" onclick={() => expandTree(node)}>
							{#if node.loading}
								<RefreshCw size={14} class="spin" />
							{:else if node.expanded}
								<ChevronDown size={14} />
							{:else}
								<ChevronRight size={14} />
							{/if}
							<Folder size={14} />
							<span>{node.name}</span>
						</button>
						{#if node.expanded && node.children.length > 0}
							<ul class="tree nested">
								{#each node.children as child (child.path)}
									<li>
										<button type="button" class="tree-row" onclick={() => expandTree(child)}>
											{#if child.expanded}<ChevronDown size={14} />{:else}<ChevronRight size={14} />{/if}
											<Folder size={14} />
											<span>{child.name}</span>
										</button>
									</li>
								{/each}
							</ul>
						{/if}
					</li>
				{/each}
			</ul>
		</aside>

		<div class="main-panel">
			<Card padding="sm">
				{#if loading}
					<p class="muted">Загрузка…</p>
				{:else}
					<div
						class="file-grid-wrap"
						oncontextmenu={(e) => showContextMenu(e, null)}
						role="listbox"
						tabindex="0"
					>
						<div class="file-grid head">
							<span class="col-check"></span>
							<span class="col-name">Имя</span>
							<span class="col-size">Размер</span>
							<span class="col-time">Изменён</span>
							<span class="col-mode">Права</span>
						</div>
						{#each entries as entry (entry.path)}
							<div
								class="file-grid row"
								class:selected={selected?.path === entry.path}
								class:checked={checked.has(entry.path)}
								oncontextmenu={(e) => showContextMenu(e, entry)}
								onclick={() => (selected = entry)}
								ondblclick={() => openEntry(entry)}
								role="option"
								aria-selected={selected?.path === entry.path}
								tabindex="-1"
							>
								<span class="col-check">
									{#if entry.name !== '..'}
										<input
											type="checkbox"
											checked={checked.has(entry.path)}
											onclick={(e) => toggleCheck(entry.path, e)}
										/>
									{/if}
								</span>
								<span class="col-name">
									<span class="entry-link">
										{#if entry.isDir}<Folder size={14} />{:else}<FileText size={14} />{/if}
										{entry.name}
									</span>
								</span>
								<span class="col-size">{entry.isDir ? '—' : formatSize(entry.size)}</span>
								<span class="col-time">{formatTime(entry.modTime)}</span>
								<span class="col-mode">{entry.mode || '—'}</span>
							</div>
						{/each}
					</div>
				{/if}
			</Card>

			{#if editorPath}
				<Card padding="sm">
					<div class="editor-head">
						<strong>{editorPath}</strong>
						<div class="editor-actions">
							{#if !readOnly}
								<Button variant="primary" loading={saving} disabled={!editorDirty} onclick={saveFile}>
									{#snippet iconBefore()}<Save size={14} />{/snippet}
									Сохранить
								</Button>
							{/if}
							<Button variant="ghost" onclick={() => (editorPath = null)}>Закрыть</Button>
						</div>
					</div>
					<textarea class="editor" bind:value={editorContent} readonly={readOnly} oninput={() => (editorDirty = true)}></textarea>
				</Card>
			{/if}
		</div>
	</div>

	{#if showTerminal}
		<Card padding="sm">
			<div class="term-head"><Terminal size={16} /> <span>Терминал</span></div>
			<SystemTerminal compact />
		</Card>
	{/if}

	{#if ctxMenu}
		<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
		<div
			bind:this={ctxMenuEl}
			class="file-ctx-menu"
			style="left:{ctxMenu.x}px;top:{ctxMenu.y}px"
			role="menu"
			oncontextmenu={(e) => e.preventDefault()}
		>
			<button type="button" role="menuitem" onclick={() => { const e = ctxTarget(); if (e) void openEntry(e); ctxMenu = null; }}>Открыть <kbd>Enter</kbd></button>
			<button type="button" role="menuitem" onclick={() => { void terminalInFolder(ctxTarget()?.isDir ? ctxTarget()?.path : currentPath); ctxMenu = null; }}>Терминал в папке</button>
			{#if ctxTarget() && !ctxTarget()?.isDir}
				<button type="button" role="menuitem" onclick={() => { downloadEntry(ctxTarget()); ctxMenu = null; }}>Скачать</button>
			{/if}
			<hr />
			<button type="button" role="menuitem" onclick={() => { void copyPath(ctxTarget()?.path); ctxMenu = null; }}>Копировать полный путь <kbd>Ctrl+Shift+C</kbd></button>
			{#if canMutate()}
				<button type="button" role="menuitem" onclick={() => { copyTarget = `${currentPath.replace(/\/$/, '')}/копия-${ctxTarget()?.name}`; ctxMenu = null; }}>Копировать… <kbd>F5</kbd></button>
				<button type="button" role="menuitem" onclick={() => { moveTarget = `${currentPath.replace(/\/$/, '')}/${ctxTarget()?.name}`; ctxMenu = null; }}>Переместить… <kbd>F6</kbd></button>
				<button type="button" role="menuitem" onclick={() => { if (ctxTarget()) startRename(ctxTarget()!); }}>Переименовать… <kbd>F2</kbd></button>
			{/if}
			<button type="button" role="menuitem" onclick={() => { propsEntry = ctxTarget(); ctxMenu = null; }}>Свойства…</button>
			{#if ctxTarget() && !ctxTarget()?.isDir}
				<button type="button" role="menuitem" onclick={() => { if (ctxTarget()) void runChecksum(ctxTarget()!, 'md5'); }}>Checksum MD5…</button>
				<button type="button" role="menuitem" onclick={() => { if (ctxTarget()) void runChecksum(ctxTarget()!, 'sha256'); }}>Checksum SHA256…</button>
			{/if}
			{#if canMutate()}
				<button type="button" role="menuitem" onclick={() => { chmodEntry = ctxTarget(); chmodMode = '644'; ctxMenu = null; }}>Права (chmod)…</button>
				<button type="button" role="menuitem" class="danger" onclick={() => { deleteTarget = ctxTarget(); ctxMenu = null; }}>В корзину <kbd>F8</kbd></button>
			{/if}
			<hr />
			{#if !readOnly}
				<button type="button" role="menuitem" onclick={() => { showMkdir = true; ctxMenu = null; }}>Создать папку <kbd>F7</kbd></button>
				<button type="button" role="menuitem" onclick={() => { showNewFile = true; ctxMenu = null; }}>Создать файл <kbd>Shift+F7</kbd></button>
				<button type="button" role="menuitem" onclick={() => { triggerUpload(); ctxMenu = null; }}>Загрузить…</button>
			{/if}
			<button type="button" role="menuitem" onclick={() => { if (parentPath()) void loadDir(parentPath()!); ctxMenu = null; }}>Вверх <kbd>Backspace</kbd></button>
			<button type="button" role="menuitem" onclick={() => { void loadDir(currentPath); ctxMenu = null; }}>Обновить</button>
			<hr />
			<button type="button" role="menuitem" onclick={() => { selectAll(); ctxMenu = null; }}>Выделить всё <kbd>Ctrl+A</kbd></button>
			<button type="button" role="menuitem" onclick={() => { invertSelection(); ctxMenu = null; }}>Инвертировать выделение <kbd>Ctrl+I</kbd></button>
		</div>
	{/if}
</div>

{#if showMkdir}
	<Card padding="sm">
		<p class="modal-title">Новый каталог в {currentPath}</p>
		<div class="modal-row">
			<input class="modal-input" bind:value={newDirName} placeholder="имя каталога" />
			<Button variant="primary" onclick={createDir}>Создать</Button>
			<Button variant="ghost" onclick={() => (showMkdir = false)}>Отмена</Button>
		</div>
	</Card>
{/if}

{#if showNewFile}
	<Card padding="sm">
		<p class="modal-title">Новый файл в {currentPath}</p>
		<div class="modal-row">
			<input class="modal-input" bind:value={newFileName} placeholder="имя файла" />
			<Button variant="primary" onclick={createFile}>Создать</Button>
			<Button variant="ghost" onclick={() => (showNewFile = false)}>Отмена</Button>
		</div>
	</Card>
{/if}

{#if renameEntry}
	<Card padding="sm">
		<p class="modal-title">Переименовать {renameEntry.path}</p>
		<div class="modal-row">
			<input class="modal-input" bind:value={renameName} />
			<Button variant="primary" onclick={confirmRename}>OK</Button>
			<Button variant="ghost" onclick={() => (renameEntry = null)}>Отмена</Button>
		</div>
	</Card>
{/if}

{#if copyTarget}
	<Card padding="sm">
		<p class="modal-title">Копировать в</p>
		<div class="modal-row">
			<input class="modal-input" bind:value={copyTarget} />
			<Button variant="primary" onclick={confirmCopy}>Копировать</Button>
			<Button variant="ghost" onclick={() => (copyTarget = '')}>Отмена</Button>
		</div>
	</Card>
{/if}

{#if moveTarget}
	<Card padding="sm">
		<p class="modal-title">Переместить в</p>
		<div class="modal-row">
			<input class="modal-input" bind:value={moveTarget} />
			<Button variant="primary" onclick={confirmMove}>Переместить</Button>
			<Button variant="ghost" onclick={() => (moveTarget = '')}>Отмена</Button>
		</div>
	</Card>
{/if}

{#if chmodEntry}
	<Card padding="sm">
		<p class="modal-title">chmod {chmodEntry.path}</p>
		<div class="modal-row">
			<input class="modal-input" bind:value={chmodMode} placeholder="644" />
			<Button variant="primary" onclick={confirmChmod}>Применить</Button>
			<Button variant="ghost" onclick={() => (chmodEntry = null)}>Отмена</Button>
		</div>
	</Card>
{/if}

{#if propsEntry}
	<Card padding="sm">
		<p class="modal-title">Свойства</p>
		<dl class="props">
			<dt>Путь</dt><dd>{propsEntry.path}</dd>
			<dt>Тип</dt><dd>{propsEntry.isDir ? 'каталог' : 'файл'}</dd>
			<dt>Размер</dt><dd>{propsEntry.isDir ? '—' : formatSize(propsEntry.size)}</dd>
			<dt>Права</dt><dd>{propsEntry.mode}</dd>
			<dt>Изменён</dt><dd>{formatTime(propsEntry.modTime)}</dd>
			{#if checksumResult}
				<dt>Checksum</dt><dd class="mono">{checksumResult}</dd>
			{/if}
		</dl>
		<Button variant="ghost" onclick={() => { propsEntry = null; checksumResult = ''; }}>Закрыть</Button>
	</Card>
{/if}

{#if deleteTarget}
	<ConfirmModal
		open={!!deleteTarget}
		title="Удалить?"
		message={deleteTarget.path}
		confirmLabel="Удалить"
		variant="danger"
		busy={deleting}
		onClose={() => (deleteTarget = null)}
		onConfirm={confirmDelete}
	/>
{/if}

<style>
	.hidden-upload { display: none; }
	.file-manager { display: flex; flex-direction: column; gap: 0.75rem; }
	.toolbar { display: flex; flex-wrap: wrap; gap: 0.5rem; justify-content: space-between; align-items: center; }
	.toolbar-left { display: flex; flex-wrap: wrap; gap: 0.35rem; align-items: center; }
	.path { font-size: 0.8rem; word-break: break-all; max-width: 100%; }
	.body { display: grid; grid-template-columns: 220px minmax(0, 1fr); gap: 0.75rem; min-height: 320px; }
	@media (max-width: 900px) { .body { grid-template-columns: 1fr; } .tree-panel { max-height: 180px; overflow: auto; } }
	.tree-panel { border: 1px solid var(--border-subtle, #333); border-radius: 8px; padding: 0.5rem; background: var(--bg-secondary, rgba(255,255,255,0.02)); }
	.tree-title { font-size: 0.75rem; font-weight: 600; opacity: 0.7; margin-bottom: 0.35rem; text-transform: uppercase; letter-spacing: 0.04em; }
	.tree, .tree.nested { list-style: none; margin: 0; padding: 0; }
	.tree.nested { padding-left: 1rem; }
	.tree-row { display: flex; align-items: center; gap: 0.25rem; width: 100%; text-align: left; background: none; border: none; color: inherit; padding: 0.2rem 0.25rem; border-radius: 4px; cursor: pointer; font-size: 0.85rem; }
	.tree-row:hover { background: var(--bg-hover, rgba(255,255,255,0.05)); }
	.main-panel { display: flex; flex-direction: column; gap: 0.75rem; min-width: 0; }
	.file-grid-wrap { overflow: auto; max-height: 420px; font-size: 0.88rem; }
	.file-grid {
		display: grid;
		grid-template-columns: 2rem minmax(0, 1fr) 5.5rem 9rem 4.5rem;
		gap: 0 0.5rem;
		align-items: center;
		padding: 0.2rem 0.25rem;
		border-bottom: 1px solid var(--border-subtle, #333);
	}
	.file-grid.head { font-weight: 600; opacity: 0.85; position: sticky; top: 0; background: var(--bg-primary, #1a1a1a); z-index: 1; }
	.file-grid.row { cursor: pointer; }
	.file-grid.row:hover { background: var(--bg-hover, rgba(255,255,255,0.04)); }
	.file-grid.row.selected { background: var(--accent-muted, rgba(99,102,241,0.12)); }
	.col-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
	.col-size, .col-time, .col-mode { font-size: 0.78rem; opacity: 0.9; white-space: nowrap; }
	.entry-link { display: inline-flex; align-items: center; gap: 0.35rem; max-width: 100%; overflow: hidden; text-overflow: ellipsis; }
	.editor-head { display: flex; justify-content: space-between; gap: 0.75rem; align-items: center; margin-bottom: 0.5rem; flex-wrap: wrap; }
	.editor-actions { display: flex; gap: 0.35rem; }
	.editor { width: 100%; min-height: 220px; font-family: ui-monospace, monospace; font-size: 0.85rem; resize: vertical; }
	.modal-row { display: flex; flex-wrap: wrap; gap: 0.35rem; align-items: center; }
	.modal-input { flex: 1; min-width: 160px; padding: 0.4rem 0.5rem; }
	.modal-title { margin: 0 0 0.5rem; }
	.term-head { display: flex; align-items: center; gap: 0.35rem; margin-bottom: 0.5rem; font-weight: 600; }
	.muted { opacity: 0.7; }
	.props { display: grid; grid-template-columns: auto 1fr; gap: 0.25rem 0.75rem; margin: 0 0 0.75rem; font-size: 0.85rem; }
	.props dd { margin: 0; word-break: break-all; }
	.mono { font-family: ui-monospace, monospace; white-space: pre-wrap; }
	:global(.spin) { animation: spin 1s linear infinite; }
	@keyframes spin { to { transform: rotate(360deg); } }

	.file-ctx-menu {
		position: fixed;
		z-index: var(--z-floating);
		min-width: 240px;
		max-width: 320px;
		max-height: min(420px, calc(100dvh - 16px));
		overflow-x: hidden;
		overflow-y: auto;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		box-shadow: var(--shadow);
		padding: 0.25rem 0;
		display: flex;
		flex-direction: column;
	}

	.file-ctx-menu button {
		background: transparent;
		border: none;
		text-align: left;
		padding: 0.45rem 0.85rem;
		font: inherit;
		font-size: 0.85rem;
		color: var(--color-text-primary);
		cursor: pointer;
		width: 100%;
		display: flex;
		justify-content: space-between;
		align-items: center;
	}

	.file-ctx-menu button:hover,
	.file-ctx-menu button:focus-visible {
		background: var(--color-bg-hover);
		outline: none;
	}

	.file-ctx-menu button.danger {
		color: var(--color-error);
	}

	.file-ctx-menu button.danger:hover {
		background: color-mix(in srgb, var(--color-error) 12%, transparent);
	}

	.file-ctx-menu hr {
		border: none;
		border-top: 1px solid var(--color-border);
		margin: 0.25rem 0;
	}

	.file-ctx-menu button kbd {
		font-size: 0.7rem;
		color: var(--color-text-muted);
		margin-left: 1rem;
		font-family: inherit;
	}
</style>
