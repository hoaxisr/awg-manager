<script lang="ts">
	import { m } from '$lib/i18n';
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
		isMihomo?: boolean;
	}
	let { value = $bindable(''), showPreview = true, compactGeoPicker = true, isMihomo = false }: Props = $props();

	// ── constants ────────────────────────────────────────────────
	const RULES_LIST_PLACEHOLDER = $derived(`# ${m.routing_singbox_list_ph_domains()}
chatgpt.com
*.openai.com
https://gemini.google.com/app

# ${m.routing_singbox_list_ph_subdomains()}
.perplexity.ai
domain_suffix:deepseek.com

# ${m.routing_singbox_list_ph_domain()}
domain:claude.ai

# IP/CIDR
1.1.1.1
8.8.8.0/24

# ${m.routing_singbox_list_ph_extra()}
keyword:youtube
geosite:xai`);

	const MIHOMO_RULES_LIST_PLACEHOLDER = $derived(`# ${m.routing_singbox_list_ph_domains()}
chatgpt.com
*.openai.com
https://gemini.google.com/app

# ${m.routing_singbox_list_ph_domain()}
domain:claude.ai

# IP / CIDR
1.1.1.1
8.8.8.0/24
2606:4700::/32

# ${m.routing_singbox_list_ph_extra()}
geosite:google-gemini
geoip:telegram`);

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
			{geoExpanding ? m.routing_singbox_list_expanding() : m.routing_singbox_list_label()}
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
				placeholder={isMihomo ? MIHOMO_RULES_LIST_PLACEHOLDER : RULES_LIST_PLACEHOLDER}
				onscroll={syncRulesListLineNumbersScroll}
			/>
		</div>
	</div>
	<details class="inline-help">
		<summary>{m.routing_singbox_list_help_summary()}</summary>

		<div class="inline-help-body">
			{#if isMihomo}
				<p class="inline-help-intro">
					{m.routing_mihomo_list_help_intro_1()} <br>
					{m.routing_mihomo_list_help_intro_2_pre()} <code>#</code>, <code>//</code>, <code>;</code> {m.routing_mihomo_list_help_intro_2_post()} <br>
					{m.routing_mihomo_list_help_intro_3()}
				</p>

				<section class="inline-help-section">
					<div class="help-label">{m.routing_singbox_list_help_domains_label()}</div>
					<ul>
						<li><code>domain.com</code>, <code>*.domain.com</code>, <code>domain_suffix:domain.com</code> {m.routing_mihomo_list_help_domains_item()}</li>
						<li><code>https://example.domain.com/…</code> {m.routing_mihomo_list_help_url()}</li>
						<li><code>*.rf</code> {m.routing_mihomo_list_help_zone()}</li>
					</ul>
				</section>

				<section class="inline-help-section">
					<div class="help-label">{m.routing_mihomo_list_help_exact_label()}</div>
					<ul>
						<li><code>domain:domain.com</code>, <code>exact:domain.com</code> {m.routing_mihomo_list_help_exact_item()}</li>
					</ul>
				</section>

				<section class="inline-help-section">
					<div class="help-label">{m.routing_mihomo_list_help_keyword_label()}</div>
					<ul>
						<li><code>keyword:word</code> {m.routing_mihomo_list_help_keyword_item()}</li>
					</ul>
				</section>

				<section class="inline-help-section">
					<div class="help-label">{m.routing_mihomo_list_help_ip_label()}</div>
					<ul>
						<li><code>1.1.1.1</code> {m.routing_mihomo_list_help_ip_single()}</li>
						<li><code>8.8.8.0/24</code> {m.routing_mihomo_list_help_ip_subnet()}</li>
						<li><code>2a00:1450::/32</code> {m.routing_mihomo_list_help_ip_v6()}</li>
						<li>{m.routing_mihomo_list_help_ip_prefixes()}</li>
					</ul>
				</section>

				<section class="inline-help-section">
					<div class="help-label">{m.routing_mihomo_list_help_geo_label()}</div>
					<ul>
						<li><code>geosite:TAG</code> {m.routing_mihomo_list_help_geosite()}</li>
						<li><code>geoip:TAG</code> {m.routing_mihomo_list_help_geoip()}</li>
					</ul>
				</section>
			{:else}
				<p class="inline-help-intro">
					{m.routing_singbox_list_help_intro_1()} <br>
					{m.routing_singbox_list_help_intro_2_pre()} <code>#</code>, <code>//</code>, <code>;</code> {m.routing_singbox_list_help_intro_2_post()} <br>
					{m.routing_singbox_list_help_intro_3()}
				</p>

				<section class="inline-help-section">
					<div class="help-label">{m.routing_singbox_list_help_domains_label()}</div>
					<ul>
						<li><code>domain.com</code>, <code>*.domain.com</code>, <code>domain_suffix:domain.com</code> {m.routing_singbox_list_help_host_and_subs()} <code>"domain.com"</code> {m.routing_singbox_list_help_no_dot()}</li>
						<li><code>https://example.domain.com/…</code> {m.routing_singbox_list_help_url()}</li>
						<li><code>*.рф</code> {m.routing_singbox_list_help_zone()} <code>xn--p1ai</code> {m.routing_singbox_list_help_no_leading_dot()}</li>
					</ul>
				</section>

				<section class="inline-help-section">
					<div class="help-label">{m.routing_singbox_list_help_sub_only_label()} <code>domain</code>)</div>
					<ul>
						<li><code>.domain.com</code> {m.routing_singbox_list_help_suffix()} <em>{m.routing_singbox_list_help_with()}</em> {m.routing_singbox_list_help_dot_in_json()} <code>[".domain.com"]</code> {m.routing_singbox_list_help_apex()}</li>
						<li><code>domain_suffix:.domain.com</code> {m.routing_singbox_list_help_dotted()} <code>".domain.com"</code></li>
					</ul>
				</section>

				<section class="inline-help-section">
					<div class="help-label">{m.routing_singbox_list_help_exact_label()}</div>
					<ul>
						<li><code>domain:domain.com</code> {m.routing_singbox_list_help_only()} <code>domain</code>, {m.routing_singbox_list_help_no_subdomains()}</li>
					</ul>
				</section>

				<section class="inline-help-section">
					<div class="help-label">{m.routing_singbox_list_help_ip_label()}</div>
					<ul>
						<li><code>1.1.1.1</code> {m.routing_singbox_list_help_in_json_as()} <code>1.1.1.1/32</code>; {m.routing_singbox_list_help_reverse_bare_ip()}</li>
						<li><code>8.8.8.0/24</code> {m.routing_singbox_list_help_cidr_as_is()} <code>/32</code> {m.routing_singbox_list_help_not_compressed()}</li>
						<li>{m.routing_singbox_list_help_prefixes()} <code>ip:</code>, <code>cidr:</code>, <code>src_ip:</code> {m.routing_singbox_list_help_same_rule()}</li>
						<li>{m.routing_singbox_list_help_ipv6_unsupported()}</li>
					</ul>
				</section>

				<section class="inline-help-section">
					<div class="help-label">{m.routing_singbox_list_help_geo_label()}</div>
					<ul>
						<li><code>geosite:TAG</code> {m.routing_singbox_list_help_geosite_pre()} <strong>{m.routing_singbox_list_help_without()}</strong> {m.routing_singbox_list_help_leading_dot_as()} <code>domain.com</code>)</li>
						<li><code>geoip:TAG</code> {m.routing_singbox_list_help_geoip()} <code>/32</code></li>
						<li><code>keyword:TAG</code>, <code>regex:…</code> {m.routing_singbox_list_help_keyword_regex()}</li>
					</ul>
				</section>

				<section class="inline-help-section">
					<div class="help-label">{m.routing_singbox_list_help_advanced_label()}</div>
					<ul>
						<li><code>port:443</code>, <code>process:curl</code>, <code>package:…</code>, <code>network:tcp|udp</code></li>
						<li>{m.routing_singbox_list_help_advanced_group()}</li>
					</ul>
				</section>

				<section class="inline-help-section">
					<div class="help-label">{m.routing_singbox_list_help_careful_label()}</div>
					<ul>
						<li><code>port:443</code> {m.routing_singbox_list_help_port()}</li>
						<li><code>process:</code> / <code>process_path:</code> {m.routing_singbox_list_help_process()}</li>
					</ul>
				</section>

				<section class="inline-help-section">
					<div class="help-label">{m.routing_singbox_list_help_not_yet_label()}</div>
					<ul>
						<li>{m.routing_singbox_list_help_ipv6_exceptions()} <code>@@</code>, <code>port_range:</code></li>
						<li>{m.routing_singbox_list_help_logic()} <code>and</code> / <code>or</code> {m.routing_singbox_list_help_extra_fields()} <code>JSON</code></li>
					</ul>
				</section>
			{/if}
		</div>
	</details>

	{#if showPreview}
		{#if listInputEmpty}
			<div class="hint list-empty-hint">{m.routing_singbox_list_empty_hint()}</div>
		{:else if listParsePreview.errors.length > 0}
			<div class="parse-messages parse-messages-error">
				<div class="parse-messages-title">{m.routing_singbox_list_parse_errors()}</div>
				<ul>
					{#each listParsePreview.errors as msg}
						<li>{msg}</li>
					{/each}
				</ul>
			</div>
		{/if}
		{#if listParsePreview.warnings.length > 0}
			<div class="parse-messages parse-messages-warning">
				<div class="parse-messages-title">{m.routing_singbox_list_parse_warnings()}</div>
				<ul>
					{#each listParsePreview.warnings as msg}
						<li>{msg}</li>
					{/each}
				</ul>
			</div>
		{/if}
		{#if listParsePreview.rules.length > 0}
			<div class="info">
				{m.routing_singbox_list_rules_to_create({ count: listParsePreview.rules.length })}
			</div>
		{/if}
		{#if listParsePreview.rules.length > 0}
			<details class="json-preview">
				<summary>{m.routing_singbox_list_json_preview()}</summary>
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
		background: var(--surface-1, rgba(255, 255, 255, 0.035));
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
		background: var(--surface-2, rgba(255, 255, 255, 0.06));
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
		background: var(--surface-1, rgba(255, 255, 255, 0.035));
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
		border-color: var(--danger, #dc2626);
		color: var(--danger, #dc2626);
		background: rgba(220, 38, 38, 0.08);
	}

	.parse-messages-warning {
		border-color: var(--color-warning, #d97706);
		color: var(--color-warning, #d97706);
		background: rgba(217, 119, 6, 0.08);
	}
	.info {
		color: #10b981;
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
