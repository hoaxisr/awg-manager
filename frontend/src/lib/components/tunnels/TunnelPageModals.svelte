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
		title="Удалить туннель"
		size="sm"
		onclose={() => ctx.deleteConfirmId = null}
	>
		<p class="confirm-text">Удалить туннель <strong>{tunnelName}</strong>?</p>
		{#snippet actions()}
			<Button variant="secondary" size="md" onclick={() => ctx.deleteConfirmId = null}>Отмена</Button>
			<Button variant="danger" size="md" onclick={() => ctx.handleDelete(ctx.deleteConfirmId!)}>Удалить</Button>
		{/snippet}
	</Modal>
{/if}

{#if ctx.unlockConfirmId}
	{@const tunnelName = ctx.awgList.find(t => t.id === ctx.unlockConfirmId)?.name ?? ctx.unlockConfirmId}
	<ConfirmModal
		open={true}
		variant="primary"
		title="Туннель защищён от изменений"
		message="Снять защиту с туннеля «{tunnelName}»? После этого его можно выключить, изменить и удалить."
		confirmLabel="Снять защиту"
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
	title="Удалить подписку?"
	size="md"
	onclose={() => {
		if (ctx.deletingSubscription) return;
		ctx.pendingSubscriptionDelete = null;
	}}
>
	<p>
		Подписка <strong>{ctx.pendingSubscriptionLabel}</strong> будет удалена
		вместе с её sing-box outbound'ами и NDMS Proxy-интерфейсом.
	</p>
	{#snippet actions()}
		<Button
			variant="ghost"
			disabled={ctx.deletingSubscription}
			onclick={() => (ctx.pendingSubscriptionDelete = null)}
		>
			Отмена
		</Button>
		<Button
			variant="danger"
			disabled={ctx.deletingSubscription}
			loading={ctx.deletingSubscription}
			onclick={ctx.confirmSubscriptionDelete}
		>
			{ctx.deletingSubscription ? 'Удаляем...' : 'Удалить'}
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
		subjectLabel="туннель"
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
