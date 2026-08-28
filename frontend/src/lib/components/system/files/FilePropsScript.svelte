<script lang="ts">
	import { api, type SystemFileEntry, type FileSystemScriptStatus } from '$lib/api/client';
	import { Button } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import { errorMessage } from '$lib/utils/errorMessage';
	import { Play, RotateCw, Square, Terminal } from 'lucide-svelte';
	import type { ScriptAction } from './types';

	interface Props {
		entry: SystemFileEntry;
		onUpdated?: () => void;
	}

	let { entry, onUpdated }: Props = $props();

	let scriptStatus = $state<FileSystemScriptStatus | null>(null);
	let runningScript = $state(false);
	let scriptOutput = $state<string | null>(null);
	let checkedPath = $state<string | null>(null);

	$effect(() => {
		if (checkedPath === entry.path) return;
		checkedPath = entry.path;
		scriptOutput = null;
		void checkScript(entry.path);
	});

	async function checkScript(p: string) {
		try {
			scriptStatus = await api.systemFilesScriptStatus(p);
		} catch {
			scriptStatus = null;
		}
	}

	async function runAction(action: ScriptAction) {
		runningScript = true;
		try {
			const res = await api.systemFilesScriptAction({ path: entry.path, action });
			scriptOutput = res.output || (res.ok ? 'Успешно выполнено' : 'Ошибка');
			if (res.ok) {
				notifications.success(`Скрипт: ${action} выполнен`);
			} else {
				notifications.error(res.error || 'Ошибка запуска');
			}
			await checkScript(entry.path);
			onUpdated?.();
		} catch (e) {
			notifications.error(errorMessage(e, 'Ошибка выполнения скрипта'));
		} finally {
			runningScript = false;
		}
	}
</script>

{#if scriptStatus?.isScript}
	<div class="section-box script-box">
		<div class="section-title">
			<Terminal size={15} />
			<span>Управление скриптом / процессом</span>
		</div>

		<div class="script-status-row">
			<div class="pill-badge" class:running={scriptStatus.running}>
				<span class="dot"></span>
				<strong>{scriptStatus.running ? 'Запущен в системе' : 'Остановлен'}</strong>
				{#if scriptStatus.pids?.length}
					<span>(PID: {scriptStatus.pids.join(', ')})</span>
				{/if}
			</div>

			<div class="script-btns">
				{#if !scriptStatus.running}
					<Button size="sm" variant="primary" loading={runningScript} onclick={() => runAction(scriptStatus?.isService ? 'start' : 'run')}>
						{#snippet iconBefore()}<Play size={13} />{/snippet}
						Запустить
					</Button>
				{:else}
					<Button size="sm" variant="secondary" loading={runningScript} onclick={() => runAction('restart')}>
						{#snippet iconBefore()}<RotateCw size={13} />{/snippet}
						Перезапуск
					</Button>
					<Button size="sm" variant="danger" loading={runningScript} onclick={() => runAction('stop')}>
						{#snippet iconBefore()}<Square size={13} />{/snippet}
						Остановить
					</Button>
				{/if}
			</div>
		</div>

		{#if scriptOutput}
			<pre class="script-out">{scriptOutput}</pre>
		{/if}
	</div>
{/if}

<style>
	.section-box {
		padding: 0.75rem;
		background: var(--color-bg-secondary);
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}
	.section-title {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		font-size: 0.85rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	/* Script runtime status */
	.script-status-row {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
	}
	.pill-badge {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		padding: 0.2rem 0.5rem;
		border-radius: 999px;
		font-size: 0.78rem;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		color: var(--color-text-muted);
	}
	.pill-badge .dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: #94a3b8;
	}
	.pill-badge.running {
		background: var(--color-success-tint, rgba(16, 185, 129, 0.15));
		border-color: rgba(16, 185, 129, 0.35);
		color: var(--color-success, #34d399);
	}
	.pill-badge.running .dot {
		background: var(--color-success, #22c55e);
		box-shadow: 0 0 6px var(--color-success, #22c55e);
	}
	.script-btns {
		display: flex;
		gap: 0.3rem;
	}
	.script-out {
		margin: 0.2rem 0 0 0;
		padding: 0.4rem;
		background: #0f172a;
		border-radius: 4px;
		font-family: var(--font-mono, monospace);
		font-size: 0.74rem;
		color: #38bdf8;
		max-height: 80px;
		overflow-y: auto;
		white-space: pre-wrap;
	}
</style>
