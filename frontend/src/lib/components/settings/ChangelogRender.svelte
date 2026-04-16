<script lang="ts" module>
	// Inline markdown → HTML string. Order matters: escape HTML first, then
	// substitute backtick-code (so ** and * inside code aren't touched), then
	// bold, then italic. All three substitutions operate on escaped text, so
	// no user content reaches the browser as raw HTML.
	export function parseInline(text: string): string {
		const escaped = text
			.replace(/&/g, '&amp;')
			.replace(/</g, '&lt;')
			.replace(/>/g, '&gt;')
			.replace(/"/g, '&quot;')
			.replace(/'/g, '&#39;');
		return escaped
			.replace(/`([^`]+)`/g, '<code>$1</code>')
			.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
			.replace(/(^|[^*])\*([^*\s][^*]*?)\*(?!\*)/g, '$1<em>$2</em>')
			.replace(/(^|[^_])_([^_\s][^_]*?)_(?!_)/g, '$1<em>$2</em>');
	}
</script>

<script lang="ts">
	import type { ChangelogEntry } from '$lib/types';

	interface Props {
		entries: ChangelogEntry[];
	}

	let { entries }: Props = $props();

	const GROUP_LABELS: Record<string, string> = {
		Added: 'Добавлено',
		Fixed: 'Исправлено',
		Changed: 'Изменено',
		Removed: 'Удалено',
		Security: 'Безопасность',
		Breaking: 'Breaking changes',
	};

	function label(heading: string): string {
		return GROUP_LABELS[heading] ?? heading;
	}
</script>

<div class="changelog">
	{#each entries as e (e.version)}
		<section class="entry">
			<header class="entry-header">
				<h3>{e.version}</h3>
				<span class="entry-date">{e.date}</span>
			</header>
			{#each e.groups as g}
				<h4 class="group-heading">{label(g.heading)}</h4>
				<ul class="group-items">
					{#each g.items as item}
						<li>{@html parseInline(item)}</li>
					{/each}
				</ul>
			{/each}
		</section>
	{/each}
</div>

<style>
	.changelog {
		display: flex;
		flex-direction: column;
		gap: 16px;
	}
	.entry {
		padding-bottom: 12px;
		border-bottom: 1px solid var(--border);
	}
	.entry:last-child {
		border-bottom: none;
	}
	.entry-header {
		display: flex;
		align-items: baseline;
		justify-content: space-between;
		gap: 12px;
		margin-bottom: 8px;
	}
	.entry-header h3 {
		margin: 0;
		font-size: 1rem;
		color: var(--text-primary);
		font-weight: 600;
	}
	.entry-date {
		color: var(--text-muted);
		font-size: 0.8125rem;
		font-variant-numeric: tabular-nums;
	}
	.group-heading {
		margin: 8px 0 4px;
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--accent);
		text-transform: none;
	}
	.group-items {
		margin: 0 0 8px;
		padding-left: 20px;
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.group-items li {
		font-size: 0.875rem;
		color: var(--text-primary);
		line-height: 1.4;
	}
	.group-items :global(code) {
		background: var(--bg-tertiary);
		padding: 0 4px;
		border-radius: 3px;
		font-family: ui-monospace, monospace;
		font-size: 0.8125rem;
	}
	.group-items :global(strong) {
		color: var(--text-primary);
		font-weight: 600;
	}
</style>
