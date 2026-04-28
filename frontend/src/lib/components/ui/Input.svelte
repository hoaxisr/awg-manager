<script lang="ts" module>
  import type { Snippet } from 'svelte';
  import type { HTMLInputAttributes } from 'svelte/elements';
  export type InputType = 'text' | 'email' | 'password' | 'number' | 'search' | 'url' | 'tel';
  export type InputAutocomplete = HTMLInputAttributes['autocomplete'];
</script>

<script lang="ts">
  interface Props {
    type?: InputType;
    value?: string;
    placeholder?: string;
    label?: string;
    hint?: string;
    error?: string;
    disabled?: boolean;
    required?: boolean;
    readonly?: boolean;
    autocomplete?: InputAutocomplete;
    name?: string;
    id?: string;
    prefix?: Snippet;
    suffix?: Snippet;
    fullWidth?: boolean;
    onchange?: (v: string) => void;
    oninput?: (v: string) => void;
  }

  let {
    type = 'text',
    value = $bindable(''),
    placeholder,
    label,
    hint,
    error,
    disabled = false,
    required = false,
    readonly = false,
    autocomplete,
    name,
    id,
    prefix,
    suffix,
    fullWidth = false,
    onchange,
    oninput,
  }: Props = $props();

  const fallbackId = `input-${Math.random().toString(36).slice(2, 8)}`;
  const fieldId = $derived(id ?? fallbackId);

  function handleInput(e: Event) {
    // bind:value already syncs into `value`; just notify the consumer callback.
    oninput?.((e.currentTarget as HTMLInputElement).value);
  }

  function handleChange(e: Event) {
    const v = (e.currentTarget as HTMLInputElement).value;
    onchange?.(v);
  }
</script>

<div class="field" class:full-width={fullWidth} class:has-error={!!error}>
  {#if label}
    <label for={fieldId} class="field-label">{label}{#if required}<span class="required">*</span>{/if}</label>
  {/if}
  <div class="control">
    {#if prefix}<span class="affix prefix">{@render prefix()}</span>{/if}
    <input
      id={fieldId}
      {type}
      {placeholder}
      {disabled}
      {required}
      {readonly}
      {autocomplete}
      {name}
      bind:value
      oninput={handleInput}
      onchange={handleChange}
      class="input"
      class:has-prefix={!!prefix}
      class:has-suffix={!!suffix}
    />
    {#if suffix}<span class="affix suffix">{@render suffix()}</span>{/if}
  </div>
  {#if error}
    <span class="hint hint-error">{error}</span>
  {:else if hint}
    <span class="hint">{hint}</span>
  {/if}
</div>

<style>
  .field {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }
  .field.full-width { width: 100%; }

  .field-label {
    font-size: 13px;
    color: var(--color-text-secondary);
    font-weight: 500;
  }

  .required {
    color: var(--color-error);
    margin-left: 0.25rem;
  }

  .control {
    position: relative;
    display: flex;
    align-items: stretch;
  }

  .input {
    width: 100%;
    background: var(--color-bg-primary);
    border: 1px solid var(--color-border);
    border-radius: var(--radius-sm);
    color: var(--color-text-primary);
    font: inherit;
    font-size: 13px;
    padding: 0.4375rem 0.625rem;
    line-height: 1.4;
    transition: border-color var(--t-fast) ease;
  }

  .input::placeholder {
    color: var(--color-text-muted);
  }

  .input:focus {
    outline: none;
    border-color: var(--color-accent);
  }

  .input:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .has-error .input {
    border-color: var(--color-error);
  }

  .input.has-prefix { padding-left: 2rem; border-top-left-radius: 0; border-bottom-left-radius: 0; }
  .input.has-suffix { padding-right: 2rem; border-top-right-radius: 0; border-bottom-right-radius: 0; }

  .affix {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    padding: 0 0.5rem;
    background: var(--color-bg-tertiary);
    border: 1px solid var(--color-border);
    color: var(--color-text-muted);
    font-size: 13px;
  }

  .prefix {
    border-right: none;
    border-radius: var(--radius-sm) 0 0 var(--radius-sm);
  }

  .suffix {
    border-left: none;
    border-radius: 0 var(--radius-sm) var(--radius-sm) 0;
  }

  .hint {
    font-size: 12px;
    color: var(--color-text-muted);
  }
  .hint-error {
    color: var(--color-error);
  }
</style>
