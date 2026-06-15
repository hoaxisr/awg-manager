<!--
  Hero-сводка «Обзора» (FE-spec §5.1). Компактная строка config-счётчиков:
  N rule_sets · N DNS-серверов / DNS-правил · N outbounds (atomic+composite) ·
  какой egress назначен `final`. Это КОНФИГ-счётчики (честны независимо от
  состояния движка) — НЕ дублируют чип-счётчики (§5.1: «без дублирования»),
  это сводная строка-карточка.

  Презентационный: получает готовый OverviewSummary пропом, своих подписок нет.
-->
<script lang="ts">
	import { Card } from '$lib/components/ui';
	import { Boxes, Globe, ListFilter, Network } from 'lucide-svelte';
	import type { OverviewSummary } from './overviewSummary';

	interface Props {
		summary: OverviewSummary;
	}

	let { summary }: Props = $props();
</script>

<Card padding="md">
	{#snippet header()}
		<h3 class="hero-title">Сводка конфигурации</h3>
	{/snippet}

	<div class="hero-grid">
		<div class="hero-item">
			<ListFilter size={16} class="hero-icon" />
			<span class="hero-value">{summary.ruleSetCount}</span>
			<span class="hero-label">rule_sets</span>
		</div>

		<div class="hero-item">
			<Globe size={16} class="hero-icon" />
			<span class="hero-value">{summary.dnsServerCount} / {summary.dnsRuleCount}</span>
			<span class="hero-label">DNS серверов / правил</span>
		</div>

		<div class="hero-item">
			<Boxes size={16} class="hero-icon" />
			<span class="hero-value">{summary.outboundTotalCount}</span>
			<span class="hero-label"
				>outbounds ({summary.outboundAtomicCount} atomic · {summary.outboundCompositeCount}
				composite)</span
			>
		</div>

		<div class="hero-item">
			<Network size={16} class="hero-icon" />
			<span class="hero-value hero-value-mono">{summary.final || '—'}</span>
			<span class="hero-label">egress по умолчанию (final)</span>
		</div>
	</div>
</Card>

<style>
	.hero-title {
		margin: 0;
		font-size: 0.9375rem;
		font-weight: 600;
		color: var(--text-primary);
	}

	.hero-grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(10rem, 1fr));
		gap: 0.875rem;
	}

	.hero-item {
		display: grid;
		grid-template-columns: auto 1fr;
		grid-template-rows: auto auto;
		align-items: center;
		column-gap: 0.5rem;
		row-gap: 0.125rem;
		min-width: 0;
	}

	.hero-item :global(.hero-icon) {
		grid-row: 1 / span 2;
		color: var(--color-accent);
		flex-shrink: 0;
	}

	.hero-value {
		font: 700 1.25rem/1 var(--font-sans);
		letter-spacing: -0.02em;
		color: var(--text-primary);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.hero-value-mono {
		font-family: var(--font-mono);
		font-size: 1rem;
	}

	.hero-label {
		grid-column: 2;
		font-size: 0.75rem;
		color: var(--text-muted);
		line-height: 1.25;
	}
</style>
