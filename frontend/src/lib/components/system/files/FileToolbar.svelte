<script lang="ts">
	import type { SystemFileEntry } from '$lib/api/client';
	import { Button } from '$lib/components/ui';
	import {
		RefreshCw,
		FolderPlus,
		FilePlus,
		Upload,
		Download,
		Terminal,
		Search,
	} from 'lucide-svelte';

	interface Props {
		loading: boolean;
		readOnly: boolean;
		selected: SystemFileEntry | null;
		showTerminal: boolean;
		searchQuery: string;
		onRefresh: () => void;
		onMkdir: () => void;
		onNewFile: () => void;
		onUploadClick: () => void;
		onDownload: (entry: SystemFileEntry) => void;
		onToggleTerminal: () => void;
	}

	let {
		loading,
		readOnly,
		selected,
		showTerminal,
		searchQuery = $bindable(''),
		onRefresh,
		onMkdir,
		onNewFile,
		onUploadClick,
		onDownload,
		onToggleTerminal,
	}: Props = $props();
</script>

<div class="fm-toolbar">
	<!-- Action buttons -->
	<div class="fm-toolbar-actions">
		<Button size="sm" variant="ghost" onclick={onRefresh} disabled={loading}>
			{#snippet iconBefore()}<RefreshCw size={14} class={loading ? 'spin' : ''} />{/snippet}
			Обновить
		</Button>

		{#if !readOnly}
			<Button size="sm" variant="secondary" onclick={onMkdir}>
				{#snippet iconBefore()}<FolderPlus size={14} />{/snippet}
				Папка
			</Button>
			<Button size="sm" variant="secondary" onclick={onNewFile}>
				{#snippet iconBefore()}<FilePlus size={14} />{/snippet}
				Файл
			</Button>
			<Button size="sm" variant="secondary" onclick={onUploadClick}>
				{#snippet iconBefore()}<Upload size={14} />{/snippet}
				Загрузить
			</Button>
		{/if}

		{#if selected && !selected.isDir}
			<Button size="sm" variant="secondary" onclick={() => selected && onDownload(selected)}>
				{#snippet iconBefore()}<Download size={14} />{/snippet}
				Скачать
			</Button>
		{/if}

		<Button size="sm" variant={showTerminal ? 'secondary' : 'ghost'} onclick={onToggleTerminal}>
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

<style>
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
</style>
