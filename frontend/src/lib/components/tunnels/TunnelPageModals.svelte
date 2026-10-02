<script lang="ts">
	// Модалки страницы туннелей — выделено из routes/+page.svelte (класс 2):
	// подтверждения удаления, графики трафика, диагностика, мастер создания,
	// импорт внешнего интерфейса, настройки connectivity. Состояние страницы
	// приходит live-контекстом (ctx), стили — глобальные (app.css).
	import { AdoptTunnelDialog, TunnelReferencedModal, ConnectivitySettingsModal } from '$lib/components/tunnels';
	import { Modal, TrafficChartModal, Button, ConfirmModal } from '$lib/components/ui';
	import TunnelDiagnosticsModal from '$lib/components/testing/TunnelDiagnosticsModal.svelte';
	import AddTunnelWizard from '$lib/components/subscriptions/AddTunnelWizard.svelte';
	import { resolveSubscriptionMemberTag } from '$lib/utils/subscriptionMember';
	import type { TunnelPageModalsContext } from './tunnelPageModalsContext';
	import { m } from '$lib/i18n';

	let { ctx }: { ctx: TunnelPageModalsContext } = $props();
</script>

<AdoptTunnelDialog
	interfaceName={ctx.adoptingInterface}
	bind:open={ctx.adoptDialogOpen}
	bind:error={ctx.adoptError}
	bind:loading={ctx.adoptLoading}
	onclose={() => ctx.adoptDialogOpen = false}
	onadopt={ctx.handleAdopt}
/>

{#if ctx.deleteConfirmId}
	{@const tunnelName = ctx.awgList.find(t => t.id === ctx.deleteConfirmId)?.name ?? ctx.deleteConfirmId}
	<Modal
		open={true}
		title={m.tunnels_modals_delete_tunnel_title()}
		size="sm"
		onclose={() => ctx.deleteConfirmId = null}
	>
		<p class="confirm-text">{m.tunnels_modals_delete_tunnel_lead()} <strong>{tunnelName}</strong>?</p>
		{#snippet actions()}
			<Button variant="secondary" size="md" onclick={() => ctx.deleteConfirmId = null}>{m.common_cancel()}</Button>
			<Button variant="danger" size="md" onclick={() => ctx.handleDelete(ctx.deleteConfirmId!)}>{m.common_delete()}</Button>
		{/snippet}
	</Modal>
{/if}

{#if ctx.confirmExternalDelete}
	<Modal
		open={true}
		title={m.tunnels_modals_delete_iface_title()}
		size="sm"
		onclose={() => (ctx.confirmExternalDelete = null)}
	>
		<p class="confirm-text">
			{m.tunnels_modals_delete_iface_lead()} <strong>{ctx.confirmExternalDelete.interfaceName}{ctx.confirmExternalDelete.label}</strong>?
		</p>
		{#if ctx.confirmExternalDelete.live}
			<p class="confirm-text confirm-warn">
				{m.tunnels_modals_iface_live_warning()}
			</p>
		{/if}
		{#if ctx.confirmExternalDelete.conflictsWith}
			<p class="confirm-text confirm-warn">
				{m.tunnels_modals_iface_conflict_warning({ address: ctx.confirmExternalDelete.address, name: ctx.confirmExternalDelete.conflictsWith })}
			</p>
		{/if}
		<p class="confirm-text">
			{m.tunnels_modals_iface_delete_note()}
		</p>
		{#snippet actions()}
			<Button variant="secondary" size="md" onclick={() => (ctx.confirmExternalDelete = null)}>{m.common_cancel()}</Button>
			<Button
				variant="danger"
				size="md"
				loading={ctx.confirmExternalDeleteBusy}
				onclick={() => ctx.confirmExternalDeleteNow()}
			>
				{m.common_delete()}
			</Button>
		{/snippet}
	</Modal>
{/if}

{#if ctx.unlockConfirmId}
	{@const tunnelName = ctx.awgList.find(t => t.id === ctx.unlockConfirmId)?.name ?? ctx.unlockConfirmId}
	<ConfirmModal
		open={true}
		variant="primary"
		title={m.tunnels_modals_unlock_title()}
		message={m.tunnels_modals_unlock_message({ name: tunnelName })}
		confirmLabel={m.tunnels_modals_unlock_confirm()}
		onConfirm={ctx.confirmUnlock}
		onClose={() => ctx.unlockConfirmId = null}
	/>
{/if}

<TunnelReferencedModal
	open={ctx.referencedDetails !== null}
	details={ctx.referencedDetails}
	tunnelName={ctx.referencedTunnelName}
	onclose={() => { ctx.referencedDetails = null; ctx.referencedTunnelName = ''; }}
/>

<AddTunnelWizard
	bind:open={ctx.createModalOpen}
	preselect={ctx.wizardPreselect}
	onAwg3={ctx.awg3Visible ? ctx.openAwg3Import : undefined}
/>

<Modal
	open={ctx.pendingSubscriptionDelete !== null}
	title={m.tunnels_modals_delete_sub_title()}
	size="md"
	onclose={() => {
		if (ctx.deletingSubscription) return;
		ctx.pendingSubscriptionDelete = null;
	}}
>
	<p>
		{m.tunnels_modals_delete_sub_lead()} <strong>{ctx.pendingSubscriptionLabel}</strong>
		{m.tunnels_modals_delete_sub_tail()}
	</p>
	{#snippet actions()}
		<Button
			variant="ghost"
			disabled={ctx.deletingSubscription}
			onclick={() => (ctx.pendingSubscriptionDelete = null)}
		>
			{m.common_cancel()}
		</Button>
		<Button
			variant="danger"
			disabled={ctx.deletingSubscription}
			loading={ctx.deletingSubscription}
			onclick={ctx.confirmSubscriptionDelete}
		>
			{ctx.deletingSubscription ? m.tunnels_modals_deleting() : m.common_delete()}
		</Button>
	{/snippet}
</Modal>

{#if ctx.detailId}
	{@const managed = ctx.awgList.find((x) => x.id === ctx.detailId)}
	{@const sys = ctx.systemList.find((x) => x.id === ctx.detailId)}
	{#if managed || sys}
		<TrafficChartModal
			open={true}
			tunnelId={ctx.detailId}
			tunnelName={managed?.name ?? sys?.description ?? ctx.detailId}
			ifaceName={managed?.interfaceName ?? sys?.interfaceName ?? ''}
			onclose={ctx.closeDetail}
		/>
	{/if}
{/if}

{#if ctx.singboxDetailTag}
	{@const sb = ctx.singboxTunnelsList.find((x) => x.tag === ctx.singboxDetailTag)}
	{@const subActiveCard = ctx.subscriptionsActiveCards.find((c) => c.activeMember.tag === ctx.singboxDetailTag)}
	{@const subListRow = ctx.subscriptionsListRows.find(
		(s) => resolveSubscriptionMemberTag(s, ctx.liveActives[s.id] || null) === ctx.singboxDetailTag,
	)}
	{@const detailName =
		subActiveCard?.subscription.label
		?? subListRow?.label
		?? sb?.tag
		?? ctx.singboxDetailTag}
	{@const detailIface =
		subActiveCard
			? (subActiveCard.subscription.proxyIndex >= 0 ? `Proxy${subActiveCard.subscription.proxyIndex}` : '')
			: (subListRow
				? (subListRow.proxyIndex >= 0 ? `Proxy${subListRow.proxyIndex}` : '')
				: (sb?.proxyInterface ?? ''))}
	<TrafficChartModal
		open={true}
		tunnelId={ctx.singboxDetailTag}
		tunnelName={detailName}
		ifaceName={detailIface}
		onclose={ctx.closeSingboxDetail}
	/>
{/if}

{#if ctx.awgDiagnosticsTarget}
	<TunnelDiagnosticsModal
		open={true}
		kind={ctx.awgDiagnosticsTarget.kind}
		targetId={ctx.awgDiagnosticsTarget.id}
		displayName={ctx.awgDiagnosticsTarget.name}
		subject="tunnel"
		onclose={ctx.closeAwgDiagnostics}
	/>
{/if}

{#if ctx.connectivitySettingsTunnel}
	<ConnectivitySettingsModal
		bind:open={ctx.connectivitySettingsOpen}
		tunnelId={ctx.connectivitySettingsTunnel.id}
		tunnelAddress={ctx.connectivitySettingsTunnel.address}
		onclose={ctx.closeConnectivitySettings}
		onSaved={ctx.closeConnectivitySettings}
	/>
{/if}
