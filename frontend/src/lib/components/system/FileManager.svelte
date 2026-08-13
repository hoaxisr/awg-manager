<script lang="ts">
	import { onMount } from 'svelte';
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
	} from 'lucide-svelte';

	type TreeDir = {
		path: string;
		name: string;
		expanded: boolean;
		loading: boolean;
		children: TreeDir[];
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
	let newDirName = $state('');
	let showMkdir = $state(false);
	let deleteTarget = $state<SystemFileEntry | null>(null);
	let deleting = $state(false);
	let showTerminal = $state(true);
	let uploadInput: HTMLInputElement | undefined = $state();

	const currentRoot = $derived(
		roots.find((r) => currentPath === r.path || currentPath.startsWith(r.path + '/')) ?? roots[0],
	);
	const readOnly = $derived(currentRoot?.readOnly ?? false);

	onMount(async () => {
		await loadRoots();
	});

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

	function downloadSelected() {
		const p = selected?.path;
		if (!p || selected?.isDir) {
			notifications.error('Выберите файл для скачивания');
			return;
		}
		window.open(api.systemFilesDownloadUrl(p), '_blank');
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
</script>

<input bind:this={uploadInput} type="file" class="hidden-upload" onchange={onUpload} />

<div class="file-manager">
	<div class="toolbar">
		<div class="toolbar-left">
			<Button variant="ghost" onclick={() => loadDir(currentPath)} disabled={loading}>
				{#snippet iconBefore()}<RefreshCw size={14} />{/snippet}
				Обновить
			</Button>
			{#if !readOnly}
				<Button variant="secondary" onclick={() => (showMkdir = true)}>
					{#snippet iconBefore()}<FolderPlus size={14} />{/snippet}
					Каталог
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
			<Button variant="ghost" onclick={downloadSelected} disabled={!selected || selected.isDir}>
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
					<div class="table-wrap">
						<table class="entries">
							<thead>
								<tr>
									<th>Имя</th>
									<th>Размер</th>
									<th>Изменён</th>
									<th></th>
								</tr>
							</thead>
							<tbody>
								{#each entries as entry (entry.path)}
									<tr class:selected={selected?.path === entry.path}>
										<td>
											<button type="button" class="entry-link" onclick={() => openEntry(entry)}>
												{#if entry.isDir}<Folder size={14} />{:else}<FileText size={14} />{/if}
												{entry.name}
											</button>
										</td>
										<td>{entry.isDir ? '—' : formatSize(entry.size)}</td>
										<td class="time">{formatTime(entry.modTime)}</td>
										<td class="row-actions">
											<button type="button" class="icon-btn" title="Копировать путь" onclick={() => copyPath(entry.path)}>
												<Copy size={14} />
											</button>
											{#if !entry.isDir}
												<a class="icon-btn" title="Скачать" href={api.systemFilesDownloadUrl(entry.path)} target="_blank" rel="noopener">
													<Download size={14} />
												</a>
											{/if}
											{#if !readOnly && entry.name !== '..'}
												<button type="button" class="icon-btn danger" title="Удалить" onclick={() => (deleteTarget = entry)}>
													<Trash2 size={14} />
												</button>
											{/if}
										</td>
									</tr>
								{/each}
							</tbody>
						</table>
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
</div>

{#if showMkdir}
	<Card padding="sm">
		<p class="mkdir-title">Новый каталог в {currentPath}</p>
		<div class="mkdir-row">
			<input class="mkdir-input" bind:value={newDirName} placeholder="имя каталога" />
			<Button variant="primary" onclick={createDir}>Создать</Button>
			<Button variant="ghost" onclick={() => (showMkdir = false)}>Отмена</Button>
		</div>
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
	.table-wrap { overflow: auto; max-height: 360px; }
	.entries { width: 100%; border-collapse: collapse; font-size: 0.88rem; table-layout: fixed; }
	.entries th, .entries td { text-align: left; padding: 0.4rem 0.45rem; border-bottom: 1px solid var(--border-subtle, #333); vertical-align: middle; }
	.entries th:nth-child(1) { width: 45%; }
	.entries th:nth-child(2) { width: 12%; }
	.entries th:nth-child(3) { width: 23%; }
	.entries th:nth-child(4) { width: 20%; }
	tr.selected { background: var(--accent-muted, rgba(99,102,241,0.12)); }
	.entry-link { display: inline-flex; align-items: center; gap: 0.35rem; background: none; border: none; color: inherit; cursor: pointer; padding: 0; max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
	.time { font-size: 0.78rem; opacity: 0.8; white-space: nowrap; }
	.row-actions { display: flex; gap: 0.15rem; justify-content: flex-end; white-space: nowrap; }
	.icon-btn { background: none; border: none; color: inherit; cursor: pointer; opacity: 0.75; padding: 0.15rem; display: inline-flex; }
	.icon-btn:hover { opacity: 1; }
	.icon-btn.danger:hover { color: var(--danger, #ef4444); }
	.editor-head { display: flex; justify-content: space-between; gap: 0.75rem; align-items: center; margin-bottom: 0.5rem; flex-wrap: wrap; }
	.editor-actions { display: flex; gap: 0.35rem; }
	.editor { width: 100%; min-height: 220px; font-family: ui-monospace, monospace; font-size: 0.85rem; resize: vertical; }
	.mkdir-row { display: flex; flex-wrap: wrap; gap: 0.35rem; align-items: center; }
	.mkdir-input { flex: 1; min-width: 160px; padding: 0.4rem 0.5rem; }
	.mkdir-title { margin: 0 0 0.5rem; }
	.term-head { display: flex; align-items: center; gap: 0.35rem; margin-bottom: 0.5rem; font-weight: 600; }
	.muted { opacity: 0.7; }
	:global(.spin) { animation: spin 1s linear infinite; }
	@keyframes spin { to { transform: rotate(360deg); } }
</style>
