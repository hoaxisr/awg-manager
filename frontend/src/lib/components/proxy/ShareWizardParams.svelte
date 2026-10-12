<script lang="ts">
	// Шаг 2 мастера «Настроить раздачу» — параметры сервера (ia.md §3.4).
	// WDTT: порт и firewall. FreeTurn: WG-сервер роутера и пир,
	// listen-порт, обфускация, firewall.
	import { m } from '$lib/i18n';
	import { Button, Dropdown, Input, Toggle } from '$lib/components/ui';
	import { obfOptions } from '../freeturn/options';
	import ShareWizardPeer from './ShareWizardPeer.svelte';
	import { randomHex, rawPortHint, type ShareWizardFields } from './shareWizard';
	import type { ProxyProtocol } from './rows';
	import type { FreeTurnServerConfig } from '$lib/types';

	interface Props {
		protocol: ProxyProtocol;
		fields: ShareWizardFields;
		/** Дефолт порта Endpoint: listen FreeTurn-клиента этого роутера (F-18). */
		endpointPort: number;
		onpeerconf: (conf: string, confError: string, portUnknown: boolean) => void;
	}

	let { protocol, fields = $bindable(), endpointPort, onpeerconf }: Props = $props();
</script>

{#if protocol === 'wdtt'}
	<div class="grid">
		<!-- Порт живёт строкой: `bind:value` у `type="number"` приводит значение к
		     числу, а подсказка WS-19 и проверка готовности работают со строкой. -->
		<Input
			label={m.proxy_common_port()}
			type="number"
			hint={rawPortHint(fields.port)}
			value={fields.port}
			oninput={(v) => (fields.port = v)}
			fullWidth
		/>
	</div>
	<div class="toggle-row">
		<Toggle
			label={m.proxy_share_params_firewall_ports()}
			checked={fields.firewall}
			onchange={(v) => (fields.firewall = v)}
		/>
	</div>
{:else}
	<ShareWizardPeer
		{endpointPort}
		onconnect={(addr) => (fields.connect = addr)}
		{onpeerconf}
	/>

	<div class="grid">
		<Input
			label={m.proxy_share_params_listen_port()}
			type="number"
			value={fields.port}
			oninput={(v) => (fields.port = v)}
			fullWidth
		/>
		<Dropdown
			label={m.proxy_share_params_obf_profile()}
			value={fields.obfProfile}
			options={obfOptions}
			onchange={(v) => (fields.obfProfile = v as FreeTurnServerConfig['obfProfile'])}
			fullWidth
		/>
		<div class="field-with-btn">
			<Input label={m.proxy_share_params_obf_key()} type="password" bind:value={fields.obfKey} fullWidth />
			<Button variant="secondary" size="sm" onclick={() => (fields.obfKey = randomHex(32))}>
				{m.common_generate()}
			</Button>
		</div>
	</div>
	<div class="toggle-row">
		<Toggle
			label={m.proxy_share_params_firewall_port()}
			checked={fields.firewall}
			onchange={(v) => (fields.firewall = v)}
		/>
	</div>
{/if}

<style>
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
		gap: 0.75rem;
		margin-top: 0.75rem;
	}

	/* Кнопка «Сгенерировать» стоит у своего поля: под общей сеткой она читалась
	   как относящаяся к соседнему. Сам класс .field-with-btn — общий, живёт в
	   app.css. */

	.toggle-row {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
		margin-top: 0.875rem;
	}
</style>
