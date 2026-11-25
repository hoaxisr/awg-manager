<!--
  Контейнер карточки «Прокси и обфускаторы» (MC2): статусы подсистем живут в
  своих сторах, поэтому установка и снос — здесь.
-->
<script lang="ts">
	import type { Snippet } from 'svelte';
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { notifications } from '$lib/stores/notifications';
	import { proxyInstallStatus, type ProxySubsystem } from '$lib/stores/proxyInstall';
	import ProxyBinariesCard from './ProxyBinariesCard.svelte';
	import { pageState } from './pageState.svelte';

	interface Props {
		phobosKernel?: Snippet;
	}

	let { phobosKernel }: Props = $props();

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
	const proxyBusy = pageState<Record<string, boolean>>('proxy-busy', () => ({}));

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
		proxyBusy[subsystem] = true;
		try {
			await action();
			notifications.success(okMessage);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : failMessage);
		} finally {
			proxyBusy[subsystem] = false;
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

<ProxyBinariesCard rows={proxyBinaryRows} {phobosKernel} />
