<script lang="ts">
	import { api } from '$lib/api/client';
	import type { SingboxRouterPreset } from '$lib/types';
	import type { OutboundGroup } from './outboundOptions';
	import PresetIcon from './PresetIcon.svelte';
	import PresetApplyModal from './PresetApplyModal.svelte';

	interface Props {
		presets: SingboxRouterPreset[];
		outboundOptions: OutboundGroup[];
		onApplied: () => Promise<void> | void;
	}
	let { presets, outboundOptions, onApplied }: Props = $props();

	let selected = $state<SingboxRouterPreset | null>(null);
	let showSensitive = $state(false);

	function cardClass(p: SingboxRouterPreset): string {
		const classes = ['card'];
		if (p.featured) classes.push('card-featured');
		if (p.rules.every((r) => r.actionTarget === 'reject')) classes.push('card-reject');
		else if (p.rules.every((r) => r.actionTarget === 'direct')) classes.push('card-direct');
		return classes.join(' ');
	}

	function cardHint(p: SingboxRouterPreset): string {
		if (p.rules.every((r) => r.actionTarget === 'reject')) return 'Блокировать';
		if (p.rules.every((r) => r.actionTarget === 'direct')) return 'Direct (bypass)';
		return 'Направить в туннель';
	}

	function cardHintClass(p: SingboxRouterPreset): string {
		if (p.rules.every((r) => r.actionTarget === 'reject')) return 'hint-reject';
		if (p.rules.every((r) => r.actionTarget === 'direct')) return 'hint-direct';
		return 'hint-tunnel';
	}

	const featured = $derived(presets.filter((p) => p.featured));
	const normal = $derived(presets.filter((p) => !p.featured && !p.sensitive));
	const sensitive = $derived(presets.filter((p) => p.sensitive));
</script>

<div class="hint">Готовые наборы из SagerNet. Клик — добавить rule_set и правило в движок.</div>

{#if featured.length > 0}
	<div class="section-label">Рекомендуемые</div>
	<div class="gallery">
		{#each featured as p (p.id)}
			<button class={cardClass(p)} onclick={() => (selected = p)} type="button">
				<PresetIcon slug={p.iconSlug} size={44} />
				<div class="card-body">
					<div class="name">{p.name}</div>
					{#if p.notice}<div class="featured-notice">{p.notice}</div>{/if}
					<div class={`card-hint ${cardHintClass(p)}`}>{cardHint(p)}</div>
				</div>
			</button>
		{/each}
	</div>
{/if}

{#if normal.length > 0}
	<div class="section-label">Сервисы и сайты</div>
	<div class="gallery">
		{#each normal as p (p.id)}
			<button class={cardClass(p)} onclick={() => (selected = p)} type="button">
				<PresetIcon slug={p.iconSlug} />
				<div class="card-body">
					<div class="name">{p.name}</div>
					<div class="rs mono">{p.ruleSets[0]?.tag ?? ''}</div>
					<div class={`card-hint ${cardHintClass(p)}`}>{cardHint(p)}</div>
				</div>
			</button>
		{/each}
	</div>
{/if}

{#if sensitive.length > 0}
	<div class="sensitive-toggle">
		<label>
			<input type="checkbox" bind:checked={showSensitive} />
			Показать чувствительные наборы (18+)
		</label>
	</div>
	{#if showSensitive}
		<div class="gallery">
			{#each sensitive as p (p.id)}
				<button class={cardClass(p)} onclick={() => (selected = p)} type="button">
					<PresetIcon slug={p.iconSlug} />
					<div class="card-body">
						<div class="name">{p.name}</div>
						<div class="rs mono">{p.ruleSets[0]?.tag ?? ''}</div>
						<div class={`card-hint ${cardHintClass(p)}`}>{cardHint(p)}</div>
					</div>
				</button>
			{/each}
		</div>
	{/if}
{/if}

{#if selected}
	<PresetApplyModal
		preset={selected}
		{outboundOptions}
		onClose={() => (selected = null)}
		onApply={async (id, outbound) => {
			await api.singboxRouterApplyPreset(id, outbound);
			selected = null;
			await onApplied();
		}}
	/>
{/if}

<style>
	.hint {
		color: var(--muted-text);
		font-size: 0.85rem;
		margin-bottom: 0.75rem;
	}
	.section-label {
		font-size: 0.7rem;
		text-transform: uppercase;
		letter-spacing: 0.5px;
		color: var(--muted-text);
		margin: 1rem 0 0.5rem;
	}
	.gallery {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
		gap: 0.5rem;
	}
	.card {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		padding: 0.75rem;
		background: var(--surface-bg);
		border: 1px solid transparent;
		border-radius: 6px;
		cursor: pointer;
		text-align: left;
		font: inherit;
		color: inherit;
		transition: border-color 0.1s, background 0.1s;
	}
	.card:hover {
		border-color: var(--accent, #3b82f6);
	}
	.card-featured {
		grid-column: span 2;
		background: linear-gradient(135deg, rgba(59, 130, 246, 0.08), transparent);
		border-color: var(--accent, #3b82f6);
	}
	.card-reject {
		border-color: var(--danger, #dc2626);
	}
	.card-direct {
		border-color: var(--success, #22c55e);
	}
	.card-body {
		flex: 1;
		min-width: 0;
	}
	.name {
		font-weight: 600;
		font-size: 0.9rem;
		margin-bottom: 0.1rem;
	}
	.card-featured .name {
		font-size: 1rem;
	}
	.featured-notice {
		font-size: 0.75rem;
		color: var(--muted-text);
		margin: 0.15rem 0 0.3rem;
		line-height: 1.3;
	}
	.rs {
		font-size: 0.7rem;
		color: var(--muted-text);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		margin-bottom: 0.25rem;
	}
	.mono {
		font-family: ui-monospace, monospace;
	}
	.card-hint {
		font-size: 0.75rem;
		font-weight: 500;
	}
	.hint-tunnel {
		color: var(--accent, #3b82f6);
	}
	.hint-reject {
		color: var(--danger, #dc2626);
	}
	.hint-direct {
		color: var(--success, #22c55e);
	}
	.sensitive-toggle {
		margin: 1rem 0 0.5rem;
		font-size: 0.85rem;
		color: var(--muted-text);
	}
	.sensitive-toggle label {
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
		cursor: pointer;
	}
</style>
