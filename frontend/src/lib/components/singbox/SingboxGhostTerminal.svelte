<script lang="ts">
	import { api } from '$lib/api/client';
	import { singboxStatus, singboxTunnels } from '$lib/stores/singbox';
	import type { SingboxImportResponse } from '$lib/types';

	interface Props {
		/** Called once after a successful import finishes. Used by the
		 * /singbox/new dedicated page to navigate back to the tunnels list.
		 * Omitted on the empty-state embedding — that one stays in place
		 * and relies on SSE to refresh the count. */
		oncomplete?: (imported: number) => void;
	}

	let { oncomplete }: Props = $props();

	let input = $state('');
	let importing = $state(false);
	let result = $state<SingboxImportResponse | null>(null);

	const status = $derived($singboxStatus.data);
	const singboxInstalled = $derived(status?.installed ?? false);
	const statusTone = $derived.by(() => {
		if ($singboxStatus.status === 'loading' && !status) return 'pending';
		if ($singboxStatus.status === 'error' && !status) return 'error';
		if (!status?.installed) return 'error';
		if (!status.proxyComponent || status.updateAvailable) return 'warn';
		return status.running ? 'ok' : 'ready';
	});
	const statusLine = $derived.by(() => {
		if ($singboxStatus.status === 'loading' && !status) return 'получаю статус...';
		if ($singboxStatus.status === 'error' && !status) return 'статус недоступен';
		if (!status?.installed) return 'sing-box · не установлен';

		const parts = ['sing-box'];
		const version = status.version || status.currentVersion;
		if (version) parts.push(`v${version}`);
		parts.push(status.running ? 'работает' : 'готов');
		if (status.running && status.pid) parts.push(`pid ${status.pid}`);
		if (status.updateAvailable) parts.push('доступно обновление');
		if (!status.proxyComponent) parts.push('нет компонента proxy');
		return parts.join(' · ');
	});

	async function submit(): Promise<void> {
		importing = true;
		result = null;
		try {
			const res = await api.singboxImportLinks(input);
			result = res;
			singboxTunnels.applyMutationResponse(res.tunnels);
			if ((res.imported?.length ?? 0) > 0) {
				input = '';
				oncomplete?.(res.imported!.length);
			}
		} catch (e) {
			result = {
				imported: [],
				errors: [{ line: 0, input: '', error: e instanceof Error ? e.message : String(e) }],
				tunnels: [],
			};
		} finally {
			importing = false;
		}
	}
</script>

<div class="ghost-terminal">
	<div class="term-status">
		<span class="status-dot {statusTone}" aria-hidden="true"></span>
		<span class="term-info">{statusLine}</span>
	</div>

	{#if singboxInstalled}
	<div class="term-singbox">
		<textarea
			class="term-singbox-input"
			placeholder={`vless://uuid@host:443?...#Germany\nhysteria2://pass@host:8443#Finland\nnaive+https://u:p@host:443#Japan`}
			rows="5"
			bind:value={input}
		></textarea>

		<div class="term-commands">
			<button
				class="term-cmd term-cmd-primary"
				onclick={submit}
				disabled={!input.trim() || importing}
			>
				<span class="term-arrow">{'>'}</span>
				{importing ? 'импорт...' : 'импортировать ссылки'}
			</button>
		</div>

		{#if result}
			{#if (result.imported?.length ?? 0) > 0}
				<div class="term-singbox-success">
					Импортировано: {result.imported.length}
				</div>
			{/if}
			{#if (result.errors?.length ?? 0) > 0}
				<div class="term-singbox-errors">
					<strong>Ошибки: {result.errors.length}</strong>
					<ul>
						{#each result.errors ?? [] as e}
							<li>Строка {e.line}: {e.error}</li>
						{/each}
					</ul>
				</div>
			{/if}
		{/if}
	</div>
	{/if}
</div>

<style>
	.ghost-terminal {
		border: 1px dashed var(--border);
		border-radius: 10px;
		padding: 24px;
		font-family: var(--font-mono, monospace);
	}
	.term-status {
		display: flex;
		align-items: center;
		justify-content: center;
		gap: 0.5rem;
		margin-bottom: 20px;
	}
	.status-dot {
		width: 8px;
		height: 8px;
		border-radius: 999px;
		background: var(--text-muted);
		box-shadow: 0 0 0 3px rgba(148, 163, 184, 0.14);
	}
	.status-dot.ok {
		background: var(--success, #10b981);
		box-shadow: 0 0 0 3px rgba(16, 185, 129, 0.16);
	}
	.status-dot.ready {
		background: var(--primary, #60a5fa);
		box-shadow: 0 0 0 3px rgba(96, 165, 250, 0.16);
	}
	.status-dot.warn {
		background: var(--warning, #f59e0b);
		box-shadow: 0 0 0 3px rgba(245, 158, 11, 0.16);
	}
	.status-dot.error {
		background: var(--error, #ef4444);
		box-shadow: 0 0 0 3px rgba(239, 68, 68, 0.16);
	}
	.term-info {
		color: var(--text-muted);
		font-size: 12px;
	}
	.term-singbox {
		width: 100%;
	}
	.term-singbox-input {
		width: 100%;
		min-height: 220px;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: 6px;
		color: var(--text);
		padding: 12px;
		font-family: inherit;
		font-size: 12px;
		resize: vertical;
	}
	.term-singbox-input:focus {
		outline: none;
		border-color: var(--primary, #60a5fa);
	}
	.term-commands {
		display: flex;
		justify-content: flex-start;
		margin-top: 8px;
	}
	.term-cmd {
		background: none;
		border: 1px solid var(--border);
		color: var(--text);
		padding: 6px 14px;
		border-radius: 4px;
		font-family: inherit;
		font-size: 12px;
		cursor: pointer;
	}
	.term-cmd:hover:not(:disabled) {
		border-color: var(--primary, #60a5fa);
		color: var(--primary, #60a5fa);
	}
	.term-cmd:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}
	.term-cmd-primary {
		color: var(--primary, #60a5fa);
		border-color: rgba(96, 165, 250, 0.4);
	}
	.term-arrow {
		margin-right: 6px;
	}
	.term-singbox-success {
		padding: 10px 14px;
		margin-top: 12px;
		background: rgba(16, 185, 129, 0.1);
		border-left: 2px solid var(--success, #10b981);
		border-radius: 3px;
		font-size: 12px;
		color: var(--success, #10b981);
	}
	.term-singbox-errors {
		padding: 10px 14px;
		margin-top: 12px;
		background: rgba(239, 68, 68, 0.08);
		border-left: 2px solid var(--error, #ef4444);
		border-radius: 3px;
		font-size: 12px;
	}
	.term-singbox-errors ul {
		margin: 6px 0 0;
		padding-left: 20px;
	}
</style>
