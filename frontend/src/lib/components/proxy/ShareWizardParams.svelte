<script lang="ts">
	// Шаг 2 мастера «Настроить раздачу» — параметры сервера (ia.md §3.4).
	// WDTT: порт и firewall. FreeTurn: WG-сервер роутера и пир,
	// listen-порт, обфускация, firewall.
	import { Button, Dropdown, FieldHint, Input, Toggle } from '$lib/components/ui';
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

{#if protocol === 'openflux'}
	<div class="grid">
		<Dropdown
			label="Транспорт"
			value={fields.transport}
			options={[
				{ value: 'yandex', label: 'Yandex.Docs' },
				{ value: 'vyandex', label: 'Yandex Volga' },
				{ value: 'cupsonline', label: 'Cups.online' },
				{ value: 'mailru', label: 'Mail.ru Docs' },
			]}
			onchange={(v) => (fields.transport = v)}
			hint="Транспорт MAX (OneMe) настраивается в параметрах инстанса — ему нужен токен"
			fullWidth
		/>
		<Input
			label="Адрес канала (URL)"
			bind:value={fields.url}
			hint="Публичный документ-релей; не нужен для Cups.online"
			fullWidth
		/>
		<Dropdown
			label="Режим выхода"
			value={fields.mode}
			options={[
				{ value: 'l4', label: 'l4 — gVisor, без root' },
				{ value: 'l3', label: 'l3 — SNAT/DNAT (быстрее)' },
			]}
			onchange={(v) => (fields.mode = v)}
			fullWidth
		/>
		{#if fields.mode === 'l3'}
			<Input
				label="Egress-адрес (localIp)"
				bind:value={fields.localIp}
				hint="Обязателен для l3: по нему строится RST-drop и SNAT"
				fullWidth
			/>
		{/if}
		<Dropdown
			label="Кодек"
			value={fields.codec}
			options={[
				{ value: 'batched', label: 'Batched (zstd)' },
				{ value: 'legacy', label: 'Legacy (LZ4)' },
			]}
			onchange={(v) => (fields.codec = v)}
			fullWidth
		/>
		<div class="field-with-btn">
			<Input label="Ключ шифрования" type="password" bind:value={fields.encryptionKey} fullWidth />
			<Button variant="secondary" size="sm" onclick={() => (fields.encryptionKey = randomHex(32))}>
				Сгенерировать
			</Button>
		</div>
		<Input
			label="DNS-серверы"
			bind:value={fields.dns}
			hint="Через запятую. Локальный 127.0.0.1 на Keenetic часто молчит — укажите 1.1.1.1,8.8.8.8, иначе транспорт упадёт на первом же адресе"
			fullWidth
		/>
	</div>
	<div class="toggle-row">
		<Toggle
			label="Через sing-box"
			checked={fields.singboxRoute}
			onchange={(v) => (fields.singboxRoute = v)}
		/>
		<FieldHint
			text="TCP-трафик абонентов ноды пойдёт по правилам sing-box (WARP и другие исходящие). Работает в режиме l4 при запущенном sing-box с перехватом; UDP и канал до релея идут напрямую. Без sing-box нода не запустится."
			ariaLabel="Подсказка: через sing-box"
		/>
	</div>
	<p class="of-note">
		Слушающих портов у выходной ноды нет: клиент и нода соединяются через релей, открывать
		firewall не нужно.
	</p>
{:else if protocol === 'wdtt'}
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
	   как относящаяся к соседнему. Сам класс .field-with-btn — общий, живёт в
	   app.css. */

	.toggle-row {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
		margin-top: 0.875rem;
	}

	.of-note {
		margin: 0.75rem 0 0;
		font-size: 0.75rem;
		color: var(--color-text-muted);
		line-height: 1.6;
	}
</style>
