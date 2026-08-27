<script lang="ts">
	// Шаг 2 мастера «Настроить раздачу» — параметры сервера (ia.md §3.4).
	// WDTT: порт и firewall. FreeTurn: WG-сервер роутера и пир,
	// listen-порт, обфускация, firewall.
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
			label="Порт"
			type="number"
			hint={rawPortHint(fields.port)}
			value={fields.port}
			oninput={(v) => (fields.port = v)}
			fullWidth
		/>
	</div>
	<div class="toggle-row">
		<Toggle
			label="Открыть порты сервера в firewall"
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
			label="Listen-порт"
			type="number"
			value={fields.port}
			oninput={(v) => (fields.port = v)}
			fullWidth
		/>
		<Dropdown
			label="Профиль обфускации"
			value={fields.obfProfile}
			options={obfOptions}
			onchange={(v) => (fields.obfProfile = v as FreeTurnServerConfig['obfProfile'])}
			fullWidth
		/>
		<div class="field-with-btn">
			<Input label="Ключ обфускации" type="password" bind:value={fields.obfKey} fullWidth />
			<Button variant="secondary" size="sm" onclick={() => (fields.obfKey = randomHex(32))}>
				Сгенерировать
			</Button>
		</div>
	</div>
	<div class="toggle-row">
		<Toggle
			label="Открыть порт в firewall"
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
	   как относящаяся к соседнему. Выравнивание — по НИЖНЕЙ кромке поля, а не
	   отступом под метку: отступ пришлось бы подгонять под её высоту, а на узкой
	   колонке метка переносится на две строки. */
	.field-with-btn {
		display: flex;
		align-items: flex-end;
		gap: 0.375rem;
		min-width: 0;
	}

	.toggle-row {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
		margin-top: 0.875rem;
	}
</style>
