<script lang="ts">
	import { Copy } from 'lucide-svelte';
	import { notifications } from '$lib/stores/notifications';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { formatAboutSection, type AboutInfoRow } from '$lib/utils/about-device';

	interface Props {
		title: string;
		rows: AboutInfoRow[];
		loading?: boolean;
	}

	let { title, rows, loading = false }: Props = $props();

	async function copyBlock() {
		if (rows.length === 0) return;
		const ok = await copyToClipboard(formatAboutSection(title, rows));
		if (ok) {
			notifications.success(`Блок «${title}» скопирован`);
		} else {
			notifications.error('Не удалось скопировать');
		}
	}
</script>

<section class="card about-section" aria-labelledby={title.replace(/\s+/g, '-')}>
	<div class="about-section-head">
		<div class="about-section-title-row">
			<h2 class="about-section-title" id={title.replace(/\s+/g, '-')}>{title}</h2>
			<button
				type="button"
				class="about-copy-btn"
				onclick={copyBlock}
				disabled={rows.length === 0}
				aria-label="Скопировать блок «{title}»"
				title="Скопировать блок"
			>
				<Copy size={13} aria-hidden="true" />
			</button>
		</div>
		{#if loading}
			<span class="about-loading" aria-live="polite">обновление…</span>
		{/if}
	</div>

	<div class="about-rows">
		{#each rows as row (row.label)}
			<div class="setting-row about-row">
				<span class="about-key">{row.label}</span>
				<span class="about-val" class:about-val-mono={row.label !== 'User-Agent'} title={row.title}>{row.value}</span>
			</div>
		{/each}
	</div>
</section>

<style>
	.about-section {
		padding: 0.75rem 0.875rem;
		min-width: 0;
	}

	.about-section-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
		margin-bottom: 0.25rem;
		padding-bottom: 0.4rem;
		border-bottom: 1px solid var(--color-border, var(--border));
	}

	.about-section-title-row {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		min-width: 0;
	}

	.about-section-title {
		margin: 0;
		font-size: 0.6875rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted, var(--text-muted));
	}

	.about-copy-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 22px;
		height: 22px;
		padding: 0;
		border: none;
		border-radius: var(--radius-sm, 4px);
		background: transparent;
		color: var(--color-text-muted, var(--text-muted));
		cursor: pointer;
		flex-shrink: 0;
	}

	.about-copy-btn:hover:not(:disabled) {
		color: var(--color-accent, var(--accent));
		background: var(--color-bg-hover, var(--bg-hover));
	}

	.about-copy-btn:disabled {
		opacity: 0.35;
		cursor: not-allowed;
	}



	.about-loading {
		font-size: 0.6875rem;
		font-family: var(--font-mono);
		color: var(--color-text-muted, var(--text-muted));
		flex-shrink: 0;
	}

	.about-rows :global(.about-row) {
		align-items: flex-start;
		padding-block: 0.4rem;
		gap: 0.625rem;
	}

	.about-rows :global(.about-row:first-child) {
		padding-top: 0.15rem;
	}

	.about-rows :global(.about-row:last-child) {
		padding-bottom: 0;
	}

	.about-key {
		flex: 1 1 38%;
		min-width: 0;
		font-size: 0.75rem;
		color: var(--color-text-secondary, var(--text-secondary));
	}

	.about-val {
		flex: 1 1 58%;
		min-width: 0;
		font-size: 0.75rem;
		color: var(--color-text-primary, var(--text-primary));
		text-align: right;
		word-break: break-word;
	}

	.about-val-mono {
		font-family: var(--font-mono);
		font-size: 0.6875rem;
		color: var(--color-text-secondary, var(--text-secondary));
	}

	@media (max-width: 640px) {
		.about-rows :global(.about-row) {
			flex-direction: column;
			align-items: stretch;
			gap: 0.25rem;
		}

		.about-key,
		.about-val {
			flex: initial;
			width: 100%;
		}

		.about-val {
			text-align: left;
			overflow-wrap: anywhere;
			word-break: normal;
			line-height: 1.45;
		}
	}
</style>
