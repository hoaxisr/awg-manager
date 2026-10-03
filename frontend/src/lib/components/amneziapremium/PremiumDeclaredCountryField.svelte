<script lang="ts">
	import { m } from '$lib/i18n';
	import { api } from '$lib/api/client';

	interface Props {
		/** Выбранное значение; пусто — выбора ещё не было. */
		value: string;
		disabled: boolean;
		/** Сообщает мастеру ДЕЙСТВУЮЩИЙ выбор: без него выдача не идёт. */
		onchange: (code: string) => void;
	}

	let { value, disabled, onchange }: Props = $props();

	let busy = $state(false);
	/** Сообщение бэкенда или `null` — тогда показываем локализованный текст по виду операции. */
	let error = $state<{ kind: 'read' | 'save'; message: string | null } | null>(null);

	// Подписи и значения — словарь портала: третьего варианта у него нет, а
	// «ag» означает «все остальные страны и регионы», не какую-то одну.
	const options = $derived<{ code: string; label: string }[]>([
		{ code: 'ru', label: m.amnezia_premium_country_ru() },
		{ code: 'ag', label: m.amnezia_premium_country_other() }
	]);

	// Отметка «за выбором уже сходили»: не $state намеренно — нигде не
	// рисуется, а реактивной сделала бы эффект зависимым от себя же
	// (ср. PremiumMirrorField).
	let requested = false;

	$effect(() => {
		void load();
	});

	async function load(): Promise<void> {
		if (requested) return;
		requested = true;
		busy = true;
		try {
			const saved = await api.amneziaPremiumDeclaredCountry();
			onchange(saved.declaredCountryCode);
		} catch (e) {
			requested = false;
			error = { kind: 'read', message: e instanceof Error ? e.message : null };
		} finally {
			busy = false;
		}
	}

	async function choose(code: string): Promise<void> {
		if (code === value) return;
		busy = true;
		error = null;
		try {
			const saved = await api.amneziaPremiumSaveDeclaredCountry(code);
			// Выбор объявляется ТОЛЬКО после удавшейся записи: иначе кнопка
			// выдачи разблокируется по значению, которого на роутере нет, и
			// выдача откажет уже после нажатия.
			onchange(saved.declaredCountryCode);
		} catch (e) {
			error = { kind: 'save', message: e instanceof Error ? e.message : null };
		} finally {
			busy = false;
		}
	}
</script>

<div class="premium-declared">
	<span class="premium-declared-label" id="premium-declared-label">
		{m.amnezia_premium_declared_label()}
	</span>
	<div class="premium-declared-options" role="group" aria-labelledby="premium-declared-label">
		{#each options as opt (opt.code)}
			<button
				type="button"
				class="premium-declared-option"
				class:premium-declared-option--active={value === opt.code}
				aria-pressed={value === opt.code}
				disabled={disabled || busy}
				onclick={() => void choose(opt.code)}
			>
				{opt.label}
			</button>
		{/each}
	</div>
	{#if error}
		<p class="field-hint is-error">{error.message || (error.kind === 'read' ? m.amnezia_premium_declared_read_failed() : m.amnezia_premium_declared_save_failed())}</p>
	{:else if !value}
		<!-- Портал требует эту страну в каждой выдаче и по ней собирает
		     параметры: без выбора кнопка выдачи заперта, и человек должен
		     понимать, чем именно. -->
		<p class="field-hint">{m.amnezia_premium_declared_hint()}</p>
	{/if}
</div>

<style>
	.premium-declared {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.premium-declared-label {
		font-size: 0.75rem;
		color: var(--text-secondary, var(--color-text-secondary));
	}

	.premium-declared-options {
		display: flex;
		flex-wrap: wrap;
		border: 1px solid var(--border, var(--color-border));
		border-radius: 8px;
		overflow: hidden;
		width: fit-content;
	}

	.premium-declared-option {
		padding: 6px 10px;
		font-size: 0.8125rem;
		background: transparent;
		border: none;
		color: var(--text-secondary, var(--color-text-secondary));
		cursor: pointer;
	}

	.premium-declared-option--active {
		background: var(--color-accent-tint);
		color: var(--accent, var(--color-accent));
	}

	.premium-declared-option:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}
</style>
