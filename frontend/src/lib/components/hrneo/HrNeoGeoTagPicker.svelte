<script lang="ts">
	import { api } from '$lib/api/client';
	import { Button } from '$lib/components/ui';
	import { m } from '$lib/i18n';

	interface GeoTag {
		name: string;
		count: number;
	}

	interface Props {
		kind: 'geosite' | 'geoip';
		files: string[];
		maxelem?: number;
		/** Уменьшенная высота списка тегов (для inline-редакторов в модалках). */
		compact?: boolean;
		onpick: (token: string) => void;
		onclose: () => void;
	}

	let { kind, files, maxelem = 0, compact = false, onpick, onclose }: Props = $props();

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

<div class="picker" class:compact role="dialog" aria-label={m.hrneo_geo_picker_aria({ kind })}>
	<div class="picker-header">
		<input
			class="form-input picker-search"
			type="text"
			placeholder={m.routing_singbox_geo_search_placeholder({ kind })}
			bind:value={query}
		/>
		<Button variant="ghost" size="sm" onclick={onclose}>{m.common_close()}</Button>
	</div>

	{#if files.length === 0}
		<div class="picker-empty">
			{m.routing_singbox_geo_no_files_prefix()} <code>{kind}.dat</code>{m.routing_singbox_geo_no_files_suffix()}
		</div>
	{:else if loading}
		<div class="picker-empty">{m.routing_singbox_geo_loading()}</div>
	{:else if filtered.length === 0}
		<div class="picker-empty">{m.routing_singbox_geo_nothing_found()}</div>
	{:else}
		<div class="picker-count">{m.routing_singbox_geo_tags_count({ count: filtered.length })}</div>
		<div class="picker-results">
			{#each filtered as r}
				{@const tooBig = kind === 'geoip' && maxelem > 0 && r.tag.count >= maxelem}
				<button
					type="button"
					class="picker-result"
					class:disabled-tag={tooBig}
					disabled={tooBig}
					title={tooBig ? m.routing_singbox_geo_too_big({ count: r.tag.count, max: maxelem }) : ''}
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

	.picker.compact {
		padding: 6px;
		gap: 4px;
		margin-bottom: 4px;
	}

	.picker.compact .picker-header {
		gap: 4px;
	}

	.picker.compact .picker-empty {
		padding: 8px;
		font-size: 0.75rem;
	}

	.picker.compact .picker-results {
		max-height: 160px;
		gap: 1px;
	}

	.picker.compact .picker-result {
		padding: 4px 8px;
		border-radius: 4px;
	}

	.picker.compact .result-meta {
		font-size: 0.625rem;
	}
</style>
