<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button, Dropdown, type DropdownOption } from '$lib/components/ui';
	import { OctagonAlert } from 'lucide-svelte';
	import SingboxSettingsModal from './SingboxSettingsModal.svelte';
	import type {
		SingboxRouterDNSServer,
		SingboxRouterDNSType,
		SingboxRouterDNSStrategy,
	} from '$lib/types';
	import { outboundGroupLabel, type OutboundGroup } from './outboundOptions';
	import {
		DNS_DIRECT_SERVER_TAG,
		getDnsDirectLegacyDetour,
		normalizeDnsServerDetour,
		sanitizeDnsServerForApi,
	} from '$lib/utils/dnsServerDetour';
	import { dnsServerDetourDisplay } from '$lib/components/sb-router/dnsServerDetourDisplay';
	import { api } from '$lib/api/client';

	interface Props {
		server?: SingboxRouterDNSServer;
		servers: SingboxRouterDNSServer[];
		outboundOptions: OutboundGroup[];
		onClose: () => void;
		onSave: (server: SingboxRouterDNSServer) => Promise<void> | void;
	}
	let { server, servers, outboundOptions, onClose, onSave }: Props = $props();

	const TYPE_OPTIONS = $derived<DropdownOption<SingboxRouterDNSType>[]>([
		{ value: 'udp', label: m.routing_singbox_dns_type_udp() },
		{ value: 'tls', label: 'DoT (DNS over TLS)' },
		{ value: 'https', label: 'DoH (DNS over HTTPS)' },
		{ value: 'quic', label: 'DoQ (DNS over QUIC)' },
		{ value: 'h3', label: 'DoH3' },
		{ value: 'local', label: m.routing_singbox_dns_type_local() },
	]);

	const STRATEGY_OPTIONS: DropdownOption<SingboxRouterDNSStrategy>[] = [
		{ value: '', label: '— default —' },
		{ value: 'ipv4_only', label: 'ipv4_only' },
		{ value: 'ipv6_only', label: 'ipv6_only' },
		{ value: 'prefer_ipv4', label: 'prefer_ipv4' },
		{ value: 'prefer_ipv6', label: 'prefer_ipv6' },
	];
	const TLS_VERSION_OPTIONS: DropdownOption[] = [
		{ value: '', label: '— default —' },
		{ value: '1.0', label: 'TLS 1.0' },
		{ value: '1.1', label: 'TLS 1.1' },
		{ value: '1.2', label: 'TLS 1.2' },
		{ value: '1.3', label: 'TLS 1.3' },
	];

	const detourOptions = $derived<DropdownOption[]>([
		{ value: '', label: m.routing_singbox_direct() },
		...outboundOptions.flatMap((g) =>
			g.items
				.filter((i) => i.value !== 'direct')
				.map((i) => ({ value: i.value, label: i.label, group: outboundGroupLabel(g.id) })),
		),
	]);

	const dnsDirectDetourOptions = $derived<DropdownOption[]>([
		{ value: '', label: m.routing_singbox_direct() },
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
	// svelte-ignore state_referenced_locally
	let tlsServerName = $state(server?.tls?.server_name ?? '');
	// svelte-ignore state_referenced_locally
	let tlsInsecure = $state(server?.tls?.insecure ?? false);
	// svelte-ignore state_referenced_locally
	let tlsALPN = $state(server?.tls?.alpn?.join(', ') ?? '');
	// svelte-ignore state_referenced_locally
	let tlsMinVersion = $state(server?.tls?.min_version ?? '');
	// svelte-ignore state_referenced_locally
	let tlsMaxVersion = $state(server?.tls?.max_version ?? '');
	// svelte-ignore state_referenced_locally
	let tlsCertificatePins = $state(server?.tls?.certificate_public_key_sha256?.join(', ') ?? '');
	let lookupBusy = $state(false);
	let lookupError = $state('');
	let lookupNeedDomain = $state(false);
	const lookupErrorText = $derived(lookupNeedDomain ? m.routing_singbox_dns_err_lookup_domain() : lookupError);
	let lookupIPs = $state<string[]>([]);
	let lookupCertificates = $state<Array<{ subject: string; issuer: string; not_after: string }>>([]);

	let busy = $state(false);
	let error = $state('');
	let errorKind = $state<'' | 'tag' | 'server' | 'resolver' | 'tls'>('');
	const errorText = $derived(
		error ||
			(errorKind === 'tag'
				? m.routing_singbox_outbound_err_tag()
				: errorKind === 'server'
					? m.routing_singbox_dns_err_server()
					: errorKind === 'resolver'
						? m.routing_singbox_dns_err_resolver()
						: errorKind === 'tls'
							? m.routing_singbox_dns_err_tls_range()
							: ''),
	);

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
	let initialTLS = $state('');
	let previousType = $state<SingboxRouterDNSType | null>(null);

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
			initialTLS = JSON.stringify({
				server_name: server.tls?.server_name?.trim() ?? '',
				insecure: server.tls?.insecure ?? false,
				alpn: server.tls?.alpn?.map((item) => item.trim()).filter(Boolean) ?? [],
				min_version: server.tls?.min_version ?? '',
				max_version: server.tls?.max_version ?? '',
				certificate_public_key_sha256:
					server.tls?.certificate_public_key_sha256?.map((item) => item.trim()).filter(Boolean) ?? [],
			});
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
			initialTLS = '';
		}
	});

	$effect(() => {
		if (previousType === null) {
			previousType = type;
			return;
		}
		if (type === previousType) return;
		previousType = type;
		serverAddr = '';
		serverPort = '';
		path = '';
		detour = '';
		strategy = '';
		resolverEnabled = false;
		resolverServer = '';
		resolverStrategy = '';
		tlsServerName = '';
		tlsInsecure = false;
		tlsALPN = '';
		tlsMinVersion = '';
		tlsMaxVersion = '';
		tlsCertificatePins = '';
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
			resolverStrategy !== initialResolverStrategy ||
			serializeTLSState() !== initialTLS
		);
	});

	const needsResolver = $derived(type !== 'udp' && type !== 'local' && !isIPLiteral(serverAddr));
	const supportsTLS = $derived(type === 'tls' || type === 'quic' || type === 'https' || type === 'h3');
	const hasOutboundDetour = $derived(!isManagedDnsDirect && detour !== '');
	const availableResolvers = $derived(servers.filter((s) => s.tag !== tag).map((s) => s.tag));
	const resolverServerOptions = $derived<DropdownOption[]>([
		{ value: '', label: m.routing_singbox_dns_choose() },
		...availableResolvers.map((t) => ({ value: t, label: t })),
	]);

	function isIPLiteral(s: string): boolean {
		return /^(\d{1,3}\.){3}\d{1,3}$/.test(s) || s.includes(':');
	}

	function splitList(value: string): string[] {
		return value.split(/[\n,]/).map((item) => item.trim()).filter(Boolean);
	}

	function serializeTLSState(): string {
		return JSON.stringify({
			server_name: tlsServerName.trim(),
			insecure: tlsInsecure,
			alpn: splitList(tlsALPN),
			min_version: tlsMinVersion,
			max_version: tlsMaxVersion,
			certificate_public_key_sha256: splitList(tlsCertificatePins),
		});
	}

	async function lookupTLS(): Promise<void> {
		if (!serverAddr.trim()) {
			lookupNeedDomain = true;
			return;
		}
		lookupBusy = true;
		lookupError = '';
		lookupNeedDomain = false;
		try {
			const hostname = serverAddr.trim();
			const lookupPort = serverPort === '' ? (type === 'https' || type === 'h3' ? 443 : 853) : serverPort;
			const result = await api.singboxRouterLookupDNSServer(hostname, lookupPort, tlsServerName || hostname);
			lookupIPs = result.ips;
			lookupCertificates = result.certificates;
			if (result.ips[0]) serverAddr = result.ips[0];
			if (!tlsServerName.trim() && !isIPLiteral(hostname)) tlsServerName = hostname;
			tlsCertificatePins = result.certificate_public_key_sha256.join(', ');
		} catch (e) {
			lookupError = (e as Error).message;
		} finally {
			lookupBusy = false;
		}
	}

	async function save(): Promise<void> {
		busy = true;
		error = '';
		errorKind = '';
		try {
			if (!tag.trim()) { errorKind = 'tag'; busy = false; return; }
			if (type !== 'local' && !serverAddr.trim()) { errorKind = 'server'; busy = false; return; }
			if (resolverEnabled && !resolverServer) { errorKind = 'resolver'; busy = false; return; }
			if (tlsMinVersion && tlsMaxVersion && Number(tlsMinVersion) > Number(tlsMaxVersion)) {
				errorKind = 'tls'; busy = false; return;
			}

			const built: SingboxRouterDNSServer = {
				tag: tag.trim(),
				type,
				server: type === 'local' ? '' : serverAddr.trim(),
			};
			if (type !== 'local') {
				if (serverPort !== '' && Number(serverPort) > 0) built.server_port = Number(serverPort);
				if (path.trim()) built.path = path.trim();
				if (!hasOutboundDetour && resolverEnabled && resolverServer) {
					built.domain_resolver = { server: resolverServer };
					if (resolverStrategy) built.domain_resolver.strategy = resolverStrategy;
				}
			}
			if (supportsTLS) {
				const tls = {
					...(tlsServerName.trim() ? { server_name: tlsServerName.trim() } : {}),
					...(tlsInsecure ? { insecure: true } : {}),
					...(splitList(tlsALPN).length ? { alpn: splitList(tlsALPN) } : {}),
					...(tlsMinVersion ? { min_version: tlsMinVersion as '1.0' | '1.1' | '1.2' | '1.3' } : {}),
					...(tlsMaxVersion ? { max_version: tlsMaxVersion as '1.0' | '1.1' | '1.2' | '1.3' } : {}),
					...(splitList(tlsCertificatePins).length
						? { certificate_public_key_sha256: splitList(tlsCertificatePins) }
						: {}),
				};
				if (Object.keys(tls).length) built.tls = tls;
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
	title={server ? m.routing_singbox_dns_edit_title() : m.routing_singbox_dns_new_title()}
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
					{m.routing_singbox_dns_local_hint()}
				</div>
			{/if}
		</div>

		{#if type !== 'local'}
			<section class="form-section form-section-divided">
				<div class="section-label">{m.routing_singbox_dns_routing()}</div>

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
								{m.routing_singbox_dns_detour_invalid()}
							</div>
						{:else}
							<div class="hint">
								{m.routing_singbox_dns_final_direct_pre()} <strong>{m.routing_singbox_dns_final_direct_strong()}</strong> {m.routing_singbox_dns_final_direct_post()}
								<code>detour</code> {m.routing_singbox_dns_detour_not_written()}
							</div>
						{/if}
					</label>
				{:else}
					<label class="field">
						<div class="lbl">Detour (outbound)</div>
						<Dropdown bind:value={detour} options={detourOptions} fullWidth />
						<div class="hint">
							{m.routing_singbox_dns_detour_hint()} <code>detour</code> {m.routing_singbox_dns_detour_not_written()}
						</div>
					</label>
				{/if}

				<label class="field">
					<div class="lbl">{m.routing_singbox_dns_strategy_label()}</div>
					<Dropdown bind:value={strategy} options={STRATEGY_OPTIONS} fullWidth />
				</label>
			</section>
		{/if}

		{#if type !== 'udp' && type !== 'local' && !hasOutboundDetour}
			<section class="form-section">
				<div class="section-label">{m.routing_singbox_dns_bootstrap()}</div>

				<label class="toggle">
					<input type="checkbox" bind:checked={resolverEnabled} />
					<span>{m.routing_singbox_dns_bootstrap_toggle()}</span>
				</label>

				{#if needsResolver && !resolverEnabled}
					<div class="warn">
						{m.routing_singbox_dns_bootstrap_warn_pre()} <code>{type}</code> {m.routing_singbox_dns_bootstrap_warn_post()}
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

		{#if supportsTLS}
			<section class="form-section form-section-divided">
				<div class="section-label">TLS</div>
				<div class="hint lookup-action">
					<Button variant="ghost" size="sm" onclick={lookupTLS} disabled={lookupBusy} loading={lookupBusy} type="button">
						{m.routing_singbox_dns_lookup()}
					</Button>
					<span>{m.routing_singbox_dns_lookup_hint()}</span>
				</div>
				{#if lookupErrorText}<div class="error">{lookupErrorText}</div>{/if}
				{#if lookupIPs.length}
					<div class="hint">{m.routing_singbox_dns_found_ips({ ips: lookupIPs.join(', ') })}</div>
				{/if}
				{#if lookupCertificates.length}
					<div class="hint">{m.routing_singbox_dns_certificates({ certificates: lookupCertificates.map((cert) => m.routing_singbox_dns_cert_item({ subject: cert.subject, notAfter: cert.not_after })).join('; ') })}</div>
				{/if}
				<div class="fields-grid">
					<label class="field span-full">
						<div class="lbl">Server name (SNI)</div>
						<input bind:value={tlsServerName} placeholder="cloudflare-dns.com" />
					</label>
					<label class="toggle span-full">
						<input type="checkbox" bind:checked={tlsInsecure} />
						<span>{m.routing_singbox_dns_accept_any_cert()}</span>
					</label>
					<label class="field span-full">
						<div class="lbl">ALPN</div>
						<input bind:value={tlsALPN} placeholder="h2, http/1.1" />
						<div class="hint">{m.routing_singbox_dns_alpn_hint()}</div>
					</label>
					<label class="field">
						<div class="lbl">{m.routing_singbox_dns_tls_min()}</div>
						<Dropdown bind:value={tlsMinVersion} options={TLS_VERSION_OPTIONS} fullWidth />
					</label>
					<label class="field">
						<div class="lbl">{m.routing_singbox_dns_tls_max()}</div>
						<Dropdown bind:value={tlsMaxVersion} options={TLS_VERSION_OPTIONS} fullWidth />
					</label>
					<label class="field span-full">
						<div class="lbl">{m.routing_singbox_dns_pin_label()}</div>
						<input bind:value={tlsCertificatePins} placeholder="base64 pin" />
						<div class="hint">{m.routing_singbox_dns_pin_hint()}</div>
					</label>
				</div>
			</section>
		{/if}

		{#if errorText}<div class="error">{errorText}</div>{/if}
	</div>

	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={onClose} type="button">{m.common_cancel()}</Button>
		<Button variant="primary" size="md" onclick={save} disabled={busy} loading={busy} type="button">
			{m.common_save()}
		</Button>
	{/snippet}
</SingboxSettingsModal>
