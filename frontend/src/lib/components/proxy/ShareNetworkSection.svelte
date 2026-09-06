<script lang="ts">
	// SH-47..SH-61 — «Сеть» раздачи. NAT, LAN и политика применяются своими
	// ручками сразу (их и правит бэкенд), остальные поля живут в редактируемой
	// копии конфига и уезжают кнопкой «Сохранить».
	import { Badge, Button, ChipMultiSelect, Dropdown, FieldHint, FormRow, Input, SegmentedControl, Toggle } from '$lib/components/ui';
	import { ServerAccessPolicyDropdown } from '$lib/components/servers';
	import ServerWgBind from '../freeturn/ServerWgBind.svelte';
	import { effectiveStaticWan } from '$lib/api/proxyInstances';
	import { obfOptions } from '../freeturn/options';
	import { listenPortNumber, setListenPort } from '$lib/utils/listenPortUtils';
	import type { NatMode } from '$lib/utils/network';
	import type { FreeTurnServerConfig, WdttServerConfig } from '$lib/types';
	import DetailSection from './DetailSection.svelte';
	import { natModeOptions, serverPortConflict } from './shareConfig';

	// Режим работы сервера. Раньше менялся только пересозданием инстанса: в
	// списке бейдж режима был, а переключателя не было нигде.

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
		/** Посторонний ACL, привязанный к интерфейсу сервера в обход выбора сегментов. */
		foreignAcls?: string[];
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
		/**
		 * Выбранный пир FreeTurn-сервера. Состояние принадлежит детали: тот же
		 * выбор показывает быстрый селект строки состояния (RB-12).
		 */
		peer?: string;
	}

	let {
		wdttServer = $bindable(),
		ftServer = $bindable(),
		lanOptions,
		exposeApplied,
		foreignAcls,
		saving = false,
		busy = false,
		onnat,
		onlan,
		onpolicy,
		onsave,
		onrevert,
		onpeerconf,
		peer = $bindable(''),
	}: Props = $props();

	const natMode = $derived((wdttServer?.natMode ?? 'full') as NatMode);
	/**
	 * Режим NAT «Интернет» требует выбранного WAN (`WdttServerConfig.Validate`).
	 * Раньше кнопка была активна, поля WAN не было нигде, и сохранённый конфиг
	 * молча становился невалидным: сервер не стартовал, исправить было негде.
	 */
	// Столкновение портов сервера: сохранять нельзя — бэкенд всё равно
	// откажет, но уже приговором конфига.
	const portConflict = $derived(wdttServer ? serverPortConflict(wdttServer) : '');

	const wanMissing = $derived(
		!!wdttServer &&
			wdttServer.natMode === 'internet-only' &&
			!effectiveStaticWan(wdttServer).trim(),
	);
	/** Показать, чего не хватает «Интернету»: клик по нему был отклонён. */
	let natWanBlocked = $state(false);

	/**
	 * Смена NAT уезжает СВОЕЙ ручкой сразу, мимо «Сохранить», поэтому «Интернет»
	 * без выбранного WAN перехватывается здесь: иначе невалидный конфиг попал бы
	 * на бэкенд немедленно, и сервер перестал бы стартовать.
	 */
	function changeNat(mode: NatMode) {
		if (mode === 'internet-only' && !(wdttServer ? effectiveStaticWan(wdttServer) : '').trim()) {
			natWanBlocked = true;
			return;
		}
		natWanBlocked = false;
		onnat(mode);
	}

	const ftPort = $derived(String(ftServer?.listen?.split(':').pop() ?? ''));
	// SH-56 держится, пока выбранное не совпало с применённым. Применённое
	// неизвестно — расхождения нет о чём заявлять, бейдж не показываем.
	const exposePending = $derived(
		exposeApplied !== undefined && (wdttServer?.exposeToPolicies ?? false) !== exposeApplied,
	);


	// Порт раздачи — главный внешний порт и обязательное поле конфига
	// (`WdttServerConfig.Validate`: «не задан listen сервера»).
	const dtlsPort = $derived(String(listenPortNumber(wdttServer?.listen ?? '', 0) || 56002));

	function applyDtlsPort(value: string) {
		const port = Number(value);
		if (!wdttServer || !Number.isFinite(port) || port <= 0) return;
		wdttServer.listen = setListenPort(wdttServer.listen || '0.0.0.0:56002', port, '0.0.0.0');
	}

	function applyFtPort(value: string) {
		if (!ftServer) return;
		const port = Math.max(1, Math.min(65535, Number(value) || 56000));
		ftServer.listen = setListenPort(ftServer.listen || '0.0.0.0:56000', port, '0.0.0.0');
	}
</script>

<DetailSection title="Сеть">
	{#if wdttServer}
		<!-- Одна сетка «метка — контрол» на всю секцию (решение по вёрстке
		     2026-08-27): раньше здесь уживались три схемы сразу. -->
		<div class="form">
			<!-- Порт раздачи — обязательное поле конфига (`Validate`), и чинить
			     его человек должен здесь, а не в «экспертном» разделе. Raw-
			     половина занимает следующий порт автоматически. -->
			<FormRow
				label="Порт раздачи"
				for="wdtt-dtls-port"
				hint="Главный внешний порт; raw-половина займёт следующий. Смена перезапустит сервер"
			>
				<div class="w-port">
					<Input
						id="wdtt-dtls-port"
						type="number"
						value={dtlsPort}
						onchange={applyDtlsPort}
						fullWidth
					/>
				</div>
			</FormRow>

			<FormRow label="Режим NAT">
				<SegmentedControl
					value={natMode}
					options={natModeOptions}
					ariaLabel="Режим NAT"
					disabled={busy}
					onchange={changeNat}
				/>
				{#if natWanBlocked}
					<span class="save-block">
						Сначала выберите выход в интернет в разделе «Дополнительно» — без него
						режим «Интернет» не работает.
					</span>
				{/if}
			</FormRow>

			<FormRow label="Доступ в LAN">
				<ChipMultiSelect
					values={wdttServer.lanSegments ?? []}
					options={lanOptions}
					disabled={busy}
					onchange={onlan}
				/>
				{#if foreignAcls?.length}
					<span class="save-block">
						К интерфейсу привязан посторонний список доступа ({foreignAcls.join(', ')}). Он срабатывает раньше выбора сегментов и может открыть клиентам больше, чем выбрано — проверьте его в настройках роутера.
					</span>
				{/if}
			</FormRow>

			<!-- Политика доступа (SH-50) — общий с «Серверами» контрол: список
			     политик роутера внутри, подпись даёт строка формы. -->
			<FormRow label="Политика доступа">
				<ServerAccessPolicyDropdown
					policy={wdttServer.policy ?? 'none'}
					disabled={busy}
					onchange={onpolicy}
					labelless
				/>
			</FormRow>

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
		<!-- SH-59/SH-60: полей конфига с такими именами нет — это виджет, который
		     пишет `-connect` и кладёт .conf пира в ссылку абоненту. Стоит ПЕРВЫМ
		     (правка владельца 2026-08-27): сначала «куда ведёт раздача», потом
		     «на каком порту принимает». -->
		<p class="sub-title">WG-сервер</p>
		<ServerWgBind
			autoApply
			compact
			peerLabel="Пир"
			bind:selected={peer}
			onConnect={(addr) => {
				if (ftServer) ftServer.connect = addr;
			}}
			onPeerConf={(conf) => onpeerconf?.(conf)}
		/>

		<div class="form">
			<FormRow
				label="Listen-порт"
				for="ft-listen"
				hint="Порт, на котором сервер принимает абонентов. Клиентам порт выделяет менеджер, серверу — задаёте вы"
			>
				<div class="w-port">
					<Input id="ft-listen" type="number" value={ftPort} onchange={applyFtPort} fullWidth />
				</div>
			</FormRow>

			<!-- Ключа обфускации в детали нет: строки под него в микрокопии не
			     заведено, а профиль без ключа задаёт мастер (WS-27). -->
			<FormRow label="Профиль обфускации">
				<div class="w-select">
					<Dropdown bind:value={ftServer.obfProfile} options={obfOptions} fullWidth />
				</div>
			</FormRow>
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

	{#if portConflict}
		<p class="save-block">{portConflict}</p>
	{/if}

	{#if wanMissing}
		<p class="save-block">
			Режиму NAT «Интернет» нужен выход в интернет — выберите его в разделе
			«Дополнительно». Без него сервер не запустится.
		</p>
	{/if}

	<div class="btn-row">
		<Button variant="primary" loading={saving} disabled={busy || wanMissing || !!portConflict} onclick={onsave}>
			Сохранить
		</Button>
		<Button variant="ghost" disabled={busy} onclick={onrevert}>Отменить</Button>
	</div>
</DetailSection>

<style>
	.save-block {
		font-size: 12px;
		color: var(--color-warning);
	}

	/* Контролы шириной по содержимому: растянутые на всю строку сегменты и
	   поле порта в пять цифр — то, за что вёрстку и назвали неряшливой.
	   Сегменты сжимаются до надписей (у них `inline-flex` внутри), поля и
	   списки ограничены разумным потолком. */
	.form :global(.form-row-control > *) {
		max-width: 420px;
	}

	.form :global(.form-row-control > [role='group']) {
		width: fit-content;
	}

	.w-port {
		width: 108px;
	}

	.w-select {
		width: 220px;
	}

	/* Сетка формы: ширину колонки метки задаёт контейнер, строки — FormRow. */
	.form {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
		--form-label-w: 140px;
		margin-bottom: 1rem;
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
