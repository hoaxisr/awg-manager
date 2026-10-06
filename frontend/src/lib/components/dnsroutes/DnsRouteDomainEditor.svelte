<script lang="ts">
	import { m } from '$lib/i18n';

	interface Props {
		domains: string[];
		onchange: (domains: string[], manualText: string) => void;
		allowGeoTags?: boolean;
		textValue?: string;
	}

	let { domains, onchange, allowGeoTags = false, textValue }: Props = $props();

	let text = $state('');
	let errorLines = $state<number[]>([]);
	let editedByUser = $state(false);

	// Sync text from props when not actively editing.
	// textValue preserves comments/blank lines; legacy rules fall back to domains.
	$effect(() => {
		if (!editedByUser) {
			text = textValue ?? domains.join('\n');
		}
	});

	const ipv4CidrRe = /^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\/\d{1,2}$/;
	const ipv6CidrRe = /^[0-9a-fA-F:]+\/\d{1,3}$/;

	function isValidDomain(line: string): boolean {
		const trimmed = line.trim();
		if (!trimmed) return true; // empty lines are ok, filtered out
		if (trimmed.startsWith('#')) return true; // full-line comments are ok, filtered out
		// HydraRoute geosite: tags (e.g. geosite:GOOGLE, geosite:TELEGRAM)
		if (allowGeoTags && /^geosite:[A-Za-z0-9_-]+$/i.test(trimmed)) return true;
		if (trimmed.includes(' ')) return false;
		if (trimmed.includes('*')) return false;
		// Allow IPv4 CIDR (e.g. 8.8.8.0/24)
		if (ipv4CidrRe.test(trimmed)) return true;
		// Allow IPv6 CIDR (e.g. 2001:b28:f23d::/48)
		if (ipv6CidrRe.test(trimmed)) return true;
		if (trimmed.includes('/')) return false;
		// Allow bare TLDs (ru, com, org) — single label without dots
		if (/^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?$/.test(trimmed)) return true;
		if (!trimmed.includes('.')) return false;
		return true;
	}

	function parseText(value: string): { domains: string[]; errors: number[] } {
		const lines = value.split('\n');
		const errors: number[] = [];
		const validDomains: string[] = [];

		for (let i = 0; i < lines.length; i++) {
			const raw = lines[i].trim();
			const line = raw.toLowerCase();
			if (!line) continue;
			if (line.startsWith('#')) continue;
			if (!isValidDomain(line)) {
				errors.push(i + 1);
			} else {
				// Preserve geosite: tags as-is (case-sensitive tag names)
				if (/^geosite:/i.test(line)) {
					validDomains.push(line);
				} else {
					// Strip leading dots (.ru → ru)
					let normalized = line.replace(/^\.+/, '');
					if (normalized) validDomains.push(normalized);
				}
			}
		}

		return {
			domains: [...new Set(validDomains)],
			errors
		};
	}

	function applyText(value: string) {
		text = value;
		editedByUser = true;

		const parsed = parseText(value);
		errorLines = parsed.errors;
		onchange(parsed.domains, value);
	}

	function handleInput(e: Event) {
		applyText((e.target as HTMLTextAreaElement).value);
	}

	let domainCount = $derived(domains.length);
	let textareaEl = $state<HTMLTextAreaElement | null>(null);

	function lineOffset(lines: string[], lineIndex: number): number {
		let offset = 0;
		for (let i = 0; i < lineIndex; i++) {
			offset += lines[i].length + 1;
		}
		return offset;
	}

	function selectedLineRange(el: HTMLTextAreaElement, lines: string[]) {
		const start = el.selectionStart;
		const end = el.selectionEnd;

		let cursor = 0;
		let startLine = 0;
		let endLine = lines.length - 1;

		for (let i = 0; i < lines.length; i++) {
			const lineEnd = cursor + lines[i].length;
			if (start >= cursor && start <= lineEnd) startLine = i;
			if (end >= cursor && end <= lineEnd) {
				endLine = i;
				break;
			}
			cursor = lineEnd + 1;
		}

		return { startLine, endLine };
	}

	function toggleCommentSelection() {
		const el = textareaEl;
		if (!el) return;

		const lines = text.split('\n');
		const { startLine, endLine } = selectedLineRange(el, lines);
		const selected = lines.slice(startLine, endLine + 1);

		const shouldComment = selected.some((line) => {
			const trimmed = line.trim();
			return trimmed !== '' && !trimmed.startsWith('#');
		});

		const changed = selected.map((line) => {
			if (line.trim() === '') return line;

			if (shouldComment) {
				const indent = line.match(/^\s*/)?.[0] ?? '';
				return `${indent}# ${line.slice(indent.length)}`;
			}

			return line.replace(/^(\s*)# ?/, '$1');
		});

		const nextLines = [
			...lines.slice(0, startLine),
			...changed,
			...lines.slice(endLine + 1)
		];

		const nextText = nextLines.join('\n');
		const nextStart = lineOffset(nextLines, startLine);
		const nextEnd = lineOffset(nextLines, endLine) + nextLines[endLine].length;

		applyText(nextText);

		setTimeout(() => {
			el.focus();
			el.setSelectionRange(nextStart, nextEnd);
		}, 0);
	}

	function handleKeydown(e: KeyboardEvent) {
		const isToggleComment =
			(e.ctrlKey || e.metaKey) &&
			(
				e.code === 'Slash' || // layout-independent: physical Slash key
				e.key === '/'
			);
		if (isToggleComment) {
			e.preventDefault();
			toggleCommentSelection();
		}
	}

	// Click on an error badge → focus the textarea, select the bad line,
	// and scroll it into view. Selecting via setSelectionRange is enough
	// — the browser brings the selection into view automatically, which
	// is faster than computing a manual scrollTop offset and handles
	// line-wrapping correctly.
	function jumpToLine(lineNumber: number) {
		const el = textareaEl;
		if (!el) return;
		const lines = text.split('\n');
		const idx = lineNumber - 1;
		if (idx < 0 || idx >= lines.length) return;
		const start = lineOffset(lines, idx);
		const end = start + lines[idx].length;
		el.focus();
		el.setSelectionRange(start, end);
	}
</script>

<div class="domain-editor">
	<div class="editor-header">
		<span class="editor-count">{m.dns_routes_editor_entries_count({ count: domainCount })}</span>
		{#if errorLines.length > 0}
			<span class="editor-errors">
				<span class="editor-errors-label">{m.dns_routes_editor_errors_label()}</span>
				{#each errorLines as line (line)}
					<button
						type="button"
						class="editor-error-chip"
						title={m.dns_routes_editor_goto_line({ line })}
						onclick={() => jumpToLine(line)}
					>{line}</button>
				{/each}
			</span>
		{/if}
	</div>
	<textarea
		bind:this={textareaEl}
		class="form-textarea"
		class:has-errors={errorLines.length > 0}
		rows="8"
		placeholder={m.dns_routes_editor_placeholder()}
		value={text}
		oninput={handleInput}
		onkeydown={handleKeydown}
	></textarea>
	<span class="editor-hint editor-hint-multiline">{m.dns_routes_editor_hint()}</span>
</div>

<style>
	.domain-editor {
		display: flex;
		flex-direction: column;
		gap: 0.375rem;
	}

	.editor-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
	}

	.editor-count {
		font-size: 0.75rem;
		color: var(--text-muted);
	}

	.editor-errors {
		display: inline-flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.25rem;
		font-size: 0.75rem;
		color: var(--error, #ef4444);
	}

	.editor-errors-label {
		margin-right: 0.25rem;
	}

	.editor-error-chip {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		min-width: 1.75rem;
		padding: 0.125rem 0.375rem;
		border: 1px solid var(--error, #ef4444);
		border-radius: 4px;
		background: color-mix(in srgb, var(--error, #ef4444) 10%, transparent);
		color: var(--error, #ef4444);
		font-size: 0.75rem;
		font-weight: 600;
		font-family: ui-monospace, SFMono-Regular, 'SF Mono', Menlo, monospace;
		cursor: pointer;
		transition: background 0.15s, transform 0.1s;
	}

	.editor-error-chip:hover {
		background: color-mix(in srgb, var(--error, #ef4444) 25%, transparent);
	}

	.editor-error-chip:active {
		transform: scale(0.95);
	}

	.form-textarea {
		width: 100%;
		padding: 0.5rem 0.75rem;
		border: 1px solid var(--border);
		border-radius: 6px;
		background: var(--bg-primary);
		color: var(--text-primary);
		font-size: 0.8125rem;
		font-family: ui-monospace, SFMono-Regular, 'SF Mono', Menlo, monospace;
		line-height: 1.6;
		resize: vertical;
		box-sizing: border-box;
	}

	.form-textarea:focus {
		outline: none;
		border-color: var(--accent);
	}

	.form-textarea.has-errors {
		border-color: var(--error, #ef4444);
	}

	.editor-hint {
		font-size: 0.6875rem;
		color: var(--text-muted);
	}

	.editor-hint-multiline {
		white-space: pre-line;
	}
</style>
