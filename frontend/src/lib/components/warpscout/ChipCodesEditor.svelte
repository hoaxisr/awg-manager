<script lang="ts">
	import { X } from 'lucide-svelte';
	import { m } from '$lib/i18n';
	import { countryFlag } from './warpscoutUi';

	interface Props {
		values: string[];
		onchange: (next: string[]) => void;
		/** Длина кода: 3 — IATA колокейшна, 2 — ISO страны. */
		codeLength: 2 | 3;
		label?: string;
		quick?: string[];
		placeholder?: string;
		emptyLabel?: string;
		showFlags?: boolean;
		disabled?: boolean;
		ariaLabel?: string;
	}

	let {
		values,
		onchange,
		codeLength,
		label = '',
		quick = [],
		placeholder = '',
		emptyLabel = '',
		showFlags = false,
		disabled = false,
		ariaLabel,
	}: Props = $props();

	let draft = $state('');
	let invalid = $state(false);

	const pattern = $derived(codeLength === 2 ? /^[A-Z]{2}$/ : /^[A-Z0-9]{3}$/);

	function add(raw: string): void {
		const parts = raw
			.toUpperCase()
			.split(/[\s,;]+/)
			.map((s) => s.trim())
			.filter(Boolean);
		if (parts.length === 0) return;
		const bad = parts.filter((p) => !pattern.test(p));
		const next = [...values];
		for (const p of parts) {
			if (pattern.test(p) && !next.includes(p)) next.push(p);
		}
		invalid = bad.length > 0;
		draft = bad.join(' ');
		if (next.length !== values.length) onchange(next);
	}

	function remove(code: string): void {
		onchange(values.filter((v) => v !== code));
	}

	function onKey(e: KeyboardEvent): void {
		if (e.key === 'Enter' || e.key === ',' || e.key === ' ') {
			e.preventDefault();
			add(draft);
		} else if (e.key === 'Backspace' && draft === '' && values.length > 0) {
			remove(values[values.length - 1]);
		} else {
			invalid = false;
		}
	}
</script>

<div class="chip-editor" class:disabled>
	{#if label || (quick.length > 0 && !disabled)}
		<div class="head-row">
			{#if label}<span class="ws-label">{label}</span>{/if}
			{#if quick.length > 0 && !disabled}
				<div class="quick">
					{#each quick as q (q)}
						<button
							type="button"
							class="quick-btn mono"
							disabled={values.includes(q)}
							onclick={() => add(q)}
						>+{q}</button>
					{/each}
				</div>
			{/if}
		</div>
	{/if}
	<div class="chips" class:invalid role="group" aria-label={ariaLabel || label}>
		{#each values as code (code)}
			<span class="code-chip">
				{#if showFlags && countryFlag(code)}<span class="flag">{countryFlag(code)}</span>{/if}
				<span class="mono">{code}</span>
				{#if !disabled}
					<button
						type="button"
						class="chip-x"
						aria-label={m.warpscout_chip_remove({ code })}
						onclick={() => remove(code)}
					>
						<X size={11} />
					</button>
				{/if}
			</span>
		{/each}
		{#if values.length === 0 && emptyLabel}
			<span class="empty">{emptyLabel}</span>
		{/if}
		<input
			class="chip-input mono"
			type="text"
			bind:value={draft}
			{placeholder}
			{disabled}
			maxlength={64}
			onkeydown={onKey}
			onblur={() => add(draft)}
			aria-label={ariaLabel || label
				? m.warpscout_chip_add_in({ group: ariaLabel || label })
				: m.warpscout_chip_add()}
		/>
	</div>
</div>

<style>
	.chip-editor {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		min-width: 0;
	}
	.chip-editor.disabled {
		opacity: 0.6;
	}
	.head-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.25rem;
		flex-wrap: nowrap;
		min-height: 1.125rem;
	}
	.ws-label {
		font-size: 0.73rem;
		font-weight: 500;
		color: var(--color-text-secondary);
		line-height: 1.2;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.chips {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.25rem;
		padding: 0.2rem 0.4rem;
		min-height: 32px;
		box-sizing: border-box;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
	}
	.chips:focus-within {
		border-color: var(--color-accent);
	}
	.chips.invalid {
		border-color: var(--color-error);
	}
	.code-chip {
		display: inline-flex;
		align-items: center;
		gap: 0.2rem;
		padding: 0.05rem 0.3rem 0.05rem 0.4rem;
		font-size: 0.72rem;
		line-height: 1.3;
		background: var(--color-accent-tint);
		border: 1px solid var(--color-accent-border);
		border-radius: 999px;
		color: var(--color-text-primary);
	}
	.chip-x {
		display: inline-flex;
		align-items: center;
		border: 0;
		background: transparent;
		color: var(--color-text-muted);
		cursor: pointer;
		padding: 0;
	}
	.chip-x:hover {
		color: var(--color-error);
	}
	.empty {
		font-size: 0.73rem;
		color: var(--color-text-muted);
	}
	.chip-input {
		flex: 1 1 3.5rem;
		min-width: 3.5rem;
		border: 0;
		outline: none;
		background: transparent;
		color: var(--color-text-primary);
		font-size: 0.78rem;
		text-transform: uppercase;
		padding: 0.05rem 0.15rem;
		line-height: 1.25;
	}
	.chip-input:focus {
		box-shadow: none;
	}
	.quick {
		display: inline-flex;
		flex-wrap: nowrap;
		flex-shrink: 0;
		gap: 0.15rem;
	}
	.quick-btn {
		font-size: 0.65rem;
		line-height: 1.15;
		padding: 0.06rem 0.3rem;
		border: 1px dashed var(--color-border-hover);
		border-radius: 999px;
		background: transparent;
		color: var(--color-text-muted);
		cursor: pointer;
		white-space: nowrap;
	}
	.quick-btn:hover:not(:disabled) {
		color: var(--color-accent);
		border-color: var(--color-accent);
	}
	.quick-btn:disabled {
		opacity: 0.35;
		cursor: default;
	}
	.mono {
		font-family: var(--font-mono);
	}
	.flag {
		font-size: 0.8rem;
	}
</style>
