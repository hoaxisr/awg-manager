<script lang="ts">
	// Шаг 2 мастера «Выхода» — параметры (WE-29..WE-37). Поля правятся на месте
	// в объекте мастера; пароль есть только у WDTT-клиента, у FreeTurn его нет.
	import { Input } from '$lib/components/ui';
	import SensitiveInput from '../proxy-panel/SensitiveInput.svelte';
	import type { ExitMode, ExitProtocol, ExitWizardFields } from './exitWizard';

	interface Props {
		protocol: ExitProtocol;
		/** Режим WDTT: в Raw отдельного AWG-туннеля нет, и подсказка WE-34 врала бы.
		 * У FreeTurn режима нет — туннель всегда WG. */
		mode: ExitMode;
		/** Поля мастера правятся здесь же: владелец значения — мастер. */
		fields: ExitWizardFields;
	}

	let { protocol, mode, fields = $bindable() }: Props = $props();
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
		hint={protocol === 'wdtt' && mode === 'raw' ? '' : 'Сюда будет смотреть AWG-туннель'}
		fullWidth
	/>
	<!-- WE-50: поле обязательное, и без подписи «Дальше» гасла бы молча. -->
	<Input
		label="VK-хеши"
		bind:value={fields.vkHashes}
		hint={protocol === 'wdtt' ? 'Обязательно — без VK-хешей клиент не запустится' : ''}
		fullWidth
	/>
	<!-- WE-37 — про округление в wdtt-клиенте; у freeturn правила кратности нет. -->
	<Input
		label="Потоков"
		type="number"
		value={fields.workers}
		oninput={(v) => (fields.workers = v)}
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
