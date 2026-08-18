<script lang="ts">
	// Панель ссылок абоненту (ia.md §3.3, LK-01..LK-11). Ссылка выдаётся на
	// пароль абонента; peer и VK-хеши правятся здесь же и пересобирают ссылку.
	import { onMount } from 'svelte';
	import { Button, Input, IconButton } from '$lib/components/ui';
	import { X } from 'lucide-svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { errText } from '$lib/utils/errorMessage';
	import type { WdttPanelUserEntry, WdttServerConfig } from '$lib/types';
	import LinkBox from './LinkBox.svelte';
	import { wdttServerPorts } from './shareConfig';

	interface Props {
		serverId: string;
		serverName: string;
		/** Конфиг сервера с бэкенда: в нём живут peer и VK-хеши ссылки. */
		server: WdttServerConfig;
		user: WdttPanelUserEntry;
		onclose: () => void;
	}

	let { serverId, serverName, server, user, onclose }: Props = $props();

	// WS-16: у абонента есть свой VK-хеш — подставляем его, иначе берём хеши
	// сервера. Обратно в конфиг сервера хеши абонента не пишутся (W-33).
	// svelte-ignore state_referenced_locally -- стартовые значения панели
	let peer = $state(server.linkPeer ?? '');
	// svelte-ignore state_referenced_locally
	let vkHashes = $state(user.vkHash?.trim() || (server.linkVkHashes ?? ''));

	// Проп `server` после нашего же PUT не перечитывается: что в конфиге уже
	// лежит, помним сами — иначе каждый `generate()` шлёт тот же PUT заново.
	// svelte-ignore state_referenced_locally -- снимок на момент открытия панели
	let persistedPeer = server.linkPeer ?? '';

	let link = $state('');
	let linkQwdtt = $state('');
	let busy = $state(false);
	let wanBusy = $state(false);

	const dtlsPort = $derived(wdttServerPorts(server)[0]?.port ?? 0);

	onMount(() => {
		void generate();
	});

	async function generate() {
		if (busy) return;
		busy = true;
		try {
			let peerParam = peer.trim();
			if (peerParam && !peerParam.includes(':') && dtlsPort) {
				peerParam = `${peerParam}:${dtlsPort}`;
			}
			const hashes = vkHashes
				.split(/[,;\s]+/)
				.map((h) => h.trim())
				.filter(Boolean);
			const res = await api.generateWdttServerLink(serverId, {
				peer: peerParam || undefined,
				vkHashes: hashes.length ? hashes : undefined,
				name: serverName,
				password: user.password,
			});
			link = res.link ?? '';
			linkQwdtt = res.linkQwdtt ?? '';
			await persistPeer(res.peer || peerParam);
		} catch (e) {
			notifications.error(errText(e));
		} finally {
			busy = false;
		}
	}

	/**
	 * peer ссылки живёт в конфиге сервера, чтобы ссылка восстанавливалась после
	 * перезагрузки страницы. VK-хеши абонента — его личные: в серверные
	 * параметры они не идут (W-33).
	 */
	async function persistPeer(value: string) {
		const next = value.trim();
		if (!next || next === persistedPeer) return;
		try {
			await api.updateWdttServerInstance(serverId, { ...server, linkPeer: next });
			persistedPeer = next;
		} catch {
			/* не критично: ссылка уже показана, peer допишется при сохранении */
		}
	}

	async function fillWan() {
		wanBusy = true;
		try {
			const ip = await api.getWANIP();
			peer = ip.includes(':') || !dtlsPort ? ip : `${ip}:${dtlsPort}`;
			await generate();
		} catch (e) {
			notifications.error(errText(e));
		} finally {
			wanBusy = false;
		}
	}
</script>

<div class="link-panel">
	<div class="panel-head">
		<div class="panel-titles">
			<span class="panel-title">Ссылки абоненту</span>
			<span class="panel-sub">Абонент: {user.comment || user.password}</span>
		</div>
		<IconButton size="sm" ariaLabel="Закрыть" onclick={onclose}>
			<X size={14} />
		</IconButton>
	</div>

	<div class="panel-fields">
		<div class="field-with-btn">
			<Input
				label="Адрес сервера для абонента"
				bind:value={peer}
				onchange={() => generate()}
				fullWidth
			/>
			<Button variant="secondary" size="sm" loading={wanBusy} onclick={fillWan}>WAN IP</Button>
		</div>
		<Input
			label="VK-хеши"
			hint="Маскировка, а не пароль подключения"
			bind:value={vkHashes}
			onchange={() => generate()}
			fullWidth
		/>
	</div>

	{#if linkQwdtt}
		<LinkBox link={linkQwdtt} title="Для приложения на телефоне" />
	{/if}
	{#if link}
		<LinkBox {link} title="Для клиента на роутере" />
	{/if}
</div>

<style>
	.link-panel {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
		margin-top: 0.75rem;
		padding: 0.75rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
	}

	.panel-head {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 0.5rem;
	}

	.panel-titles {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
		min-width: 0;
	}

	.panel-title {
		font-size: 0.875rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.panel-sub {
		font-size: 0.75rem;
		color: var(--color-text-secondary);
	}

	.panel-fields {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(14rem, 1fr));
		gap: 0.5rem;
		align-items: end;
	}

	.field-with-btn {
		display: flex;
		align-items: flex-end;
		gap: 0.375rem;
		min-width: 0;
	}
</style>
