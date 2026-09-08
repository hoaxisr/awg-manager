<script lang="ts">
	import type { WireguardServerPeer } from '$lib/types';
	import { Modal, Button } from '$lib/components/ui';
	import { protocols, calcTotalSize, MAX_SIGNATURE_BYTES, type ProtocolKey, type SignaturePackets } from '$lib/utils/protocols';
	import PeerSignatureEditor from './PeerSignatureEditor.svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { servers } from '$lib/stores/servers';

	interface Props {
		open: boolean;
		serverId: string;
		peer: WireguardServerPeer;
		onclose: () => void;
		onUpdated: () => void;
	}

	let { open = $bindable(false), serverId, peer, onclose, onUpdated }: Props = $props();

	let description = $state('');
	let tunnelIP = $state('');
	let saving = $state(false);
	let wasOpen = $state(false);
	let sigProfile = $state<ProtocolKey | ''>('');
	let sigPackets = $state<SignaturePackets>({ i1: '', i2: '', i3: '', i4: '', i5: '' });

	function peerTunnelIP(p: WireguardServerPeer): string {
		const raw = p.allowedIPs?.find((ip) => ip.includes('/32')) || p.allowedIPs?.[0] || '';
		if (!raw) return '';
		if (raw.includes('/')) return raw;
		return `${raw}/32`;
	}

	function peerProfile(): ProtocolKey | '' {
		const p = peer.signatureProfile ?? '';
		return p in protocols ? (p as ProtocolKey) : '';
	}

	function peerPackets(): SignaturePackets {
		return {
			i1: peer.i1 ?? '',
			i2: peer.i2 ?? '',
			i3: peer.i3 ?? '',
			i4: peer.i4 ?? '',
			i5: peer.i5 ?? '',
		};
	}

	$effect(() => {
		if (open && !wasOpen) {
			description = peer.description;
			tunnelIP = peerTunnelIP(peer);
			sigProfile = peerProfile();
			sigPackets = peerPackets();
		}
		wasOpen = open;
	});

	const sigDirty = $derived.by(() => {
		const orig = peerPackets();
		return (
			sigProfile !== peerProfile() ||
			(['i1', 'i2', 'i3', 'i4', 'i5'] as const).some((k) => sigPackets[k] !== orig[k])
		);
	});

	const sigOver = $derived(calcTotalSize(sigPackets) > MAX_SIGNATURE_BYTES);

	async function handleSave() {
		saving = true;
		try {
			const fresh = await api.updateSystemServerPeer(serverId, peer.publicKey, {
				description,
				tunnelIP,
				signature: sigDirty ? { profile: sigProfile, ...sigPackets } : undefined,
			});
			servers.applyMutationResponse(fresh);
			notifications.success('Клиент обновлён');
			onclose();
			onUpdated();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка сохранения');
		} finally {
			saving = false;
		}
	}
</script>

<Modal {open} title="Редактировать клиента" size="sm" {onclose}>
	<div class="form-fields">
		<div class="form-group">
			<label class="label" for="esp-desc">Имя / описание</label>
			<input type="text" id="esp-desc" class="input" bind:value={description} />
		</div>
		<div class="form-group">
			<label class="label" for="esp-ip">Tunnel IP (CIDR)</label>
			<input type="text" id="esp-ip" class="input" bind:value={tunnelIP} />
		</div>
		<!-- Сигнатуре негде жить без локального ключа клиента: пир заведён вне
		     AWG Manager, и бэкенд такую правку отвергает (NO_PEER_SECRET). -->
		{#if peer.confAvailable}
			<PeerSignatureEditor
				profile={sigProfile}
				packets={sigPackets}
				onchange={(n) => { sigProfile = n.profile; sigPackets = n.packets; }}
			/>
		{/if}
	</div>

	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={onclose}>Отмена</Button>
		<Button variant="primary" size="md" onclick={handleSave} loading={saving} disabled={saving || sigOver}>
			Сохранить
		</Button>
	{/snippet}
</Modal>

<style>
	.form-fields {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.form-group {
		display: flex;
		flex-direction: column;
		gap: 0.375rem;
	}

	.label {
		font-size: 0.8125rem;
		font-weight: 500;
		color: var(--text-secondary);
	}

	.input {
		padding: 8px 12px;
		font-size: 13px;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: 6px;
		color: var(--text-primary);
	}

	.input:focus {
		outline: none;
		border-color: var(--accent);
	}
</style>
