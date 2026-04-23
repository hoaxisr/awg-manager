<script lang="ts">
	interface Props {
		domains: string[];
		onchange: (domains: string[]) => void;
		allowGeoTags?: boolean;
	}

	let { domains, onchange, allowGeoTags = false }: Props = $props();

	let text = $state('');
	let errorLines = $state<number[]>([]);
	let editedByUser = $state(false);

	// Sync text from domains prop when not actively editing
	$effect(() => {
		if (!editedByUser) {
			text = domains.join('\n');
		}
	});

	const ipv4CidrRe = /^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\/\d{1,2}$/;
	const ipv6CidrRe = /^[0-9a-fA-F:]+\/\d{1,3}$/;

	function isValidDomain(line: string): boolean {
		const trimmed = line.trim();
		if (!trimmed) return true; // empty lines are ok, filtered out
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

	function handleInput(e: Event) {
		const value = (e.target as HTMLTextAreaElement).value;
		text = value;
		editedByUser = true;

		const lines = value.split('\n');
		const errors: number[] = [];
		const validDomains: string[] = [];

		for (let i = 0; i < lines.length; i++) {
			const line = lines[i].trim().toLowerCase();
			if (!line) continue;
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

		errorLines = errors;
		// Deduplicate
		const unique = [...new Set(validDomains)];
		onchange(unique);
	}

	let domainCount = $derived(domains.length);
	let textareaEl = $state<HTMLTextAreaElement | null>(null);

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
		let start = 0;
		for (let i = 0; i < idx; i++) {
			start += lines[i].length + 1; // +1 for the newline itself
		}
		const end = start + lines[idx].length;
		el.focus();
		el.setSelectionRange(start, end);
	}
</script>

<div class="domain-editor">
	<div class="editor-header">
		<span class="editor-count">{domainCount} записей</span>
		{#if errorLines.length > 0}
			<span class="editor-errors">
				<span class="editor-errors-label">Ошибки в строках:</span>
				{#each errorLines as line (line)}
					<button
						type="button"
						class="editor-error-chip"
						title="Перейти к строке {line}"
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
		placeholder="youtube.com&#10;instagram.com&#10;tiktok.com"
		value={text}
		oninput={handleInput}
	></textarea>
	<span class="editor-hint">Один домен или CIDR на строку. Формат: domain.tld, IP/mask или IPv6/prefix. Нажмите номер строки, чтобы перейти к ошибке.</span>
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
</style>
