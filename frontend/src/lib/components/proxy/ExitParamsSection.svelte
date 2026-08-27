<script lang="ts">
	// EX-15..24, EX-57, EX-59..EX-65 — «Параметры» клиента. Поля правятся в
	// конфиге инстанса на месте; сохраняет и откатывает страница (владелец
	// конфига).
	import { Button, Dropdown, Input, SegmentedControl } from '$lib/components/ui';
	import { setPeer, switchConnMode } from '$lib/utils/wdttPeerMode';
	import SensitiveInput from '../proxy-panel/SensitiveInput.svelte';
	import { dnsModeOptions, modeOptions, platformOptions, transportOptions } from '../freeturn/options';
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

	// Режим подключения к серверу. Раньше он приезжал ТОЛЬКО из импортируемой
	// ссылки, и сменить его в UI было нечем — при живом бейдже режима в списке.
	const connModeOptions = [
		{ value: 'wg' as const, label: 'WG' },
		{ value: 'raw' as const, label: 'Raw' },
	];

	// -captcha-mode: auto|rjs|wv, дефолт роутера rjs (internal/wdtt/types.go:24).
	const captchaOptions = [
		{ value: 'rjs', label: 'rjs (рекомендуется)' },
		{ value: 'auto', label: 'auto' },
		{ value: 'wv', label: 'wv' },
	];
</script>

<DetailSection title="Параметры">
	{#if wdttClient}
		<div class="mode-row">
			<span class="row-label">Режим подключения</span>
			<SegmentedControl
				value={wdttClient.connMode === 'raw' ? 'raw' : 'wg'}
				options={connModeOptions}
				ariaLabel="Режим подключения"
				onchange={(v) => {
					if (wdttClient) switchConnMode(wdttClient, v);
				}}
			/>
			<span class="mode-hint">
				У режимов разные порты сервера: адрес подставится из сохранённого для
				выбранного режима. Применяется при перезапуске.
			</span>
		</div>
	{/if}
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
			<Dropdown
				label="Режим капчи"
				bind:value={wdttClient.captchaMode}
				options={captchaOptions}
				fullWidth
			/>
		{:else if ftClient}
			<Input label="Адрес сервера" bind:value={ftClient.peer} fullWidth />
			<Input label="Ссылки VK Calls" bind:value={ftClient.links} fullWidth />
			<Input
				label="Потоков"
				type="number"
				value={String(ftClient.streams)}
				onchange={(v) => (ftClient.streams = Number(v) || ftClient.streams)}
				fullWidth
			/>
			<Input
				label="Потоков на кред"
				type="number"
				value={String(ftClient.streamsPerCred)}
				onchange={(v) => (ftClient.streamsPerCred = Number(v) || ftClient.streamsPerCred)}
				fullWidth
			/>
			<Dropdown label="Режим" bind:value={ftClient.mode} options={modeOptions} fullWidth />
			<Dropdown
				label="Транспорт"
				bind:value={ftClient.transport}
				options={transportOptions}
				fullWidth
			/>
			<Dropdown
				label="Платформа"
				bind:value={ftClient.platform}
				options={platformOptions}
				fullWidth
			/>
			<Dropdown label="DNS-режим" bind:value={ftClient.dnsMode} options={dnsModeOptions} fullWidth />
			<Input label="DNS-серверы" bind:value={ftClient.dnsServers} fullWidth />
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

	.mode-row {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		flex-wrap: wrap;
		margin-bottom: 0.75rem;
	}

	.row-label {
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}

	.mode-hint {
		font-size: 0.75rem;
		color: var(--color-text-tertiary);
		flex: 1 1 240px;
	}

</style>
