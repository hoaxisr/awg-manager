<script lang="ts">
	import Modal from '$lib/components/ui/Modal.svelte';
	import type {
		SingboxRouterDNSServer,
		SingboxRouterDNSType,
		SingboxRouterDNSStrategy,
	} from '$lib/types';
	import type { OutboundGroup } from './outboundOptions';

	interface Props {
		server?: SingboxRouterDNSServer;
		servers: SingboxRouterDNSServer[];
		outboundOptions: OutboundGroup[];
		onClose: () => void;
		onSave: (server: SingboxRouterDNSServer) => Promise<void> | void;
	}
	let { server, servers, outboundOptions, onClose, onSave }: Props = $props();

	// svelte-ignore state_referenced_locally
	let tag = $state(server?.tag ?? '');
	// svelte-ignore state_referenced_locally
	let type = $state<SingboxRouterDNSType>(server?.type ?? 'udp');
	// svelte-ignore state_referenced_locally
	let serverAddr = $state(server?.server ?? '');
	// svelte-ignore state_referenced_locally
	let serverPort = $state<number | ''>(server?.server_port ?? '');
	// svelte-ignore state_referenced_locally
	let path = $state(server?.path ?? '');
	// svelte-ignore state_referenced_locally
	let detour = $state(server?.detour ?? '');
	// svelte-ignore state_referenced_locally
	let strategy = $state<SingboxRouterDNSStrategy>(server?.domain_strategy ?? '');
	// svelte-ignore state_referenced_locally
	let resolverEnabled = $state(server?.domain_resolver != null);
	// svelte-ignore state_referenced_locally
	let resolverServer = $state(server?.domain_resolver?.server ?? '');
	// svelte-ignore state_referenced_locally
	let resolverStrategy = $state<SingboxRouterDNSStrategy>(server?.domain_resolver?.strategy ?? '');

	let busy = $state(false);
	let error = $state('');

	const needsResolver = $derived(type !== 'udp' && !isIPLiteral(serverAddr));
	const availableResolvers = $derived(servers.filter((s) => s.tag !== tag).map((s) => s.tag));

	function isIPLiteral(s: string): boolean {
		return /^(\d{1,3}\.){3}\d{1,3}$/.test(s) || s.includes(':');
	}

	async function save(): Promise<void> {
		busy = true;
		error = '';
		try {
			if (!tag.trim()) { error = 'Tag обязателен'; busy = false; return; }
			if (!serverAddr.trim()) { error = 'Server обязателен'; busy = false; return; }
			if (resolverEnabled && !resolverServer) { error = 'Укажите domain_resolver'; busy = false; return; }

			const built: SingboxRouterDNSServer = {
				tag: tag.trim(),
				type,
				server: serverAddr.trim(),
			};
			if (serverPort !== '' && Number(serverPort) > 0) built.server_port = Number(serverPort);
			if (path.trim()) built.path = path.trim();
			if (detour) built.detour = detour;
			if (strategy) built.domain_strategy = strategy;
			if (resolverEnabled && resolverServer) {
				built.domain_resolver = { server: resolverServer };
				if (resolverStrategy) built.domain_resolver.strategy = resolverStrategy;
			}

			await onSave(built);
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = false;
		}
	}
</script>

<Modal open onclose={onClose} title={server ? 'Редактировать DNS сервер' : 'Новый DNS сервер'}>
	<div class="form">
		<label class="field">
			<div class="lbl">Tag <span class="req">*</span></div>
			<input bind:value={tag} placeholder="bootstrap, cloudflare, vpn-dns" />
		</label>

		<label class="field">
			<div class="lbl">Type <span class="req">*</span></div>
			<select bind:value={type}>
				<option value="udp">UDP (обычный DNS)</option>
				<option value="tls">DoT (DNS over TLS)</option>
				<option value="https">DoH (DNS over HTTPS)</option>
				<option value="quic">DoQ (DNS over QUIC)</option>
				<option value="h3">DoH3</option>
			</select>
		</label>

		<label class="field">
			<div class="lbl">Server <span class="req">*</span></div>
			<input bind:value={serverAddr} placeholder={type === 'udp' ? '1.1.1.1' : 'cloudflare-dns.com'} />
		</label>

		<div class="row-2">
			<label class="field">
				<div class="lbl">Server port</div>
				<input type="number" bind:value={serverPort} placeholder={type === 'udp' ? '53' : type === 'https' ? '443' : '853'} />
			</label>
			{#if type === 'https'}
				<label class="field">
					<div class="lbl">Path</div>
					<input bind:value={path} placeholder="/dns-query" />
				</label>
			{/if}
		</div>

		<div class="section-label">Маршрутизация</div>

		<label class="field">
			<div class="lbl">Detour (outbound)</div>
			<select bind:value={detour}>
				<option value="">— через route (по умолчанию) —</option>
				{#each outboundOptions as group}
					<optgroup label={group.group}>
						{#each group.items as item}
							<option value={item.value}>{item.label}</option>
						{/each}
					</optgroup>
				{/each}
			</select>
			<div class="hint">
				Через какой outbound сам сервер отправляет запросы. <code>direct</code> — через провайдера,
				выбранный туннель — через VPN (шифрованный DNS без утечек).
			</div>
		</label>

		<label class="field">
			<div class="lbl">Стратегия (IPv4/IPv6)</div>
			<select bind:value={strategy}>
				<option value="">— default —</option>
				<option value="ipv4_only">ipv4_only</option>
				<option value="ipv6_only">ipv6_only</option>
				<option value="prefer_ipv4">prefer_ipv4</option>
				<option value="prefer_ipv6">prefer_ipv6</option>
			</select>
		</label>

		{#if type !== 'udp'}
			<div class="section-label">Bootstrap resolver (для домена сервера)</div>
			<label class="toggle">
				<input type="checkbox" bind:checked={resolverEnabled} />
				Использовать другой DNS для резолва домена этого сервера
			</label>
			{#if needsResolver && !resolverEnabled}
				<div class="warn">
					У <code>{type}</code> сервера адрес — доменное имя. Без bootstrap resolver sing-box не сможет его резолвить.
				</div>
			{/if}
			{#if resolverEnabled}
				<div class="row-2">
					<label class="field">
						<div class="lbl">Resolver server (tag)</div>
						<select bind:value={resolverServer}>
							<option value="">— выберите —</option>
							{#each availableResolvers as t}
								<option value={t}>{t}</option>
							{/each}
						</select>
					</label>
					<label class="field">
						<div class="lbl">Resolver strategy</div>
						<select bind:value={resolverStrategy}>
							<option value="">— default —</option>
							<option value="ipv4_only">ipv4_only</option>
							<option value="ipv6_only">ipv6_only</option>
							<option value="prefer_ipv4">prefer_ipv4</option>
							<option value="prefer_ipv6">prefer_ipv6</option>
						</select>
					</label>
				</div>
			{/if}
		{/if}

		{#if error}<div class="error">{error}</div>{/if}

		<div class="actions">
			<button class="btn btn-secondary" onclick={onClose} type="button">Отмена</button>
			<button class="btn btn-primary" onclick={save} disabled={busy} type="button">Сохранить</button>
		</div>
	</div>
</Modal>

<style>
	.form {
		display: grid;
		gap: 0.6rem;
		min-width: 460px;
	}
	.section-label {
		font-size: 0.7rem;
		text-transform: uppercase;
		letter-spacing: 0.5px;
		color: var(--muted-text);
		margin: 0.5rem 0 0.1rem;
		padding-top: 0.5rem;
		border-top: 1px solid var(--border);
	}
	.field {
		display: grid;
		gap: 0.25rem;
	}
	.lbl {
		font-size: 0.75rem;
		color: var(--muted-text);
	}
	.req {
		color: var(--danger, #dc2626);
	}
	.field input,
	.field select {
		background: var(--bg);
		border: 1px solid var(--border);
		padding: 0.4rem 0.6rem;
		border-radius: 4px;
		color: var(--text);
		font-family: ui-monospace, monospace;
		font-size: 0.85rem;
		box-sizing: border-box;
		width: 100%;
	}
	.hint {
		font-size: 0.75rem;
		color: var(--muted-text);
		line-height: 1.4;
	}
	.hint code {
		background: var(--bg);
		padding: 0.05rem 0.25rem;
		border-radius: 2px;
		font-family: ui-monospace, monospace;
	}
	.row-2 {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 0.5rem;
	}
	.toggle {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		font-size: 0.85rem;
		color: var(--text);
		cursor: pointer;
	}
	.warn {
		padding: 0.5rem 0.7rem;
		background: rgba(224, 175, 104, 0.12);
		border-left: 3px solid var(--warning, #e0af68);
		border-radius: 3px;
		font-size: 0.8rem;
		color: var(--muted-text);
		line-height: 1.4;
	}
	.warn code {
		background: var(--bg);
		padding: 0.05rem 0.25rem;
		border-radius: 2px;
		font-family: ui-monospace, monospace;
	}
	.error {
		color: var(--danger, #dc2626);
		font-size: 0.85rem;
	}
	.actions {
		display: flex;
		justify-content: flex-end;
		gap: 0.5rem;
	}
</style>
