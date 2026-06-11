<script lang="ts">
	import { Button, Dropdown, type DropdownOption } from '$lib/components/ui';
	import { OctagonAlert } from 'lucide-svelte';
	import SingboxSettingsModal from './SingboxSettingsModal.svelte';
	import type {
		SingboxRouterDNSServer,
		SingboxRouterDNSType,
		SingboxRouterDNSStrategy,
	} from '$lib/types';
	import type { OutboundGroup } from './outboundOptions';
	import {
		DNS_DIRECT_SERVER_TAG,
		getDnsDirectLegacyDetour,
		normalizeDnsServerDetour,
		sanitizeDnsServerForApi,
	} from '$lib/utils/dnsServerDetour';
	import { dnsServerDetourDisplay } from '$lib/components/sb-router/dnsServerDetourDisplay';

	interface Props {
		server?: SingboxRouterDNSServer;
		servers: SingboxRouterDNSServer[];
		outboundOptions: OutboundGroup[];
		onClose: () => void;
		onSave: (server: SingboxRouterDNSServer) => Promise<void> | void;
	}
	let { server, servers, outboundOptions, onClose, onSave }: Props = $props();

	const TYPE_OPTIONS: DropdownOption<SingboxRouterDNSType>[] = [
		{ value: 'udp', label: 'UDP (обычный DNS)' },
		{ value: 'tls', label: 'DoT (DNS over TLS)' },
		{ value: 'https', label: 'DoH (DNS over HTTPS)' },
		{ value: 'quic', label: 'DoQ (DNS over QUIC)' },
		{ value: 'h3', label: 'DoH3' },
		{ value: 'local', label: 'Local (системный resolver роутера)' },
	];

	const STRATEGY_OPTIONS: DropdownOption<SingboxRouterDNSStrategy>[] = [
		{ value: '', label: '— default —' },
		{ value: 'ipv4_only', label: 'ipv4_only' },
		{ value: 'ipv6_only', label: 'ipv6_only' },
		{ value: 'prefer_ipv4', label: 'prefer_ipv4' },
		{ value: 'prefer_ipv6', label: 'prefer_ipv6' },
	];

	const detourOptions = $derived<DropdownOption[]>([
		{ value: '', label: 'Напрямую' },
		...outboundOptions.flatMap((g) =>
			g.items
				.filter((i) => i.value !== 'direct')
				.map((i) => ({ value: i.value, label: i.label, group: g.group })),
		),
	]);

	const dnsDirectDetourOptions = $derived<DropdownOption[]>([
		{ value: '', label: 'Напрямую' },
	]);

	const legacyDnsDirectDetour = $derived(server ? getDnsDirectLegacyDetour(server) : null);

	const legacyDnsDirectDisplay = $derived.by(() => {
		if (!server || !legacyDnsDirectDetour) return null;
		return dnsServerDetourDisplay(server, [], outboundOptions);
	});

	const dnsDirectLegacyDetourOptions = $derived<DropdownOption[]>(
		legacyDnsDirectDetour && legacyDnsDirectDisplay
			? [{ value: legacyDnsDirectDetour, label: legacyDnsDirectDisplay.label }]
			: dnsDirectDetourOptions,
	);

	// svelte-ignore state_referenced_locally
	let tag = $state(server?.tag ?? '');
	const isManagedDnsDirect = $derived(tag.trim() === DNS_DIRECT_SERVER_TAG);
	// svelte-ignore state_referenced_locally
	let type = $state<SingboxRouterDNSType>(server?.type ?? 'udp');
	// svelte-ignore state_referenced_locally
	let serverAddr = $state(server?.server ?? '');
	// svelte-ignore state_referenced_locally
	let serverPort = $state<number | ''>(server?.server_port ?? '');
	// svelte-ignore state_referenced_locally
	let path = $state(server?.path ?? '');
	// svelte-ignore state_referenced_locally
	let detour = $state(
		server?.tag === DNS_DIRECT_SERVER_TAG
			? ''
			: (normalizeDnsServerDetour(server?.detour) ?? ''),
	);
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

	// Snapshot initial state for isDirty detection
	let initialTag = $state('');
	let initialType = $state<SingboxRouterDNSType>('udp');
	let initialServerAddr = $state('');
	let initialServerPort = $state<number | ''>('');
	let initialPath = $state('');
	let initialDetour = $state('');
	let initialStrategy = $state<SingboxRouterDNSStrategy>('');
	let initialResolverEnabled = $state(false);
	let initialResolverServer = $state('');
	let initialResolverStrategy = $state<SingboxRouterDNSStrategy>('');

	// Initialize snapshot when modal opens
	$effect(() => {
		if (server) {
			initialTag = server.tag;
			initialType = server.type;
			initialServerAddr = server.server;
			initialServerPort = server.server_port ?? '';
			initialPath = server.path ?? '';
			initialDetour =
				server.tag === DNS_DIRECT_SERVER_TAG
					? ''
					: (normalizeDnsServerDetour(server.detour) ?? '');
			initialStrategy = server.domain_strategy ?? '';
			initialResolverEnabled = server.domain_resolver != null;
			initialResolverServer = server.domain_resolver?.server ?? '';
			initialResolverStrategy = server.domain_resolver?.strategy ?? '';
		} else {
			initialTag = '';
			initialType = 'udp';
			initialServerAddr = '';
			initialServerPort = '';
			initialPath = '';
			initialDetour = '';
			initialStrategy = '';
			initialResolverEnabled = false;
			initialResolverServer = '';
			initialResolverStrategy = '';
		}
	});

	const isDirty = $derived.by(() => {
		return (
			!!legacyDnsDirectDetour ||
			tag !== initialTag ||
			type !== initialType ||
			serverAddr !== initialServerAddr ||
			serverPort !== initialServerPort ||
			path !== initialPath ||
			(detour !== initialDetour && !isManagedDnsDirect) ||
			strategy !== initialStrategy ||
			resolverEnabled !== initialResolverEnabled ||
			resolverServer !== initialResolverServer ||
			resolverStrategy !== initialResolverStrategy
		);
	});

	const needsResolver = $derived(type !== 'udp' && type !== 'local' && !isIPLiteral(serverAddr));
	const availableResolvers = $derived(servers.filter((s) => s.tag !== tag).map((s) => s.tag));
	const resolverServerOptions = $derived<DropdownOption[]>([
		{ value: '', label: '— выберите —' },
		...availableResolvers.map((t) => ({ value: t, label: t })),
	]);

	function isIPLiteral(s: string): boolean {
		return /^(\d{1,3}\.){3}\d{1,3}$/.test(s) || s.includes(':');
	}

	async function save(): Promise<void> {
		busy = true;
		error = '';
		try {
			if (!tag.trim()) { error = 'Tag обязателен'; busy = false; return; }
			if (type !== 'local' && !serverAddr.trim()) { error = 'Server обязателен'; busy = false; return; }
			if (resolverEnabled && !resolverServer) { error = 'Укажите domain_resolver'; busy = false; return; }

			const built: SingboxRouterDNSServer = {
				tag: tag.trim(),
				type,
				server: type === 'local' ? '' : serverAddr.trim(),
			};
			if (type !== 'local') {
				if (serverPort !== '' && Number(serverPort) > 0) built.server_port = Number(serverPort);
				if (path.trim()) built.path = path.trim();
				if (resolverEnabled && resolverServer) {
					built.domain_resolver = { server: resolverServer };
					if (resolverStrategy) built.domain_resolver.strategy = resolverStrategy;
				}
			}
			if (type !== 'local') {
				if (strategy) built.domain_strategy = strategy;
			}

			const payload =
				type === 'local'
					? built
					: sanitizeDnsServerForApi({ ...built, detour: detour || undefined });

			await onSave(payload);
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = false;
		}
	}
</script>

<SingboxSettingsModal
	title={server ? 'Редактировать DNS сервер' : 'Новый DNS сервер'}
	onClose={onClose}
	size="lg"
	hasUnsavedChanges={() => isDirty}
>
	<div class="form">
		<div class="fields-grid">
			<label class="field">
				<div class="lbl">Tag <span class="req">*</span></div>
				<input bind:value={tag} placeholder="bootstrap, cloudflare, vpn-dns" />
			</label>

			<label class="field">
				<div class="lbl">Type <span class="req">*</span></div>
				<Dropdown bind:value={type} options={TYPE_OPTIONS} fullWidth />
			</label>

			{#if type !== 'local'}
				<label class="field span-full">
					<div class="lbl">Server <span class="req">*</span></div>
					<input bind:value={serverAddr} placeholder={type === 'udp' ? '1.1.1.1' : 'cloudflare-dns.com'} />
				</label>

				<label class="field" class:span-full={type !== 'https'}>
					<div class="lbl">Server port</div>
					<input type="number" bind:value={serverPort} placeholder={type === 'udp' ? '53' : type === 'https' ? '443' : '853'} />
				</label>

				{#if type === 'https'}
					<label class="field">
						<div class="lbl">Path</div>
						<input bind:value={path} placeholder="/dns-query" />
					</label>
				{/if}
			{:else}
				<div class="field span-full hint">
					Local-сервер резолвит через системный resolver роутера (NDMS/AdGuard/Pi-hole). Адрес и порт не требуются.
				</div>
			{/if}
		</div>

		{#if type !== 'local'}
			<section class="form-section form-section-divided">
				<div class="section-label">Маршрутизация</div>

				{#if isManagedDnsDirect}
					<label class="field">
						<div class="lbl">Detour (outbound)</div>
						<div class="detour-legacy-wrap" class:detour-legacy-invalid={!!legacyDnsDirectDetour}>
							{#if legacyDnsDirectDetour}
								<OctagonAlert size={16} strokeWidth={2} aria-hidden={true} class="detour-legacy-icon" />
							{/if}
							<div class="detour-legacy-dropdown">
								<Dropdown
									value={legacyDnsDirectDetour ?? ''}
									options={dnsDirectLegacyDetourOptions}
									disabled
									fullWidth
								/>
							</div>
						</div>
						{#if legacyDnsDirectDetour}
							<div class="warn">
								Сейчас в конфиге указан недопустимый detour. Должно быть «Напрямую» — будет
								исправлено при сохранении.
							</div>
						{:else}
							<div class="hint">
								Final DNS — запросы к резолверу идут <strong>напрямую</strong> (WAN). Ключ
								<code>detour</code> в конфиг не пишется.
							</div>
						{/if}
					</label>
				{:else}
					<label class="field">
						<div class="lbl">Detour (outbound)</div>
						<Dropdown bind:value={detour} options={detourOptions} fullWidth />
						<div class="hint">
							Через какой outbound DNS-сервер достучится до IP резолвера. «Напрямую» — с роутера
							(WAN), ключ <code>detour</code> в конфиг не пишется.
						</div>
					</label>
				{/if}

				<label class="field">
					<div class="lbl">Стратегия (IPv4/IPv6)</div>
					<Dropdown bind:value={strategy} options={STRATEGY_OPTIONS} fullWidth />
				</label>
			</section>
		{/if}

		{#if type !== 'udp' && type !== 'local'}
			<section class="form-section">
				<div class="section-label">Bootstrap resolver (для домена сервера)</div>

				<label class="toggle">
					<input type="checkbox" bind:checked={resolverEnabled} />
					<span>Использовать другой DNS для резолва домена этого сервера</span>
				</label>

				{#if needsResolver && !resolverEnabled}
					<div class="warn">
						У <code>{type}</code> сервера адрес — доменное имя. Без bootstrap resolver sing-box не сможет его резолвить.
					</div>
				{/if}

				{#if resolverEnabled}
					<div class="resolver-fields">
						<label class="field">
							<div class="lbl">Resolver server (tag)</div>
							<Dropdown bind:value={resolverServer} options={resolverServerOptions} fullWidth />
						</label>
						<label class="field">
							<div class="lbl">Resolver strategy</div>
							<Dropdown bind:value={resolverStrategy} options={STRATEGY_OPTIONS} fullWidth />
						</label>
					</div>
				{/if}
			</section>
		{/if}

		{#if error}<div class="error">{error}</div>{/if}
	</div>

	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={onClose} type="button">Отмена</Button>
		<Button variant="primary" size="md" onclick={save} disabled={busy} loading={busy} type="button">
			Сохранить
		</Button>
	{/snippet}
</SingboxSettingsModal>
