<script lang="ts">
	import type { Snippet } from 'svelte';
	import { Button } from '$lib/components/ui';

	interface Props {
		running?: boolean;
		meta?: string;
		metaExtra?: Snippet;
		saving?: boolean;
		starting?: boolean;
		canSave?: boolean;
		canStart?: boolean;
		saveLabel?: string;
		showWizardButton?: boolean;
		wizardLabel?: string;
		onSave?: () => void | Promise<void>;
		onToggle?: (on: boolean) => void | Promise<void>;
		onOpenWizard?: () => void;
	}

	let {
		running = false,
		meta = '',
		metaExtra,
		saving = false,
		starting = false,
		canSave = true,
		canStart = true,
		saveLabel = 'Сохранить',
		showWizardButton = false,
		wizardLabel = 'Мастер',
		onSave,
		onToggle,
		onOpenWizard
	}: Props = $props();
</script>

<div class="proxy-status-bar">
	<div class="proxy-status-left">
		<span class="proxy-status-dot" class:running aria-hidden="true"></span>
		<span class="proxy-status-label">{running ? 'Запущен' : 'Остановлен'}</span>
		{#if meta}
			<span class="proxy-status-meta">{meta}</span>
		{/if}
		{#if metaExtra}
			<span class="proxy-status-meta-extra">
				{@render metaExtra()}
			</span>
		{/if}
	</div>
	<div class="proxy-status-actions">
		{#if showWizardButton && onOpenWizard}
			<Button variant="ghost" size="sm" onclick={() => onOpenWizard?.()}>
				{wizardLabel}
			</Button>
		{/if}
		{#if onSave}
			<Button variant="secondary" size="sm" loading={saving} disabled={!canSave} onclick={() => onSave?.()}>
				{saveLabel}
			</Button>
		{/if}
		{#if onToggle}
			{#if running}
				<Button variant="danger" size="sm" disabled={starting} onclick={() => onToggle(false)}>
					Остановить
				</Button>
			{:else}
				<Button variant="primary" size="sm" loading={starting} disabled={!canStart} onclick={() => onToggle(true)}>
					Запустить
				</Button>
			{/if}
		{/if}
	</div>
</div>

<style>
	.proxy-status-bar {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		padding: 0.625rem 0.875rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
	}

	.proxy-status-left {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.5rem 0.75rem;
		min-width: 0;
	}

	.proxy-status-dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--color-text-tertiary);
		flex-shrink: 0;
	}

	.proxy-status-dot.running {
		background: var(--color-success, #2e7d32);
	}

	.proxy-status-label {
		font-size: 0.875rem;
		font-weight: 600;
	}

	.proxy-status-meta {
		font-size: 0.75rem;
		font-family: var(--font-mono);
		color: var(--color-text-secondary);
	}

	.proxy-status-meta-extra {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
	}

	.proxy-status-actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
	}
</style>
