<!--
  Контейнер карточки sing-box (MC2): статус движка живёт в своём сторе, поэтому
  установка, обновление, снос и управление процессом — здесь.
-->
<script lang="ts">
	import type { Snippet } from 'svelte';
	import { api } from '$lib/api/client';
	import { m, type UiText } from '$lib/i18n';
	import { notifications } from '$lib/stores/notifications';
	import { setSettings as setGlobalSettings } from '$lib/stores/settings';
	import { singboxStatus } from '$lib/stores/singbox';
	import { singboxErrorText } from '$lib/utils/singboxError';
	import SingboxCard from './SingboxCard.svelte';
	import { pageState } from './pageState.svelte';
	import type { SettingsEditor } from './settingsEditor.svelte';

	interface Props {
		editor: SettingsEditor;
		children?: Snippet;
	}

	let { editor, children }: Props = $props();

	// Ход операций и отказ переживают уход со вкладки: вернувшийся не запустит
	// вторую поверх идущей и увидит, чем кончилась первая.
	const op = pageState('singbox-op', () => ({
		installing: false,
		uninstalling: false,
		updating: false,
		controlling: false,
		installError: null as UiText | null,
		updateError: null as UiText | null,
		savingBootstrapDNS: false,
		savingClashPort: false,
		clashPortError: null as string | null,
	}));

	const status = $derived($singboxStatus.data ?? null);
	const statusLoading = $derived(
		$singboxStatus.lastFetchedAt === 0 && ($singboxStatus.status === 'idle' || $singboxStatus.status === 'loading'),
	);

	async function install() {
		op.installing = true;
		op.installError = null;
		try {
			singboxStatus.applyMutationResponse(await api.singboxInstall());
			notifications.success(m.settings_page_singbox_installed());
		} catch (e) {
			op.installError = singboxErrorText(e);
		} finally {
			op.installing = false;
		}
	}

	async function uninstall() {
		op.uninstalling = true;
		try {
			singboxStatus.applyMutationResponse(await api.singboxUninstall());
			notifications.success(m.settings_page_singbox_removed());
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_page_singbox_remove_failed());
		} finally {
			op.uninstalling = false;
		}
	}

	async function update() {
		op.updating = true;
		op.updateError = null;
		try {
			singboxStatus.applyMutationResponse(await api.singboxUpdate());
			notifications.success(m.settings_page_singbox_updated());
		} catch (e) {
			op.updateError = singboxErrorText(e);
		} finally {
			op.updating = false;
		}
	}

	async function control(action: 'start' | 'stop' | 'restart') {
		op.controlling = true;
		try {
			singboxStatus.applyMutationResponse(await api.singboxControl(action));
			notifications.success(
				action === 'restart' ? m.settings_page_singbox_restarted() :
				action === 'stop' ? m.settings_page_singbox_stopped() : m.settings_page_singbox_started(),
			);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_page_singbox_control_failed());
		} finally {
			op.controlling = false;
		}
	}

	// Bootstrap-DNS применяется бэкендом сразу: он переписывает адрес в
	// 00-base.json и перечитывает конфиг sing-box без перезапуска.
	async function saveBootstrapDNS(value: string) {
		op.savingBootstrapDNS = true;
		try {
			editor.settings = await api.updateSettings({ singboxBootstrapDNS: value });
			setGlobalSettings(editor.settings);
			notifications.success(value ? m.settings_page_bootstrap_set({ value }) : m.settings_page_bootstrap_cleared());
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.settings_page_bootstrap_save_failed());
		} finally {
			op.savingBootstrapDNS = false;
		}
	}

	// Порт Clash API применяется бэкендом сразу: он переписывает
	// external_controller в 00-base.json, перечитывает конфиг sing-box и
	// переставляет собственного клиента. Отказ по занятости порта приходит
	// текстом ошибки и показывается прямо под полем.
	async function saveClashPort(value: number) {
		op.savingClashPort = true;
		op.clashPortError = null;
		try {
			editor.settings = await api.updateSettings({ singboxClashPort: value });
			setGlobalSettings(editor.settings);
			notifications.success(m.settings_page_clash_port_saved({ value }));
		} catch (e) {
			const msg = e instanceof Error ? e.message : m.settings_page_clash_port_save_failed();
			op.clashPortError = msg;
			notifications.error(msg);
		} finally {
			op.savingClashPort = false;
		}
	}
</script>

<SingboxCard
	singboxStatus={status}
	singboxStatusLoading={statusLoading}
	singboxInstalling={op.installing}
	singboxUpdating={op.updating}
	singboxInstallError={op.installError}
	singboxUpdateError={op.updateError}
	oninstallSingbox={install}
	onupdateSingbox={update}
	onuninstallSingbox={uninstall}
	singboxUninstalling={op.uninstalling}
	oncontrol={control}
	controlBusy={op.controlling}
	bootstrapDNS={editor.settings.singboxBootstrapDNS ?? ''}
	bootstrapSaving={op.savingBootstrapDNS}
	onsaveBootstrapDNS={saveBootstrapDNS}
	clashPort={editor.settings.singboxClashPort ?? 0}
	clashPortSaving={op.savingClashPort}
	clashPortError={op.clashPortError}
	onsaveClashPort={saveClashPort}
	{children}
/>
