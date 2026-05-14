<script lang="ts">
	import { Button } from '$lib/components/ui';
	import {
		parseAWG,
		detectVersion,
		runChecks,
		calcScores,
		buildFixes,
		getVerdict,
		dpiLabel,
		camouflageFromI1,
		scoreRingDashArray,
		type AwgParsed,
		type AwgVersionInfo,
		type AwgCheck,
		type AwgScores,
		type AwgVerdict,
	} from '$lib/utils/awgConfAnalyzer';

	let raw = $state('');
	let error = $state('');
	let parsed: AwgParsed | null = $state(null);
	let version: AwgVersionInfo | null = $state(null);
	let checks: AwgCheck[] = $state([]);
	let awgScores = $state<AwgScores | null>(null);
	let verdict: AwgVerdict | null = $state(null);
	let fixes: string[] = $state([]);
	let camouflage = $state<'LOW' | 'MEDIUM' | 'HIGH'>('LOW');
	let fileInput: HTMLInputElement | undefined = $state();

	function analyze() {
		error = '';
		parsed = null;
		version = null;
		checks = [];
		awgScores = null;
		verdict = null;
		fixes = [];

		const t = raw.trim();
		if (!t) {
			error = 'Вставьте содержимое .conf файла AmneziaWG / WireGuard';
			return;
		}

		try {
			const p = parseAWG(t);
			const v = detectVersion(p.iface);
			const c = runChecks(p.iface, p.peer, v);
			const s = calcScores(c, p.iface, v);
			const f = buildFixes(c, p.iface, p.peer, v);
			const ver = getVerdict(s.total);
			const cam = camouflageFromI1(p.iface);

			parsed = p;
			version = v;
			checks = c;
			awgScores = s;
			verdict = ver;
			fixes = f;
			camouflage = cam;
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	function clearAll() {
		raw = '';
		error = '';
		parsed = null;
		version = null;
		checks = [];
		awgScores = null;
		verdict = null;
		fixes = [];
		camouflage = 'LOW';
	}

	function onPickFile(e: Event) {
		const input = e.target as HTMLInputElement;
		const file = input.files?.[0];
		if (!file) return;
		const reader = new FileReader();
		reader.onload = () => {
			raw = String(reader.result ?? '').trim();
			analyze();
		};
		reader.readAsText(file);
		input.value = '';
	}

	function onDrop(e: DragEvent) {
		e.preventDefault();
		const file = e.dataTransfer?.files?.[0];
		if (!file) return;
		const reader = new FileReader();
		reader.onload = () => {
			raw = String(reader.result ?? '').trim();
			analyze();
		};
		reader.readAsText(file);
	}

	function onKeydown(e: KeyboardEvent) {
		if (e.key !== 'Enter') return;
		if (!e.ctrlKey && !e.metaKey) return;
		e.preventDefault();
		analyze();
	}

	let parsedLines = $derived.by(() => {
		if (!parsed) return [] as { key: string; value: string }[];
		const { iface, peer } = parsed;
		const rows: [string, string][] = [
			['privatekey', iface.privatekey ? `${iface.privatekey.slice(0, 16)}…` : '—'],
			['address', iface.address || '—'],
			['dns', iface.dns || '—'],
			['mtu', iface.mtu || '—'],
			[
				'jc/jmin/jmax',
				[iface.jc, iface.jmin, iface.jmax].map((v) => v ?? '—').join(' / '),
			],
			['s1/s2', [iface.s1, iface.s2].map((v) => v ?? '—').join(' / ')],
			[
				's3/s4',
				iface.s3 || iface.s4 ? [iface.s3, iface.s4].map((v) => v ?? '—').join(' / ') : '—',
			],
			['h1/h2/h3/h4', [iface.h1, iface.h2, iface.h3, iface.h4].map((v) => v ?? '—').join(' / ')],
			[
				'i1',
				iface.i1 ? `${iface.i1.slice(0, 40)}${iface.i1.length > 40 ? '…' : ''}` : '—',
			],
			['endpoint', peer.endpoint || '—'],
			['publickey', peer.publickey ? `${peer.publickey.slice(0, 16)}…` : '—'],
			['allowedips', peer.allowedips || '—'],
			['keepalive', peer.persistentkeepalive || '—'],
		];
		return rows
			.filter(([, v]) => v && v !== '—')
			.map(([key, value]) => ({ key, value }));
	});

	let categories = $derived([...new Set(checks.map((c) => c.cat))]);

	const icons: Record<string, string> = {
		pass: '✓',
		warn: '!',
		fail: '✗',
		info: 'i',
	};

	let dpiL = $derived.by(() => {
		const s = awgScores;
		if (!s) return { text: '—', color: 'var(--text-tertiary)' };
		return dpiLabel(s.dpi);
	});
</script>

<svelte:window onkeydown={onKeydown} />

<div class="shell">
	<p class="hint">
		Анализ выполняется только в браузере; конфиг не отправляется на сервер. Оценки эвристические, не
		гарантируют обход DPI.
	</p>

	<div class="layout">
		<div class="col-input">
			<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
			<label
				class="drop"
				ondragover={(e) => e.preventDefault()}
				ondrop={onDrop}
			>
				<span class="drop-label">AWG / WireGuard .conf — вставьте или перетащите файл</span>
				<textarea
					class="ta"
					bind:value={raw}
					rows="16"
					spellcheck="false"
					autocomplete="off"
					placeholder="[Interface]&#10;PrivateKey = …&#10;…"
				></textarea>
			</label>

			<div class="bar">
				<Button variant="primary" onclick={analyze}>Анализировать</Button>
				<Button variant="secondary" onclick={() => fileInput?.click()}>Файл…</Button>
				<Button variant="ghost" onclick={clearAll}>Очистить</Button>
				<span class="kbd">Ctrl+Enter</span>
			</div>
			<input
				bind:this={fileInput}
				type="file"
				accept=".conf,.txt"
				class="sr"
				onchange={onPickFile}
			/>

			{#if error}
				<div class="err" role="alert">{error}</div>
			{/if}
		</div>

		<div class="col-results">
			{#if version && awgScores && verdict && parsed}
		<section class="card ver">
			<span class="ver-badge">{version.ver}</span>
			<p class="ver-desc">{version.desc}</p>
		</section>

		<section class="card score">
			<div class="score-row">
				<div class="ring-hold">
					<svg
						class="ring"
						viewBox="0 0 120 120"
						aria-hidden="true"
						focusable="false"
					>
						<circle class="ring-track" cx="60" cy="60" r="50" />
						<circle
							class="ring-fill"
							cx="60"
							cy="60"
							r="50"
							stroke-dasharray={scoreRingDashArray(awgScores.total)}
							stroke={verdict.color}
						/>
						<text x="60" y="53" class="ring-num" text-anchor="middle">{awgScores.total}</text>
						<text x="60" y="68" class="ring-sub" text-anchor="middle">{version.ver}</text>
					</svg>
				</div>
				<div class="verdict">
					<span class="verdict-badge" style:background={verdict.tint} style:border-color={verdict.color} style:color={verdict.color}>
						{verdict.label}
					</span>
					<p class="verdict-text">{verdict.text}</p>
				</div>
			</div>

			<div class="minis">
				<div class="mini">
					<div class="mini-l">Версия</div>
					<div class="mini-v accent">{version.ver}</div>
				</div>
				<div class="mini">
					<div class="mini-l">DPI риск</div>
					<div class="mini-v" style:color={dpiL.color}>{dpiL.text}</div>
				</div>
				<div class="mini">
					<div class="mini-l">Stealth</div>
					<div class="mini-v soft">{awgScores.stealth}%</div>
				</div>
				<div class="mini">
					<div class="mini-l">Балл</div>
					<div class="mini-v">{awgScores.total}%</div>
				</div>
				<div class="mini">
					<div class="mini-l">Камуфляж</div>
					<div class="mini-v soft">{camouflage}</div>
				</div>
			</div>
		</section>

		{#if fixes.length > 0}
			<section class="card fixes">
				<div class="fixes-head">
					<span class="fixes-head-icon" aria-hidden="true">
						<svg
							width="18"
							height="18"
							viewBox="0 0 24 24"
							fill="none"
							stroke="currentColor"
							stroke-width="2"
							stroke-linecap="round"
							stroke-linejoin="round"
						>
							<circle cx="12" cy="12" r="10" />
							<path d="M12 16v-4" />
							<path d="M12 8h.01" />
						</svg>
					</span>
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

		{#if parsedLines.length > 0}
			<section class="card prewrap">
				<h3 class="block-h">Параметры (усечено)</h3>
				<pre class="mono">{#each parsedLines as row (`${row.key}-${row.value}`)}
<span class="k">{row.key.padEnd(14)}</span>= {row.value}
{/each}</pre>
			</section>
		{/if}

		{#each categories as cat (cat)}
			<h4 class="cat">{cat}</h4>
			<div class="grid">
				{#each checks.filter((c) => c.cat === cat) as c (c.title + c.value)}
					<div class="check check-{c.status}">
						<div class="check-ic">{icons[c.status] ?? '?'}</div>
						<div class="check-body">
							<div class="check-t">{c.title}</div>
							<code class="check-val">{c.value}</code>
							<div class="check-d">{c.detail}</div>
						</div>
						<div class="check-w">{c.max > 0 ? `${c.pts}/${c.max}` : ''}</div>
					</div>
				{/each}
			</div>
		{/each}
			{:else}
				<div class="results-empty">
					<p class="results-empty-title">Результаты анализа</p>
					<p class="results-empty-text">
						После нажатия «Анализировать» здесь появятся оценка, рекомендации и список проверок.
					</p>
				</div>
			{/if}
		</div>
	</div>
</div>

<style>
	.shell {
		box-sizing: border-box;
		width: 100%;
		max-width: 1180px;
		margin: 0 auto;
		padding: 12px 16px 28px;
	}

	.hint {
		margin: 0 auto 14px;
		max-width: 52rem;
		font-size: 12px;
		line-height: 1.45;
		color: var(--text-secondary);
		text-align: center;
	}

	.layout {
		display: grid;
		gap: 20px;
		grid-template-columns: 1fr;
		align-items: start;
	}

	@media (min-width: 1024px) {
		.layout {
			grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
			gap: 24px 28px;
		}

		.ta {
			min-height: min(62vh, 520px);
		}

		.col-results {
			position: sticky;
			top: 12px;
			max-height: calc(100vh - 100px);
			overflow-y: auto;
			padding-right: 4px;
		}
	}

	.col-input,
	.col-results {
		min-width: 0;
	}

	.results-empty {
		padding: 20px 18px;
		border-radius: 10px;
		border: 1px dashed var(--color-border);
		background: var(--bg-secondary);
		text-align: center;
	}

	.results-empty-title {
		margin: 0 0 8px;
		font-size: 13px;
		font-weight: 600;
		color: var(--text-primary);
	}

	.results-empty-text {
		margin: 0;
		font-size: 12px;
		line-height: 1.5;
		color: var(--text-secondary);
	}

	.drop {
		display: block;
		padding: 12px 14px;
		background: var(--bg-secondary);
		border: 1px dashed var(--color-border);
		border-radius: 10px;
		cursor: text;
		transition: border-color 0.15s, background 0.15s;
	}

	.drop:focus-within {
		border-color: var(--color-accent, #8b5cf6);
		background: var(--bg-tertiary, var(--bg-secondary));
	}

	.drop-label {
		display: block;
		font-size: 11px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.06em;
		color: var(--text-tertiary);
		margin-bottom: 8px;
	}

	.ta {
		width: 100%;
		min-height: 260px;
		resize: vertical;
		border: none;
		background: transparent;
		color: var(--text-primary);
		font-family: var(--font-mono, ui-monospace, monospace);
		font-size: 12px;
		line-height: 1.45;
		outline: none;
	}

	.ta::placeholder {
		color: var(--text-tertiary);
	}

	.bar {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 8px;
		margin-top: 12px;
		margin-bottom: 14px;
	}

	.kbd {
		margin-left: auto;
		font-size: 11px;
		padding: 4px 8px;
		color: var(--text-tertiary);
		font-family: var(--font-mono);
	}

	.sr {
		position: absolute;
		width: 1px;
		height: 1px;
		opacity: 0;
		pointer-events: none;
	}

	.err {
		padding: 10px 12px;
		border-radius: 8px;
		background: var(--color-error-tint);
		border: 1px solid var(--color-error-border, rgba(239, 68, 68, 0.35));
		color: var(--color-error);
		font-size: 13px;
		margin-bottom: 12px;
	}

	.card {
		background: var(--bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 10px;
		padding: 14px 16px;
		margin-bottom: 12px;
	}

	.ver {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-start;
		gap: 12px 16px;
	}

	.ver-badge {
		flex-shrink: 0;
		padding: 6px 14px;
		border-radius: 999px;
		font-weight: 700;
		font-size: 13px;
		background: var(--bg-tertiary, rgba(0, 0, 0, 0.2));
		border: 1px solid var(--color-border);
		color: var(--text-primary);
	}

	.ver-desc {
		margin: 0;
		flex: 1;
		min-width: 200px;
		font-size: 13px;
		line-height: 1.5;
		color: var(--text-secondary);
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
		fill: var(--text-primary);
	}

	.ring-sub {
		font-size: 9px;
		fill: var(--text-tertiary);
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
		color: var(--text-secondary);
	}

	.minis {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(130px, 1fr));
		gap: 8px;
	}

	.mini {
		background: var(--bg-primary, rgba(0, 0, 0, 0.15));
		border: 1px solid var(--color-border);
		border-radius: 8px;
		padding: 8px 10px;
	}

	.mini-l {
		font-size: 10px;
		text-transform: uppercase;
		letter-spacing: 0.06em;
		color: var(--text-tertiary);
		margin-bottom: 4px;
	}

	.mini-v {
		font-size: 15px;
		font-weight: 700;
		color: var(--text-primary);
	}

	.mini-v.accent {
		color: var(--color-accent, #a78bfa);
	}

	.mini-v.soft {
		color: var(--text-secondary);
	}

	.block-h {
		margin: 0 0 10px;
		font-size: 11px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.08em;
		color: var(--color-accent, #a78bfa);
	}

	.card.fixes {
		border-color: color-mix(in srgb, var(--color-success, #22c55e) 28%, var(--color-border));
		background: linear-gradient(
			165deg,
			color-mix(in srgb, var(--color-success, #22c55e) 12%, var(--bg-secondary)) 0%,
			var(--bg-secondary) 55%
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
		color: var(--color-success, #22c55e);
		background: var(--color-success-tint);
		border: 1px solid color-mix(in srgb, var(--color-success, #22c55e) 35%, transparent);
	}

	.fixes-head-icon svg {
		display: block;
	}

	.fixes-h {
		margin: 0;
		flex: 1;
		min-width: 0;
		font-size: 11px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.08em;
		color: var(--color-success, #22c55e);
	}

	.fixes-count {
		flex-shrink: 0;
		font-size: 11px;
		font-weight: 700;
		padding: 4px 10px;
		border-radius: 999px;
		font-variant-numeric: tabular-nums;
		background: var(--color-success-tint);
		color: var(--color-success, #22c55e);
		border: 1px solid color-mix(in srgb, var(--color-success, #22c55e) 30%, transparent);
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
		background: var(--bg-primary, rgba(0, 0, 0, 0.12));
		border: 1px solid var(--color-border);
		transition: border-color 0.15s ease, background 0.15s ease;
	}

	.fix-item:hover {
		border-color: color-mix(in srgb, var(--color-success, #22c55e) 35%, var(--color-border));
		background: color-mix(in srgb, var(--color-success-tint) 40%, var(--bg-primary, rgba(0, 0, 0, 0.12)));
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
		color: var(--color-success, #22c55e);
		background: var(--color-success-tint);
		border: 1px solid color-mix(in srgb, var(--color-success, #22c55e) 25%, transparent);
	}

	.fix-text {
		flex: 1;
		min-width: 0;
		font-size: 13px;
		line-height: 1.5;
		color: var(--text-primary);
		white-space: pre-line;
	}

	.prewrap .mono {
		margin: 0;
		font-family: var(--font-mono);
		font-size: 12px;
		line-height: 1.5;
		color: var(--text-secondary);
		white-space: pre-wrap;
		word-break: break-word;
	}

	.prewrap .k {
		color: var(--color-accent, #a78bfa);
	}

	.cat {
		margin: 18px 0 8px;
		font-size: 11px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.1em;
		color: var(--text-tertiary);
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
		background: var(--bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 8px;
		border-left-width: 3px;
	}

	.check-pass {
		border-left-color: var(--color-success, #22c55e);
	}
	.check-warn {
		border-left-color: var(--color-warning, #f59e0b);
	}
	.check-fail {
		border-left-color: var(--color-error, #ef4444);
	}
	.check-info {
		border-left-color: var(--color-accent, #8b5cf6);
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
		background: color-mix(in srgb, var(--color-accent, #8b5cf6) 20%, transparent);
		color: var(--color-accent, #8b5cf6);
	}

	.check-body {
		flex: 1;
		min-width: 0;
	}

	.check-t {
		font-weight: 600;
		font-size: 13px;
		color: var(--text-primary);
		margin-bottom: 2px;
	}

	.check-val {
		display: inline-block;
		margin-top: 2px;
		padding: 1px 6px;
		border-radius: 4px;
		background: var(--bg-primary, rgba(0, 0, 0, 0.2));
		font-family: var(--font-mono);
		font-size: 11px;
		color: var(--color-info, #22d3ee);
		word-break: break-all;
		max-width: 100%;
	}

	.check-d {
		margin-top: 4px;
		font-size: 12px;
		line-height: 1.4;
		color: var(--text-secondary);
	}

	.check-w {
		font-size: 11px;
		color: var(--text-tertiary);
		font-family: var(--font-mono);
		flex-shrink: 0;
		align-self: center;
		min-width: 36px;
		text-align: right;
	}

	@media (max-width: 520px) {
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
