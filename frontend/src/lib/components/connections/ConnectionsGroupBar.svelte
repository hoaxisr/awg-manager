<script lang="ts">
	interface Props {
		group: 'none' | 'client' | 'host';
		visible: number;
		total: number;
		onChange: (g: 'none' | 'client' | 'host') => void;
	}

	let { group, visible, total, onChange }: Props = $props();

	const SEGMENTS = [
		['none', 'Нет'],
		['client', 'По клиенту'],
		['host', 'По хосту'],
	] as const;
</script>

<div class="group-bar">
	<span class="lbl">Группировка:</span>
	<div class="segments">
		{#each SEGMENTS as [val, label] (val)}
			<button type="button" class="segment" class:active={group === val} onclick={() => onChange(val)}>
				{label}
			</button>
		{/each}
	</div>
	<span class="visible">Видимо: <span class="num">{visible}</span> из <span class="num">{total}</span></span>
</div>

<style>
	.group-bar {
		display: flex;
		align-items: center;
		/* На 390px строка не влезает, и «По хосту» обрезался краем .segments
		   (overflow: hidden). Переносим счётчик на вторую строку. */
		flex-wrap: wrap;
		gap: 10px;
		margin-bottom: 10px;
		font-size: 12px;
	}
	.lbl { color: var(--color-text-muted); }
	.segments {
		display: inline-flex;
		border: 1px solid var(--color-border);
		border-radius: 6px;
		overflow: hidden;
	}
	.segment {
		all: unset;
		padding: 5px 12px;
		cursor: pointer;
		white-space: nowrap;
		color: var(--color-text-secondary);
	}
	.segment:hover { background: var(--color-bg-hover); }
	.segment.active {
		background: var(--color-accent);
		color: var(--color-bg-primary);
	}
	.visible {
		margin-left: auto;
		color: var(--color-text-muted);
		white-space: nowrap;
	}
	.num { font-family: var(--font-mono); font-variant-numeric: tabular-nums; }
</style>
