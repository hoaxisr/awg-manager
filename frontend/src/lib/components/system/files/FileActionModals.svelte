<script lang="ts">
	import { Modal, Button, ConfirmModal } from '$lib/components/ui';
	import { api, type SystemFileEntry } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { errorMessage } from '$lib/utils/errorMessage';
	import { FolderPlus, FilePlus, Edit2, Copy, Move, Trash2 } from 'lucide-svelte';

	interface Props {
		currentPath: string;
		// Mkdir
		showMkdir: boolean;
		onCloseMkdir: () => void;
		// New file
		showNewFile: boolean;
		onCloseNewFile: () => void;
		// Rename
		renameEntry: SystemFileEntry | null;
		onCloseRename: () => void;
		// Copy
		copyTarget: string;
		onCloseCopy: () => void;
		// Move
		moveTarget: string;
		onCloseMove: () => void;
		// Delete
		deleteTarget: SystemFileEntry | null;
		onCloseDelete: () => void;
		onSuccess: () => void;
	}

	let {
		currentPath,
		showMkdir = $bindable(false),
		onCloseMkdir,
		showNewFile = $bindable(false),
		onCloseNewFile,
		renameEntry = $bindable(null),
		onCloseRename,
		copyTarget = $bindable(''),
		onCloseCopy,
		moveTarget = $bindable(''),
		onCloseMove,
		deleteTarget = $bindable(null),
		onCloseDelete,
		onSuccess,
	}: Props = $props();

	let newDirName = $state('');
	let newFileName = $state('');
	let renameName = $state('');
	let copyPath = $state('');
	let movePath = $state('');
	let busy = $state(false);

	$effect(() => {
		if (showMkdir) newDirName = '';
	});
	$effect(() => {
		if (showNewFile) newFileName = '';
	});
	$effect(() => {
		if (renameEntry) renameName = renameEntry.name;
	});
	$effect(() => {
		if (copyTarget) copyPath = copyTarget;
	});
	$effect(() => {
		if (moveTarget) movePath = moveTarget;
	});

	async function handleMkdir() {
		const name = newDirName.trim();
		if (!name) return;
		const path = `${currentPath.replace(/\/$/, '')}/${name}`;
		busy = true;
		try {
			await api.systemFilesMkdir(path);
			notifications.success(`Каталог «${name}» создан`);
			onCloseMkdir();
			onSuccess();
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось создать каталог'));
		} finally {
			busy = false;
		}
	}

	async function handleNewFile() {
		const name = newFileName.trim();
		if (!name) return;
		const path = `${currentPath.replace(/\/$/, '')}/${name}`;
		busy = true;
		try {
			await api.systemFilesWrite(path, '');
			notifications.success(`Файл «${name}» создан`);
			onCloseNewFile();
			onSuccess();
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось создать файл'));
		} finally {
			busy = false;
		}
	}

	async function handleRename() {
		if (!renameEntry) return;
		const name = renameName.trim();
		if (!name || name === renameEntry.name) {
			onCloseRename();
			return;
		}
		const base = renameEntry.path.replace(/\/[^/]+$/, '');
		const to = `${base}/${name}`;
		busy = true;
		try {
			await api.systemFilesRename(renameEntry.path, to);
			notifications.success(`Переименовано в «${name}»`);
			onCloseRename();
			onSuccess();
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось переименовать'));
		} finally {
			busy = false;
		}
	}

	async function handleCopy() {
		const target = copyPath.trim();
		if (!target || !copyTarget) return;
		busy = true;
		try {
			await api.systemFilesCopy(copyTarget, target);
			notifications.success('Успешно скопировано');
			onCloseCopy();
			onSuccess();
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось скопировать'));
		} finally {
			busy = false;
		}
	}

	async function handleMove() {
		const target = movePath.trim();
		if (!target || !moveTarget) return;
		busy = true;
		try {
			await api.systemFilesRename(moveTarget, target);
			notifications.success('Успешно перемещено');
			onCloseMove();
			onSuccess();
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось переместить'));
		} finally {
			busy = false;
		}
	}

	async function handleDelete() {
		if (!deleteTarget) return;
		busy = true;
		try {
			await api.systemFilesRemove(deleteTarget.path);
			notifications.success(`«${deleteTarget.name}» удалён`);
			onCloseDelete();
			onSuccess();
		} catch (e) {
			notifications.error(errorMessage(e, 'Не удалось удалить объект'));
		} finally {
			busy = false;
		}
	}
</script>

<!-- 1. Создание каталога -->
{#if showMkdir}
	<Modal open={showMkdir} title="Новый каталог" size="sm" onclose={onCloseMkdir}>
		<form onsubmit={(e) => { e.preventDefault(); void handleMkdir(); }}>
			<p class="modal-hint">Будет создан каталог внутри: <code>{currentPath}</code></p>
			<!-- svelte-ignore a11y_autofocus -->
			<input
				type="text"
				class="modal-input"
				placeholder="Имя нового каталога..."
				bind:value={newDirName}
				autofocus
			/>
		</form>
		{#snippet actions()}
			<Button variant="ghost" onclick={onCloseMkdir}>Отмена</Button>
			<Button variant="primary" loading={busy} disabled={!newDirName.trim()} onclick={handleMkdir}>
				{#snippet iconBefore()}<FolderPlus size={14} />{/snippet}
				Создать
			</Button>
		{/snippet}
	</Modal>
{/if}

<!-- 2. Создание файла -->
{#if showNewFile}
	<Modal open={showNewFile} title="Новый файл" size="sm" onclose={onCloseNewFile}>
		<form onsubmit={(e) => { e.preventDefault(); void handleNewFile(); }}>
			<p class="modal-hint">Будет создан пустой файл внутри: <code>{currentPath}</code></p>
			<!-- svelte-ignore a11y_autofocus -->
			<input
				type="text"
				class="modal-input"
				placeholder="Имя нового файла (например, config.json)..."
				bind:value={newFileName}
				autofocus
			/>
		</form>
		{#snippet actions()}
			<Button variant="ghost" onclick={onCloseNewFile}>Отмена</Button>
			<Button variant="primary" loading={busy} disabled={!newFileName.trim()} onclick={handleNewFile}>
				{#snippet iconBefore()}<FilePlus size={14} />{/snippet}
				Создать
			</Button>
		{/snippet}
	</Modal>
{/if}

<!-- 3. Переименование -->
{#if renameEntry}
	<Modal open={!!renameEntry} title="Переименовать" size="sm" onclose={onCloseRename}>
		<form onsubmit={(e) => { e.preventDefault(); void handleRename(); }}>
			<p class="modal-hint">Текущий путь: <code>{renameEntry.path}</code></p>
			<!-- svelte-ignore a11y_autofocus -->
			<input
				type="text"
				class="modal-input"
				bind:value={renameName}
				autofocus
			/>
		</form>
		{#snippet actions()}
			<Button variant="ghost" onclick={onCloseRename}>Отмена</Button>
			<Button variant="primary" loading={busy} disabled={!renameName.trim()} onclick={handleRename}>
				{#snippet iconBefore()}<Edit2 size={14} />{/snippet}
				Переименовать
			</Button>
		{/snippet}
	</Modal>
{/if}

<!-- 4. Копирование -->
{#if copyTarget}
	<Modal open={!!copyTarget} title="Копировать объект" size="md" onclose={onCloseCopy}>
		<form onsubmit={(e) => { e.preventDefault(); void handleCopy(); }}>
			<p class="modal-hint">Исходный путь: <code>{copyTarget}</code></p>
			<label class="input-label">Куда скопировать:</label>
			<input
				type="text"
				class="modal-input"
				bind:value={copyPath}
			/>
		</form>
		{#snippet actions()}
			<Button variant="ghost" onclick={onCloseCopy}>Отмена</Button>
			<Button variant="primary" loading={busy} disabled={!copyPath.trim()} onclick={handleCopy}>
				{#snippet iconBefore()}<Copy size={14} />{/snippet}
				Копировать
			</Button>
		{/snippet}
	</Modal>
{/if}

<!-- 5. Перемещение -->
{#if moveTarget}
	<Modal open={!!moveTarget} title="Переместить объект" size="md" onclose={onCloseMove}>
		<form onsubmit={(e) => { e.preventDefault(); void handleMove(); }}>
			<p class="modal-hint">Исходный путь: <code>{moveTarget}</code></p>
			<label class="input-label">Куда переместить:</label>
			<input
				type="text"
				class="modal-input"
				bind:value={movePath}
			/>
		</form>
		{#snippet actions()}
			<Button variant="ghost" onclick={onCloseMove}>Отмена</Button>
			<Button variant="primary" loading={busy} disabled={!movePath.trim()} onclick={handleMove}>
				{#snippet iconBefore()}<Move size={14} />{/snippet}
				Переместить
			</Button>
		{/snippet}
	</Modal>
{/if}

<!-- 6. Удаление -->
{#if deleteTarget}
	<ConfirmModal
		open={!!deleteTarget}
		title={`Удалить ${deleteTarget.isDir ? 'каталог' : 'файл'}?`}
		message={`Вы действительно хотите безвозвратно удалить: ${deleteTarget.path}`}
		confirmLabel="Удалить"
		variant="danger"
		busy={busy}
		onClose={onCloseDelete}
		onConfirm={handleDelete}
	/>
{/if}

<style>
	.modal-hint {
		font-size: 0.82rem;
		color: var(--color-text-muted);
		margin-bottom: 0.6rem;
		word-break: break-all;
	}
	.modal-hint code {
		color: var(--color-text-primary);
	}
	.input-label {
		display: block;
		font-size: 0.82rem;
		font-weight: 500;
		color: var(--color-text-secondary);
		margin-bottom: 0.3rem;
	}
	.modal-input {
		width: 100%;
		padding: 0.5rem 0.65rem;
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-primary);
		font-size: 0.9rem;
		outline: none;
	}
	.modal-input:focus {
		border-color: var(--color-accent);
	}
</style>
