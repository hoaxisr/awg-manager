<script lang="ts">
	import { CircleAlert } from 'lucide-svelte';
	import { scoreRingDashArray, type ScoreResult, type ScoreCheck } from '$lib/utils/awgConfScore';

	interface Props {
		result: ScoreResult;
		fixes: string[];
	}

	let { result, fixes }: Props = $props();

	const icons: Record<ScoreCheck['status'], string> = { pass: '✓', warn: '!', fail: '✗', info: 'i' };

	const toneVar: Record<ScoreResult['verdict']['tone'], { color: string; tint: string }> = {
		error: { color: 'var(--color-error, var(--error))', tint: 'var(--color-error-tint)' },
		accent: { color: 'var(--color-accent, var(--accent))', tint: 'var(--color-accent-tint)' },
		success: { color: 'var(--color-success, var(--success))', tint: 'var(--color-success-tint)' },
		warning: { color: 'var(--color-warning, var(--warning))', tint: 'var(--color-warning-tint)' },
		muted: { color: 'var(--color-text-muted, var(--text-muted))', tint: 'transparent' },
	};

	let tone = $derived(toneVar[result.verdict.tone]);
	let categories = $derived([...new Set(result.checks.map((c) => c.cat))]);
	let compatFirst = $derived(result.checks.some((c) => c.cat === 'Совместимость' && c.status === 'fail'));

	function deltaLabel(c: ScoreCheck): string {
		if (c.delta > 0) return `+${c.delta}`;
		if (c.delta < 0) return String(c.delta);
		return '';
	}
</script>

<section class="card score">
	<div class="score-row">
		<div class="ring-hold">
			<svg class="ring" viewBox="0 0 120 120" aria-hidden="true" focusable="false">
				<circle class="ring-track" cx="60" cy="60" r="50" />
				<circle
					class="ring-fill"
					cx="60"
					cy="60"
					r="50"
					stroke-dasharray={scoreRingDashArray(result.score)}
					stroke={tone.color}
				/>
				<text x="60" y="53" class="ring-num" text-anchor="middle">{result.score}</text>
				<text x="60" y="68" class="ring-sub" text-anchor="middle">{result.facts.profile}</text>
			</svg>
		</div>
		<div class="verdict">
			<span
				class="verdict-badge"
				style:background={tone.tint}
				style:border-color={tone.color}
				style:color={tone.color}
			>
				{result.verdict.label}
			</span>
			<p class="verdict-text">{result.verdict.text}</p>
		</div>
	</div>
	<div class="minis">
		<div class="mini">
			<div class="mini-l">Профиль</div>
			<div class="mini-v accent">{result.facts.profile}</div>
		</div>
		<div class="mini">
			<div class="mini-l">Header protection</div>
			<div class="mini-v">{result.facts.headerProtection ? 'вкл' : 'выкл'}</div>
		</div>
		<div class="mini">
			<div class="mini-l">CPS</div>
			<div class="mini-v soft">{result.facts.cps}</div>
		</div>
		<div class="mini">
			<div class="mini-l">Trailers 3.1</div>
			<div class="mini-v">{result.facts.trailers ? 'вкл' : 'выкл'}</div>
		</div>
	</div>
</section>

{#if compatFirst}
	<section class="card compat-block" role="alert">
		<h3 class="block-h">Конфиг не поднимется</h3>
		<ul class="fix-list">
			{#each result.checks.filter((c) => c.cat === 'Совместимость' && c.status === 'fail') as c (c.title + c.detail)}
				<li class="fix-item">
					<span class="fix-bullet" aria-hidden="true">✗</span>
					<span class="fix-text">{c.detail}</span>
				</li>
			{/each}
		</ul>
	</section>
{/if}

<section class="card summary" aria-labelledby="awg-summary-h">
	<h3 id="awg-summary-h" class="block-h">Что это за конфиг</h3>
	<dl class="summary-dl">
		{#each result.summary as row (row.label)}
			<div class="summary-row"><dt>{row.label}</dt><dd>{row.value}</dd></div>
		{/each}
	</dl>
</section>

{#if fixes.length > 0}
	<section class="card fixes" style:--awg-fix-accent={tone.color} style:--awg-fix-tint={tone.tint}>
		<div class="fixes-head">
			<span class="fixes-head-icon" aria-hidden="true"><CircleAlert size={18} /></span>
			<h3 class="fixes-h">Рекомендации</h3>
			<span class="fixes-count">{fixes.length}</span>
		</div>
		<ul class="fix-list">
			{#each fixes as line, idx (idx)}
				<li class="fix-item">
					<span class="fix-bullet" aria-hidden="true">→</span>
					<span class="fix-text">{line}</span>
				</li>
			{/each}
		</ul>
	</section>
{/if}

{#each categories as cat (cat)}
	<h4 class="cat">{cat}</h4>
	<div class="grid">
		{#each result.checks.filter((c) => c.cat === cat) as c (c.title + c.value)}
			<div class="check check-{c.status}">
				<div class="check-ic">{icons[c.status]}</div>
				<div class="check-body">
					<div class="check-t">{c.title}</div>
					<code class="check-val">{c.value}</code>
					<div class="check-d">{c.detail}</div>
				</div>
				<div class="check-w">{deltaLabel(c)}</div>
			</div>
		{/each}
	</div>
{/each}

<style>
	/* Перенесено из AwgConfigAnalyzer.svelte без изменений семантики.
	   .card определён в app.css — здесь только отступ между карточками. */
	.score,
	.compat-block,
	.summary,
	.fixes {
		margin-bottom: 12px;
	}

	.compat-block {
		border-color: var(--color-error, var(--error));
	}

	.score-row {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 20px 28px;
		margin-bottom: 16px;
	}

	/* Inline SVG in a flex row otherwise leaves a “frame”/gap under the circle. */
	.ring-hold {
		flex-shrink: 0;
		width: 120px;
		height: 120px;
		display: flex;
		align-items: center;
		justify-content: center;
		line-height: 0;
		user-select: none;
		-webkit-user-select: none;
	}

	.ring {
		display: block;
		width: 120px;
		height: 120px;
		overflow: visible;
		outline: none;
		border: none;
		box-shadow: none;
		/* Clip square SVG paint bounds so no faint rectangular halo / selection box. */
		clip-path: circle(50% at 50% 50%);
		-webkit-tap-highlight-color: transparent;
	}

	.ring:focus,
	.ring:focus-visible,
	.ring-hold:focus-visible {
		outline: none;
	}

	.ring-track {
		fill: none;
		stroke: var(--color-border);
		stroke-width: 10;
	}

	.ring-fill {
		fill: none;
		stroke-width: 10;
		stroke-linecap: round;
		transform: rotate(-90deg);
		transform-box: fill-box;
		transform-origin: center;
		transition: stroke-dasharray 0.45s ease, stroke 0.25s ease;
	}

	.ring-num {
		font-size: 22px;
		font-weight: 800;
		fill: var(--color-text-primary, var(--text-primary));
	}

	.ring-sub {
		font-size: 9px;
		fill: var(--color-text-muted, var(--text-muted));
	}

	.verdict {
		flex: 1;
		min-width: 200px;
	}

	.verdict-badge {
		display: inline-block;
		padding: 5px 14px;
		border-radius: 999px;
		font-weight: 700;
		font-size: 14px;
		border: 1px solid transparent;
		margin-bottom: 8px;
	}

	.verdict-text {
		margin: 0;
		font-size: 13px;
		line-height: 1.5;
		color: var(--color-text-secondary, var(--text-secondary));
	}

	.minis {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(130px, 1fr));
		gap: 8px;
	}

	.mini {
		background: var(--color-settings-control-bg, var(--color-bg-tertiary, var(--bg-tertiary)));
		border: 1px solid var(--color-border);
		border-radius: 8px;
		padding: 8px 10px;
	}

	.mini-l {
		font-size: 10px;
		text-transform: uppercase;
		letter-spacing: 0.06em;
		color: var(--color-text-muted, var(--text-muted));
		margin-bottom: 4px;
	}

	.mini-v {
		font-size: 15px;
		font-weight: 700;
		color: var(--color-text-primary, var(--text-primary));
	}

	.mini-v.accent {
		color: var(--color-accent, var(--accent));
	}

	.mini-v.soft {
		color: var(--color-text-secondary, var(--text-secondary));
	}

	.summary-dl {
		margin: 4px 0 0;
	}

	.summary-row {
		display: grid;
		grid-template-columns: minmax(7.5rem, 36%) 1fr;
		gap: 4px 12px;
		padding: 7px 0;
		border-bottom: 1px solid var(--color-border);
		font-size: 12px;
		line-height: 1.45;
	}

	.summary-row:last-child {
		border-bottom: none;
		padding-bottom: 0;
	}

	.summary-row dt {
		margin: 0;
		font-weight: 600;
		color: var(--color-text-muted, var(--text-muted));
	}

	.summary-row dd {
		margin: 0;
		color: var(--color-text-primary, var(--text-primary));
		word-break: break-word;
	}

	.block-h {
		margin: 0 0 10px;
		font-size: 11px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.08em;
		color: var(--color-accent, var(--accent));
	}

	.card.fixes {
		--awg-fix-accent: var(--color-success, var(--success));
		--awg-fix-tint: var(--color-success-tint);
		border-color: color-mix(in srgb, var(--awg-fix-accent) 28%, var(--color-border));
		background: linear-gradient(
			165deg,
			color-mix(in srgb, var(--awg-fix-accent) 12%, var(--color-bg-secondary, var(--bg-secondary))) 0%,
			var(--color-bg-secondary, var(--bg-secondary)) 55%
		);
	}

	.fixes-head {
		display: flex;
		align-items: center;
		gap: 10px;
		margin-bottom: 12px;
	}

	.fixes-head-icon {
		display: flex;
		align-items: center;
		justify-content: center;
		width: 34px;
		height: 34px;
		border-radius: 10px;
		flex-shrink: 0;
		color: var(--awg-fix-accent);
		background: var(--awg-fix-tint);
		border: 1px solid color-mix(in srgb, var(--awg-fix-accent) 35%, transparent);
	}

	.fixes-h {
		margin: 0;
		flex: 1;
		min-width: 0;
		font-size: 11px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.08em;
		color: var(--awg-fix-accent);
	}

	.fixes-count {
		flex-shrink: 0;
		font-size: 11px;
		font-weight: 700;
		padding: 4px 10px;
		border-radius: 999px;
		font-variant-numeric: tabular-nums;
		background: var(--awg-fix-tint);
		color: var(--awg-fix-accent);
		border: 1px solid color-mix(in srgb, var(--awg-fix-accent) 30%, transparent);
	}

	.fix-list {
		margin: 0;
		padding: 0;
		list-style: none;
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.fix-item {
		display: flex;
		align-items: flex-start;
		gap: 10px;
		padding: 11px 12px;
		border-radius: 9px;
		background: var(--color-bg-tertiary, var(--bg-tertiary));
		border: 1px solid var(--color-border);
		transition: border-color 0.15s ease, background 0.15s ease;
	}

	.fix-item:hover {
		border-color: color-mix(in srgb, var(--awg-fix-accent) 35%, var(--color-border));
		background: color-mix(in srgb, var(--awg-fix-tint) 40%, var(--color-bg-tertiary, var(--bg-tertiary)));
	}

	.fix-bullet {
		flex-shrink: 0;
		width: 24px;
		height: 24px;
		display: flex;
		align-items: center;
		justify-content: center;
		margin-top: 1px;
		border-radius: 7px;
		font-size: 13px;
		font-weight: 700;
		line-height: 1;
		color: var(--awg-fix-accent);
		background: var(--awg-fix-tint);
		border: 1px solid color-mix(in srgb, var(--awg-fix-accent) 25%, transparent);
	}

	.fix-text {
		flex: 1;
		min-width: 0;
		font-size: 13px;
		line-height: 1.5;
		color: var(--color-text-primary, var(--text-primary));
		white-space: pre-line;
	}

	.cat {
		margin: 18px 0 8px;
		font-size: 11px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.1em;
		color: var(--color-text-muted, var(--text-muted));
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.cat::after {
		content: '';
		flex: 1;
		height: 1px;
		background: var(--color-border);
	}

	.grid {
		display: grid;
		gap: 8px;
	}

	.check {
		display: flex;
		align-items: flex-start;
		gap: 10px;
		padding: 10px 12px;
		background: var(--color-bg-secondary, var(--bg-secondary));
		border: 1px solid var(--color-border);
		border-radius: 8px;
		border-left-width: 3px;
	}

	.check-pass {
		border-left-color: var(--color-success, var(--success));
	}
	.check-warn {
		border-left-color: var(--color-warning, var(--warning));
	}
	.check-fail {
		border-left-color: var(--color-error, var(--error));
	}
	.check-info {
		border-left-color: var(--color-accent, var(--accent));
	}

	.check-ic {
		width: 22px;
		height: 22px;
		border-radius: 50%;
		display: flex;
		align-items: center;
		justify-content: center;
		font-size: 11px;
		font-weight: 800;
		flex-shrink: 0;
		margin-top: 2px;
	}

	.check-pass .check-ic {
		background: var(--color-success-tint);
		color: var(--color-success);
	}
	.check-warn .check-ic {
		background: var(--color-warning-tint);
		color: var(--color-warning);
	}
	.check-fail .check-ic {
		background: var(--color-error-tint);
		color: var(--color-error);
	}
	.check-info .check-ic {
		background: var(--color-accent-tint);
		color: var(--color-accent, var(--accent));
	}

	.check-body {
		flex: 1;
		min-width: 0;
	}

	.check-t {
		font-weight: 600;
		font-size: 13px;
		color: var(--color-text-primary, var(--text-primary));
		margin-bottom: 2px;
	}

	.check-val {
		display: inline-block;
		margin-top: 2px;
		padding: 1px 6px;
		border-radius: 4px;
		background: var(--color-bg-tertiary, var(--bg-tertiary));
		font-family: var(--font-mono);
		font-size: 11px;
		color: var(--color-text-secondary, var(--text-secondary));
		word-break: break-all;
		max-width: 100%;
	}

	.check-d {
		margin-top: 4px;
		font-size: 12px;
		line-height: 1.4;
		color: var(--color-text-secondary, var(--text-secondary));
	}

	.check-w {
		font-size: 11px;
		color: var(--color-text-muted, var(--text-muted));
		font-family: var(--font-mono);
		flex-shrink: 0;
		align-self: center;
		min-width: 36px;
		text-align: right;
	}

	@media (max-width: 640px) {
		.score,
		.compat-block,
		.summary,
		.fixes {
			padding: 12px;
			margin-bottom: 0.765rem;
		}

		.summary-row {
			grid-template-columns: 1fr;
			gap: 0.25rem;
		}

		.minis {
			grid-template-columns: repeat(2, minmax(0, 1fr));
		}

		.score-row {
			flex-direction: column;
			align-items: flex-start;
		}

		.ring-hold {
			width: 100px;
			height: 100px;
		}

		.ring {
			width: 100px;
			height: 100px;
		}
	}
</style>
