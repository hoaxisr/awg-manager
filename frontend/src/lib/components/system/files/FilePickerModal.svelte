<script lang="ts">
	import { api, type SystemFileEntry } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { errorMessage } from '$lib/utils/errorMessage';
	import { Modal, Button } from '$lib/components/ui';
	import FileBreadcrumbs from './FileBreadcrumbs.svelte';
	import FilePickList from './FilePickList.svelte';
	import FileTree from './FileTree.svelte';
	import type { TreeDir } from './types';

	interface Props {
		open: boolean;
		onclose: () => void;
		onpick: (path: string) => void;
	}

	let { open, onclose, onpick }: Props = $props();

	let treeRoots = $state<TreeDir[]>([]);
	let currentPath = $state('');
	let entries = $state<SystemFileEntry[]>([]);
	let selected = $state('');

	// Корни грузим на каждое открытие: за время, пока пикер был закрыт,
	// каталог мог измениться, а показывать устаревший снимок — хуже, чем
	// один лишний запрос на открытие.
	$effect(() => {
		if (open) void loadRoots();
	});

	async function loadRoots() {
		// Прошлый снимок гасим до запроса: иначе при переоткрытии видны строки
		// старого каталога, а «Выбрать» остаётся живой с прежним путём.
		entries = [];
		selected = '';
		try {
			const roots = await api.systemFilesRoots();
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
		selected = '';
		try {
			const res = await api.systemFilesList(path);
			currentPath = res.path;
			entries = res.entries;
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось прочитать каталог'));
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

	function openEntry(entry: SystemFileEntry) {
		if (entry.isDir) {
			void loadDir(entry.path);
			return;
		}
		selected = entry.path;
	}
</script>

<Modal {open} title="Выбор файла" size="wide" {onclose}>
	{#snippet children()}
		<div class="pick-body">
			<FileTree nodes={treeRoots} {currentPath} onToggle={expandTree} onNavigate={loadDir} />
			<div class="pick-main">
				<FileBreadcrumbs {currentPath} onNavigate={loadDir} />
				<FilePickList {entries} {selected} onopen={openEntry} />
			</div>
		</div>
	{/snippet}
	{#snippet actions()}
		<Button variant="secondary" onclick={onclose}>Отмена</Button>
		<Button variant="primary" disabled={!selected} onclick={() => onpick(selected)}>Выбрать</Button>
	{/snippet}
</Modal>

<style>
	.pick-body {
		display: grid;
		grid-template-columns: 200px 1fr;
		gap: 0.75rem;
	}
	.pick-main {
		min-width: 0;
	}
	@media (max-width: 900px) {
		.pick-body {
			grid-template-columns: 1fr;
		}
	}
</style>
