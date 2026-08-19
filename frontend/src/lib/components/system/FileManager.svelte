<script lang="ts">
	import { onMount, tick } from 'svelte';
	import { api, type SystemFileEntry, type SystemFileRoot } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Card } from '$lib/components/ui';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { errorMessage } from '$lib/utils/errorMessage';
	import {
		FileModals,
		FileToolbar,
		FileBreadcrumbs,
		FileTree,
		FileTable,
		FileContextMenu,
		FileTerminalDrawer,
		clampMenuPosition,
		createScriptStatuses,
		type TreeDir,
		type CtxMenu,
	} from './files';

	// State
	let roots = $state<SystemFileRoot[]>([]);
	let currentPath = $state('/opt');
	let entries = $state<SystemFileEntry[]>([]);
	let treeRoots = $state<TreeDir[]>([]);
	let loading = $state(false);
	let selected = $state<SystemFileEntry | null>(null);
	let checked = $state<Set<string>>(new Set());
	let searchQuery = $state('');

	// Script statuses (path -> status) + текущее выполняемое действие
	const scripts = createScriptStatuses();

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
		scripts.reset();
		try {
			const res = await api.systemFilesList(path);
			currentPath = res.path;
			entries = res.entries;
			void scripts.load(res.entries);
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
		const { x, y } = clampMenuPosition(ctxMenuEl, ax, ay);
		ctxMenu = { x, y, entry };
	}

	async function copyPath(path?: string) {
		const p = path ?? selected?.path ?? currentPath;
		if (!p) return;
		const ok = await copyToClipboard(p);
		if (ok) notifications.success('Путь скопирован');
	}

	function downloadEntry(entry: SystemFileEntry) {
		window.open(api.systemFilesDownloadUrl(entry.path), '_blank');
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
</script>

<input bind:this={uploadInput} type="file" class="hidden-upload" onchange={onUpload} />

<div class="file-manager-root">
	<!-- Top Controls Toolbar -->
	<Card padding="sm">
		<FileToolbar
			{loading}
			{readOnly}
			{selected}
			{showTerminal}
			bind:searchQuery
			onRefresh={() => void loadDir(currentPath)}
			onMkdir={() => (showMkdir = true)}
			onNewFile={() => (showNewFile = true)}
			onUploadClick={() => uploadInput?.click()}
			onDownload={downloadEntry}
			onToggleTerminal={() => (showTerminal = !showTerminal)}
		/>

		<!-- Breadcrumbs Navigation -->
		<FileBreadcrumbs {currentPath} onNavigate={(p) => void loadDir(p)} />
	</Card>

	<!-- Main Explorer View -->
	<Card padding="sm">
		<div class="modern-layout">
			<!-- Left folders tree -->
			<FileTree
				nodes={treeRoots}
				{currentPath}
				onToggle={(node) => void expandTree(node)}
				onNavigate={(p) => void loadDir(p)}
			/>

			<!-- Main file table -->
			<FileTable
				entries={filteredEntries}
				{loading}
				{readOnly}
				selectedPath={selected?.path ?? null}
				{checked}
				scriptStatuses={scripts.statuses}
				runningActionPath={scripts.runningPath}
				onSelect={(entry) => (selected = entry)}
				onOpen={(entry) => void openEntry(entry)}
				onToggleCheck={toggleCheck}
				onContextMenu={(ev, entry) => void showContextMenu(ev, entry)}
				onScriptAction={(entry, action) => void scripts.run(entry, action)}
				onProps={(entry) => (propsModalEntry = entry)}
				onEdit={(entry) => void openEditor(entry)}
				onDelete={(entry) => (deleteTarget = entry)}
			/>
		</div>
	</Card>

	<!-- Collapsible Terminal Console -->
	{#if showTerminal}
		<FileTerminalDrawer {currentPath} onHide={() => (showTerminal = false)} />
	{/if}
</div>

<!-- Modal windows: properties, editor, mkdir / new file / rename / copy / move / delete -->
<FileModals
	{currentPath}
	{readOnly}
	bind:propsEntry={propsModalEntry}
	bind:editorPath={editorModalPath}
	bind:editorContent={editorModalContent}
	bind:showMkdir
	bind:showNewFile
	bind:renameEntry
	bind:copyTarget
	bind:moveTarget
	bind:deleteTarget
	onEdit={(e) => void openEditor(e)}
	onChanged={() => void loadDir(currentPath)}
/>

<!-- Context Menu -->
{#if ctxMenu}
	<FileContextMenu
		menu={ctxMenu}
		{readOnly}
		scriptStatuses={scripts.statuses}
		bind:el={ctxMenuEl}
		onClose={() => (ctxMenu = null)}
		onOpen={(entry) => void openEntry(entry)}
		onProps={(entry) => (propsModalEntry = entry)}
		onEdit={(entry) => void openEditor(entry)}
		onDownload={downloadEntry}
		onScriptAction={(entry, action) => void scripts.run(entry, action)}
		onCopyPath={(p) => void copyPath(p)}
		onRename={(entry) => (renameEntry = entry)}
		onCopyTo={(p) => (copyTarget = p)}
		onMoveTo={(p) => (moveTarget = p)}
		onDelete={(entry) => (deleteTarget = entry)}
		onMkdir={() => (showMkdir = true)}
		onNewFile={() => (showNewFile = true)}
		onUploadClick={() => uploadInput?.click()}
		onSelectAll={selectAll}
		onRefresh={() => void loadDir(currentPath)}
	/>
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

	/* Layout: Modern */
	.modern-layout {
		display: grid;
		grid-template-columns: 200px 1fr;
		gap: 0.75rem;
		min-height: 480px;
	}

	@media (max-width: 900px) {
		.modern-layout {
			grid-template-columns: 1fr;
		}
	}
</style>
