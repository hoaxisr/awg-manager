<script lang="ts">
	import { api } from '$lib/api/client';
	import { systemInfo } from '$lib/stores/system';
	import { singboxTunnels } from '$lib/stores/singbox';
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

	const singboxVersion = $derived($systemInfo?.singbox?.version ?? '');

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
		<span class="term-prompt">$ sing-box status</span>
		<span class="term-info">
			{singboxVersion ? singboxVersion + ' · ' : ''}installed · 0 туннелей
		</span>
	</div>

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
</div>

<style>
	.ghost-terminal {
		border: 1px dashed var(--border);
		border-radius: 10px;
		padding: 24px;
		font-family: var(--font-mono, monospace);
	}
	.term-status {
		text-align: center;
		margin-bottom: 20px;
	}
	.term-prompt {
		display: block;
		color: var(--primary, #60a5fa);
		font-size: 14px;
		margin-bottom: 4px;
	}
	.term-info {
		color: var(--text-muted);
		font-size: 12px;
	}
	.term-singbox {
		max-width: 720px;
		margin: 0 auto;
	}
	.term-singbox-input {
		width: 100%;
		min-height: 160px;
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
