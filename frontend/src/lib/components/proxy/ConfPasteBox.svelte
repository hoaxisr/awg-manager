<script lang="ts">
	// Ручная вставка клиентского .conf — общий блок детали (EX-45) и мастера
	// (WE-49): кнопка раскрытия и поле. Что делать с текстом, решает владелец:
	// деталь заводит туннель сразу, мастер несёт конфиг до «Сохранить и
	// запустить».
	import type { Snippet } from 'svelte';
	import { Button } from '$lib/components/ui';

	interface Props {
		/** Подпись кнопки: EX-45 в детали, WE-49 в мастере. */
		label: string;
		/** Вставленный текст конфига. */
		value?: string;
		/** Действия под полем — своя кнопка владельца, если она нужна. */
		children?: Snippet;
	}

	let { label, value = $bindable(''), children }: Props = $props();

	let open = $state(false);
</script>

<div class="btn-row">
	<Button variant="ghost" onclick={() => (open = !open)}>{label}</Button>
</div>
{#if open}
	<textarea class="manual-conf" bind:value rows="8" aria-label="WireGuard-конфиг"></textarea>
	{#if children}{@render children()}{/if}
{/if}

<style>
	.manual-conf {
		width: 100%;
		margin-top: 0.5rem;
		font-family: var(--font-mono);
		font-size: 0.75rem;
		padding: 0.5rem 0.625rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
		resize: vertical;
	}
</style>
