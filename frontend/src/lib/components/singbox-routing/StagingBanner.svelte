<script lang="ts">
	import { fly } from 'svelte/transition';
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { api } from '$lib/api/client';
	import { Button, Modal } from '$lib/components/ui';
	import { formatTime } from '$lib/utils/format';
	import { stripAnsi } from '$lib/utils/ansi';
	import { triggerSelectiveRebuildIfEnabled } from '$lib/components/sb-router/selectiveRebuildTrigger';
	import type { RouterStagingValidationError, RouterValidationErrorDTO } from '$lib/types';

	const stagingStore = singboxRouter.staging;
	const staging = $derived($stagingStore);
	const hasDraft = $derived(staging?.hasDraft === true);
	const draftedAt = $derived(staging?.draftedAt ? new Date(staging.draftedAt) : null);

	let applying = $state(false);
	let discarding = $state(false);
	let inlineErrors = $state<RouterValidationErrorDTO[] | null>(null);
	let inlineSbCheck = $state<string | null>(null);
	let confirmDiscard = $state(false);

	async function onApply(): Promise<void> {
		if (applying) return;
		applying = true;
		inlineErrors = null;
		inlineSbCheck = null;
		try {
			await api.singboxRouterStagingApply();
			// Rebuild ipset once after Apply (opens progress modal inside trigger).
			setTimeout(() => void triggerSelectiveRebuildIfEnabled(), 400);
		} catch (e: unknown) {
			const err = e as { status?: number; body?: RouterStagingValidationError };
			if (err.status === 422 && err.body?.validation) {
				inlineErrors = err.body.validation.errors;
			} else if (err.status === 422 && err.body?.sbCheck) {
				inlineSbCheck = stripAnsi(err.body.sbCheck);
			} else {
				inlineSbCheck = String(e);
			}
		} finally {
			applying = false;
		}
	}

	async function onDiscard(): Promise<void> {
		if (discarding) return;
		discarding = true;
		try {
			await api.singboxRouterStagingDiscard();
		} finally {
			discarding = false;
			confirmDiscard = false;
		}
	}

	function formatDrafted(d: Date | null): string {
		if (!d) return '';
		return `с ${formatTime(d.toISOString())}`;
	}

	const hasErrors = $derived(!!(inlineErrors || inlineSbCheck));

	const missingRuleSetTag = $derived(
		inlineSbCheck
			? (inlineSbCheck.match(/rule-set not found:\s*([^\s\n]+)/)?.[1] ?? null)
			: null,
	);

	let inlineEl = $state<HTMLDivElement | undefined>();
	let showFloating = $state(false);
	let floatInset = $state({ left: 0, width: 0 });

	function syncFloatInset(): void {
		if (!inlineEl) return;
		const rect = inlineEl.getBoundingClientRect();
		floatInset = { left: rect.left, width: rect.width };
	}

	$effect(() => {
		const el = inlineEl;
		if (!el || !hasDraft || hasErrors || typeof IntersectionObserver === 'undefined') {
			showFloating = false;
			return;
		}

		// Скролл живёт в .shell-content (<main>), а не в окне — считаем уход
		// баннера относительно верха этого контейнера.
		const root = el.closest('main');

		const observer = new IntersectionObserver(
			([entry]) => {
				const rootTop = entry.rootBounds?.top ?? 0;
				const passed = !entry.isIntersecting && entry.boundingClientRect.bottom <= rootTop;
				if (passed) syncFloatInset();
				showFloating = passed;
			},
			{ root, threshold: 0 },
		);

		observer.observe(el);
		return () => observer.disconnect();
	});

	$effect(() => {
		if (!showFloating) return;
		syncFloatInset();
		const onLayout = () => syncFloatInset();
		window.addEventListener('resize', onLayout);
		// capture: скролл контейнера не всплывает до window.
		window.addEventListener('scroll', onLayout, { passive: true, capture: true });
		return () => {
			window.removeEventListener('resize', onLayout);
			window.removeEventListener('scroll', onLayout, { capture: true });
		};
	});
</script>

{#snippet bannerActions()}
	<Button
		variant="ghost"
		size="sm"
		disabled={applying || discarding}
		onclick={() => (confirmDiscard = true)}
	>
		Сбросить
	</Button>
	<Button variant="primary" size="sm" disabled={applying || discarding} onclick={onApply}>
		{applying ? 'Применяю…' : 'Применить'}
	</Button>
{/snippet}

{#if hasDraft}
	<div
		class="staging-inline"
		class:sticky-errors={hasErrors}
		bind:this={inlineEl}
	>
		<div class="staging-banner" class:has-errors={hasErrors}>
			<div class="staging-row">
				<span class="dot" aria-hidden="true"></span>
				<span class="title">
					{hasErrors ? 'Не могу применить' : 'Несохранённые изменения'}
					·&nbsp;<span class="time">{formatDrafted(draftedAt)}</span>
				</span>
				<div class="spacer"></div>
				{@render bannerActions()}
			</div>
			{#if inlineErrors}
				<ul class="error-list">
					{#each inlineErrors as e}
						<li><strong>{e.inRule || e.slot}</strong>: {e.message}{#if e.tag} ({e.tag}){/if}</li>
					{/each}
				</ul>
			{/if}
			{#if missingRuleSetTag}
				<div class="hint">
					DNS-правило ссылается на rule_set «{missingRuleSetTag}», которого нет в Route → Наборы. Добавьте набор или удалите DNS-правило, которое на него ссылается.
				</div>
			{/if}
			{#if inlineSbCheck}
				<pre class="sb-check">{inlineSbCheck}</pre>
			{/if}
		</div>
	</div>

	{#if showFloating}
		<div
			class="staging-float"
			style:left="{floatInset.left}px"
			style:width="{floatInset.width}px"
			transition:fly={{ y: -20, duration: 280, easing: (t) => 1 - Math.pow(1 - t, 3) }}
		>
			<div class="staging-banner compact">
				<div class="staging-row">
					<span class="dot" aria-hidden="true"></span>
					<span class="title">Несохранённые изменения</span>
					<div class="spacer compact-spacer"></div>
					{@render bannerActions()}
				</div>
			</div>
		</div>
	{/if}
{/if}

<Modal
	open={confirmDiscard}
	title="Откатить правки?"
	size="sm"
	onclose={() => (confirmDiscard = false)}
>
	<p class="discard-body">Все накопленные изменения будут отброшены.</p>
	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={() => (confirmDiscard = false)}>Отмена</Button>
		<Button variant="danger" size="md" disabled={discarding} onclick={onDiscard}>
			{discarding ? 'Откатываю…' : 'Сбросить'}
		</Button>
	{/snippet}
</Modal>

<style>
	.staging-inline {
		margin-bottom: 0.75rem;
		min-width: 0;
		max-width: 100%;
	}

	.staging-inline.sticky-errors {
		position: sticky;
		top: 0;
		z-index: var(--z-sticky-secondary);
	}

	.staging-banner {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		width: 100%;
		padding: 0.75rem 1rem;
		background: color-mix(in srgb, var(--color-warning) 10%, var(--color-bg-primary));
		border-left: 3px solid var(--color-warning);
		border-radius: var(--radius-sm);
		font-size: 0.9rem;
	}

	.staging-banner.has-errors {
		background: color-mix(in srgb, var(--color-error) 12%, var(--color-bg-primary));
		border-left-color: var(--color-error);
	}

	.staging-float {
		position: fixed;
		/* под TopBar (53px, border-box) — верх скролл-контейнера */
		top: 53px;
		z-index: var(--z-sticky-secondary);
		box-sizing: border-box;
		padding-top: 0.375rem;
		display: flex;
		justify-content: flex-end;
		pointer-events: none;
	}

	.staging-banner.compact {
		width: auto;
		max-width: 100%;
		min-width: 0;
		flex-direction: row;
		align-items: center;
		gap: 0.5rem;
		padding: 0.3125rem 0.375rem 0.3125rem 0.75rem;
		border-left: none;
		border: 1px solid color-mix(in srgb, var(--color-warning) 35%, var(--color-border));
		border-radius: var(--radius-sm);
		box-shadow: 0 2px 10px color-mix(in srgb, #000 12%, transparent);
		font-size: 0.8125rem;
		pointer-events: auto;
	}

	.staging-row {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		min-width: 0;
	}

	.staging-banner.compact .staging-row {
		gap: 0.375rem;
		flex-wrap: nowrap;
	}

	.dot {
		width: 8px;
		height: 8px;
		border-radius: 999px;
		background: var(--color-warning);
		flex-shrink: 0;
	}

	.staging-banner.has-errors .dot {
		background: var(--color-error);
	}

	.title {
		font-weight: 600;
		white-space: nowrap;
	}

	.time {
		color: var(--color-text-muted);
		font-weight: 400;
	}

	.spacer {
		flex: 1;
		min-width: 0;
	}

	.staging-banner.compact .staging-row :global(.btn.variant-ghost) {
		padding-inline: 0.625rem;
	}

	.staging-banner.compact .compact-spacer {
		display: none;
	}

	@media (max-width: 768px) {
		.staging-banner:not(.compact) {
			padding: 0.625rem 0.75rem;
		}

		.staging-banner:not(.compact) .staging-row {
			flex-wrap: wrap;
			row-gap: 0.625rem;
		}

		.staging-banner:not(.compact) .title {
			white-space: normal;
			flex: 1 1 calc(100% - 1.25rem);
			min-width: 0;
			line-height: 1.35;
		}

		.staging-banner:not(.compact) .spacer {
			display: none;
		}

		.staging-banner:not(.compact) .staging-row :global(.btn) {
			flex: 1 1 calc(50% - 0.25rem);
			min-width: 0;
		}

		.staging-float {
			justify-content: stretch;
		}

		.staging-banner.compact {
			width: 100%;
			padding: 0.5rem 0.625rem;
		}

		.staging-banner.compact .staging-row {
			width: 100%;
		}

		.staging-banner.compact .compact-spacer {
			display: block;
		}

		.staging-banner.compact .title {
			overflow: hidden;
			text-overflow: ellipsis;
			min-width: 0;
		}
	}

	.error-list {
		margin: 0;
		padding-left: 1.5rem;
		font-size: 0.85rem;
	}

	.hint {
		padding: 0.5rem 0.6rem;
		background: var(--color-bg-tertiary);
		border-radius: 4px;
		font-size: 0.85rem;
		line-height: 1.35;
	}

	.sb-check {
		margin: 0;
		padding: 0.5rem;
		background: var(--color-bg-tertiary);
		border-radius: 4px;
		font-size: 0.8rem;
		white-space: pre-wrap;
		overflow-x: auto;
	}

	.discard-body {
		margin: 0;
		color: var(--color-text-secondary);
	}
</style>
