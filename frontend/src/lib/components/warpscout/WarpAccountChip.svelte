<script lang="ts">
	import { KeyRound, Network, RefreshCcw, Trash2, UserPlus } from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { Button, ConfirmModal, Input } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import type { WarpAccount, WarpTunnelRef } from '$lib/types/warpscout';
	import WarpChipPopover from './WarpChipPopover.svelte';
	import WarpIpv4Notice from './WarpIpv4Notice.svelte';
	import { formatTime, parseTunnelIpv4, shortId, warpErrorMessage } from './warpscoutUi';

	interface Props {
		account: WarpAccount | null;
		targets?: WarpTunnelRef[];
		usedIpv4?: string[];
		onchanged: () => void | Promise<void>;
	}

	let { account, targets = [], usedIpv4 = [], onchanged }: Props = $props();

	let accountBusy = $state(false);
	let license = $state('');
	let acctIpv4 = $state('');
	let syncedIpv4 = $state<string | undefined>(undefined);
	let confirmReissue = $state(false);
	let confirmDelete = $state(false);

	$effect(() => {
		const nextSrv = account ? account.addressV4 || '172.16.0.2' : '';
		if (syncedIpv4 !== nextSrv) {
			syncedIpv4 = nextSrv;
			acctIpv4 = nextSrv;
		}
	});

	const acctIpv4Parsed = $derived(parseTunnelIpv4(acctIpv4));
	const acctIpv4Valid = $derived(acctIpv4Parsed !== null);
	const acctIpv4Changed = $derived.by(() => {
		if (!acctIpv4Parsed) return false;
		const cur = parseTunnelIpv4(account?.addressV4 || '172.16.0.2')?.normalized ?? '172.16.0.2';
		return acctIpv4Parsed.normalized !== cur;
	});

	async function saveAccountAddress(e: SubmitEvent): Promise<void> {
		e.preventDefault();
		if (!acctIpv4Parsed) return;
		accountBusy = true;
		try {
			const updated = await api.warpscoutSetAddress(acctIpv4Parsed.normalized);
			const next = updated.addressV4 || acctIpv4Parsed.normalized;
			syncedIpv4 = next;
			acctIpv4 = next;
			notifications.success(m.warpscout_ipv4_saved());
			await onchanged();
		} catch (err) {
			notifications.error(warpErrorMessage(err));
		} finally {
			accountBusy = false;
		}
	}

	async function register(): Promise<void> {
		accountBusy = true;
		try {
			await api.warpscoutRegister();
			notifications.success(m.warpscout_acct_registered());
			confirmReissue = false;
			await onchanged();
		} catch (e) {
			notifications.error(warpErrorMessage(e));
		} finally {
			accountBusy = false;
		}
	}

	async function applyLicense(e: SubmitEvent): Promise<void> {
		e.preventDefault();
		const key = license.trim();
		if (!key) return;
		accountBusy = true;
		try {
			const acct = await api.warpscoutLicense(key);
			notifications.success(
				acct.warpPlus ? m.warpscout_license_plus_applied() : m.warpscout_license_applied(),
			);
			license = '';
			await onchanged();
		} catch (err) {
			notifications.error(warpErrorMessage(err));
		} finally {
			accountBusy = false;
		}
	}

	async function deleteAccount(): Promise<void> {
		accountBusy = true;
		try {
			await api.warpscoutDeleteAccount();
			notifications.success(m.warpscout_acct_deleted());
			confirmDelete = false;
			await onchanged();
		} catch (e) {
			notifications.error(warpErrorMessage(e));
		} finally {
			accountBusy = false;
		}
	}
</script>

{#if !account}
	<div class="acct-cta">
		<span class="cta-dot"></span>
		<span class="cta-text">{m.warpscout_acct_not_registered()}</span>
		<Button size="sm" variant="primary" loading={accountBusy} onclick={register}>
			{#snippet iconBefore()}<UserPlus size={13} />{/snippet}
			{m.warpscout_register_btn()}
		</Button>
	</div>
{:else}
	<WarpChipPopover title={m.warpscout_acct_pop_title()} width="22.5rem">
		{#snippet chip()}
			<span class="chip-dot"></span>
			<span class="chip-strong">{account.warpPlus ? 'WARP+' : 'WARP Free'}</span>
			<span class="chip-sep">·</span>
			<span class="mono chip-dim">{account.addressV4 || shortId(account.id)}</span>
		{/snippet}

		{#snippet headExtra()}
			<span class="tier-pill" class:plus={account.warpPlus}>
				{account.warpPlus ? 'WARP+' : account.accountType || 'free'}
			</span>
		{/snippet}

		{#snippet children(close)}
			<div class="kv-grid">
				<div class="kv">
					<span class="k">Device ID</span>
					<span class="v mono" title={account.id}>{shortId(account.id)}</span>
				</div>
				<div class="kv">
					<span class="k">{m.warpscout_acct_created()}</span>
					<span class="v">{formatTime(account.createdAt)}</span>
				</div>
				<div class="kv">
					<span class="k">{m.warpscout_acct_keys()}</span>
					<span class="v">{m.warpscout_acct_encrypted()}</span>
				</div>
			</div>

			<!-- IPv4 туннеля: можно сменить до создания туннеля или для сканера -->
			<form class="lic-box" onsubmit={saveAccountAddress}>
				<label class="lic-label" for="warp-acct-ipv4">
					<Network size={12} />
					<span>{m.warpscout_acct_ipv4()}</span>
				</label>
				<div class="lic-row">
					<Input
						id="warp-acct-ipv4"
						type="text"
						placeholder="172.16.0.2"
						autocomplete="off"
						fullWidth
						bind:value={acctIpv4}
					/>
					<Button
						size="sm"
						variant="primary"
						type="submit"
						loading={accountBusy}
						disabled={!acctIpv4Valid || !acctIpv4Changed}
					>
						{m.common_save()}
					</Button>
				</div>
				<WarpIpv4Notice
					value={acctIpv4}
					{targets}
					{usedIpv4}
					hint={m.warpscout_ipv4_hint()}
					onsuggest={(ip) => (acctIpv4 = ip)}
				/>
			</form>

			<!-- Форма: Enter в поле привязывает ключ. -->
			<form class="lic-box" onsubmit={applyLicense}>
				<label class="lic-label" for="warp-license">
					<KeyRound size={12} />
					<span>{m.warpscout_license_label()}</span>
				</label>
				<div class="lic-row">
					<Input
						id="warp-license"
						type="password"
						placeholder="xxxxxxxx-xxxxxxxx-xxxxxxxx"
						autocomplete="off"
						fullWidth
						bind:value={license}
					/>
					<Button
						size="sm"
						variant="primary"
						type="submit"
						loading={accountBusy}
						disabled={!license.trim()}
					>
						{m.warpscout_license_bind()}
					</Button>
				</div>
			</form>

			<div class="pop-foot">
				<Button
					size="sm"
					variant="ghost"
					disabled={accountBusy}
					onclick={() => {
						close();
						confirmReissue = true;
					}}
				>
					{#snippet iconBefore()}<RefreshCcw size={13} />{/snippet}
					{m.warpscout_reissue_btn()}
				</Button>
				<Button
					size="sm"
					variant="outline-danger"
					disabled={accountBusy}
					onclick={() => {
						close();
						confirmDelete = true;
					}}
				>
					{#snippet iconBefore()}<Trash2 size={13} />{/snippet}
					{m.common_delete()}
				</Button>
			</div>
		{/snippet}
	</WarpChipPopover>
{/if}

<ConfirmModal
	open={confirmReissue}
	title={m.warpscout_confirm_reissue_title()}
	message={m.warpscout_confirm_reissue_msg()}
	secondary={m.warpscout_confirm_reissue_sec()}
	confirmLabel={m.warpscout_reissue_btn()}
	variant="danger"
	busy={accountBusy}
	onConfirm={register}
	onClose={() => (confirmReissue = false)}
/>

<ConfirmModal
	open={confirmDelete}
	title={m.warpscout_confirm_del_acct_title()}
	message={m.warpscout_confirm_del_acct_msg()}
	secondary={m.warpscout_confirm_del_acct_sec()}
	confirmLabel={m.common_delete()}
	variant="danger"
	busy={accountBusy}
	onConfirm={deleteAccount}
	onClose={() => (confirmDelete = false)}
/>

<style>
	.acct-cta {
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
		padding: 0.15rem 0.25rem 0.15rem 0.55rem;
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		flex-wrap: wrap;
	}
	.cta-dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		flex-shrink: 0;
		background: var(--color-text-muted);
	}
	.cta-text {
		font-size: 0.76rem;
		font-weight: 500;
		color: var(--color-text-secondary);
	}
	.tier-pill {
		padding: 0.08rem 0.45rem;
		border-radius: 999px;
		font-size: 0.68rem;
		font-weight: 600;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		color: var(--color-text-secondary);
	}
	.tier-pill.plus {
		background: var(--color-accent-tint);
		border-color: var(--color-accent-border);
		color: var(--color-accent);
	}
	.kv-grid {
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr));
		gap: 0.4rem;
	}
	.kv {
		display: flex;
		flex-direction: column;
		gap: 0.1rem;
		padding: 0.35rem 0.5rem;
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
	}
	.kv .k {
		font-size: 0.67rem;
		color: var(--color-text-muted);
	}
	.kv .v {
		font-size: 0.76rem;
		font-weight: 500;
		color: var(--color-text-primary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.lic-box {
		display: flex;
		flex-direction: column;
		gap: 0.3rem;
		padding-top: 0.5rem;
		border-top: 1px solid var(--color-border);
	}
	.lic-label {
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		font-size: 0.72rem;
		font-weight: 500;
		color: var(--color-text-secondary);
	}
	.lic-row {
		display: flex;
		align-items: center;
		gap: 0.4rem;
	}
	.mono {
		font-family: var(--font-mono);
	}
</style>
