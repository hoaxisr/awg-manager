<script lang="ts">
	import { m } from '$lib/i18n';
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
	// Вывод последнего запуска; пустой output подменяется текстом при отрисовке.
	let scriptRun = $state<{ output: string; ok: boolean } | null>(null);
	let checkedPath = $state<string | null>(null);

	$effect(() => {
		if (checkedPath === entry.path) return;
		checkedPath = entry.path;
		scriptRun = null;
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
			scriptRun = { output: res.output, ok: res.ok };
			if (res.ok) {
				notifications.success(m.system_files_fps_action_done({ action }));
			} else {
				notifications.error(res.error || m.system_files_fps_run_failed());
			}
			await checkScript(entry.path);
			onUpdated?.();
		} catch (e) {
			notifications.error(errorMessage(e, m.system_files_fps_exec_failed()));
		} finally {
			runningScript = false;
		}
	}
</script>

{#if scriptStatus?.isScript}
	<div class="section-box script-box">
		<div class="section-title">
			<Terminal size={15} />
			<span>{m.system_files_fps_title()}</span>
		</div>

		<div class="script-status-row">
			<div class="pill-badge" class:running={scriptStatus.running}>
				<span class="dot"></span>
				<strong>{scriptStatus.running ? m.system_files_fps_running() : m.system_files_fps_stopped()}</strong>
				{#if scriptStatus.pids?.length}
					<span>(PID: {scriptStatus.pids.join(', ')})</span>
				{/if}
			</div>

			<div class="script-btns">
				{#if !scriptStatus.running}
					<Button size="sm" variant="primary" loading={runningScript} onclick={() => runAction(scriptStatus?.isService ? 'start' : 'run')}>
						{#snippet iconBefore()}<Play size={13} />{/snippet}
						{m.system_files_fps_start()}
					</Button>
				{:else}
					<Button size="sm" variant="secondary" loading={runningScript} onclick={() => runAction('restart')}>
						{#snippet iconBefore()}<RotateCw size={13} />{/snippet}
						{m.system_files_fps_restart()}
					</Button>
					<Button size="sm" variant="danger" loading={runningScript} onclick={() => runAction('stop')}>
						{#snippet iconBefore()}<Square size={13} />{/snippet}
						{m.proxy_common_stop()}
					</Button>
				{/if}
			</div>
		</div>

		{#if scriptRun}
			<pre class="script-out">{scriptRun.output || (scriptRun.ok ? m.system_files_fps_ok() : m.tunnels_error_generic())}</pre>
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
