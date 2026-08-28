<script lang="ts">
	import type { SystemFileEntry } from '$lib/api/client';
	import FilePropsModal from './FilePropsModal.svelte';
	import FileEditorModal from './FileEditorModal.svelte';
	import FileActionModals from './FileActionModals.svelte';

	interface Props {
		currentPath: string;
		readOnly: boolean;
		propsEntry: SystemFileEntry | null;
		editorPath: string | null;
		editorContent: string;
		showMkdir: boolean;
		showNewFile: boolean;
		renameEntry: SystemFileEntry | null;
		copyTarget: string;
		moveTarget: string;
		deleteTarget: SystemFileEntry | null;
		onEdit: (entry: SystemFileEntry) => void;
		onChanged: () => void;
	}

	let {
		currentPath,
		readOnly,
		propsEntry = $bindable(null),
		editorPath = $bindable(null),
		editorContent = $bindable(''),
		showMkdir = $bindable(false),
		showNewFile = $bindable(false),
		renameEntry = $bindable(null),
		copyTarget = $bindable(''),
		moveTarget = $bindable(''),
		deleteTarget = $bindable(null),
		onEdit,
		onChanged,
	}: Props = $props();
</script>

<!-- 1. Properties & Chmod Modal -->
<FilePropsModal
	open={propsEntry !== null}
	entry={propsEntry}
	{readOnly}
	onClose={() => (propsEntry = null)}
	onEdit={(e) => onEdit(e)}
	onUpdated={onChanged}
/>

<!-- 2. Fullscreen / Big Editor Modal -->
<FileEditorModal
	open={editorPath !== null}
	path={editorPath}
	bind:content={editorContent}
	{readOnly}
	onClose={() => (editorPath = null)}
	onSaved={onChanged}
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
	onSuccess={onChanged}
/>
