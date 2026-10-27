<!--
  Контейнер карточки «Интеграции» (MC2): статусы sing-box, Hydra, zapret и
  подсистем прокси живут в своих сторах, поэтому установка/обновление/снос —
  здесь. Bootstrap-DNS и порт Clash меняют общие настройки — их сохраняет
  страница (onsaveBootstrapDNS, onsaveClashPort).
-->
<script lang="ts">
	import { api } from '$lib/api/client';
	import { m, type UiText } from '$lib/i18n';
	import { notifications } from '$lib/stores/notifications';
	import { singboxStatus } from '$lib/stores/singbox';
	import { hydrarouteStatus } from '$lib/stores/hydraroute';
	import { zapretStatusIfExpert } from '$lib/stores/zapret';
	import { proxyInstallStatus, type ProxySubsystem } from '$lib/stores/proxyInstall';
	import { singboxErrorText } from '$lib/utils/singboxError';
	import IntegrationsCard from './IntegrationsCard.svelte';

	interface Props {
		showSingbox: boolean;
		showHydra: boolean;
		bootstrapDNS: string;
		bootstrapSaving: boolean;
		onsaveBootstrapDNS: (value: string) => void;
		clashPort: number;
		clashPortSaving: boolean;
		clashPortError: string | null;
		onsaveClashPort: (value: number) => void;
	}

	let {
		showSingbox,
		showHydra,
		bootstrapDNS,
		bootstrapSaving,
		onsaveBootstrapDNS,
		clashPort,
		clashPortSaving,
		clashPortError,
		onsaveClashPort,
	}: Props = $props();

	let singboxInstalling = $state(false);
	let singboxUninstalling = $state(false);
	let singboxInstallError = $state<UiText | null>(null);
	let singboxUpdating = $state(false);
	let singboxUpdateError = $state<UiText | null>(null);

	const singboxStatusValue = $derived($singboxStatus.data ?? null);
	const singboxStatusLoading = $derived(
		$singboxStatus.lastFetchedAt === 0 &&
		($singboxStatus.status === 'idle' || $singboxStatus.status === 'loading')
	);
	const hydraStatusValue = $derived($hydrarouteStatus.data ?? null);
	const hydraStatusLoading = $derived(
		$hydrarouteStatus.lastFetchedAt === 0 &&
		($hydrarouteStatus.status === 'idle' || $hydrarouteStatus.status === 'loading')
	);
	const hydraStatusError = $derived($hydrarouteStatus.error);
	const zapretStatusValue = $derived($zapretStatusIfExpert?.data ?? null);

	async function installSingbox() {
		singboxInstalling = true;
		singboxInstallError = null;
		try {
			const fresh = await api.singboxInstall();
			singboxStatus.applyMutationResponse(fresh);
			notifications.success(m.settings_page_singbox_installed());
		} catch (e) {
			singboxInstallError = singboxErrorText(e);
		} finally {
			singboxInstalling = false;
		}
	}

	async function uninstallSingbox() {
		singboxUninstalling = true;
		try {
			const fresh = await api.singboxUninstall();
			singboxStatus.applyMutationResponse(fresh);
			notifications.success(m.settings_page_singbox_removed());
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_page_singbox_remove_failed());
		} finally {
			singboxUninstalling = false;
		}
	}

	async function updateSingbox() {
		singboxUpdating = true;
		singboxUpdateError = null;
		try {
			const fresh = await api.singboxUpdate();
			singboxStatus.applyMutationResponse(fresh);
			notifications.success(m.settings_page_singbox_updated());
		} catch (e) {
			singboxUpdateError = singboxErrorText(e);
		} finally {
			singboxUpdating = false;
		}
	}

	// ── подсистемы прокси (WDTT, FreeTurn, обфускаторы) ─────────────
	// Бинари ставятся и снимаются целиком подсистемой: version-файл у половин
	// общий, а раздельный снос сделал бы статус неоднозначным.
	//
	// Статус живёт в polling-store, подписанном на `proxyrt.instances`: удаление
	// инстанса в другой вкладке иначе оставило бы кнопку «Удалить» запертой до
	// перезагрузки страницы.
	const PROXY_SUBSYSTEMS = [
		{ key: 'wdtt' as const, label: 'WDTT' },
		{ key: 'freeturn' as const, label: 'FreeTurn' },
		{ key: 'obf-phobos' as const, label: 'wg-obfuscator (Phobos)' },
		{ key: 'obf-clusterm' as const, label: 'wg-obfuscator (ClusterM)' },
	];
	let proxyBusy = $state<Record<string, boolean>>({});

	// Автоподписка `$store` работает только с идентификатором, поэтому
	// store'ы разложены по переменным.
	const wdttInstallStore = proxyInstallStatus.wdtt;
	const freeturnInstallStore = proxyInstallStatus.freeturn;
	const obfPhobosInstallStore = proxyInstallStatus['obf-phobos'];
	const obfClusterMInstallStore = proxyInstallStatus['obf-clusterm'];
	const proxyStatuses = $derived({
		wdtt: $wdttInstallStore.data,
		freeturn: $freeturnInstallStore.data,
		'obf-phobos': $obfPhobosInstallStore.data,
		'obf-clusterm': $obfClusterMInstallStore.data,
	});

	async function runProxyBinaries(
		subsystem: ProxySubsystem,
		action: () => Promise<void>,
		okMessage: string,
		failMessage: string,
	) {
		proxyBusy = { ...proxyBusy, [subsystem]: true };
		try {
			await action();
			notifications.success(okMessage);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : failMessage);
		} finally {
			proxyBusy = { ...proxyBusy, [subsystem]: false };
			await proxyInstallStatus[subsystem].refetch();
		}
	}

	const proxyBinaryRows = $derived(
		PROXY_SUBSYSTEMS.map(({ key, label }) => {
			const st = proxyStatuses[key];
			return {
				key,
				label,
				present: st?.binariesPresent === true,
				installAvailable: st?.installAvailable === true,
				updateAvailable: st?.updateAvailable === true,
				installedVersion: st?.installedVersion,
				installVersion: st?.installVersion,
				instances: st?.instances ?? 0,
				busy: proxyBusy[key] === true,
				oninstall: () =>
					void runProxyBinaries(key, () => api.proxyInstall(key),
						m.settings_page_proxy_installed({ label }), m.settings_page_proxy_install_failed({ label })),
				onuninstall: () =>
					void runProxyBinaries(key, () => api.proxyUninstall(key),
						m.settings_page_proxy_removed({ label }), m.settings_page_proxy_remove_failed({ label })),
			};
		// Подсистема без статуса и без возможности установки — не наша арка:
		// строка была бы мёртвой.
		}).filter((row) => row.present || row.installAvailable),
	);
</script>

<IntegrationsCard
	singboxStatus={singboxStatusValue}
	{singboxStatusLoading}
	hydraStatus={hydraStatusValue}
	{hydraStatusLoading}
	{hydraStatusError}
	{singboxInstalling}
	{singboxUpdating}
	{singboxInstallError}
	{singboxUpdateError}
	oninstallSingbox={installSingbox}
	onupdateSingbox={updateSingbox}
	onuninstallSingbox={uninstallSingbox}
	{singboxUninstalling}
	{showSingbox}
	{showHydra}
	zapretStatus={zapretStatusValue}
	{bootstrapDNS}
	{bootstrapSaving}
	{onsaveBootstrapDNS}
	{clashPort}
	{clashPortSaving}
	{clashPortError}
	{onsaveClashPort}
	proxyBinaries={proxyBinaryRows}
/>
