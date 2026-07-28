<script lang="ts">
	import { Button, SyntaxHighlightedTextarea } from '$lib/components/ui';
	import { highlightInlineRuleListContent } from '$lib/utils/singboxInlineRulesHighlight';
	import { api } from '$lib/api/client';
	import type { GeoFileEntry } from '$lib/types';
	import { HrNeoGeoTagPicker } from '$lib/components/hrneo';
	import {
		isInlineRuleListEmpty,
		parseInlineRuleList,
	} from '$lib/utils/singboxInlineRules';
	import { expandGeoLinesInInput } from '$lib/utils/singboxInlineGeoExpand';

	interface Props {
		value?: string;
		showPreview?: boolean;
		/** Компактный geo-picker (половинная высота списка тегов). */
		compactGeoPicker?: boolean;
	}
	let { value = $bindable(''), showPreview = true, compactGeoPicker = true }: Props = $props();

	// ── constants ────────────────────────────────────────────────
	const RULES_LIST_PLACEHOLDER = `# Домены и все поддомены
chatgpt.com
*.openai.com
https://gemini.google.com/app

# Только поддомены
.perplexity.ai
domain_suffix:deepseek.com

# Только домен
domain:claude.ai

# IP/CIDR
1.1.1.1
8.8.8.0/24

# Дополнительно
keyword:youtube
geosite:xai`;

	// ── geo state ────────────────────────────────────────────────
	let geoFiles = $state<GeoFileEntry[]>([]);
	let geositePickerOpen = $state(false);
	let geoipPickerOpen = $state(false);
	let expandedRulesList = $state('');
	let geoExpandWarnings = $state<string[]>([]);
	let geoExpanding = $state(false);

	let geositeFiles = $derived(geoFiles.filter((g) => g.type === 'geosite').map((g) => g.path));
	let geoipFiles = $derived(geoFiles.filter((g) => g.type === 'geoip').map((g) => g.path));

	$effect(() => {
		void (async () => {
			try {
				geoFiles = (await api.getGeoFiles()) ?? [];
			} catch {
				geoFiles = [];
			}
		})();
	});

	$effect(() => {
		const input = value;
		const timer = setTimeout(() => {
			void (async () => {
				geoExpanding = true;
				try {
					const { text, warnings } = await expandGeoLinesInInput(input, async (kind, tag) => {
						const res = await api.expandGeoTag(kind, tag);
						return res.lines;
					});
					if (input === value) {
						expandedRulesList = text;
						geoExpandWarnings = warnings;
					}
				} catch {
					if (input === value) {
						expandedRulesList = input;
						geoExpandWarnings = [];
					}
				} finally {
					if (input === value) geoExpanding = false;
				}
			})();
		}, 350);
		return () => clearTimeout(timer);
	});

	// ── derived preview ──────────────────────────────────────────
	const listParsePreview = $derived.by(() => {
		const source = expandedRulesList || value;
		const parsed = parseInlineRuleList(source);
		if (geoExpandWarnings.length === 0) return parsed;
		return {
			...parsed,
			warnings: [...geoExpandWarnings, ...parsed.warnings],
		};
	});

	const listInputEmpty = $derived(isInlineRuleListEmpty(value));

	// ── append helpers ───────────────────────────────────────────
	function appendRulesLine(token: string): void {
		const trimmed = value.trimEnd();
		value = trimmed ? `${trimmed}\n${token}` : token;
	}

	function appendGeositeLine(token: string): void {
		appendRulesLine(token);
		geositePickerOpen = false;
	}

	// ── line number gutter ───────────────────────────────────────
	function lineNumbersFor(text: string): string {
		const count = Math.max(1, text.split(/\r?\n/).length);
		return Array.from({ length: count }, (_, i) => String(i + 1)).join('\n');
	}

	let rulesListTextarea = $state<HTMLTextAreaElement | null>(null);
	let rulesListLineNumberGutter = $state<HTMLPreElement | null>(null);

	const rulesListLineNumbers = $derived(lineNumbersFor(value));

	function syncRulesListLineNumbersScroll(): void {
		if (!rulesListTextarea || !rulesListLineNumberGutter) return;
		rulesListLineNumberGutter.scrollTop = rulesListTextarea.scrollTop;
	}
</script>

<div class="inline-rule-list-editor">
	<div class="list-toolbar">
		<div class="lbl" class:lbl-expanding={geoExpanding}>
			{geoExpanding ? 'Разворачиваем geosite:/geoip: теги…' : 'Список правил'}
		</div>
		<div class="list-toolbar-actions">
			<Button variant="ghost" size="sm" onclick={() => (geositePickerOpen = !geositePickerOpen)}>
				+ geosite:TAG
			</Button>
			<Button variant="ghost" size="sm" onclick={() => (geoipPickerOpen = !geoipPickerOpen)}>
				+ geoip:TAG
			</Button>
		</div>
	</div>
	{#if geositePickerOpen}
		<HrNeoGeoTagPicker
			kind="geosite"
			files={geositeFiles}
			compact={compactGeoPicker}
			onpick={appendGeositeLine}
			onclose={() => (geositePickerOpen = false)}
		/>
	{/if}
	{#if geoipPickerOpen}
		<HrNeoGeoTagPicker
			kind="geoip"
			files={geoipFiles}
			compact={compactGeoPicker}
			onpick={(t) => appendRulesLine(t)}
			onclose={() => (geoipPickerOpen = false)}
		/>
	{/if}
	<div class="rules-editor">
		<pre class="line-numbers" aria-hidden="true" bind:this={rulesListLineNumberGutter}>{rulesListLineNumbers}</pre>
		<div class="rules-editor-input">
			<SyntaxHighlightedTextarea
				bind:value={value}
				bind:textareaRef={rulesListTextarea}
				highlight={highlightInlineRuleListContent}
				wrap="pre-wrap"
				class="rules-list-ta"
				placeholder={RULES_LIST_PLACEHOLDER}
				onscroll={syncRulesListLineNumbersScroll}
			/>
		</div>
	</div>
	<details class="inline-help">
		<summary>Подсказка по формату списка</summary>

		<div class="inline-help-body">
			<p class="inline-help-intro">
				Одна строка — одно значение. Пустые строки игнорируются. <br>
				В списке можно писать комментарии: <code>#</code>, <code>//</code>, <code>;</code> (целая строка или в конце — после пробела). <br>
				При сохранении или переходе на вкладку JSON комментарии удаляются и в JSON не сохраняются. Порядок строк в списке не имеет значения и может меняться при сохранении.
			</p>

			<section class="inline-help-section">
				<div class="help-label">Домены и поддомены</div>
				<ul>
					<li><code>domain.com</code>, <code>*.domain.com</code>, <code>domain_suffix:domain.com</code> → хост и его поддомены (в JSON: <code>"domain.com"</code> без точки)</li>
					<li><code>https://example.domain.com/…</code> — из URL берётся hostname и хранится так же</li>
					<li><code>*.рф</code> — доменная зона; кириллица будет конвертирована в punycode <code>xn--p1ai</code> без ведущей точки</li>
				</ul>
			</section>

			<section class="inline-help-section">
				<div class="help-label">Только поддомены (без отдельного <code>domain</code>)</div>
				<ul>
					<li><code>.domain.com</code> — суффикс <em>с</em> точкой в JSON: <code>[".domain.com"]</code> (apex не матчится)</li>
					<li><code>domain_suffix:.domain.com</code> — явная dotted-форма, в JSON будет <code>".domain.com"</code></li>
				</ul>
			</section>

			<section class="inline-help-section">
				<div class="help-label">Только точный хост</div>
				<ul>
					<li><code>domain:domain.com</code> — только <code>domain</code>, без поддоменов</li>
				</ul>
			</section>

			<section class="inline-help-section">
				<div class="help-label">IP и подсети (только IPv4)</div>
				<ul>
					<li><code>1.1.1.1</code> — в JSON как <code>1.1.1.1/32</code>; при обратном переводе снова голый IP</li>
					<li><code>8.8.8.0/24</code> — CIDR как есть; маски кроме <code>/32</code> не сжимаются</li>
					<li>префиксы <code>ip:</code>, <code>cidr:</code>, <code>src_ip:</code> — то же правило</li>
					<li>IPv6 в режиме «Список» не поддерживается — адреса и префиксы IPv6 задавайте в JSON</li>
				</ul>
			</section>

			<section class="inline-help-section">
				<div class="help-label">Geo и прочие matchers</div>
				<ul>
					<li><code>geosite:TAG</code> — разворачивается в домены; суффиксы из .dat — <strong>без</strong> ведущей точки (как <code>domain.com</code>)</li>
					<li><code>geoip:TAG</code> — разворачивается в CIDR; одиночные хосты из geo — в списке без <code>/32</code></li>
					<li><code>keyword:TAG</code>, <code>regex:…</code> — отдельные поля в JSON</li>
				</ul>
			</section>

			<section class="inline-help-section">
				<div class="help-label">Расширенные matchers</div>
				<ul>
					<li><code>port:443</code>, <code>process:curl</code>, <code>package:…</code>, <code>network:tcp|udp</code></li>
					<li>каждый тип — отдельная группа правил в JSON (не смешивается с доменами в одной записи)</li>
				</ul>
			</section>

			<section class="inline-help-section">
				<div class="help-label">Осторожно</div>
				<ul>
					<li><code>port:443</code> — отдельное правило на весь HTTPS-трафик</li>
					<li><code>process:</code> / <code>process_path:</code> на Keenetic/Entware — только локальные процессы роутера, не LAN-клиенты</li>
				</ul>
			</section>

			<section class="inline-help-section">
				<div class="help-label">Пока не в режиме «Список»</div>
				<ul>
					<li>IPv6, исключения <code>@@</code>, <code>port_range:</code></li>
					<li>логика <code>and</code> / <code>or</code> и любые лишние поля sing-box — только режим <code>JSON</code></li>
				</ul>
			</section>
		</div>
	</details>

	{#if showPreview}
		{#if listInputEmpty}
			<div class="hint list-empty-hint">Список пуст — добавьте домены, IP или geosite:/geoip: тег.</div>
		{:else if listParsePreview.errors.length > 0}
			<div class="parse-messages parse-messages-error">
				<div class="parse-messages-title">Ошибки разбора</div>
				<ul>
					{#each listParsePreview.errors as msg}
						<li>{msg}</li>
					{/each}
				</ul>
			</div>
		{/if}
		{#if listParsePreview.warnings.length > 0}
			<div class="parse-messages parse-messages-warning">
				<div class="parse-messages-title">Предупреждения</div>
				<ul>
					{#each listParsePreview.warnings as msg}
						<li>{msg}</li>
					{/each}
				</ul>
			</div>
		{/if}
		{#if listParsePreview.rules.length > 0}
			<div class="info">
				Будет создано групп правил: {listParsePreview.rules.length}
			</div>
		{/if}
		{#if listParsePreview.rules.length > 0}
			<details class="json-preview">
				<summary>Предпросмотр JSON</summary>
				<pre>{JSON.stringify(listParsePreview.rules, null, 2)}</pre>
			</details>
		{/if}
	{/if}
</div>

<style>
	.inline-rule-list-editor {
		display: grid;
		gap: 0.25rem;
		min-width: 0;
	}
	.lbl {
		font-size: 0.85rem;
		font-weight: 600;
		color: var(--text-primary, var(--text));
	}
	.lbl-expanding {
		color: var(--accent, #3b82f6);
		font-weight: 600;
	}
	.list-toolbar {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.35rem 0.75rem;
	}
	.list-toolbar-actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.35rem;
		margin-left: auto;
	}
	.hint {
		font-size: 0.75rem;
		color: var(--muted-text);
		line-height: 1.4;
		margin-top: 0.25rem;
	}
	.inline-help {
		margin-top: 0.35rem;
		padding: 0.5rem 0.65rem;
		border: 1px solid var(--border);
		border-radius: 0.45rem;
		background: var(--color-bg-tertiary);
		color: var(--muted-text);
		font-size: 0.8rem;
		line-height: 1.45;
	}

	.inline-help summary {
		cursor: pointer;
		color: var(--text);
		font-weight: 700;
		outline: none;
	}

	.inline-help-body {
		margin-top: 0.45rem;
		display: grid;
		gap: 0.55rem;
	}

	.inline-help-intro {
		margin: 0;
	}

	.inline-help-section ul {
		margin: 0.2rem 0 0;
		padding-left: 1.15rem;
	}

	.inline-help-section li {
		margin: 0.12rem 0;
	}

	.inline-help-section li::marker {
		color: var(--muted-text);
	}

	.inline-help code {
		background: var(--color-bg-hover);
		border-radius: 0.25rem;
		padding: 0.05rem 0.25rem;
		font-size: 0.78rem;
	}

	.help-label {
		color: var(--text);
		font-weight: 600;
	}
	.parse-messages {
		padding: 0.55rem 0.65rem;
		border: 1px solid var(--border);
		border-radius: 0.45rem;
		background: var(--color-bg-tertiary);
		font-size: 0.82rem;
		line-height: 1.4;
		max-height: min(12rem, 32vh);
		overflow: auto;
		overflow-wrap: anywhere;
		word-break: break-word;
		min-width: 0;
	}

	.parse-messages-title {
		font-weight: 700;
		margin-bottom: 0.35rem;
	}

	.parse-messages ul {
		margin: 0;
		padding-left: 1.1rem;
		display: grid;
		gap: 0.22rem;
	}

	.parse-messages li {
		margin: 0;
	}

	.parse-messages-error {
		border-color: var(--color-error);
		color: var(--color-error);
		background: color-mix(in srgb, var(--color-error) 8%, transparent);
	}

	.parse-messages-warning {
		border-color: var(--color-warning, #d97706);
		color: var(--color-warning, #d97706);
		background: rgba(217, 119, 6, 0.08);
	}
	.info {
		color: var(--color-success);
		font-size: 0.85rem;
	}
	.json-preview {
		margin: 0;
	}
	.json-preview summary {
		cursor: pointer;
		font-size: 0.85rem;
		color: var(--accent, #3b82f6);
		outline: none;
	}
	.json-preview pre {
		background: var(--bg);
		border: 1px solid var(--border);
		border-radius: 4px;
		padding: 0.5rem 0.6rem;
		font-size: 0.75rem;
		overflow-x: auto;
		margin-top: 0.25rem;
	}
	.rules-editor {
		display: grid;
		grid-template-columns: auto 1fr;
		height: 16rem;
		min-height: 8rem;
		max-height: min(70vh, 36rem);
		resize: vertical;
		overflow: hidden;
		min-width: 0;
		align-items: stretch;
		background: var(--sbr-control-bg, var(--bg-tertiary, var(--bg)));
		border: 1px solid var(--sbr-control-border, var(--border));
		border-radius: var(--sbr-control-radius, 4px);
	}
	.line-numbers {
		margin: 0;
		padding: 0.5rem 0.45rem 0.5rem 0.55rem;
		border-right: 1px solid var(--sbr-control-border, var(--border));
		background: color-mix(in srgb, var(--sbr-control-bg, var(--bg-tertiary, var(--bg))) 85%, var(--sbr-control-border, var(--border)));
		color: var(--muted-text);
		font-family: ui-monospace, monospace;
		font-size: 0.8rem;
		line-height: 1.45;
		text-align: right;
		user-select: none;
		overflow: hidden;
		min-width: 2.25rem;
		white-space: pre;
		height: 100%;
		box-sizing: border-box;
	}
	.rules-editor-input {
		min-width: 0;
		min-height: 0;
		height: 100%;
		padding: 0.5rem 0.6rem;
		box-sizing: border-box;
	}

	.rules-editor-input :global(.shl-stack) {
		font-family: ui-monospace, monospace;
		font-size: 0.8rem;
		line-height: 1.45;
	}
</style>
