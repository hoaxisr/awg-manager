<script lang="ts">
	// SH-47..SH-61 — «Сеть» раздачи. NAT, LAN и политика применяются своими
	// ручками сразу (их и правит бэкенд), остальные поля живут в редактируемой
	// копии конфига и уезжают кнопкой «Сохранить».
	import { Badge, Button, ChipMultiSelect, Dropdown, FieldHint, Input, SegmentedControl, Toggle } from '$lib/components/ui';
	import { ServerAccessPolicyDropdown } from '$lib/components/servers';
	import ServerWgBind from '../freeturn/ServerWgBind.svelte';
	import { obfOptions } from '../freeturn/options';
	import { setListenPort } from '$lib/utils/listenPortUtils';
	import type { NatMode } from '$lib/utils/network';
	import type { FreeTurnServerConfig, WdttServerConfig } from '$lib/types';
	import DetailSection from './DetailSection.svelte';
	import { natModeOptions } from './shareConfig';

	interface Props {
		/** Редактируемая копия конфига детали — правится на месте. */
		wdttServer?: WdttServerConfig;
		ftServer?: FreeTurnServerConfig;
		/** Сегменты LAN роутера для мультивыбора. */
		lanOptions: { value: string; label: string }[];
		/**
		 * Значение `exposeToPolicies`, с которым сервер запущен (из статуса).
		 * `undefined` — применённого значения не знает и бэкенд: сервер не
		 * запускался, остановлен либо процесс усыновлён.
		 */
		exposeApplied?: boolean;
		saving?: boolean;
		/** Общий замок мутаций сервера: одна операция за раз. */
		busy?: boolean;
		onnat: (mode: NatMode) => void;
		onlan: (segments: string[]) => void;
		onpolicy: (policy: string) => void;
		onsave: () => void;
		onrevert: () => void;
		/** .conf выбранного пира — уезжает в ссылку абоненту FreeTurn (FS-18). */
		onpeerconf?: (conf: string) => void;
	}

	let {
		wdttServer = $bindable(),
		ftServer = $bindable(),
		lanOptions,
		exposeApplied,
		saving = false,
		busy = false,
		onnat,
		onlan,
		onpolicy,
		onsave,
		onrevert,
		onpeerconf,
	}: Props = $props();

	const natMode = $derived((wdttServer?.natMode ?? 'full') as NatMode);
	const wgPort = $derived(String(wdttServer?.wgPort || 56001));
	const ftPort = $derived(String(ftServer?.listen?.split(':').pop() ?? ''));
	// SH-56 держится, пока выбранное не совпало с применённым. Применённое
	// неизвестно — расхождения нет о чём заявлять, бейдж не показываем.
	const exposePending = $derived(
		exposeApplied !== undefined && (wdttServer?.exposeToPolicies ?? false) !== exposeApplied,
	);

	function applyWgPort(value: string) {
		if (!wdttServer) return;
		wdttServer.wgPort = Math.max(1, Math.min(65535, Number(value) || 56001));
	}

	function applyFtPort(value: string) {
		if (!ftServer) return;
		const port = Math.max(1, Math.min(65535, Number(value) || 56000));
		ftServer.listen = setListenPort(ftServer.listen || '0.0.0.0:56000', port, '0.0.0.0');
	}
</script>

<DetailSection title="Сеть">
	{#if wdttServer}
		<div class="row">
			<span class="row-label">Режим NAT</span>
			<SegmentedControl
				value={natMode}
				options={natModeOptions}
				ariaLabel="Режим NAT"
				disabled={busy}
				onchange={onnat}
			/>
		</div>

		<div class="row">
			<span class="row-label">Доступ в LAN</span>
			<div class="row-control">
				<ChipMultiSelect
					values={wdttServer.lanSegments ?? []}
					options={lanOptions}
					disabled={busy}
					onchange={onlan}
				/>
			</div>
		</div>

		<!-- Политика доступа (SH-50) — общий с «Серверами» контрол: своя подпись
		     и список политик роутера внутри. -->
		<ServerAccessPolicyDropdown
			policy={wdttServer.policy ?? 'none'}
			disabled={busy}
			onchange={onpolicy}
		/>

		<div class="grid">
			<Input
				label="Внутренний WG-порт"
				type="number"
				value={wgPort}
				hint="Смена перезапустит сервер; занятый порт менеджер подберёт сам"
				onchange={applyWgPort}
				fullWidth
			/>
		</div>

		<div class="toggle-row">
			<Toggle
				label="Использовать в политиках доступа"
				checked={wdttServer.exposeToPolicies ?? false}
				disabled={busy}
				onchange={(v) => {
					if (wdttServer) wdttServer.exposeToPolicies = v;
				}}
			/>
			<FieldHint
				text="Выключено — интерфейсы сервера остаются внутренними: роутер не предлагает их в политиках доступа. Включено — предлагает."
				ariaLabel="Подсказка: использовать в политиках доступа"
			/>
			{#if exposePending}
				<Badge size="sm" variant="warning">применится после перезапуска сервера</Badge>
			{/if}
		</div>

		<div class="toggle-row">
			<Toggle
				label="Открыть порты сервера в firewall"
				checked={wdttServer.openFirewall !== false}
				disabled={busy}
				onchange={(v) => {
					if (wdttServer) wdttServer.openFirewall = v;
				}}
			/>
		</div>
	{:else if ftServer}
		<div class="grid">
			<Input
				label="Listen-порт"
				type="number"
				value={ftPort}
				onchange={applyFtPort}
				fullWidth
			/>
		</div>

		<!-- SH-59/SH-60: полей конфига с такими именами нет — это виджет, который
		     пишет `-connect` и кладёт .conf пира в ссылку абоненту. -->
		<p class="sub-title">WG-сервер</p>
		<ServerWgBind
			autoApply
			compact
			peerLabel="Пир"
			onConnect={(addr) => {
				if (ftServer) ftServer.connect = addr;
			}}
			onPeerConf={(conf) => onpeerconf?.(conf)}
		/>

		<!-- Ключа обфускации в детали нет: строки под него в микрокопии не
		     заведено, а профиль без ключа задаёт мастер (WS-27). -->
		<div class="grid">
			<Dropdown label="Профиль обфускации" bind:value={ftServer.obfProfile} options={obfOptions} />
		</div>

		<div class="toggle-row">
			<Toggle
				label="Открыть порт в firewall"
				checked={ftServer.openFirewall !== false}
				disabled={busy}
				onchange={(v) => {
					if (ftServer) ftServer.openFirewall = v;
				}}
			/>
		</div>
	{/if}

	<div class="btn-row">
		<Button variant="primary" loading={saving} disabled={busy} onclick={onsave}>Сохранить</Button>
		<Button variant="ghost" disabled={busy} onclick={onrevert}>Отменить</Button>
	</div>
</DetailSection>

<style>
	.row {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		flex-wrap: wrap;
		margin-bottom: 0.75rem;
	}

	.row-label {
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		min-width: 9rem;
	}

	.row-control {
		flex: 1 1 220px;
		min-width: 0;
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
		gap: 0.75rem;
		margin-top: 0.75rem;
	}

	.toggle-row {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
		margin-top: 0.875rem;
	}

	.sub-title {
		margin: 1.25rem 0 0.5rem;
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--color-text-secondary);
	}
</style>
