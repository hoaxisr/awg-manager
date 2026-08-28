<script lang="ts">
	// EX-15..24, EX-57, EX-59..EX-65 — «Параметры» клиента. Поля правятся в
	// конфиге инстанса на месте; сохраняет и откатывает страница (владелец
	// конфига).
	import { Button, Dropdown, FormRow, Input, SegmentedControl } from '$lib/components/ui';
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
		/** Непусто — сохранять нечего: конфиг заведомо не заработает. */
		saveBlockedHint?: string;
		onsave: () => void;
		onrevert: () => void;
	}

	let {
		wdttClient = $bindable(),
		ftClient = $bindable(),
		raw = false,
		saving = false,
		saveBlockedHint = '',
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
		<!-- Одна сетка «метка — контрол» на всю секцию (решение по вёрстке
		     2026-08-27): метки в колонке, ширина поля по содержимому. -->
		<div class="form">
			<FormRow
				label="Режим подключения"
				hint="У режимов разные порты сервера: адрес подставится из сохранённого для выбранного режима. Применяется при перезапуске"
			>
				<SegmentedControl
					value={wdttClient.connMode === 'raw' ? 'raw' : 'wg'}
					options={connModeOptions}
					ariaLabel="Режим подключения"
					onchange={(v) => {
						if (wdttClient) switchConnMode(wdttClient, v);
					}}
				/>
			</FormRow>

			<FormRow
				label="Адрес сервера"
				for="exit-peer"
				hint="Смена удалит AWG-туннели этого клиента — перезапустите его"
			>
				<Input
					id="exit-peer"
					value={wdttClient.peer}
					oninput={(v) => setPeer(wdttClient, v)}
					fullWidth
				/>
			</FormRow>

			<FormRow label="Пароль" hint="Применяется при перезапуске">
				<SensitiveInput bind:value={wdttClient.password} />
			</FormRow>

			<FormRow
				label="Локальный порт"
				for="exit-listen"
				hint={raw ? '' : 'Сюда смотрит AWG-туннель'}
			>
				<div class="w-listen">
					<Input id="exit-listen" bind:value={wdttClient.listen} fullWidth />
				</div>
			</FormRow>

			<FormRow label="VK-хеши" for="exit-vk" hint="Применяется при перезапуске">
				<Input id="exit-vk" bind:value={wdttClient.vkHashes} fullWidth />
			</FormRow>

			<FormRow label="Потоков" for="exit-workers">
				<div class="w-num">
					<Input
						id="exit-workers"
						type="number"
						value={String(wdttClient.workers)}
						onchange={(v) => (wdttClient.workers = Number(v) || wdttClient.workers)}
						fullWidth
					/>
				</div>
			</FormRow>

			<FormRow label="Режим капчи">
				<div class="w-select">
					<Dropdown bind:value={wdttClient.captchaMode} options={captchaOptions} fullWidth />
				</div>
			</FormRow>
		</div>
	{:else if ftClient}
		<div class="grid">
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
		</div>
	{/if}
	<div class="btn-row">
		<Button variant="primary" loading={saving} disabled={!!saveBlockedHint} onclick={onsave}>
			Сохранить
		</Button>
		<Button variant="ghost" onclick={onrevert}>Отменить</Button>
		{#if saveBlockedHint}
			<span class="save-blocked">{saveBlockedHint}</span>
		{/if}
	</div>
</DetailSection>

<style>
	.save-blocked {
		font-size: 12px;
		color: var(--color-warning-text, var(--color-text-secondary));
		align-self: center;
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
		gap: 0.75rem;
	}

	/* Сетка формы WDTT-клиента: у FreeTurn полей вдвое больше и они
	   однотипные — там сетка карточек читается лучше строчной. */
	.form {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
		--form-label-w: 150px;
	}

	.form :global(.form-row-control > *) {
		max-width: 420px;
	}

	.form :global(.form-row-control > [role='group']) {
		width: fit-content;
	}

	.w-listen {
		width: 200px;
	}

	.w-num {
		width: 96px;
	}

	.w-select {
		width: 220px;
	}




</style>
