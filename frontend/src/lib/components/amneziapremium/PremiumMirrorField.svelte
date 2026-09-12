<script lang="ts">
	import { api } from '$lib/api/client';
	import Button from '$lib/components/ui/Button.svelte';

	interface Props {
		/** Экран ошибки: правка адреса — единственный способ починиться, прятать её там нельзя. */
		forceOpen: boolean;
	}

	let { forceOpen }: Props = $props();

	let expanded = $state(false);
	let value = $state('');
	let busy = $state(false);
	let error = $state('');
	let saved = $state(false);

	const shown = $derived(forceOpen || expanded);

	// Отметка «за адресом уже сходили». Намеренно НЕ $state: она нигде не
	// рисуется, а реактивной сделала бы эффект зависимым от себя же.
	let requested = false;

	// Адрес запрашивается только при раскрытии: на роутере это лишний запрос
	// на каждое открытие мастера, а поле — настройка «на чёрный день».
	$effect(() => {
		if (shown) void load();
	});

	async function load(): Promise<void> {
		if (requested) return;
		requested = true;
		busy = true;
		try {
			const mirror = await api.amneziaPremiumMirror();
			// Бэкенд отдаёт ДЕЙСТВУЮЩИЙ адрес: пустое хранимое значение он уже
			// подставил сам. Своего умолчания у фронта нет и быть не должно —
			// иначе адрес перестал бы ротироваться с релизом.
			value = mirror.mirrorUrl;
		} catch (e) {
			requested = false;
			error = e instanceof Error ? e.message : 'Не удалось получить адрес зеркала';
		} finally {
			busy = false;
		}
	}

	async function save(next: string): Promise<void> {
		busy = true;
		error = '';
		saved = false;
		try {
			const mirror = await api.amneziaPremiumSaveMirror(next);
			value = mirror.mirrorUrl;
			saved = true;
		} catch (e) {
			// Текст отказа пишет бэкенд (в том числе про не-https адрес) —
			// проглотить его значит оставить человека без причины отказа.
			error = e instanceof Error ? e.message : 'Не удалось сохранить адрес зеркала';
		} finally {
			busy = false;
		}
	}
</script>

<div class="premium-mirror">
	{#if !forceOpen}
		<button
			type="button"
			class="premium-mirror-toggle"
			aria-expanded={expanded}
			onclick={() => (expanded = !expanded)}
		>
			Адрес зеркала Amnezia
		</button>
	{/if}

	{#if shown}
		<div class="premium-mirror-body">
			<label class="premium-mirror-label" for="premium-mirror-url">Адрес зеркала Amnezia</label>
			<input
				id="premium-mirror-url"
				class="field-input"
				type="url"
				spellcheck="false"
				disabled={busy}
				value={value}
				oninput={(e) => {
					value = e.currentTarget.value;
					saved = false;
				}}
			/>
			<div class="premium-mirror-actions">
				<Button variant="secondary" size="sm" disabled={busy} onclick={() => void save(value)}>
					Сохранить
				</Button>
				<Button variant="ghost" size="sm" disabled={busy} onclick={() => void save('')}>
					Вернуть по умолчанию
				</Button>
			</div>
			{#if error}
				<p class="field-hint is-error">{error}</p>
			{:else if saved}
				<p class="field-hint">Адрес сохранён.</p>
			{/if}
		</div>
	{/if}
</div>

<style>
	.premium-mirror {
		margin-top: 10px;
	}

	.premium-mirror-toggle {
		padding: 0;
		font-size: 0.75rem;
		background: none;
		border: none;
		color: var(--text-muted, var(--color-text-muted));
		cursor: pointer;
	}

	.premium-mirror-toggle:hover {
		text-decoration: underline;
	}

	.premium-mirror-body {
		display: flex;
		flex-direction: column;
		gap: 6px;
		margin-top: 6px;
	}

	.premium-mirror-label {
		font-size: 0.75rem;
		color: var(--text-secondary, var(--color-text-secondary));
	}

	.premium-mirror-actions {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
	}
</style>
