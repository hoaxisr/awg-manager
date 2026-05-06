<script lang="ts">
	import type { SingboxRouterPreset, SingboxRouterPresetCategory } from '$lib/types';
	import { singboxWizard } from '$lib/stores/singboxWizard';

	interface Props {
		presets: SingboxRouterPreset[];
	}
	let { presets }: Props = $props();

	const wizardState = singboxWizard.state;

	const CATEGORY_LABELS: Record<SingboxRouterPresetCategory, string> = {
		social: 'Соцсети',
		media: 'Медиа',
		ai: 'AI',
		developer: 'Разработка',
		cloud: 'Облако',
		gaming: 'Игры',
		block: 'Блок',
	};
	const CATEGORY_ORDER: SingboxRouterPresetCategory[] = [
		'social',
		'media',
		'ai',
		'developer',
		'cloud',
		'gaming',
		'block',
	];

	type CategoryFilter = 'all' | SingboxRouterPresetCategory;
	let activeCategory = $state<CategoryFilter>('all');

	const visible = $derived(presets.filter((p) => !p.featured && !p.sensitive));
	const counts = $derived(
		CATEGORY_ORDER.reduce<Record<SingboxRouterPresetCategory, number>>(
			(acc, k) => {
				acc[k] = visible.filter((p) => p.category === k).length;
				return acc;
			},
			{ social: 0, media: 0, ai: 0, developer: 0, cloud: 0, gaming: 0, block: 0 },
		),
	);
	const visibleCategories = $derived(CATEGORY_ORDER.filter((k) => counts[k] > 0));

	const filtered = $derived(
		activeCategory === 'all' ? visible : visible.filter((p) => p.category === activeCategory),
	);

	const selectedIds = $derived($wizardState.presetIds);
	function toggle(id: string): void {
		singboxWizard.togglePresetId(id);
	}
</script>

<div class="title">Какие сервисы пустить через VPN?</div>
<div class="subtitle">
	Отметьте пресеты — для каждого мастер настроит и маршрутизацию, и DNS.
	Источники: публичные SagerNet rule-set URL (<code>.srs</code>), актуальные для РФ-сценариев.
</div>

<div class="chip-row" role="tablist" aria-label="Категории">
	<button
		type="button"
		role="tab"
		class="chip"
		class:chip-active={activeCategory === 'all'}
		aria-selected={activeCategory === 'all'}
		onclick={() => (activeCategory = 'all')}
	>
		Все <span class="chip-count">{visible.length}</span>
	</button>
	{#each visibleCategories as key (key)}
		<button
			type="button"
			role="tab"
			class="chip"
			class:chip-active={activeCategory === key}
			aria-selected={activeCategory === key}
			onclick={() => (activeCategory = key)}
		>
			{CATEGORY_LABELS[key]} <span class="chip-count">{counts[key]}</span>
		</button>
	{/each}
</div>

<div class="grid">
	{#each filtered as p (p.id)}
		<button
			type="button"
			class="preset"
			class:checked={selectedIds.includes(p.id)}
			onclick={() => toggle(p.id)}
		>
			<span class="cb" class:checked={selectedIds.includes(p.id)}></span>
			<span>{p.name}</span>
		</button>
	{/each}
</div>

<style>
	.title { font-size: 1.05rem; color: var(--color-text-primary); font-weight: 600; margin-bottom: 0.3rem; }
	.subtitle { color: var(--color-text-muted); font-size: 0.85rem; margin-bottom: 1rem; }
	.chip-row { display: flex; flex-wrap: wrap; gap: 0.4rem; margin-bottom: 1rem; }
	.chip {
		padding: 0.25rem 0.7rem;
		border-radius: 999px;
		background: var(--color-bg-secondary);
		color: var(--color-text-muted);
		font: inherit;
		font-size: 0.78rem;
		border: 1px solid transparent;
		cursor: pointer;
	}
	.chip:hover { border-color: var(--color-accent); }
	.chip-active {
		background: rgba(88,166,255,0.15);
		color: var(--color-accent);
		border-color: rgba(88,166,255,0.3);
	}
	.chip-count { font-size: 0.7rem; opacity: 0.7; margin-left: 0.25rem; }
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
		gap: 0.5rem;
	}
	.preset {
		display: flex;
		align-items: center;
		gap: 0.55rem;
		padding: 0.55rem 0.75rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 6px;
		font: inherit;
		font-size: 0.82rem;
		color: var(--color-text-primary);
		cursor: pointer;
		text-align: left;
	}
	.preset.checked {
		border-color: var(--color-accent);
		background: rgba(88,166,255,0.06);
	}
	.cb {
		width: 14px; height: 14px;
		border: 1px solid var(--color-border);
		border-radius: 3px;
		background: var(--color-bg-primary);
		flex-shrink: 0;
		display: inline-block;
	}
	.cb.checked { background: var(--color-accent); border-color: var(--color-accent); }
</style>
