<script lang="ts">
	// Шаг 2 мастера «Выхода» — параметры (WE-29..WE-37). Поля правятся на месте
	// в объекте мастера; пароль есть только у WDTT-клиента, у FreeTurn его нет.
	import { m } from '$lib/i18n';
	import { Dropdown, Input, Toggle } from '$lib/components/ui';
	import SensitiveInput from '../proxy-panel/SensitiveInput.svelte';
	import { autoReconnectIntervalOptions } from '../freeturn/options';
	import type { ExitProtocol, ExitWizardFields } from './exitWizard';

	interface Props {
		protocol: ExitProtocol;
		/** Поля мастера правятся здесь же: владелец значения — мастер. */
		fields: ExitWizardFields;
	}

	let { protocol, fields = $bindable() }: Props = $props();
</script>

<p class="lead">{m.proxy_exit_params_lead()}</p>

<div class="grid">
	<Input label={m.proxy_common_name()} bind:value={fields.name} fullWidth />
	<Input label={m.proxy_exit_params_server_address()} bind:value={fields.peer} fullWidth />
	{#if protocol === 'wdtt'}
		<SensitiveInput label={m.proxy_common_password()} bind:value={fields.password} />
	{/if}
	<!-- WE-50/WE-51: поле обязательное у обоих протоколов (`exitStep2Ready`), и
	     без подписи «Дальше» гасла бы молча. Значение у них разное: у WDTT это
	     VK-хеши, у FreeTurn — ссылки VK Calls (`links`), отсюда две строки и две
	     подписи: WE-35 у WDTT и EX-59 у FreeTurn (та же, что на детали). -->
	<Input
		label={protocol === 'wdtt' ? m.proxy_exit_params_vk_hashes() : m.proxy_exit_params_vk_links()}
		bind:value={fields.vkHashes}
		hint={protocol === 'wdtt'
			? m.proxy_exit_params_vk_hashes_required()
			: m.proxy_exit_params_vk_links_required()}
		fullWidth
	/>
	<!-- WE-37 — про округление в wdtt-клиенте; у freeturn правила кратности нет. -->
	<Input
		label={m.proxy_exit_params_workers()}
		type="number"
		value={fields.workers}
		oninput={(v) => (fields.workers = v)}
		hint={protocol === 'wdtt' ? m.proxy_exit_params_workers_hint() : ''}
		fullWidth
	/>
	<div class="reconnect-box">
		<Toggle
			label={m.proxy_exit_params_auto_reconnect()}
			hint={m.proxy_exit_params_auto_reconnect_hint()}
			checked={fields.autoReconnect ?? false}
			onchange={(v) => {
				fields.autoReconnect = v;
				if (v && !fields.autoReconnectInterval) {
					fields.autoReconnectInterval = 'on_failure';
				}
			}}
		/>
		{#if fields.autoReconnect}
			<div class="reconnect-interval">
				<Dropdown
					label={m.proxy_exit_params_auto_reconnect_interval()}
					bind:value={fields.autoReconnectInterval}
					options={autoReconnectIntervalOptions()}
					fullWidth
				/>
			</div>
		{/if}
	</div>
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

	.reconnect-box {
		grid-column: 1 / -1;
		padding-top: 0.5rem;
	}

	.reconnect-interval {
		max-width: 240px;
		margin-top: 0.5rem;
	}
</style>
