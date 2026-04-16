<script lang="ts">
	import type { OversizedTag } from '$lib/types';

	interface Props {
		tags: OversizedTag[];
		maxelem: number;
	}

	let { tags, maxelem }: Props = $props();

	function fmtCount(n: number): string {
		if (n < 0) return '?';
		return n.toLocaleString('ru-RU');
	}
</script>

<div class="disabled-pane">
	<header class="pane-header">
		<h2>Отключённые теги</h2>
		<span class="pane-meta">{tags.length}</span>
	</header>

	<div class="warn-banner">
		HR Neo исключил {tags.length}
		{tags.length === 1 ? 'тег' : 'тегов'} из маршрутизации — превышают
		<code>IpsetMaxElem = {fmtCount(maxelem)}</code>.
	</div>

	<div class="tag-list">
		{#each tags as t (t.name)}
			<div class="tag-row">
				<span class="tag-name">{t.name}</span>
				<span class="tag-count">{fmtCount(t.count)} записей</span>
			</div>
		{/each}
	</div>
</div>

<style>
	.disabled-pane {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.pane-header {
		display: flex;
		align-items: baseline;
		gap: 10px;
		padding-bottom: 10px;
		border-bottom: 1px solid var(--border);
	}
	.pane-header h2 {
		margin: 0;
		font-size: 1.0625rem;
		color: var(--text-primary);
	}
	.pane-meta {
		color: var(--text-muted);
		font-size: 0.8125rem;
	}

	.warn-banner {
		background: rgba(224, 175, 104, 0.1);
		border-left: 3px solid var(--warning);
		color: var(--text-primary);
		padding: 10px 12px;
		border-radius: 0 6px 6px 0;
		font-size: 0.8125rem;
	}
	.warn-banner code {
		background: var(--bg-tertiary);
		padding: 0 4px;
		border-radius: 3px;
		font-family: ui-monospace, monospace;
		font-size: 0.75rem;
	}

	.tag-list {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.tag-row {
		display: flex;
		justify-content: space-between;
		align-items: center;
		padding: 10px 12px;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: 6px;
	}

	.tag-name {
		font-family: ui-monospace, monospace;
		font-weight: 600;
		color: #bb8bff;
	}

	.tag-count {
		color: var(--text-muted);
		font-size: 0.8125rem;
		font-variant-numeric: tabular-nums;
	}
</style>
