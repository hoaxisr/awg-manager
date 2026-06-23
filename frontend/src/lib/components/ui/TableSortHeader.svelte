<script lang="ts" generics="T extends string = string">
	interface Props {
		label: string;
		sortKey: T;
		activeSortKey: T | null;
		sortAsc: boolean;
		onchange: (key: T) => void;
	}

	let { label, sortKey, activeSortKey, sortAsc, onchange }: Props = $props();

	const active = $derived(activeSortKey === sortKey);
</script>

<button
	type="button"
	class="sort-header-btn"
	class:active
	aria-pressed={active}
	onclick={() => onchange(sortKey)}
	title={`Сортировать по колонке «${label}». Повторный клик — смена направления, третий — исходный порядок`}
>
	<span>{label}</span>
	<span class="sort-indicator" aria-hidden="true">
		{#if active}
			{sortAsc ? '↑' : '↓'}
		{:else}
			↕
		{/if}
	</span>
</button>

<style>
	.sort-header-btn {
		/* inline-flex без width:100% → кнопка inline-level и наследует text-align <th>,
		   чтобы ярлык совпадал с выравниванием данных в колонке (а не центрировался) */
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		padding: 0;
		border: 0;
		background: transparent;
		color: inherit;
		font: inherit;
		font-weight: inherit;
		text-transform: inherit;
		letter-spacing: inherit;
		cursor: pointer;
	}

	.sort-header-btn:hover {
		color: var(--text-primary, var(--color-text-primary));
	}

	.sort-header-btn.active {
		color: var(--accent, var(--color-accent));
	}

	.sort-indicator {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 1em;
		height: 1em;
		flex: 0 0 auto;
		line-height: 1;
		opacity: 0.65;
		font-size: 0.8em;
	}

	.sort-header-btn.active .sort-indicator {
		opacity: 1;
	}
</style>
