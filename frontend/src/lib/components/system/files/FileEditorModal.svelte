<script lang="ts">
	import { api, type FileSystemScriptStatus } from '$lib/api/client';
	import { Modal, Button } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import { errorMessage } from '$lib/utils/errorMessage';
	import { formatBytes } from '$lib/utils/format';
	import { Save, FileText, Play, RotateCw, Square, Check, Terminal, AlertCircle, X } from 'lucide-svelte';

	interface Props {
		open: boolean;
		path: string | null;
		content: string;
		readOnly?: boolean;
		onClose: () => void;
		onSaved?: () => void;
	}

	let { open = false, path, content = $bindable(''), readOnly = false, onClose, onSaved }: Props = $props();

	let dirty = $state(false);
	let saving = $state(false);
	let textareaEl = $state<HTMLTextAreaElement | null>(null);

	// Script execution state
	let scriptStatus = $state<FileSystemScriptStatus | null>(null);
	let runningAction = $state(false);
	let lastOutput = $state<string | null>(null);

	$effect(() => {
		if (open) {
			dirty = false;
			lastOutput = null;
			if (path) {
				void checkScriptStatus(path);
			}
		}
	});

	async function checkScriptStatus(filePath: string) {
		try {
			scriptStatus = await api.systemFilesScriptStatus(filePath);
		} catch {
			scriptStatus = null;
		}
	}

	async function handleSave() {
		if (!path || readOnly) return;
		saving = true;
		try {
			await api.systemFilesWrite(path, content);
			dirty = false;
			notifications.success('Файл сохранён');
			onSaved?.();
			if (path) void checkScriptStatus(path);
		} catch (e) {
			notifications.error(errorMessage(e, 'Ошибка сохранения файла'));
		} finally {
			saving = false;
		}
	}

	async function handleScriptAction(action: 'start' | 'stop' | 'restart' | 'run') {
		if (!path) return;
		if (dirty) {
			await handleSave();
		}
		runningAction = true;
		try {
			const res = await api.systemFilesScriptAction({ path, action });
			lastOutput = res.output || (res.ok ? 'Команда выполнена успешно' : 'Завершено с ошибкой');
			if (res.ok) {
				notifications.success(`Скрипт: действие «${action}» выполнено`);
			} else {
				notifications.error(res.error || 'Ошибка выполнения');
			}
			await checkScriptStatus(path);
		} catch (e) {
			notifications.error(errorMessage(e, 'Ошибка запуска скрипта'));
		} finally {
			runningAction = false;
		}
	}

	function handleKeyDown(e: KeyboardEvent) {
		if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') {
			e.preventDefault();
			void handleSave();
		}
		if (e.key === 'Tab' && textareaEl) {
			e.preventDefault();
			const start = textareaEl.selectionStart;
			const end = textareaEl.selectionEnd;
			content = content.substring(0, start) + '\t' + content.substring(end);
			dirty = true;
			setTimeout(() => {
				if (textareaEl) {
					textareaEl.selectionStart = textareaEl.selectionEnd = start + 1;
				}
			}, 0);
		}
	}
</script>

<Modal
	{open}
	title={path ? path.split('/').pop() || path : 'Редактор файла'}
	size="xl"
	onclose={onClose}
>
	<div class="editor-wrap">
		<!-- Top Editor Toolbar -->
		<div class="editor-header">
			<div class="header-left">
				<FileText size={16} class="text-accent" />
				<code class="file-path">{path}</code>
				{#if dirty}
					<span class="dirty-badge">● Не сохранено</span>
				{/if}
			</div>

			<!-- Script Runner Controls in Editor Header -->
			{#if scriptStatus?.isScript}
				<div class="script-ctrl-bar">
					<div class="script-status-pill" class:running={scriptStatus.running}>
						<span class="status-dot"></span>
						<span class="status-txt">
							{#if scriptStatus.running}
								Запущен {scriptStatus.pids?.length ? `(PID ${scriptStatus.pids.join(', ')})` : ''}
							{:else}
								Остановлен
							{/if}
						</span>
					</div>

					<div class="script-btn-group">
						{#if !scriptStatus.running}
							<Button
								size="sm"
								variant="primary"
								loading={runningAction}
								onclick={() => handleScriptAction(scriptStatus?.isService ? 'start' : 'run')}
							>
								{#snippet iconBefore()}<Play size={13} />{/snippet}
								{scriptStatus.isService ? 'Запустить службу' : 'Запустить скрипт'}
							</Button>
						{:else}
							<Button
								size="sm"
								variant="secondary"
								loading={runningAction}
								onclick={() => handleScriptAction('restart')}
							>
								{#snippet iconBefore()}<RotateCw size={13} />{/snippet}
								Перезапустить
							</Button>
							<Button
								size="sm"
								variant="danger"
								loading={runningAction}
								onclick={() => handleScriptAction('stop')}
							>
								{#snippet iconBefore()}<Square size={13} />{/snippet}
								Остановить
							</Button>
						{/if}
					</div>
				</div>
			{/if}

			<div class="header-right">
				<span class="file-meta">{content.length} симв. ({formatBytes(new Blob([content]).size)})</span>
			</div>
		</div>

		<!-- Script Output Banner (if any) -->
		{#if lastOutput}
			<div class="script-output-banner">
				<div class="banner-head">
					<div class="head-title"><Terminal size={14} /> Вывод выполнения:</div>
					<button type="button" class="btn-close-output" onclick={() => (lastOutput = null)}><X size={14} /></button>
				</div>
				<pre class="output-text">{lastOutput}</pre>
			</div>
		{/if}

		<!-- svelte-ignore a11y_autofocus -->
		<textarea
			bind:this={textareaEl}
			class="editor-textarea"
			bind:value={content}
			readonly={readOnly}
			oninput={() => (dirty = true)}
			onkeydown={handleKeyDown}
			placeholder="Содержимое файла..."
			spellcheck="false"
			autofocus
		></textarea>
	</div>

	{#snippet actions()}
		<div class="editor-footer">
			<div class="footer-hint">
				<span><kbd>Ctrl+S</kbd> — сохранить</span>
				<span><kbd>Tab</kbd> — отступ</span>
			</div>
			<div class="footer-btns">
				<Button variant="ghost" onclick={onClose}>Закрыть</Button>
				{#if !readOnly}
					<Button variant="primary" loading={saving} disabled={!dirty} onclick={handleSave}>
						{#snippet iconBefore()}<Save size={14} />{/snippet}
						Сохранить
					</Button>
				{/if}
			</div>
		</div>
	{/snippet}
</Modal>

<style>
	.editor-wrap {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		height: 68vh;
		min-height: 420px;
	}

	.editor-header {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		justify-content: space-between;
		align-items: center;
		padding: 0.4rem 0.6rem;
		background: var(--color-bg-tertiary);
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		font-size: 0.8rem;
	}
	.header-left {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		min-width: 0;
		overflow: hidden;
	}
	.file-path {
		font-size: 0.78rem;
		color: var(--color-text-primary);
		word-break: break-all;
	}
	.dirty-badge {
		color: var(--color-warning, #f59e0b);
		font-size: 0.75rem;
		font-weight: 600;
		white-space: nowrap;
	}
	.file-meta {
		color: var(--color-text-muted);
		font-size: 0.75rem;
		white-space: nowrap;
	}

	/* Script runner bar in header */
	.script-ctrl-bar {
		display: flex;
		align-items: center;
		gap: 0.6rem;
	}
	.script-status-pill {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		padding: 0.15rem 0.45rem;
		border-radius: 999px;
		font-size: 0.75rem;
		font-weight: 600;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		color: var(--color-text-muted);
	}
	.script-status-pill .status-dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: #94a3b8;
	}
	.script-status-pill.running {
		background: var(--color-success-tint, rgba(16, 185, 129, 0.15));
		border-color: rgba(16, 185, 129, 0.3);
		color: var(--color-success, #34d399);
	}
	.script-status-pill.running .status-dot {
		background: var(--color-success, #22c55e);
		box-shadow: 0 0 6px var(--color-success, #22c55e);
	}
	.script-btn-group {
		display: flex;
		gap: 0.3rem;
	}

	.script-output-banner {
		background: #0f172a;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm, 6px);
		padding: 0.4rem 0.6rem;
		max-height: 110px;
		overflow-y: auto;
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}
	.banner-head {
		display: flex;
		justify-content: space-between;
		align-items: center;
		font-size: 0.75rem;
		font-weight: 600;
		color: #94a3b8;
	}
	.head-title {
		display: flex;
		align-items: center;
		gap: 0.3rem;
	}
	.btn-close-output {
		background: none;
		border: none;
		color: #94a3b8;
		cursor: pointer;
		font-size: 0.8rem;
		padding: 0.1rem 0.3rem;
	}
	.btn-close-output:hover {
		color: #fff;
	}
	.output-text {
		margin: 0;
		font-family: var(--font-mono, monospace);
		font-size: 0.75rem;
		color: #38bdf8;
		white-space: pre-wrap;
		word-break: break-all;
	}

	.editor-textarea {
		flex: 1;
		width: 100%;
		padding: 0.75rem;
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-primary);
		font-family: var(--font-mono, monospace);
		font-size: 0.85rem;
		line-height: 1.5;
		resize: none;
		tab-size: 4;
		outline: none;
	}
	.editor-textarea:focus {
		border-color: var(--color-accent);
	}

	.editor-footer {
		display: flex;
		justify-content: space-between;
		align-items: center;
		width: 100%;
	}
	.footer-hint {
		display: flex;
		gap: 0.75rem;
		font-size: 0.75rem;
		color: var(--color-text-muted);
	}
	.footer-hint kbd {
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		padding: 0.1rem 0.3rem;
		border-radius: 3px;
		font-size: 0.7rem;
	}
	.footer-btns {
		display: flex;
		gap: 0.5rem;
	}
</style>
