<script lang="ts">
	interface Props {
		domains: string[];
		onchange: (domains: string[]) => void;
	}

	let { domains, onchange }: Props = $props();

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
		if (trimmed.includes(' ')) return false;
		if (trimmed.includes('*')) return false;
		// Allow IPv4 CIDR (e.g. 8.8.8.0/24)
		if (ipv4CidrRe.test(trimmed)) return true;
		// Allow IPv6 CIDR (e.g. 2001:b28:f23d::/48)
		if (ipv6CidrRe.test(trimmed)) return true;
		if (trimmed.includes('/')) return false;
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
				validDomains.push(line);
			}
		}

		errorLines = errors;
		// Deduplicate
		const unique = [...new Set(validDomains)];
		onchange(unique);
	}

	let domainCount = $derived(domains.length);
</script>

<div class="domain-editor">
	<div class="editor-header">
		<span class="editor-count">{domainCount} записей</span>
		{#if errorLines.length > 0}
			<span class="editor-errors">
				Ошибки в строках: {errorLines.join(', ')}
			</span>
		{/if}
	</div>
	<textarea
		class="form-textarea"
		class:has-errors={errorLines.length > 0}
		rows="8"
		placeholder="youtube.com&#10;instagram.com&#10;tiktok.com"
		value={text}
		oninput={handleInput}
	></textarea>
	<span class="editor-hint">Один домен или CIDR на строку. Формат: domain.tld, IP/mask или IPv6/prefix</span>
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
		font-size: 0.75rem;
		color: var(--error, #ef4444);
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
