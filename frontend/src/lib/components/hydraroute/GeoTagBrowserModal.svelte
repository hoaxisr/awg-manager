<script lang="ts">
	import { Modal } from '$lib/components/ui';
	import type { GeoTag } from '$lib/types';

	interface Props {
		open: boolean;
		title: string;
		tags: GeoTag[];
		fileType: 'geosite' | 'geoip';
		ipsetMaxElem?: number;
		ifaceUsage?: number;
		ifaceName?: string;
		onclose: () => void;
	}

	let {
		open,
		title,
		tags,
		fileType,
		ipsetMaxElem = 65536,
		ifaceUsage = 0,
		ifaceName = '',
		onclose,
	}: Props = $props();

	let search = $state('');
	let copiedTag = $state<string | null>(null);

	const prefix = $derived(fileType === 'geosite' ? 'geosite' : 'geoip');
	const unit = $derived(fileType === 'geosite' ? 'доменов' : 'подсетей');

	const filtered = $derived(
		search.trim()
			? tags.filter((t) => t.name.toLowerCase().includes(search.trim().toLowerCase()))
			: tags
	);

	const usagePercent = $derived(
		ipsetMaxElem > 0 ? Math.min(100, Math.round((ifaceUsage / ipsetMaxElem) * 100)) : 0
	);

	const usageColor = $derived(
		usagePercent >= 90 ? 'var(--error, #ef4444)' :
		usagePercent >= 70 ? 'var(--warning, #f59e0b)' :
		'var(--accent, #3b82f6)'
	);

	function wouldExceed(tag: GeoTag): boolean {
		return fileType === 'geoip' && (ifaceUsage + tag.count) > ipsetMaxElem;
	}

	function exceedsAlone(tag: GeoTag): boolean {
		return fileType === 'geoip' && tag.count > ipsetMaxElem;
	}

	function warningText(tag: GeoTag): string {
		if (exceedsAlone(tag)) return 'Превышает лимит ipset';
		return `Нет места (${ifaceUsage + tag.count} > ${ipsetMaxElem})`;
	}

	async function copyTag(tag: GeoTag) {
		const text = `${prefix}:${tag.name}`;
		try {
			await navigator.clipboard.writeText(text);
			copiedTag = tag.name;
			setTimeout(() => { copiedTag = null; }, 1500);
		} catch {
			// clipboard not available
		}
	}

	function handleClose() {
		search = '';
		onclose();
	}
</script>

<Modal {open} {title} size="lg" onclose={handleClose}>
	<div class="modal-content">
		{#if fileType === 'geoip' && ifaceName}
			<div class="ipset-bar-wrap">
				<div class="ipset-bar-label">
					Использование ipset для <strong>{ifaceName}</strong>: {ifaceUsage.toLocaleString()} / {ipsetMaxElem.toLocaleString()}
				</div>
				<div class="ipset-bar-track">
					<div
						class="ipset-bar-fill"
						style="width: {usagePercent}%; background: {usageColor};"
					></div>
				</div>
				<div class="ipset-bar-pct" style="color: {usageColor};">{usagePercent}%</div>
			</div>
		{/if}

		<div class="search-row">
			<input
				class="search-input"
				type="text"
				placeholder="Поиск тега..."
				bind:value={search}
			/>
			<span class="tag-count">
				{#if search.trim()}
					{filtered.length} из {tags.length} тегов
				{:else}
					{tags.length} тегов
				{/if}
			</span>
		</div>

		<div class="tag-list">
			{#each filtered as tag (tag.name)}
				{@const exceeded = wouldExceed(tag)}
				<div class="tag-row" class:tag-row--exceeded={exceeded}>
					<span class="tag-name" class:tag-name--exceeded={exceeded}>{tag.name}</span>
					<span class="tag-meta">
						{tag.count.toLocaleString()} {unit}
						{#if exceeded}
							<span class="tag-warning">{warningText(tag)}</span>
						{/if}
					</span>
					{#if !exceeded}
						<button
							class="btn-copy"
							onclick={() => copyTag(tag)}
							title="Скопировать {prefix}:{tag.name}"
						>
							{copiedTag === tag.name ? 'OK!' : 'Copy'}
						</button>
					{/if}
				</div>
			{/each}
			{#if filtered.length === 0}
				<div class="tag-empty">Ничего не найдено</div>
			{/if}
		</div>
	</div>
</Modal>

<style>
	.modal-content {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
	}

	/* ipset usage bar */
	.ipset-bar-wrap {
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm, 4px);
		padding: 0.625rem 0.75rem;
		display: flex;
		flex-direction: column;
		gap: 0.375rem;
	}

	.ipset-bar-label {
		font-size: 0.8125rem;
		color: var(--text-secondary);
	}

	.ipset-bar-track {
		height: 6px;
		background: var(--bg-tertiary);
		border-radius: 3px;
		overflow: hidden;
	}

	.ipset-bar-fill {
		height: 100%;
		border-radius: 3px;
		transition: width 0.3s ease, background 0.3s ease;
	}

	.ipset-bar-pct {
		font-size: 0.75rem;
		font-weight: 600;
		align-self: flex-end;
		margin-top: -0.25rem;
	}

	/* search row */
	.search-row {
		display: flex;
		align-items: center;
		gap: 0.75rem;
	}

	.search-input {
		flex: 1;
		padding: 0.375rem 0.625rem;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm, 4px);
		color: var(--text-primary);
		font-size: 0.875rem;
		outline: none;
	}

	.search-input:focus {
		border-color: var(--accent);
	}

	.tag-count {
		font-size: 0.8125rem;
		color: var(--text-muted);
		white-space: nowrap;
		flex-shrink: 0;
	}

	/* tag list */
	.tag-list {
		display: flex;
		flex-direction: column;
		border: 1px solid var(--border);
		border-radius: var(--radius-sm, 4px);
		overflow: hidden;
		max-height: 420px;
		overflow-y: auto;
	}

	.tag-row {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		padding: 0.4rem 0.75rem;
		border-bottom: 1px solid var(--border);
		background: var(--bg-secondary);
	}

	.tag-row:last-child {
		border-bottom: none;
	}

	.tag-row:nth-child(odd) {
		background: var(--bg-primary);
	}

	.tag-row--exceeded {
		opacity: 0.5;
	}

	.tag-name {
		flex: 0 0 auto;
		font-family: var(--font-mono, monospace);
		font-size: 0.8125rem;
		color: var(--text-primary);
		min-width: 180px;
	}

	.tag-name--exceeded {
		text-decoration: line-through;
		color: var(--text-muted);
	}

	.tag-meta {
		flex: 1;
		font-size: 0.75rem;
		color: var(--text-muted);
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.tag-warning {
		font-size: 0.7rem;
		color: var(--error, #ef4444);
		font-style: italic;
	}

	.btn-copy {
		flex-shrink: 0;
		padding: 0.2rem 0.5rem;
		font-size: 0.75rem;
		background: transparent;
		border: 1px solid var(--border);
		border-radius: var(--radius-sm, 4px);
		color: var(--text-secondary);
		cursor: pointer;
		transition: background 0.15s, color 0.15s;
		min-width: 3.5rem;
	}

	.btn-copy:hover {
		background: var(--bg-hover);
		color: var(--text-primary);
	}

	.tag-empty {
		padding: 1.5rem;
		text-align: center;
		font-size: 0.875rem;
		color: var(--text-muted);
	}
</style>
