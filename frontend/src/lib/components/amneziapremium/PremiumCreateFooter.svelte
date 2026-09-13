<script lang="ts">
	import type { PremiumWizardBackend } from './AmneziaPremiumWizard.svelte';

	interface Props {
		name: string;
		backend: PremiumWizardBackend;
		/** Недоступный бэкенд выбрать нельзя: см. chosenBackend в мастере (F277). */
		nativewgAvailable: boolean;
		kernelAvailable: boolean;
		disabled: boolean;
		onname: (name: string) => void;
		onbackend: (backend: PremiumWizardBackend) => void;
	}

	let { name, backend, nativewgAvailable, kernelAvailable, disabled, onname, onbackend }: Props =
		$props();

	const unavailableTitle = 'Недоступен на этом роутере';
</script>

<input
	class="field-input premium-name-input"
	type="text"
	placeholder="имя туннеля"
	aria-label="Имя туннеля"
	value={name}
	{disabled}
	oninput={(e) => onname(e.currentTarget.value)}
/>
<div class="premium-backend" role="group" aria-label="Бэкенд туннеля">
	<button
		type="button"
		class="premium-backend-option"
		class:premium-backend-option--active={backend === 'nativewg'}
		aria-pressed={backend === 'nativewg'}
		disabled={disabled || !nativewgAvailable}
		title={nativewgAvailable ? '' : unavailableTitle}
		onclick={() => onbackend('nativewg')}
	>
		NativeWG
	</button>
	<button
		type="button"
		class="premium-backend-option"
		class:premium-backend-option--active={backend === 'kernel'}
		aria-pressed={backend === 'kernel'}
		disabled={disabled || !kernelAvailable}
		title={kernelAvailable ? '' : unavailableTitle}
		onclick={() => onbackend('kernel')}
	>
		Kernel
	</button>
</div>

<style>
	.premium-name-input {
		flex: 1 1 140px;
		min-width: 0;
	}

	.premium-backend {
		display: flex;
		flex-shrink: 0;
		border: 1px solid var(--border, var(--color-border));
		border-radius: 8px;
		overflow: hidden;
	}

	.premium-backend-option {
		padding: 6px 10px;
		font-size: 0.8125rem;
		background: transparent;
		border: none;
		color: var(--text-secondary, var(--color-text-secondary));
		cursor: pointer;
	}

	.premium-backend-option--active {
		background: var(--color-accent-tint);
		color: var(--accent, var(--color-accent));
	}

	.premium-backend-option:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}
</style>
