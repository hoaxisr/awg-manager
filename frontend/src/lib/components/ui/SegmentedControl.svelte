<script lang="ts" generics="T extends string">
	import ViewLayoutIcon from './ViewLayoutIcon.svelte';
	import type { SegmentedOption } from './segmentedControl';

	interface Props {
		value: T;
		options: SegmentedOption<T>[];
		ariaLabel: string;
		disabled?: boolean;
		/** Icon-only buttons (28px); label used for aria-label and title. */
		variant?: 'text' | 'icon';
		/** Растянуть на ширину контейнера, кнопки делят её поровну. */
		fullWidth?: boolean;
		onchange: (value: T) => void;
	}

	let {
		value,
		options,
		ariaLabel,
		disabled = false,
		variant = 'text',
		fullWidth = false,
		onchange,
	}: Props = $props();

	const isIcon = $derived(variant === 'icon' || options.some((o) => o.icon != null));

	function isOptionDisabled(option: SegmentedOption<T>): boolean {
		return disabled || !!option.disabled;
	}

	function select(next: T) {
		const option = options.find((o) => o.value === next);
		if (!option || isOptionDisabled(option) || next === value) return;
		onchange(next);
	}
</script>

<div
	class="segmented-control"
	class:segmented-control--icon={isIcon}
	class:segmented-control--full={fullWidth}
	role="group"
	aria-label={ariaLabel}
>
	{#each options as option (option.value)}
		<button
			type="button"
			class="segmented-control-btn"
			class:active={value === option.value}
			aria-pressed={value === option.value}
			aria-label={isIcon ? option.label : undefined}
			title={isIcon ? option.label : undefined}
			disabled={isOptionDisabled(option)}
			onclick={() => select(option.value)}
		>
			{#if option.icon}
				<ViewLayoutIcon name={option.icon} />
			{:else}
				{option.label}
			{/if}
		</button>
	{/each}
</div>

<style>
	.segmented-control {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
		box-sizing: border-box;
		height: 32px;
		padding: 2px;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
		flex-shrink: 0;
	}

	.segmented-control-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		height: 26px;
		padding: 0 12px;
		border: none;
		border-radius: calc(var(--radius-sm) - 2px);
		background: transparent;
		color: var(--color-text-muted);
		font-size: 12px;
		font-weight: 500;
		font-family: inherit;
		cursor: pointer;
		white-space: nowrap;
		transition:
			background var(--t-fast) ease,
			color var(--t-fast) ease;
	}

	.segmented-control--icon .segmented-control-btn {
		width: 28px;
		padding: 0;
	}

	.segmented-control-btn:hover:not(:disabled) {
		background: var(--color-bg-hover);
		color: var(--color-text-primary);
	}

	.segmented-control-btn.active {
		background: var(--color-accent-tint);
		color: var(--color-accent);
		font-weight: 600;
	}

	.segmented-control-btn:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
	}

	.segmented-control-btn:disabled {
		opacity: 0.6;
		cursor: not-allowed;
	}

	.segmented-control--full {
		display: flex;
		width: 100%;
		min-width: 0;
	}

	.segmented-control--full .segmented-control-btn {
		flex: 1 1 0;
		min-width: 0;
		padding-inline: 0.5rem;
	}

	/* Кнопки-текст: min-width:0 даёт им сжаться уже надписи, а nowrap-текст
	   иначе наползает на соседа — обрезаем многоточием (см. мобильный блок). */
	.segmented-control--full:not(.segmented-control--icon) .segmented-control-btn {
		display: block;
		line-height: 26px;
		text-align: center;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	@media (max-width: 640px) {
		.segmented-control {
			display: flex;
			width: 100%;
			min-width: 0;
		}

		.segmented-control-btn {
			flex: 1 1 0;
			min-width: 0;
			padding-inline: 0.5rem;
		}

		/* Text buttons: min-width:0 lets them shrink below the label's
		   intrinsic width, and nowrap text would otherwise spill over the
		   neighbouring button (labels visually overlapping). Clip with an
		   ellipsis instead. display:block because text-overflow does not
		   apply to text inside a flex container; line-height keeps the
		   26px vertical centering. Icon buttons keep their flex layout. */
		.segmented-control:not(.segmented-control--icon) .segmented-control-btn {
			display: block;
			line-height: 26px;
			text-align: center;
			overflow: hidden;
			text-overflow: ellipsis;
			white-space: nowrap;
		}

		.segmented-control--icon .segmented-control-btn {
			flex: 1 1 28px;
			min-width: 28px;
			width: auto;
			padding: 0;
		}
	}
</style>
