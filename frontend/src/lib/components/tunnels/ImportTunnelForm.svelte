<script lang="ts">
	import { notifications } from '$lib/stores/notifications';

	interface Props {
		loading: boolean;
		onImport: (content: string, name: string) => void;
	}

	let { loading, onImport }: Props = $props();

	let importContent = $state('');
	let importName = $state('');
	let fileInput = $state<HTMLInputElement>();
	let dragOver = $state(false);

	function handleFileSelect(event: Event) {
		const input = event.target as HTMLInputElement;
		if (input.files && input.files[0]) {
			readFile(input.files[0]);
		}
	}

	function handleDrop(event: DragEvent) {
		event.preventDefault();
		dragOver = false;
		if (event.dataTransfer?.files && event.dataTransfer.files[0]) {
			readFile(event.dataTransfer.files[0]);
		}
	}

	function handleDragOver(event: DragEvent) {
		event.preventDefault();
		dragOver = true;
	}

	function handleDragLeave() {
		dragOver = false;
	}

	function readFile(file: File) {
		if (!importName) {
			const baseName = file.name.replace(/\.conf$/i, '');
			importName = baseName;
		}

		const reader = new FileReader();
		reader.onload = (e) => {
			const content = e.target?.result as string;
			if (content) {
				importContent = content;
				notifications.success(`Файл "${file.name}" загружен`);
			}
		};
		reader.onerror = () => {
			notifications.error('Не удалось прочитать файл');
		};
		reader.readAsText(file);
	}

	function handleImport() {
		if (!importContent.trim()) {
			notifications.error('Вставьте содержимое конфигурации');
			return;
		}
		onImport(importContent, importName);
	}
</script>

<div class="card import-card">
	<div class="form-group">
		<label class="label" for="import-name">Название туннеля</label>
		<input type="text" id="import-name" class="input" bind:value={importName} placeholder="Мой VPN">
	</div>

	<div class="form-group">
		<label class="label" for="import-file">Файл конфигурации</label>
		<div
			class="file-drop-zone"
			class:drag-over={dragOver}
			ondrop={handleDrop}
			ondragover={handleDragOver}
			ondragleave={handleDragLeave}
			role="button"
			tabindex="0"
			onclick={() => fileInput?.click()}
			onkeydown={(e) => e.key === 'Enter' && fileInput?.click()}
		>
			<input
				type="file"
				id="import-file"
				accept=".conf"
				bind:this={fileInput}
				onchange={handleFileSelect}
				style="display: none"
			>
			<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="32" height="32">
				<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/>
				<polyline points="17 8 12 3 7 8"/>
				<line x1="12" y1="3" x2="12" y2="15"/>
			</svg>
			<p>Перетащите .conf файл сюда или нажмите для выбора</p>
		</div>
	</div>

	<div class="form-group">
		<label class="label" for="import-content">Или вставьте конфигурацию</label>
		<textarea
			id="import-content"
			class="textarea"
			bind:value={importContent}
			rows="10"
			placeholder="Вставьте содержимое .conf файла WireGuard/AmneziaWG..."
		></textarea>
		<p class="form-hint">Поддерживаются WireGuard и AmneziaWG конфигурации с параметрами Jc, Jmin, Jmax, S1-S4, H1-H4, I1-I5.</p>
	</div>

	<button class="btn btn-primary btn-full" onclick={handleImport} disabled={loading}>
		{#if loading}
			<span class="spinner"></span>
		{/if}
		Импортировать
	</button>
</div>

<style>
	.import-card {
		max-width: 700px;
	}

	.form-group {
		display: flex;
		flex-direction: column;
		gap: 6px;
		margin-bottom: 16px;
	}

	.form-group:last-child {
		margin-bottom: 0;
	}

	.label {
		font-size: 13px;
		font-weight: 500;
		color: var(--text-secondary);
	}

	.input,
	.textarea {
		padding: 10px 12px;
		font-size: 14px;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: 6px;
		color: var(--text-primary);
		transition: border-color 0.15s;
	}

	.input:focus,
	.textarea:focus {
		outline: none;
		border-color: var(--accent);
	}

	.textarea {
		font-family: monospace;
		font-size: 13px;
		resize: vertical;
		overflow: auto;
		min-height: 120px;
	}

	.form-hint {
		font-size: 12px;
		color: var(--text-muted);
		margin-top: 4px;
	}

	.file-drop-zone {
		border: 2px dashed var(--border);
		border-radius: 8px;
		padding: 32px;
		text-align: center;
		cursor: pointer;
		transition: all 0.15s ease;
		background: var(--bg-secondary);
	}

	.file-drop-zone:hover {
		border-color: var(--accent);
		background: var(--bg-tertiary);
	}

	.file-drop-zone.drag-over {
		border-color: var(--accent);
		background: rgba(99, 102, 241, 0.1);
	}

	.file-drop-zone svg {
		color: var(--text-muted);
		margin-bottom: 8px;
	}

	.file-drop-zone p {
		color: var(--text-secondary);
		font-size: 14px;
		margin: 0;
	}

	.btn-full {
		width: 100%;
		margin-top: 20px;
	}
</style>
