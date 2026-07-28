<script lang="ts">
	export interface WizardGuideItem {
		id: string;
		text: string;
		done?: boolean;
		active?: boolean;
		pending?: boolean;
	}

	interface Props {
		title?: string;
		items: WizardGuideItem[];
	}

	let { title = 'Порядок настройки', items }: Props = $props();

	const activeItem = $derived(items.find((i) => i.active && !i.done));
	const doneCount = $derived(items.filter((i) => i.done).length);
	const allDone = $derived(items.length > 0 && doneCount === items.length);
</script>

{#if items.length > 0}
	<aside class="wizard-guide" aria-label={title}>
		<div class="wizard-guide-head">
			<div class="wizard-guide-title">{title}</div>
			<span class="wizard-guide-count">{doneCount}/{items.length}</span>
		</div>
		{#if activeItem}
			<div class="wizard-guide-now">
				<span class="wizard-guide-now-label">Сейчас</span>
				{activeItem.text}
			</div>
		{:else if allDone}
			<div class="wizard-guide-now wizard-guide-now--ready">
				<span class="wizard-guide-now-label">Готово</span>
				Все пункты шага выполнены — переходите дальше
			</div>
		{/if}
		<ol class="wizard-guide-list">
			{#each items as item, idx (item.id)}
				<li
					class="wizard-guide-item"
					class:done={item.done}
					class:active={item.active && !item.done}
					class:pending={item.pending && !item.done}
				>
					<span class="wizard-guide-num" aria-hidden="true">{item.done ? '✓' : idx + 1}</span>
					<span class="wizard-guide-text">{item.text}</span>
				</li>
			{/each}
		</ol>
	</aside>
{/if}

<style>
	.wizard-guide {
		padding: 0.625rem 0.75rem;
		border-radius: var(--radius-sm);
		border: 1px solid color-mix(in srgb, var(--color-accent, #3b82f6) 28%, var(--color-border));
		background: color-mix(in srgb, var(--color-accent, #3b82f6) 6%, var(--color-bg-primary));
	}

	.wizard-guide-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
		margin-bottom: 0.5rem;
	}

	.wizard-guide-title {
		font-size: 0.6875rem;
		font-weight: 700;
		letter-spacing: 0.04em;
		text-transform: uppercase;
		color: var(--color-text-secondary);
	}

	.wizard-guide-count {
		font-size: 0.6875rem;
		font-weight: 600;
		font-family: var(--font-mono);
		color: var(--color-text-secondary);
	}

	.wizard-guide-now {
		margin-bottom: 0.625rem;
		padding: 0.5rem 0.625rem;
		border-radius: var(--radius-sm);
		background: color-mix(in srgb, var(--color-accent, #3b82f6) 12%, var(--color-bg-primary));
		border: 1px solid color-mix(in srgb, var(--color-accent, #3b82f6) 35%, var(--color-border));
		font-size: 0.8125rem;
		line-height: 1.45;
		color: var(--color-text-primary);
		font-weight: 500;
	}

	.wizard-guide-now-label {
		display: block;
		font-size: 0.625rem;
		font-weight: 700;
		letter-spacing: 0.05em;
		text-transform: uppercase;
		color: var(--color-accent, #3b82f6);
		margin-bottom: 0.2rem;
	}

	.wizard-guide-now--ready {
		border-color: color-mix(in srgb, var(--color-success, #2e7d32) 35%, var(--color-border));
		background: color-mix(in srgb, var(--color-success, #2e7d32) 8%, var(--color-bg-primary));
	}

	.wizard-guide-now--ready .wizard-guide-now-label {
		color: var(--color-success, #2e7d32);
	}

	.wizard-guide-list {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 0.3rem;
	}

	.wizard-guide-item {
		display: flex;
		align-items: flex-start;
		gap: 0.5rem;
		font-size: 0.8125rem;
		line-height: 1.4;
		color: var(--color-text-secondary);
	}

	.wizard-guide-item.active {
		color: var(--color-text-primary);
		font-weight: 500;
	}

	.wizard-guide-item.done {
		opacity: 0.65;
	}

	.wizard-guide-item.done .wizard-guide-text {
		text-decoration: line-through;
	}

	.wizard-guide-item.pending {
		opacity: 0.45;
	}

	.wizard-guide-num {
		flex-shrink: 0;
		width: 1.125rem;
		height: 1.125rem;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		border-radius: 50%;
		font-size: 0.6875rem;
		font-weight: 700;
		background: var(--color-bg-tertiary);
		color: var(--color-text-secondary);
	}

	.wizard-guide-item.active .wizard-guide-num {
		background: var(--color-accent, #3b82f6);
		color: #fff;
	}

	.wizard-guide-item.done .wizard-guide-num {
		background: var(--color-success-bg, rgba(46, 125, 50, 0.15));
		color: var(--color-success, #2e7d32);
	}
</style>
