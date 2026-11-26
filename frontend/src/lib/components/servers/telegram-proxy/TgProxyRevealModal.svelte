<script lang="ts">
	// «Подключить Telegram»: ссылки, QR и секрет выбранного пользователя.
	// Раскрытие секрета и его правка живут здесь вместе с кэшем раскрытий:
	// это одно целое, владелец карточки дёргает только show/invalidate/reset.
	import { m } from '$lib/i18n';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { renderCrispQrDataUrl } from '$lib/utils/qrSvg';
	import type {
		TgWebProxyConfig,
		TgWebProxyStatus,
		TgWebProxyRevealData,
		TelegramUser,
		TgUserRevealData,
		WebProxyLink,
	} from '$lib/types';
	import { Button, Modal } from '$lib/components/ui';
	import { EXTERNAL_ACCESS_LABELS } from '../external-access/connectionLabels';
	import TgProxyRevealLinks from './TgProxyRevealLinks.svelte';
	import { isValidSecretInput } from './tgProxyModel';
	import { RefreshCw, Smartphone, Globe, Trash2 } from 'lucide-svelte';

	interface Props {
		users: TelegramUser[];
		config: TgWebProxyConfig | null;
		expectsMt: boolean;
		expectsWeb: boolean;
		/** Идёт удаление пользователя — кнопка удаления в подвале гаснет. */
		deleteBusy: boolean;
		ondeleteuser: (user: TelegramUser) => void;
		/** Секрет пользователя изменён: новый статус сервера. */
		onsecretchanged: (status: TgWebProxyStatus) => void | Promise<void>;
	}

	let { users, config, expectsMt, expectsWeb, deleteBusy, ondeleteuser, onsecretchanged }: Props = $props();

	let shareTab = $state<'webproxy' | 'mtproxy'>('mtproxy');
	let selectedWebProxyId = $state<string>('');
	let selectedRevealUserId = $state<string>('');

	let revealOpen = $state(false);
	let revealLoading = $state(false);
	let revealData = $state<TgWebProxyRevealData | null>(null);
	let revealQrDataUrl = $state('');
	let qrGenerating = $state(false);
	let revealSecretInput = $state('');
	let revealSecretSaving = $state(false);
	// Reveal returns a single user and the backend allows one disclosure of a
	// given secret per 5 s. Responses are reused for that window only — also
	// across close/reopen of the modal and user switching — so the UI never
	// asks again for a secret it has just received. The TTL is counted from
	// receipt (never refreshed on reuse), which is not earlier than the
	// server-side timestamp, so an expired entry is always re-fetchable.
	const REVEAL_REUSE_MS = 5000;
	const revealCache = new Map<string, { data: TgWebProxyRevealData; at: number }>();

	// Any user, secret or config change can alter what a cached reveal shows
	// (secret, links, enabled flag), so every successful mutation drops it.
	export function invalidate() {
		revealCache.clear();
	}

	function rememberReveal(data: TgWebProxyRevealData | null) {
		if (data?.user_id) revealCache.set(data.user_id, { data, at: Date.now() });
	}

	function cachedReveal(userId: string): TgWebProxyRevealData | null {
		const hit = revealCache.get(userId);
		if (!hit) return null;
		if (Date.now() - hit.at >= REVEAL_REUSE_MS) {
			revealCache.delete(userId);
			return null;
		}
		return hit.data;
	}

	function syncRevealSecretInput() {
		revealSecretInput = getActiveUserReveal()?.secret || '';
	}

	export async function show(preferredTab?: 'mtproxy' | 'webproxy', targetId?: string, userId?: string) {
		if (revealLoading) return;
		revealLoading = true;
		revealOpen = true;
		if (preferredTab) {
			shareTab = preferredTab;
		} else {
			shareTab = expectsMt ? 'mtproxy' : 'webproxy';
		}
		// A remembered selection is only used while that user still exists; no
		// 'default' fallback: with zero users the backend answers an empty
		// reveal (200) instead of 404 for a non-existent "default" user.
		const rememberedUserId = users.some((u) => u.id === selectedRevealUserId) ? selectedRevealUserId : '';
		const targetUserId = userId || rememberedUserId || users[0]?.id || '';
		selectedRevealUserId = targetUserId;
		try {
			const cached = targetUserId ? cachedReveal(targetUserId) : null;
			revealData = cached ?? (await api.revealTgWebProxySecret(targetUserId || undefined));
			if (revealData?.user_id) {
				selectedRevealUserId = revealData.user_id;
			}
			if (!cached) rememberReveal(revealData);
			syncRevealSecretInput();
			if (targetId) {
				selectedWebProxyId = targetId;
			} else if (!selectedWebProxyId && revealData?.web_proxies && revealData.web_proxies.length > 0) {
				selectedWebProxyId = revealData.web_proxies[0].id;
			}
			const active = getActiveUserReveal();
			const hasMt = expectsMt && Boolean(active?.mtproxy_url || revealData?.direct_host || !expectsWeb);
			const hasWeb = expectsWeb || Boolean((active?.web_proxies || revealData?.web_proxies)?.length);
			if (shareTab === 'mtproxy' && !hasMt && hasWeb) {
				shareTab = 'webproxy';
			} else if (shareTab === 'webproxy' && !hasWeb && hasMt) {
				shareTab = 'mtproxy';
			}
			await updateRevealQR();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_tg_reveal_error());
			close();
		} finally {
			revealLoading = false;
		}
	}

	export function close() {
		revealOpen = false;
		revealData = null;
		revealQrDataUrl = '';
		revealSecretInput = '';
		selectedRevealUserId = '';
	}

	/** Сброс сервера: всё раскрытое забыть и окно закрыть. */
	export function reset() {
		invalidate();
		revealData = null;
		revealQrDataUrl = '';
		revealOpen = false;
	}

	/** Пользователь удалён (список users уже обновлён): показать оставшегося или закрыть. */
	export async function afterUserDeleted(deletedId: string) {
		if (!revealOpen || selectedRevealUserId !== deletedId) return;
		if (users.length > 0) {
			selectedRevealUserId = users[0].id;
			revealData = await api.revealTgWebProxySecret(selectedRevealUserId);
			rememberReveal(revealData);
			syncRevealSecretInput();
			await updateRevealQR();
		} else {
			close();
		}
	}

	function getActiveUserReveal(): TgUserRevealData | TgWebProxyRevealData | null {
		if (!revealData) return null;
		if (revealData.users && revealData.users.length > 0) {
			if (selectedRevealUserId) {
				const found = revealData.users.find((u) => u.id === selectedRevealUserId);
				if (found) return found;
			}
			return revealData.users[0];
		}
		return revealData;
	}

	function selectRevealUser(userId: string) {
		if (userId === revealData?.user_id) return;
		void show(shareTab, selectedWebProxyId || undefined, userId);
	}

	function getActiveWebProxyLink(): WebProxyLink | null {
		const activeUser = getActiveUserReveal();
		const proxies = activeUser?.web_proxies || revealData?.web_proxies;
		if (!proxies || proxies.length === 0) return null;
		if (selectedWebProxyId) {
			const found = proxies.find((p) => p.id === selectedWebProxyId);
			if (found) return found;
		}
		return proxies[0];
	}

	async function updateRevealQR() {
		const activeUser = getActiveUserReveal();
		if (!activeUser) return;
		let url = '';
		if (shareTab === 'webproxy') {
			const activeEp = getActiveWebProxyLink();
			url = activeEp ? activeEp.tg_url || activeEp.bridge_url : activeUser.tg_url || activeUser.bridge_url;
		} else {
			url = activeUser.mtproxy_url;
		}
		if (!url) {
			revealQrDataUrl = '';
			return;
		}
		if (!revealQrDataUrl) {
			qrGenerating = true;
		}
		try {
			revealQrDataUrl = await renderCrispQrDataUrl(url);
		} catch {
			revealQrDataUrl = '';
		} finally {
			qrGenerating = false;
		}
	}

	function selectRevealEndpoint(epId: string) {
		selectedWebProxyId = epId;
		void updateRevealQR();
	}

	function setShareTab(tab: 'webproxy' | 'mtproxy') {
		shareTab = tab;
		if (revealOpen && revealData) {
			void updateRevealQR();
		}
	}

	function getActiveRevealUserId(): string {
		if (selectedRevealUserId) return selectedRevealUserId;
		const active = getActiveUserReveal();
		if (active && 'id' in active && active.id) return active.id;
		if (active && 'user_id' in active && active.user_id) return active.user_id;
		return users[0]?.id || '';
	}

	/** Сохранить введённый секрет или перегенерировать его. */
	async function updateUserSecret(regenerate: boolean) {
		const targetId = getActiveRevealUserId();
		if (!targetId) return;
		const cleaned = revealSecretInput.trim();
		if (!regenerate && !isValidSecretInput(cleaned)) {
			notifications.error(m.tg_user_secret_error());
			return;
		}
		revealSecretSaving = true;
		try {
			const status = await api.tgWebProxyAction(
				'update_user',
				regenerate ? { user_id: targetId, regenerate_secret: true } : { user_id: targetId, secret: cleaned }
			);
			invalidate();
			revealData = await api.revealTgWebProxySecret(targetId);
			rememberReveal(revealData);
			syncRevealSecretInput();
			await updateRevealQR();
			notifications.success(regenerate ? m.tg_user_secret_regenerated() : m.tg_user_secret_saved());
			await onsecretchanged(status);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.tg_user_secret_error());
		} finally {
			revealSecretSaving = false;
		}
	}

	async function copy(text: string, label: string) {
		if (await copyToClipboard(text)) {
			notifications.success(m.servers_tg_copied({ label }));
		}
	}
</script>

<Modal bind:open={revealOpen} title={m.servers_tg_reveal_title()} onclose={close} size="lg">
	{#if revealLoading}
		<div class="reveal-loading">
			<RefreshCw size={24} class="spin mb-2" />
			<span>{m.servers_tg_reveal_loading()}</span>
		</div>
	{:else if revealData}
		{@const activeUser = getActiveUserReveal()}
		{@const showMtTab = expectsMt && Boolean(activeUser?.mtproxy_url || revealData.direct_host || !expectsWeb)}
		{@const showWebTab = expectsWeb || Boolean((activeUser?.web_proxies || revealData.web_proxies)?.length)}
		<div class="reveal-content">
			{#if users.length > 0}
				<div class="reveal-user-selector">
					<label for="tg-reveal-user-select" class="ep-selector-label">{m.servers_tg_reveal_user_label()}</label>
					<select
						id="tg-reveal-user-select"
						value={revealData.user_id || selectedRevealUserId || users[0].id}
						onchange={(e) => selectRevealUser((e.currentTarget as HTMLSelectElement).value)}
						class="select-input reveal-user-select"
					>
						{#each users as u (u.id)}
							<option value={u.id}>{u.enabled ? u.remark : m.servers_tg_reveal_user_disabled({ name: u.remark })}</option>
						{/each}
					</select>
				</div>
			{/if}

			<div class="reveal-tabs">
				{#if showMtTab}
					<button
						type="button"
						class="reveal-tab"
						class:active={shareTab === 'mtproxy'}
						onclick={() => setShareTab('mtproxy')}
					>
						<Smartphone size={14} class="mr-1" />
						{EXTERNAL_ACCESS_LABELS.telegramMtproto}
					</button>
				{/if}
				{#if showWebTab}
					<button
						type="button"
						class="reveal-tab"
						class:active={shareTab === 'webproxy'}
						onclick={() => setShareTab('webproxy')}
					>
						<Globe size={14} class="mr-1" />
						{EXTERNAL_ACCESS_LABELS.telegramWeb}
					</button>
				{/if}
			</div>

			<div class="reveal-body">
				<!-- QR Code Column -->
				<div class="reveal-qr-col">
					{#if qrGenerating}
						<div class="qr-placeholder">{m.servers_tg_qr_generating()}</div>
					{:else if revealQrDataUrl}
						<div class="qr-wrapper">
							<img src={revealQrDataUrl} alt={m.servers_tg_qr_alt()} class="qr-img" />
						</div>
						<div class="qr-hint">
							{#if shareTab === 'mtproxy'}
								{m.servers_tg_qr_hint_mtproxy()}
							{:else}
								{m.servers_tg_qr_hint_web()}
							{/if}
						</div>
					{/if}
				</div>

				<TgProxyRevealLinks
					{shareTab}
					{revealData}
					{activeUser}
					activeEp={getActiveWebProxyLink()}
					{selectedWebProxyId}
					{config}
					bind:secretInput={revealSecretInput}
					secretSaving={revealSecretSaving}
					onselectendpoint={selectRevealEndpoint}
					onregenerate={() => updateUserSecret(true)}
					onsavesecret={() => updateUserSecret(false)}
					oncopy={copy}
				/>
			</div>
		</div>
	{/if}

	{#snippet actions()}
		{@const activeUserForAction = getActiveUserReveal()}
		{#if activeUserForAction}
			{@const targetUserObj = users.find((u) => u.id === (activeUserForAction as TgUserRevealData).id) || users[0]}
			{#if targetUserObj}
				<Button
					variant="danger"
					onclick={() => ondeleteuser(targetUserObj)}
					disabled={revealSecretSaving || deleteBusy}
				>
					<Trash2 size={14} class="mr-1" />
					{m.tg_user_delete_btn()}
				</Button>
			{/if}
		{/if}
		<Button variant="secondary" onclick={close}>{m.common_close()}</Button>
	{/snippet}
</Modal>

<style>
	.reveal-loading {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		padding: 2.5rem;
		color: var(--color-text-muted);
	}

	.reveal-content {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.reveal-user-selector {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 12px;
		padding: 8px 12px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
	}

	.ep-selector-label {
		font-size: 11px;
		font-weight: 500;
		color: var(--color-text-muted);
		text-transform: uppercase;
		letter-spacing: 0.05em;
	}

	.select-input {
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 6px 10px;
		font-size: 13px;
		color: var(--color-text-primary);
	}

	.reveal-user-select {
		max-width: 260px;
	}

	.reveal-tabs {
		display: flex;
		gap: 8px;
		border-bottom: 1px solid var(--color-border);
		padding-bottom: 8px;
		flex-wrap: wrap;
	}

	.reveal-tab {
		display: flex;
		align-items: center;
		padding: 6px 12px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-secondary);
		font-size: 12px;
		cursor: pointer;
	}

	.reveal-tab.active {
		background: var(--color-accent-tint);
		color: var(--color-accent);
		border-color: var(--color-accent-border);
		font-weight: 600;
	}

	.reveal-body {
		display: grid;
		grid-template-columns: 230px 1fr;
		gap: 1.25rem;
		align-items: start;
	}

	@media (max-width: 640px) {
		.reveal-body {
			grid-template-columns: 1fr;
		}
	}

	.reveal-qr-col {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 8px;
	}

	.qr-wrapper {
		background: white;
		padding: 8px;
		border-radius: var(--radius-sm);
		box-shadow: 0 1px 3px rgba(0, 0, 0, 0.2);
	}

	.qr-img {
		width: 210px;
		height: 210px;
		image-rendering: crisp-edges;
		display: block;
	}

	.qr-placeholder {
		width: 210px;
		height: 210px;
		display: flex;
		align-items: center;
		justify-content: center;
		background: var(--color-bg-secondary);
		border: 1px dashed var(--color-border);
		border-radius: var(--radius-sm);
		color: var(--color-text-muted);
		font-size: 12px;
	}

	.qr-hint {
		font-size: 11px;
		color: var(--color-text-muted);
		text-align: center;
		line-height: 1.4;
	}
</style>
