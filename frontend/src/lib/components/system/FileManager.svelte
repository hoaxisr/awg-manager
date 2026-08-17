<script lang="ts">
	import { onMount, tick } from 'svelte';
	import { api, type SystemFileEntry, type SystemFileRoot, type FileSystemScriptStatus } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Button, Card } from '$lib/components/ui';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { errorMessage } from '$lib/utils/errorMessage';
	import SystemTerminal from './SystemTerminal.svelte';
	import FilePropsModal from './files/FilePropsModal.svelte';
	import FileEditorModal from './files/FileEditorModal.svelte';
	import FileActionModals from './files/FileActionModals.svelte';
	import { getFileTypeInfo } from './files/fileIcons';
	import {
		ChevronDown,
		ChevronRight,
		Folder,
		FileText,
		RefreshCw,
		FolderPlus,
		Download,
		Upload,
		Copy,
		Terminal,
		FilePlus,
		ArrowUp,
		Eye,
		Edit2,
		Trash2,
		Play,
		RotateCw,
		Square,
		Search,
		Check,
		ExternalLink,
		Move,
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

	// State
	let roots = $state<SystemFileRoot[]>([]);
	let currentPath = $state('/opt');
	let entries = $state<SystemFileEntry[]>([]);
	let treeRoots = $state<TreeDir[]>([]);
	let loading = $state(false);
	let selected = $state<SystemFileEntry | null>(null);
	let checked = $state<Set<string>>(new Set());
	let searchQuery = $state('');

	// Script statuses map (path -> status)
	let scriptStatuses = $state<Record<string, FileSystemScriptStatus>>({});
	let runningActionPath = $state<string | null>(null);

	// Terminal
	let showTerminal = $state(false);
	let uploadInput: HTMLInputElement | undefined = $state();

	// Modals state
	let propsModalEntry = $state<SystemFileEntry | null>(null);
	let editorModalPath = $state<string | null>(null);
	let editorModalContent = $state('');
	let showMkdir = $state(false);
	let showNewFile = $state(false);
	let renameEntry = $state<SystemFileEntry | null>(null);
	let copyTarget = $state('');
	let moveTarget = $state('');
	let deleteTarget = $state<SystemFileEntry | null>(null);

	// Context Menu
	let ctxMenu = $state<CtxMenu | null>(null);
	let ctxMenuEl = $state<HTMLDivElement | undefined>(undefined);

	const currentRoot = $derived(
		roots.find((r) => currentPath === r.path || currentPath.startsWith(r.path + '/')) ?? roots[0],
	);
	const readOnly = $derived(currentRoot?.readOnly ?? false);

	const filteredEntries = $derived.by(() => {
		const q = searchQuery.trim().toLowerCase();
		if (!q) return entries;
		return entries.filter((e) => e.name.toLowerCase().includes(q));
	});

	const breadcrumbParts = $derived.by(() => {
		if (!currentPath || currentPath === '/') return [{ name: '/', path: '/' }];
		const parts = currentPath.split('/').filter(Boolean);
		const res: { name: string; path: string }[] = [{ name: '/', path: '/' }];
		let acc = '';
		for (const p of parts) {
			acc += '/' + p;
			res.push({ name: p, path: acc });
		}
		return res;
	});

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
		if (propsModalEntry || editorModalPath || showMkdir || showNewFile || renameEntry || copyTarget || moveTarget || deleteTarget) return;

		if (ev.key === 'F3' && selected && !selected.isDir) {
			ev.preventDefault();
			propsModalEntry = selected;
		}
		if (ev.key === 'F4' && selected && !selected.isDir) {
			ev.preventDefault();
			void openEditor(selected);
		}
		if (ev.key === 'F5') {
			ev.preventDefault();
			void loadDir(currentPath);
		}
		if (ev.key === 'F2' && selected && selected.name !== '..') {
			ev.preventDefault();
			renameEntry = selected;
		}
		if (ev.key === 'F7') {
			ev.preventDefault();
			showMkdir = true;
		}
		if (ev.key === 'F8' && selected && selected.name !== '..') {
			ev.preventDefault();
			deleteTarget = selected;
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
			if (roots.length > 0) {
				currentPath = roots[0].path;
				await loadDir(currentPath);
			}
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось загрузить корневые папки'));
		}
	}

	async function loadDir(path: string) {
		loading = true;
		selected = null;
		checked = new Set();
		searchQuery = '';
		scriptStatuses = {};
		try {
			const res = await api.systemFilesList(path);
			currentPath = res.path;
			entries = res.entries;
			void fetchScriptStatuses(res.entries);
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось прочитать каталог'));
		} finally {
			loading = false;
		}
	}

	async function fetchScriptStatuses(items: SystemFileEntry[]) {
		const scriptItems = items.filter(
			(e) => !e.isDir && (getFileTypeInfo(e.name, e.isDir).kind === 'script' || e.mode.includes('x')),
		);
		for (const item of scriptItems) {
			try {
				const st = await api.systemFilesScriptStatus(item.path);
				if (st.isScript) {
					scriptStatuses = { ...scriptStatuses, [item.path]: st };
				}
			} catch {
				// ignore
			}
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
		if (entry.isDir) {
			await loadDir(entry.path);
			return;
		}
		await openEditor(entry);
	}

	async function openEditor(entry: SystemFileEntry) {
		try {
			const res = await api.systemFilesRead(entry.path);
			editorModalPath = res.path;
			editorModalContent = res.content;
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось открыть файл'));
		}
	}

	async function executeScriptAction(entry: SystemFileEntry, action: 'start' | 'stop' | 'restart' | 'run') {
		runningActionPath = entry.path;
		try {
			const res = await api.systemFilesScriptAction({ path: entry.path, action });
			if (res.ok) {
				const actionName = action === 'start' || action === 'run' ? 'запущен' : action === 'restart' ? 'перезапущен' : 'остановлен';
				notifications.success(`Скрипт «${entry.name}» ${actionName}`);
			} else {
				notifications.error(res.error || 'Ошибка выполнения');
			}
			const st = await api.systemFilesScriptStatus(entry.path);
			scriptStatuses = { ...scriptStatuses, [entry.path]: st };
		} catch (e) {
			notifications.error(errorMessage(e, 'Ошибка выполнения'));
		} finally {
			runningActionPath = null;
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

	async function showContextMenu(ev: MouseEvent, entry: SystemFileEntry | null) {
		ev.preventDefault();
		ev.stopPropagation();
		if (entry) {
			selected = entry;
		}
		const ax = ev.clientX;
		const ay = ev.clientY;
		ctxMenu = { x: -10000, y: -10000, entry };
		await tick();
		await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
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
		ctxMenu = { x, y, entry };
	}

	async function copyPath(path?: string) {
		const p = path ?? selected?.path ?? currentPath;
		if (!p) return;
		const ok = await copyToClipboard(p);
		if (ok) notifications.success('Путь скопирован');
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
			notifications.error(errorMessage(e, 'Не удалось загрузить файл'));
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
		return Number.isNaN(d.getTime()) ? iso : d.toLocaleDateString() + ' ' + d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
	}

	function parentPath(p: string): string | null {
		if (!p || p === '/') return null;
		const clean = p.replace(/\/$/, '');
		const i = clean.lastIndexOf('/');
		return i <= 0 ? '/' : clean.slice(0, i);
	}
</script>

<input bind:this={uploadInput} type="file" class="hidden-upload" onchange={onUpload} />

<div class="file-manager-root">
	<!-- Top Controls Toolbar -->
	<Card padding="sm">
		<div class="fm-toolbar">
			<!-- Action buttons -->
			<div class="fm-toolbar-actions">
				<Button size="sm" variant="ghost" onclick={() => loadDir(currentPath)} disabled={loading}>
					{#snippet iconBefore()}<RefreshCw size={14} class={loading ? 'spin' : ''} />{/snippet}
					Обновить
				</Button>

				{#if !readOnly}
					<Button size="sm" variant="secondary" onclick={() => (showMkdir = true)}>
						{#snippet iconBefore()}<FolderPlus size={14} />{/snippet}
						Папка
					</Button>
					<Button size="sm" variant="secondary" onclick={() => (showNewFile = true)}>
						{#snippet iconBefore()}<FilePlus size={14} />{/snippet}
						Файл
					</Button>
					<Button size="sm" variant="secondary" onclick={() => uploadInput?.click()}>
						{#snippet iconBefore()}<Upload size={14} />{/snippet}
						Загрузить
					</Button>
				{/if}

				{#if selected && !selected.isDir}
					<Button size="sm" variant="secondary" onclick={() => window.open(api.systemFilesDownloadUrl(selected?.path ?? ''), '_blank')}>
						{#snippet iconBefore()}<Download size={14} />{/snippet}
						Скачать
					</Button>
				{/if}

				<Button size="sm" variant={showTerminal ? 'secondary' : 'ghost'} onclick={() => (showTerminal = !showTerminal)}>
					{#snippet iconBefore()}<Terminal size={14} />{/snippet}
					Терминал
				</Button>
			</div>

			<!-- Search box -->
			<div class="search-box">
				<Search size={13} class="search-icon" />
				<input
					type="text"
					placeholder="Поиск в папке…"
					bind:value={searchQuery}
				/>
			</div>
		</div>

		<!-- Breadcrumbs Navigation -->
		<div class="nav-bar">
			<div class="breadcrumbs">
				{#if parentPath(currentPath)}
					<button type="button" class="btn-up" onclick={() => loadDir(parentPath(currentPath)!)} title="Наверх">
						<ArrowUp size={13} />
					</button>
				{/if}
				{#each breadcrumbParts as part, i}
					{#if i > 0}<span class="bc-sep">/</span>{/if}
					<button
						type="button"
						class="bc-item"
						class:active={i === breadcrumbParts.length - 1}
						onclick={() => loadDir(part.path)}
					>
						{part.name}
					</button>
				{/each}
			</div>
		</div>
	</Card>

	<!-- Main Explorer View -->
	<Card padding="sm">
		<div class="modern-layout">
			<!-- Left folders tree -->
			<aside class="side-tree">
				<div class="tree-head">Структура /opt</div>
				<ul class="tree-root">
					{#each treeRoots as node (node.path)}
						<li>
							<button type="button" class="tree-item" class:active={currentPath === node.path} onclick={() => expandTree(node)}>
								{#if node.loading}
									<RefreshCw size={13} class="spin" />
								{:else if node.expanded}
									<ChevronDown size={13} />
								{:else}
									<ChevronRight size={13} />
								{/if}
								<Folder size={13} class="tree-folder-icon" />
								<span>{node.name}</span>
							</button>
							{#if node.expanded && node.children.length > 0}
								<ul class="tree-nested">
									{#each node.children as child (child.path)}
										<li>
											<button
												type="button"
												class="tree-item"
												class:active={currentPath === child.path}
												onclick={() => loadDir(child.path)}
											>
												<Folder size={13} class="tree-folder-icon" />
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

			<!-- Main file table -->
			<div class="files-table-container">
				{#if loading}
					<div class="loading-state">Загрузка каталога…</div>
				{:else if filteredEntries.length === 0}
					<div class="empty-state">Каталог пуст или файлы не найдены</div>
				{:else}
					<div class="table-wrap">
						<table class="fm-table" oncontextmenu={(e) => showContextMenu(e, null)}>
							<thead>
								<tr>
									<th style="width: 32px;"><span class="sr-only">Выбор</span></th>
									<th>Имя</th>
									<th style="width: 100px;">Размер</th>
									<th style="width: 150px;">Изменён</th>
									<th style="width: 90px;">Права</th>
									<th style="width: 145px; text-align: right;">Действия</th>
								</tr>
							</thead>
							<tbody>
								{#each filteredEntries as entry (entry.path)}
									{@const info = getFileTypeInfo(entry.name, entry.isDir)}
									{@const scriptSt = scriptStatuses[entry.path]}
									{@const isScript = !entry.isDir && (info.kind === 'script' || entry.mode.includes('x') || scriptSt?.isScript)}
									{@const isRunning = scriptSt?.running ?? false}
									{@const isBusy = runningActionPath === entry.path}

									<tr
										class:selected={selected?.path === entry.path}
										class:checked={checked.has(entry.path)}
										onclick={() => (selected = entry)}
										ondblclick={() => openEntry(entry)}
										oncontextmenu={(e) => showContextMenu(e, entry)}
									>
										<td class="col-check">
											{#if entry.name !== '..'}
												<input
													type="checkbox"
													checked={checked.has(entry.path)}
													onclick={(e) => toggleCheck(entry.path, e)}
												/>
											{/if}
										</td>
										<td class="col-name">
											<div class="name-wrap">
												{#if entry.isDir}
													<Folder size={16} class="icon-folder" />
												{:else}
													<FileText size={16} style="color: {info.color};" />
												{/if}
												<span class="entry-name" class:is-dir={entry.isDir}>{entry.name}</span>
												{#if info.badge}
													<span class="file-badge" class:badge-script={info.kind === 'script'}>{info.badge}</span>
												{/if}
												{#if isScript && isRunning}
													<span class="running-pill" title={scriptSt?.pids?.length ? `PID: ${scriptSt.pids.join(', ')}` : 'Запущен'}>
														<span class="dot"></span>
														<span>Запущен</span>
													</span>
												{/if}
											</div>
										</td>
										<td class="col-size">
											{entry.isDir ? '—' : formatSize(entry.size)}
										</td>
										<td class="col-time">
											{formatTime(entry.modTime)}
										</td>
										<td class="col-mode">
											<code>{entry.mode || '—'}</code>
										</td>
										<td class="col-actions">
											{#if entry.name !== '..'}
												<div class="row-quick-btns">
													{#if isScript}
														{#if isRunning}
															<button
																type="button"
																class="icon-act-btn restart-act-btn"
																title="Перезапустить скрипт / службу"
																disabled={isBusy}
																onclick={(e) => { e.stopPropagation(); void executeScriptAction(entry, 'restart'); }}
															>
																<RotateCw size={12} class={isBusy ? 'spin' : ''} />
															</button>
															<button
																type="button"
																class="icon-act-btn stop-act-btn"
																title="Остановить"
																disabled={isBusy}
																onclick={(e) => { e.stopPropagation(); void executeScriptAction(entry, 'stop'); }}
															>
																<Square size={12} />
															</button>
														{:else}
															<button
																type="button"
																class="icon-act-btn script-act-btn"
																title="Запустить скрипт / службу"
																disabled={isBusy}
																onclick={(e) => { e.stopPropagation(); void executeScriptAction(entry, scriptSt?.isService ? 'start' : 'run'); }}
															>
																<Play size={12} class={isBusy ? 'spin' : ''} />
															</button>
														{/if}
													{/if}

													<button
														type="button"
														class="icon-act-btn"
														title="Свойства"
														onclick={(e) => { e.stopPropagation(); propsModalEntry = entry; }}
													>
														<Eye size={13} />
													</button>
													{#if !entry.isDir}
														<button
															type="button"
															class="icon-act-btn"
															title="Редактировать"
															onclick={(e) => { e.stopPropagation(); void openEditor(entry); }}
														>
															<Edit2 size={13} />
														</button>
													{/if}
													{#if !readOnly}
														<button
															type="button"
															class="icon-act-btn danger"
															title="Удалить"
															onclick={(e) => { e.stopPropagation(); deleteTarget = entry; }}
														>
															<Trash2 size={13} />
														</button>
													{/if}
												</div>
											{/if}
										</td>
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
				{/if}
			</div>
		</div>
	</Card>

	<!-- Collapsible Terminal Console -->
	{#if showTerminal}
		<Card padding="sm">
			<div class="terminal-drawer-head">
				<div class="drawer-title">
					<Terminal size={15} />
					<span>Встроенная консоль (текущий каталог: <code>{currentPath}</code>)</span>
				</div>
				<Button size="sm" variant="ghost" onclick={() => (showTerminal = false)}>Скрыть</Button>
			</div>
			<SystemTerminal compact />
		</Card>
	{/if}
</div>

<!-- ========================================================================= -->
<!-- MODAL WINDOWS                                                             -->
<!-- ========================================================================= -->

<!-- 1. Properties & Chmod Modal -->
<FilePropsModal
	open={propsModalEntry !== null}
	entry={propsModalEntry}
	{readOnly}
	onClose={() => (propsModalEntry = null)}
	onEdit={(e) => void openEditor(e)}
	onUpdated={() => void loadDir(currentPath)}
/>

<!-- 2. Fullscreen / Big Editor Modal -->
<FileEditorModal
	open={editorModalPath !== null}
	path={editorModalPath}
	bind:content={editorModalContent}
	{readOnly}
	onClose={() => (editorModalPath = null)}
	onSaved={() => void loadDir(currentPath)}
/>

<!-- 3. Action Modals (Mkdir, New File, Rename, Copy, Move, Delete) -->
<FileActionModals
	currentPath={currentPath}
	bind:showMkdir
	onCloseMkdir={() => (showMkdir = false)}
	bind:showNewFile
	onCloseNewFile={() => (showNewFile = false)}
	bind:renameEntry
	onCloseRename={() => (renameEntry = null)}
	bind:copyTarget
	onCloseCopy={() => (copyTarget = '')}
	bind:moveTarget
	onCloseMove={() => (moveTarget = '')}
	bind:deleteTarget
	onCloseDelete={() => (deleteTarget = null)}
	onSuccess={() => void loadDir(currentPath)}
/>

<!-- Context Menu -->
{#if ctxMenu}
	<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
	<div
		bind:this={ctxMenuEl}
		class="file-ctx-menu"
		style="left:{ctxMenu.x}px;top:{ctxMenu.y}px"
		role="menu"
		oncontextmenu={(e) => e.preventDefault()}
	>
		{#if ctxMenu.entry}
			<button type="button" role="menuitem" onclick={() => { const e = ctxMenu?.entry; if (e) void openEntry(e); ctxMenu = null; }}>
				<ExternalLink size={13} /> Открыть <kbd>Enter</kbd>
			</button>
			<button type="button" role="menuitem" onclick={() => { propsModalEntry = ctxMenu?.entry ?? null; ctxMenu = null; }}>
				<Eye size={13} /> Свойства… <kbd>F3</kbd>
			</button>
			{#if !ctxMenu.entry.isDir}
				<button type="button" role="menuitem" onclick={() => { const e = ctxMenu?.entry; if (e) void openEditor(e); ctxMenu = null; }}>
					<Edit2 size={13} /> Редактировать <kbd>F4</kbd>
				</button>
				<button type="button" role="menuitem" onclick={() => { if (ctxMenu?.entry) window.open(api.systemFilesDownloadUrl(ctxMenu.entry.path), '_blank'); ctxMenu = null; }}>
					<Download size={13} /> Скачать
				</button>
				{@const info = getFileTypeInfo(ctxMenu.entry.name, ctxMenu.entry.isDir)}
				{@const isRunning = scriptStatuses[ctxMenu.entry.path]?.running}
				{#if info.kind === 'script' || ctxMenu.entry.mode.includes('x')}
					{#if isRunning}
						<button type="button" role="menuitem" onclick={() => { if (ctxMenu?.entry) void executeScriptAction(ctxMenu.entry, 'restart'); ctxMenu = null; }}>
							<RotateCw size={13} /> Перезапустить скрипт
						</button>
						<button type="button" role="menuitem" class="danger" onclick={() => { if (ctxMenu?.entry) void executeScriptAction(ctxMenu.entry, 'stop'); ctxMenu = null; }}>
							<Square size={13} /> Остановить скрипт
						</button>
					{:else}
						<button type="button" role="menuitem" onclick={() => { if (ctxMenu?.entry) void executeScriptAction(ctxMenu.entry, 'start'); ctxMenu = null; }}>
							<Play size={13} /> Запустить скрипт
						</button>
					{/if}
				{/if}
			{/if}
			<hr />
			<button type="button" role="menuitem" onclick={() => { void copyPath(ctxMenu?.entry?.path); ctxMenu = null; }}>
				<Copy size={13} /> Копировать путь
			</button>
			{#if !readOnly && ctxMenu.entry.name !== '..'}
				<button type="button" role="menuitem" onclick={() => { renameEntry = ctxMenu?.entry ?? null; ctxMenu = null; }}>
					<Edit2 size={13} /> Переименовать… <kbd>F2</kbd>
				</button>
				<button type="button" role="menuitem" onclick={() => { copyTarget = ctxMenu?.entry?.path ?? ''; ctxMenu = null; }}>
					<Copy size={13} /> Копировать в…
				</button>
				<button type="button" role="menuitem" onclick={() => { moveTarget = ctxMenu?.entry?.path ?? ''; ctxMenu = null; }}>
					<Move size={13} /> Переместить в…
				</button>
				<button type="button" role="menuitem" class="danger" onclick={() => { deleteTarget = ctxMenu?.entry ?? null; ctxMenu = null; }}>
					<Trash2 size={13} /> Удалить <kbd>F8</kbd>
				</button>
			{/if}
		{:else}
			{#if !readOnly}
				<button type="button" role="menuitem" onclick={() => { showMkdir = true; ctxMenu = null; }}>
					<FolderPlus size={13} /> Создать папку <kbd>F7</kbd>
				</button>
				<button type="button" role="menuitem" onclick={() => { showNewFile = true; ctxMenu = null; }}>
					<FilePlus size={13} /> Создать файл
				</button>
				<button type="button" role="menuitem" onclick={() => { uploadInput?.click(); ctxMenu = null; }}>
					<Upload size={13} /> Загрузить файл…
				</button>
			{/if}
			<button type="button" role="menuitem" onclick={() => { selectAll(); ctxMenu = null; }}>
				<Check size={13} /> Выделить всё <kbd>Ctrl+A</kbd>
			</button>
			<button type="button" role="menuitem" onclick={() => { void loadDir(currentPath); ctxMenu = null; }}>
				<RefreshCw size={13} /> Обновить <kbd>F5</kbd>
			</button>
		{/if}
	</div>
{/if}

<style>
	.file-manager-root {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
	}

	.hidden-upload {
		display: none;
	}

	.fm-toolbar {
		display: flex;
		flex-wrap: wrap;
		gap: 0.75rem;
		justify-content: space-between;
		align-items: center;
	}
	.fm-toolbar-actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.35rem;
		align-items: center;
	}

	.nav-bar {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		margin-top: 0.5rem;
		padding-top: 0.5rem;
		border-top: 1px solid var(--color-border);
	}
	.breadcrumbs {
		display: flex;
		align-items: center;
		gap: 0.2rem;
		flex-wrap: wrap;
		font-size: 0.82rem;
	}
	.btn-up {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		color: var(--color-text-primary);
		padding: 0.2rem 0.4rem;
		border-radius: 4px;
		cursor: pointer;
		display: inline-flex;
		align-items: center;
	}
	.btn-up:hover {
		background: var(--color-bg-tertiary);
	}
	.bc-item {
		background: none;
		border: none;
		padding: 0.15rem 0.35rem;
		border-radius: 4px;
		font-family: var(--font-mono, monospace);
		font-size: 0.8rem;
		color: var(--color-text-secondary);
		cursor: pointer;
	}
	.bc-item:hover {
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
	}
	.bc-item.active {
		font-weight: 700;
		color: var(--color-text-primary);
	}
	.bc-sep {
		color: var(--color-text-muted);
		font-size: 0.75rem;
	}

	.search-box {
		position: relative;
		display: flex;
		align-items: center;
	}
	:global(.search-icon) {
		position: absolute;
		left: 0.55rem;
		color: var(--color-text-muted);
	}
	.search-box input {
		padding: 0.3rem 0.55rem 0.3rem 1.65rem;
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-primary);
		font-size: 0.8rem;
		width: 190px;
	}
	.search-box input:focus {
		border-color: var(--color-accent);
		outline: none;
	}

	/* Layout: Modern */
	.modern-layout {
		display: grid;
		grid-template-columns: 200px 1fr;
		gap: 0.75rem;
		min-height: 480px;
	}
	.side-tree {
		border-right: 1px solid var(--color-border);
		padding-right: 0.5rem;
		overflow-y: auto;
		max-height: 520px;
	}
	.tree-head {
		font-size: 0.75rem;
		font-weight: 600;
		text-transform: uppercase;
		color: var(--color-text-muted);
		margin-bottom: 0.4rem;
	}
	.tree-root, .tree-nested {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	.tree-nested {
		padding-left: 0.75rem;
	}
	.tree-item {
		display: flex;
		align-items: center;
		gap: 0.3rem;
		width: 100%;
		padding: 0.25rem 0.35rem;
		border-radius: 4px;
		background: none;
		border: none;
		color: var(--color-text-secondary);
		font-size: 0.8rem;
		text-align: left;
		cursor: pointer;
	}
	.tree-item:hover {
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
	}
	.tree-item.active {
		background: var(--color-accent-tint, rgba(122, 162, 247, 0.18));
		color: var(--color-accent);
		font-weight: 600;
	}
	:global(.tree-folder-icon) {
		color: #60a5fa;
		flex-shrink: 0;
	}

	.files-table-container {
		min-width: 0;
	}
	.table-wrap {
		max-height: 520px;
		overflow-y: auto;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm, 6px);
		background: var(--color-bg-secondary);
	}
	.fm-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.84rem;
	}
	.fm-table th, .fm-table td {
		padding: 0.45rem 0.6rem;
		border-bottom: 1px solid var(--color-border);
		text-align: left;
		vertical-align: middle;
	}
	.fm-table th {
		position: sticky;
		top: 0;
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
		font-size: 0.75rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.03em;
		z-index: 1;
	}
	.fm-table tr {
		cursor: pointer;
	}
	.fm-table tr:hover {
		background: var(--color-bg-hover, rgba(255,255,255,0.03));
	}
	.fm-table tr.selected {
		background: var(--color-accent-tint, rgba(122, 162, 247, 0.15));
	}

	.name-wrap {
		display: inline-flex;
		align-items: center;
		gap: 0.45rem;
		white-space: nowrap;
	}
	:global(.icon-folder) {
		color: #60a5fa;
		flex-shrink: 0;
	}
	.entry-name {
		font-weight: 500;
		color: var(--color-text-primary);
		white-space: nowrap;
	}
	.entry-name.is-dir {
		font-weight: 600;
	}
	.file-badge {
		font-size: 0.65rem;
		font-weight: 700;
		padding: 0.05rem 0.25rem;
		border-radius: 3px;
		background: var(--color-bg-tertiary);
		color: var(--color-text-muted);
		white-space: nowrap;
	}
	.file-badge.badge-script {
		background: rgba(16, 185, 129, 0.1);
		color: #047857;
		border: 1px solid rgba(16, 185, 129, 0.25);
	}
	:global(.dark) .file-badge.badge-script {
		background: rgba(16, 185, 129, 0.15);
		color: #34d399;
		border-color: rgba(16, 185, 129, 0.35);
	}
	.running-pill {
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		font-size: 0.72rem;
		font-weight: 600;
		color: #047857;
		background: rgba(16, 185, 129, 0.1);
		border: 1px solid rgba(16, 185, 129, 0.3);
		padding: 0.1rem 0.45rem;
		border-radius: 999px;
		white-space: nowrap;
	}
	:global(.dark) .running-pill {
		color: #34d399;
		background: rgba(16, 185, 129, 0.15);
		border-color: rgba(16, 185, 129, 0.35);
	}
	.running-pill .dot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: #10b981;
		flex-shrink: 0;
	}

	.row-quick-btns {
		display: flex;
		gap: 0.25rem;
		justify-content: flex-end;
	}
	.icon-act-btn {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		color: var(--color-text-secondary);
		border-radius: 4px;
		padding: 0.25rem;
		cursor: pointer;
		display: inline-flex;
		align-items: center;
	}
	.icon-act-btn:hover {
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
	}
	.icon-act-btn.script-act-btn {
		color: var(--color-success, #22c55e);
	}
	.icon-act-btn.script-act-btn:hover {
		background: rgba(34, 197, 94, 0.15);
		border-color: var(--color-success);
	}
	.icon-act-btn.restart-act-btn {
		color: var(--color-accent, #60a5fa);
	}
	.icon-act-btn.restart-act-btn:hover {
		background: var(--color-accent-tint, rgba(96, 165, 250, 0.15));
		border-color: var(--color-accent);
	}
	.icon-act-btn.stop-act-btn {
		color: var(--color-error, #f87171);
	}
	.icon-act-btn.stop-act-btn:hover {
		background: var(--color-error-tint, rgba(239, 68, 68, 0.15));
		border-color: var(--color-error);
	}
	.icon-act-btn.danger:hover {
		color: var(--color-error, #f87171);
		border-color: var(--color-error);
	}

	/* Terminal drawer */
	.terminal-drawer-head {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 0.4rem;
	}
	.drawer-title {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		font-size: 0.85rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	/* Context Menu */
	.file-ctx-menu {
		position: fixed;
		z-index: 1100;
		background: var(--color-bg-secondary, #1f2335);
		border: 1px solid var(--color-border, #3b4261);
		border-radius: 6px;
		padding: 0.3rem 0;
		box-shadow: 0 4px 16px rgba(0,0,0,0.4);
		min-width: 200px;
		display: flex;
		flex-direction: column;
	}
	.file-ctx-menu button {
		background: none;
		border: none;
		padding: 0.35rem 0.75rem;
		display: flex;
		align-items: center;
		gap: 0.5rem;
		color: var(--color-text-primary);
		font-size: 0.82rem;
		cursor: pointer;
		text-align: left;
		width: 100%;
	}
	.file-ctx-menu button:hover {
		background: var(--color-bg-hover, #292e42);
		color: var(--color-accent, #7aa2f7);
	}
	.file-ctx-menu button.danger {
		color: var(--color-error, #f7768e);
	}
	.file-ctx-menu hr {
		border: none;
		border-top: 1px solid var(--color-border);
		margin: 0.25rem 0;
	}
	.file-ctx-menu kbd {
		margin-left: auto;
		font-size: 0.7rem;
		opacity: 0.6;
		background: var(--color-bg-tertiary);
		padding: 0.1rem 0.3rem;
		border-radius: 3px;
	}

	.loading-state, .empty-state {
		padding: 2rem;
		text-align: center;
		color: var(--color-text-muted);
		font-size: 0.85rem;
	}

	@media (max-width: 900px) {
		.modern-layout {
			grid-template-columns: 1fr;
		}
		.side-tree {
			display: none;
		}
	}
</style>
