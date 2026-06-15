<!--
  SourceBadge — «честный» бейдж источника данных (FE-spec §2, мокап `.bdg`).
  Маркирует, ОТКУДА взят конкретный показатель, чтобы UI не выдавал config за
  live и наоборот. Вариант определяет символ + тон (токены приложения):

    - live     ●  — поток/SSE (Clash WS, status-стрим). Тон: success.
    - ondemand ◴  — по запросу: значение появляется только после кнопки/пробы
                    (egress-IP, health, RCI-readback). Тон: warning.
    - cfg         — наш конфиг: отдаётся целиком, честно вне зависимости от
                    движка. Тон: нейтральный (text-secondary).

  Символы ●/◴ — типографика, НЕ эмодзи (рендерятся одним глифом, без VS16).
-->
<script lang="ts" module>
	export type SourceVariant = 'live' | 'ondemand' | 'cfg';
</script>

<script lang="ts">
	interface Props {
		variant: SourceVariant;
		/** Текст после символа. По умолчанию короткая подпись варианта. */
		label?: string;
		/** Только символ, без подписи (компактный инлайн-маркер у значения). */
		symbolOnly?: boolean;
	}

	let { variant, label, symbolOnly = false }: Props = $props();

	const SYMBOL: Record<SourceVariant, string> = {
		live: '●', // ● BLACK CIRCLE
		ondemand: '◴', // ◴ WHITE CIRCLE WITH UPPER LEFT QUADRANT
		cfg: '',
	};

	const DEFAULT_LABEL: Record<SourceVariant, string> = {
		live: 'live',
		ondemand: 'по запросу',
		cfg: 'cfg',
	};

	const symbol = $derived(SYMBOL[variant]);
	const text = $derived(symbolOnly ? '' : (label ?? DEFAULT_LABEL[variant]));
	const title = $derived(
		variant === 'live'
			? 'Источник: живой поток (SSE/WS)'
			: variant === 'ondemand'
				? 'Источник: по запросу (кнопка/проба)'
				: 'Источник: наш конфиг',
	);
</script>

<span class="bdg" class:live={variant === 'live'} class:od={variant === 'ondemand'} class:cfg={variant === 'cfg'} {title}>
	{#if symbol}<span class="sym" aria-hidden="true">{symbol}</span>{/if}{#if text}<span class="txt">{text}</span>{/if}
</span>

<style>
	.bdg {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		font-size: 9px;
		line-height: 1.4;
		padding: 1px 5px;
		border-radius: 4px;
		vertical-align: middle;
		white-space: nowrap;
		font-weight: 600;
	}

	.sym {
		font-size: 8px;
	}

	/* live → success-тон (мокап b-live зелёный). */
	.live {
		background: var(--color-success-tint);
		color: var(--color-success);
	}

	/* ondemand → warning-тон (мокап b-od янтарный). */
	.od {
		background: var(--color-warning-tint);
		color: var(--color-warning);
	}

	/* cfg → нейтральный приглушённый (мокап b-cfg «наш конфиг»). */
	.cfg {
		background: var(--color-muted-tint);
		color: var(--text-secondary);
	}
</style>
