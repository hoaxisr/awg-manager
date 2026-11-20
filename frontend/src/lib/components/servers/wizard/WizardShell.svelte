<script lang="ts">
	import type { Snippet } from 'svelte';
	import { Modal, Button } from '$lib/components/ui';
	import { ChevronRight, ChevronLeft, Check } from 'lucide-svelte';

	interface Step {
		id: string;
		label: string;
	}

	interface Props {
		open: boolean;
		title: string;
		subtitle?: string;
		steps: Step[];
		currentStepIndex: number;
		canNext?: boolean;
		canBack?: boolean;
		nextLabel?: string;
		backLabel?: string;
		loading?: boolean;
		hideFooter?: boolean;
		onnext?: () => void;
		onback?: () => void;
		onclose: () => void;
		children: Snippet;
	}

	let {
		open = $bindable(false),
		title,
		subtitle = '',
		steps,
		currentStepIndex,
		canNext = true,
		canBack = true,
		nextLabel = 'Далее',
		backLabel = 'Назад',
		loading = false,
		hideFooter = false,
		onnext,
		onback,
		onclose,
		children
	}: Props = $props();
</script>

<Modal {open} {title} {onclose} size="xl" bodyLayout="fill">
	<div class="wizard-container">
		<!-- Subtitle if present -->
		{#if subtitle}
			<div class="wizard-subtitle">
				<p class="subtitle-text">{subtitle}</p>
			</div>
		{/if}

		<!-- Step progress rail -->
		{#if steps.length > 1}
			<div class="wizard-stepper">
				<div class="mobile-step-indicator">
					<span class="mobile-step-count">Шаг {currentStepIndex + 1} из {steps.length}</span>
					<span class="mobile-step-title">
						{(steps[currentStepIndex]?.label || '').replace(/^\d+\.\s*/, '')}
					</span>
				</div>
				<div class="stepper-row">
					{#each steps as step, idx}
						<div class="step-item" class:active={idx === currentStepIndex} class:done={idx < currentStepIndex}>
							<div class="step-num">
								{#if idx < currentStepIndex}
									<Check size={12} strokeWidth={3} />
								{:else}
									{idx + 1}
								{/if}
							</div>
							<span class="step-label">
								{step.label.replace(/^\d+\.\s*/, '')}
							</span>
						</div>
						{#if idx < steps.length - 1}
							<div class="step-line" class:done={idx < currentStepIndex}></div>
						{/if}
					{/each}
				</div>
			</div>
		{/if}

		<!-- Scrollable Content Body -->
		<div class="wizard-body">
			{@render children()}
		</div>

		<!-- Footer navigation -->
		{#if !hideFooter}
			<div class="wizard-footer">
				<div>
					{#if currentStepIndex > 0 && canBack}
						<Button variant="secondary" onclick={onback} disabled={loading}>
							<ChevronLeft size={16} class="mr-1" />
							{backLabel}
						</Button>
					{/if}
				</div>
				<div class="wizard-footer-actions">
					<Button variant="ghost" onclick={onclose} disabled={loading}>
						Отмена
					</Button>
					{#if onnext}
						<Button variant="primary" onclick={onnext} disabled={!canNext || loading}>
							{#if loading}
								<span class="btn-spinner"></span>
							{/if}
							{nextLabel}
							{#if !loading && nextLabel === 'Далее'}
								<ChevronRight size={16} class="ml-1" />
							{/if}
						</Button>
					{/if}
				</div>
			</div>
		{/if}
	</div>
</Modal>

<style>
	.wizard-container {
		display: flex;
		flex-direction: column;
		height: 100%;
		min-height: 0;
		background: var(--color-bg-secondary);
	}

	.wizard-subtitle {
		padding: 0.625rem 1.5rem;
		background: var(--color-bg-secondary);
		border-bottom: 1px solid var(--color-border);
	}

	.subtitle-text {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		margin: 0;
		line-height: 1.4;
	}

	.wizard-stepper {
		padding: 0.65rem 1.25rem;
		background: var(--color-bg-primary);
		border-bottom: 1px solid var(--color-border);
		flex-shrink: 0;
	}

	.mobile-step-indicator {
		display: none;
		align-items: baseline;
		justify-content: space-between;
		gap: 0.5rem;
		margin-bottom: 0.5rem;
	}

	.mobile-step-count {
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--color-accent);
		white-space: nowrap;
	}

	.mobile-step-title {
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--color-text-primary);
		text-align: right;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.stepper-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.25rem;
	}

	.step-item {
		display: flex;
		align-items: center;
		gap: 0.35rem;
		min-width: 0;
		flex-shrink: 1;
	}

	.step-num {
		width: 20px;
		height: 20px;
		flex-shrink: 0;
		border-radius: 50%;
		display: flex;
		align-items: center;
		justify-content: center;
		font-size: 0.6875rem;
		font-weight: 600;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		color: var(--color-text-muted);
		transition: all var(--t-fast) ease;
	}

	.step-item.active .step-num {
		background: var(--color-accent);
		border-color: var(--color-accent);
		color: #ffffff;
		box-shadow: 0 0 0 2px var(--color-accent-tint);
	}

	.step-item.done .step-num {
		background: var(--color-success-tint);
		border-color: var(--color-success-border);
		color: var(--color-success);
	}

	.step-label {
		font-size: 0.6875rem;
		font-weight: 500;
		color: var(--color-text-muted);
		transition: color var(--t-fast) ease;
		line-height: 1.15;
	}

	@media (max-width: 640px) {
		.wizard-subtitle {
			padding: 0.5rem 1rem;
		}
		.wizard-stepper {
			padding: 0.625rem 1rem;
		}
		.mobile-step-indicator {
			display: flex;
		}
		.step-label {
			display: none;
		}
		.wizard-body {
			padding: 1rem;
			overflow-x: hidden;
		}
		.wizard-footer {
			padding: 0.75rem 1rem;
			flex-shrink: 0;
		}
	}

	.step-item.active .step-label {
		color: var(--color-text-primary);
		font-weight: 600;
	}

	.step-item.done .step-label {
		color: var(--color-text-secondary);
	}

	.step-line {
		flex: 1;
		height: 2px;
		margin: 0 0.375rem;
		background: var(--color-border);
		transition: background var(--t-fast) ease;
		min-width: 8px;
	}

	.step-line.done {
		background: var(--color-accent);
	}

	.wizard-body {
		flex: 1;
		overflow-y: auto;
		padding: 1.5rem;
		background: var(--color-bg-secondary);
	}

	.wizard-footer {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 0.875rem 1.5rem;
		background: var(--color-bg-secondary);
		border-top: 1px solid var(--color-border);
	}

	.wizard-footer-actions {
		display: flex;
		align-items: center;
		gap: 0.75rem;
	}

	.btn-spinner {
		display: inline-block;
		width: 14px;
		height: 14px;
		border: 2px solid rgba(255, 255, 255, 0.3);
		border-top-color: #ffffff;
		border-radius: 50%;
		animation: spin 0.8s linear infinite;
		margin-right: 0.5rem;
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}
</style>
