<script lang="ts">
	// Шаг 2 мастера «Выхода» — параметры (WE-29..WE-37). Поля правятся на месте
	// в объекте мастера; пароль есть только у WDTT-клиента, у FreeTurn его нет.
	import { Input } from '$lib/components/ui';
	import SensitiveInput from '../proxy-panel/SensitiveInput.svelte';
	import type { ExitProtocol, ExitWizardFields } from './exitWizard';

	interface Props {
		protocol: ExitProtocol;
		/** Поля мастера правятся здесь же: владелец значения — мастер. */
		fields: ExitWizardFields;
	}

	let { protocol, fields = $bindable() }: Props = $props();
</script>

<p class="lead">Значения из ссылки — поправьте, если нужно.</p>

<div class="grid">
	<Input label="Имя" bind:value={fields.name} fullWidth />
	<Input label="Адрес сервера" bind:value={fields.peer} fullWidth />
	{#if protocol === 'wdtt'}
		<SensitiveInput label="Пароль" bind:value={fields.password} />
	{/if}
	<Input
		label="Локальный порт"
		bind:value={fields.listen}
		hint="Сюда будет смотреть AWG-туннель"
		fullWidth
	/>
	<Input label="VK-хеши" bind:value={fields.vkHashes} fullWidth />
	<!-- WE-37 — про округление в wdtt-клиенте; у freeturn правила кратности нет. -->
	<Input
		label="Потоков"
		type="number"
		bind:value={fields.workers}
		hint={protocol === 'wdtt' ? 'Клиент округлит вниз до кратного 9 (минимум 9)' : ''}
		fullWidth
	/>
</div>

<style>
	.lead {
		margin: 0 0 0.875rem;
		font-size: 0.875rem;
		color: var(--color-text-secondary);
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
		gap: 0.75rem;
	}
</style>
