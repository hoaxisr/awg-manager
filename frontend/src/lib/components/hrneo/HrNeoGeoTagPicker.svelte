<script lang="ts">
	import { api } from '$lib/api/client';
	import { Button } from '$lib/components/ui';

	interface GeoTag {
		name: string;
		count: number;
	}

	interface Props {
		kind: 'geosite' | 'geoip';
		files: string[];
		maxelem?: number;
		onpick: (token: string) => void;
		onclose: () => void;
	}

	let { kind, files, maxelem = 0, onpick, onclose }: Props = $props();

	let query = $state('');
	let allTags = $state<Array<{ tag: GeoTag; file: string }>>([]);
	let loading = $state(false);

	$effect(() => {
		(async () => {
			loading = true;
			const collected: Array<{ tag: GeoTag; file: string }> = [];
			for (const f of files) {
				try {
					const tags = await api.getGeoTags(f);
					for (const t of tags) collected.push({ tag: t, file: f });
				} catch {
					/* file may have vanished */
				}
			}
			allTags = collected;
			loading = false;
		})();
	});

	let filtered = $derived.by(() => {
		const q = query.trim().toLowerCase();
		if (!q) return allTags;
		return allTags.filter((t) => t.tag.name.toLowerCase().includes(q));
	});

	function pick(tag: GeoTag) {
		onpick(`${kind}:${tag.name}`);
	}

	function fileName(p: string): string {
		return p.split('/').pop() ?? p;
	}
</script>

<div class="picker" role="dialog" aria-label="Выбор {kind} тега">
	<div class="picker-header">
		<input
			class="form-input picker-search"
			type="text"
			placeholder="Поиск {kind}:TAG…"
			bind:value={query}
		/>
		<Button variant="ghost" size="sm" onclick={onclose}>Закрыть</Button>
	</div>

	{#if files.length === 0}
		<div class="picker-empty">
			Нет загруженных файлов <code>{kind}.dat</code>. Добавьте их в разделе «Гео-данные».
		</div>
	{:else if loading}
		<div class="picker-empty">Загрузка тегов…</div>
	{:else if filtered.length === 0}
		<div class="picker-empty">Ничего не найдено</div>
	{:else}
		<div class="picker-count">{filtered.length} тегов</div>
		<div class="picker-results">
			{#each filtered as r}
				{@const tooBig = kind === 'geoip' && maxelem > 0 && r.tag.count >= maxelem}
				<button
					type="button"
					class="picker-result"
					class:disabled-tag={tooBig}
					disabled={tooBig}
					title={tooBig ? `Превышает лимит ipset: ${r.tag.count} ≥ ${maxelem}` : ''}
					onclick={() => pick(r.tag)}
				>
					<span class="result-name">{r.tag.name}</span>
					<span class="result-meta">{r.tag.count} · {fileName(r.file)}</span>
				</button>
			{/each}
		</div>
	{/if}
</div>

<style>
	.picker {
		background: var(--bg-secondary);
		border: 1px solid var(--accent);
		border-radius: 8px;
		padding: 10px;
		display: flex;
		flex-direction: column;
		gap: 8px;
		margin-bottom: 8px;
	}

	.picker-header {
		display: flex;
		gap: 6px;
		align-items: center;
	}

	.picker-search {
		flex: 1;
	}

	.picker-empty {
		color: var(--text-muted);
		font-size: 0.8125rem;
		padding: 14px;
		text-align: center;
		font-style: italic;
	}

	.picker-results {
		max-height: 320px;
		overflow-y: auto;
		display: flex;
		flex-direction: column;
		gap: 2px;
	}
	.picker-count {
		color: var(--text-muted);
		font-size: 0.6875rem;
		text-align: right;
		padding: 2px 4px;
	}

	.picker-result {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 8px;
		padding: 8px 10px;
		background: var(--bg-tertiary);
		border: 1px solid transparent;
		border-radius: 6px;
		cursor: pointer;
		font-family: inherit;
		text-align: left;
	}
	.picker-result:hover {
		border-color: var(--accent);
	}
	.picker-result.disabled-tag {
		opacity: 0.45;
		cursor: not-allowed;
	}
	.picker-result.disabled-tag:hover {
		border-color: transparent;
	}

	.result-name {
		font-weight: 600;
		color: var(--text-primary);
		font-family: ui-monospace, monospace;
	}

	.result-meta {
		color: var(--text-muted);
		font-size: 0.6875rem;
	}
</style>
