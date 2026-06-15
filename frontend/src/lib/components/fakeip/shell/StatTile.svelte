<!--
  StatTile — стат-тайл шапки FakeIP (мокап `.stat`): крупное значение + подпись
  (с опциональным SourceBadge) + мелкий sabline. Тон значения красит цифру:
    - default → accent (мокап жёлтый → токен приложения --color-accent);
    - success → success (живые «работает»/активные устройства).
  Бейдж источника передаётся как Snippet, чтобы тайл не знал про варианты —
  страница сама решает live/cfg/ondemand у каждого показателя.
-->
<script lang="ts" module>
	import type { Snippet } from 'svelte';
	export type StatTone = 'default' | 'success';
</script>

<script lang="ts">
	interface Props {
		/** Крупное значение (число или короткий текст вроде «работает»). */
		value: string | number;
		/** Подпись под значением. */
		label: string;
		/** Мелкая вторая строка (опционально). */
		sub?: string;
		/** Тон значения. */
		tone?: StatTone;
		/** SourceBadge рядом с подписью (опционально). */
		badge?: Snippet;
	}

	let { value, label, sub, tone = 'default', badge }: Props = $props();
</script>

<div class="stat">
	<div class="v" class:success={tone === 'success'}>{value}</div>
	<div class="l">
		<span class="l-text">{label}</span>
		{#if badge}{@render badge()}{/if}
	</div>
	{#if sub}<div class="s">{sub}</div>{/if}
</div>

<style>
	.stat {
		background: var(--color-bg-secondary);
		padding: var(--sp-3, 0.75rem);
		min-width: 0;
	}

	.v {
		color: var(--color-accent);
		font-size: 1.3125rem;
		font-weight: 800;
		line-height: 1.1;
		word-break: break-word;
	}

	.v.success {
		color: var(--color-success);
	}

	.l {
		display: flex;
		align-items: center;
		gap: 6px;
		flex-wrap: wrap;
		color: var(--text-secondary);
		font-size: 0.6875rem;
		margin-top: 2px;
	}

	.s {
		color: var(--text-muted);
		font-size: 0.625rem;
		margin-top: 3px;
	}
</style>
