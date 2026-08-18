<script lang="ts">
	// EX-15..24 — «Параметры» клиента. Поля правятся в конфиге инстанса на
	// месте; сохраняет и откатывает страница (владелец конфига).
	import { Button, Input } from '$lib/components/ui';
	import { setPeer } from '$lib/utils/wdttPeerMode';
	import SensitiveInput from '../proxy-panel/SensitiveInput.svelte';
	import type { FreeTurnClientConfig, WdttClientConfig } from '$lib/types';
	import DetailSection from './DetailSection.svelte';

	interface Props {
		/** Редактируемая копия конфига детали — правится на месте. */
		wdttClient?: WdttClientConfig;
		ftClient?: FreeTurnClientConfig;
		/** Режим Raw: отдельного AWG-туннеля нет, и подсказка EX-20 врала бы. */
		raw?: boolean;
		saving?: boolean;
		onsave: () => void;
		onrevert: () => void;
	}

	let {
		wdttClient = $bindable(),
		ftClient = $bindable(),
		raw = false,
		saving = false,
		onsave,
		onrevert,
	}: Props = $props();
</script>

<DetailSection title="Параметры">
	<div class="grid">
		{#if wdttClient}
			<Input
				label="Адрес сервера"
				value={wdttClient.peer}
				oninput={(v) => setPeer(wdttClient, v)}
				hint="Смена удалит AWG-туннели этого клиента — перезапустите его"
				fullWidth
			/>
			<SensitiveInput
				label="Пароль"
				bind:value={wdttClient.password}
				hint="Применяется при перезапуске"
			/>
			<Input
				label="Локальный порт"
				bind:value={wdttClient.listen}
				hint={raw ? '' : 'Сюда смотрит AWG-туннель'}
				fullWidth
			/>
			<Input
				label="VK-хеши"
				bind:value={wdttClient.vkHashes}
				hint="Применяется при перезапуске"
				fullWidth
			/>
			<Input
				label="Потоков"
				type="number"
				value={String(wdttClient.workers)}
				onchange={(v) => (wdttClient.workers = Number(v) || wdttClient.workers)}
				fullWidth
			/>
		{:else if ftClient}
			<Input label="Адрес сервера" bind:value={ftClient.peer} fullWidth />
			<Input
				label="Локальный порт"
				bind:value={ftClient.listen}
				hint="Сюда смотрит AWG-туннель"
				fullWidth
			/>
			<Input
				label="Потоков"
				type="number"
				value={String(ftClient.streams)}
				onchange={(v) => (ftClient.streams = Number(v) || ftClient.streams)}
				fullWidth
			/>
		{/if}
	</div>
	<div class="btn-row">
		<Button variant="primary" loading={saving} onclick={onsave}>Сохранить</Button>
		<Button variant="ghost" onclick={onrevert}>Отменить</Button>
	</div>
</DetailSection>

<style>
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
		gap: 0.75rem;
	}

</style>
