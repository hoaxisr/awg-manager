<script lang="ts">
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { Button, ConfirmModal, Modal, SegmentedControl } from '$lib/components/ui';
	import type { SegmentedOption } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import WarpAccountImportForm from './WarpAccountImportForm.svelte';
	import WarpAccountProxyForm from './WarpAccountProxyForm.svelte';
	import { warpErrorMessage } from './warpscoutUi';

	interface Props {
		open: boolean;
		initialTab?: 'import' | 'proxy';
		hasExistingAccount: boolean;
		accountKeyInUseBy?: string;
		onclose: () => void;
		onchanged: () => void | Promise<void>;
	}

	let {
		open,
		initialTab = 'import',
		hasExistingAccount,
		accountKeyInUseBy = '',
		onclose,
		onchanged,
	}: Props = $props();

	let altTab = $state<'import' | 'proxy'>('import');
	let importRaw = $state('');
	let proxyUrl = $state('');
	let relayUrl = $state('');
	let busy = $state(false);
	let confirmImport = $state(false);
	let confirmProxyRegister = $state(false);
	let wasOpen = false;

	const altTabOptions: SegmentedOption<'import' | 'proxy'>[] = $derived([
		{ value: 'import', label: m.warpscout_alt_tab_import() },
		{ value: 'proxy', label: m.warpscout_alt_tab_proxy() },
	]);

	function resetForm(): void {
		importRaw = '';
		proxyUrl = '';
		relayUrl = '';
		confirmImport = false;
		confirmProxyRegister = false;
	}

	$effect(() => {
		if (open && !wasOpen) {
			wasOpen = true;
			altTab = initialTab;
		} else if (!open && wasOpen) {
			wasOpen = false;
			resetForm();
		}
	});

	function closeModal(): void {
		if (busy) return;
		resetForm();
		onclose();
	}

	async function doImport(): Promise<void> {
		const raw = importRaw.trim();
		if (!raw) return;
		busy = true;
		try {
			await api.warpscoutImportAccount(raw);
			notifications.success(m.warpscout_acct_imported_ok());
			resetForm();
			onclose();
			await onchanged();
		} catch (e) {
			notifications.error(warpErrorMessage(e));
		} finally {
			busy = false;
		}
	}

	function requestImport(): void {
		if (!importRaw.trim() || busy) return;
		if (hasExistingAccount) {
			confirmImport = true;
			return;
		}
		void doImport();
	}

	async function doProxyRegister(): Promise<void> {
		const p = proxyUrl.trim();
		const r = relayUrl.trim();
		if (!p && !r) return;
		busy = true;
		try {
			await api.warpscoutRegister({
				proxyUrl: p || undefined,
				relayUrl: r || undefined,
			});
			notifications.success(m.warpscout_acct_registered());
			resetForm();
			onclose();
			await onchanged();
		} catch (e) {
			notifications.error(warpErrorMessage(e));
		} finally {
			busy = false;
		}
	}

	function requestProxyRegister(): void {
		if ((!proxyUrl.trim() && !relayUrl.trim()) || !!accountKeyInUseBy || busy) return;
		if (hasExistingAccount) {
			confirmProxyRegister = true;
			return;
		}
		void doProxyRegister();
	}
</script>

<Modal {open} title={m.warpscout_alt_modal_title()} size="md" onclose={closeModal}>
	<div class="alt-modal-body">
		<SegmentedControl
			value={altTab}
			options={altTabOptions}
			ariaLabel={m.warpscout_alt_modal_title()}
			onchange={(v) => (altTab = v)}
		/>

		{#if altTab === 'import'}
			<WarpAccountImportForm bind:raw={importRaw} {busy} />
		{:else}
			<WarpAccountProxyForm bind:proxyUrl bind:relayUrl {accountKeyInUseBy} />
		{/if}
	</div>

	{#snippet actions()}
		<Button variant="ghost" onclick={closeModal} disabled={busy}>
			{m.common_cancel()}
		</Button>
		{#if altTab === 'import'}
			<Button variant="primary" loading={busy} disabled={!importRaw.trim()} onclick={requestImport}>
				{m.warpscout_import_btn()}
			</Button>
		{:else}
			<Button
				variant="primary"
				loading={busy}
				disabled={(!proxyUrl.trim() && !relayUrl.trim()) || !!accountKeyInUseBy}
				onclick={requestProxyRegister}
			>
				{m.warpscout_proxy_register_btn()}
			</Button>
		{/if}
	{/snippet}
</Modal>

<ConfirmModal
	open={confirmImport}
	title={m.warpscout_confirm_import_title()}
	message={m.warpscout_confirm_import_msg()}
	secondary={m.warpscout_confirm_import_sec()}
	confirmLabel={m.warpscout_import_btn()}
	variant="danger"
	{busy}
	onConfirm={doImport}
	onClose={() => (confirmImport = false)}
/>

<ConfirmModal
	open={confirmProxyRegister}
	title={m.warpscout_confirm_reissue_title()}
	message={m.warpscout_confirm_reissue_msg()}
	secondary={m.warpscout_confirm_reissue_sec()}
	confirmLabel={m.warpscout_proxy_register_btn()}
	variant="danger"
	{busy}
	onConfirm={doProxyRegister}
	onClose={() => (confirmProxyRegister = false)}
/>

<style>
	.alt-modal-body {
		display: flex;
		flex-direction: column;
		gap: 0.7rem;
	}
</style>
