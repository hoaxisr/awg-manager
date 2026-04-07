<script lang="ts">
    import type { DnsRoute, StaticRouteList, RoutingTunnel } from '$lib/types';
    import { api } from '$lib/api/client';
    import { detectQueryType, ipInCIDR, isIPv4 } from '$lib/utils/cidr';
    import RoutingSearchResults from './RoutingSearchResults.svelte';
    import type { MatchedRule, ResolveMatch } from './types';

    interface Props {
        dnsRoutes: DnsRoute[];
        staticRoutes: StaticRouteList[];
        tunnels?: RoutingTunnel[];
        onRuleClick?: (id: string, type: 'dns' | 'ip') => void;
    }

    let { dnsRoutes, staticRoutes, tunnels = [], onRuleClick }: Props = $props();

    function resolveTunnelName(routes: Array<{ tunnelId?: string; interface?: string }>): string {
        if (!routes || routes.length === 0) return '';
        const first = routes[0];
        const found = tunnels.find(t => t.id === first.tunnelId);
        return found?.name ?? first.interface ?? first.tunnelId ?? '';
    }

    let query = $state('');
    let hasSearched = $state(false);
    let dnsResults: MatchedRule[] = $state([]);
    let ipResults: MatchedRule[] = $state([]);
    let resolveMatch: ResolveMatch | null = $state(null);
    let resolving = $state(false);
    let resolveError = $state('');

    function searchDnsRules(q: string, queryType: 'ip' | 'cidr' | 'domain'): MatchedRule[] {
        const results: MatchedRule[] = [];
        const qLower = q.toLowerCase();

        for (const route of dnsRoutes) {
            const matches: string[] = [];

            if (queryType === 'domain') {
                const allDomains = [
                    ...(route.manualDomains || []),
                    ...(route.domains || []),
                    ...(route.excludes || [])
                ];
                for (const domain of allDomains) {
                    const domainLower = domain.toLowerCase();
                    if (domainLower.includes(qLower) || qLower.endsWith('.' + domainLower)) {
                        matches.push(domain);
                    }
                }
            } else if (queryType === 'ip') {
                for (const subnet of (route.subnets || [])) {
                    if (ipInCIDR(q, subnet)) {
                        matches.push(subnet);
                    }
                }
            } else if (queryType === 'cidr') {
                for (const subnet of (route.subnets || [])) {
                    if (subnet.includes(q)) {
                        matches.push(subnet);
                    }
                }
            }

            if (matches.length > 0) {
                const subCount = route.subscriptions?.length ?? 0;
                const manualCount = route.manualDomains?.length ?? 0;
                let sourceSummary = '';
                if (subCount > 0 && manualCount > 0) sourceSummary = `${subCount} листов + ${manualCount} вручную`;
                else if (subCount > 0) sourceSummary = `${subCount} листов`;
                else if (manualCount > 0) sourceSummary = 'все вручную';

                results.push({
                    id: route.id,
                    name: route.name,
                    type: 'dns',
                    matches,
                    totalMatches: matches.length,
                    enabled: route.enabled,
                    tunnelName: resolveTunnelName(route.routes ?? []),
                    domainCount: route.domains?.length ?? 0,
                    sourceSummary,
                });
            }
        }

        return results;
    }

    function searchIpRules(q: string, queryType: 'ip' | 'cidr' | 'domain'): MatchedRule[] {
        if (queryType === 'domain') return [];
        const results: MatchedRule[] = [];

        for (const route of staticRoutes) {
            const matches: string[] = [];

            if (queryType === 'ip') {
                for (const subnet of route.subnets) {
                    if (ipInCIDR(q, subnet)) {
                        matches.push(subnet);
                    }
                }
            } else if (queryType === 'cidr') {
                for (const subnet of route.subnets) {
                    if (subnet.includes(q)) {
                        matches.push(subnet);
                    }
                }
            }

            if (matches.length > 0) {
                const ipTunnel = tunnels.find(t => t.id === route.tunnelID);
                results.push({
                    id: route.id,
                    name: route.name,
                    type: 'ip',
                    matches,
                    totalMatches: matches.length,
                    enabled: route.enabled,
                    tunnelName: ipTunnel?.name ?? route.tunnelID ?? '',
                    domainCount: 0,
                    sourceSummary: `${route.subnets?.length ?? 0} подсетей`,
                });
            }
        }

        return results;
    }

    function findCIDRMatchesForIPs(ips: string[]): MatchedRule[] {
        const results: MatchedRule[] = [];

        // Check IP routes
        for (const route of staticRoutes) {
            const matches: string[] = [];
            for (const ip of ips) {
                for (const subnet of route.subnets) {
                    if (ipInCIDR(ip, subnet)) {
                        matches.push(subnet);
                    }
                }
            }
            if (matches.length > 0) {
                const ipTunnel = tunnels.find(t => t.id === route.tunnelID);
                results.push({
                    id: route.id,
                    name: route.name,
                    type: 'ip',
                    matches: [...new Set(matches)],
                    totalMatches: new Set(matches).size,
                    enabled: route.enabled,
                    tunnelName: ipTunnel?.name ?? route.tunnelID ?? '',
                    domainCount: 0,
                    sourceSummary: `${route.subnets?.length ?? 0} подсетей`,
                });
            }
        }

        // Check DNS route subnets
        for (const route of dnsRoutes) {
            const matches: string[] = [];
            for (const ip of ips) {
                for (const subnet of (route.subnets || [])) {
                    if (ipInCIDR(ip, subnet)) {
                        matches.push(subnet);
                    }
                }
            }
            if (matches.length > 0) {
                results.push({
                    id: route.id,
                    name: route.name,
                    type: 'dns',
                    matches: [...new Set(matches)],
                    totalMatches: new Set(matches).size,
                    enabled: route.enabled,
                    tunnelName: resolveTunnelName(route.routes ?? []),
                    domainCount: route.domains?.length ?? 0,
                    sourceSummary: '',
                });
            }
        }

        return results;
    }

    async function handleSearch() {
        const q = query.trim();
        if (!q) return;

        hasSearched = true;
        resolveMatch = null;
        resolveError = '';
        resolving = false;

        const queryType = detectQueryType(q);

        dnsResults = searchDnsRules(q, queryType);
        ipResults = searchIpRules(q, queryType);

        // DNS resolve for domain queries
        if (queryType === 'domain') {
            resolving = true;
            try {
                const result = await api.resolveDomain(q);
                if (result.error) {
                    resolveError = result.error;
                } else if (result.ips.length > 0) {
                    const cidrMatches = findCIDRMatchesForIPs(result.ips);
                    resolveMatch = {
                        domain: result.domain,
                        ips: result.ips,
                        rules: cidrMatches
                    };
                }
            } catch (e) {
                resolveError = e instanceof Error ? e.message : 'Ошибка резолва';
            } finally {
                resolving = false;
            }
        }
    }

    function handleClear() {
        query = '';
        hasSearched = false;
        dnsResults = [];
        ipResults = [];
        resolveMatch = null;
        resolveError = '';
        resolving = false;
    }

    function handleKeydown(e: KeyboardEvent) {
        if (e.key === 'Enter') {
            handleSearch();
        }
    }
</script>

<div class="routing-search">
    <div class="search-input-wrapper">
        <input
            type="text"
            class="search-input"
            placeholder="Поиск домена или IP по всем правилам..."
            bind:value={query}
            onkeydown={handleKeydown}
        />
        {#if query}
            <button class="btn-clear" onclick={handleClear} title="Очистить">
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="16" height="16">
                    <line x1="18" y1="6" x2="6" y2="18"/>
                    <line x1="6" y1="6" x2="18" y2="18"/>
                </svg>
            </button>
        {/if}
        <button class="btn btn-sm btn-primary search-btn" onclick={handleSearch} disabled={!query.trim()}>
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="16" height="16">
                <circle cx="11" cy="11" r="8"/>
                <line x1="21" y1="21" x2="16.65" y2="16.65"/>
            </svg>
            Поиск
        </button>
    </div>

    {#if hasSearched}
        <RoutingSearchResults
            {dnsResults}
            {ipResults}
            {resolveMatch}
            {resolving}
            {resolveError}
            {onRuleClick}
        />
    {/if}
</div>

<style>
    .routing-search {
        position: relative;
        margin-bottom: 16px;
    }

    .search-input-wrapper {
        display: flex;
        align-items: center;
        gap: 8px;
    }

    .search-input {
        flex: 1;
        padding: 8px 12px;
        border: 1px solid var(--border);
        border-radius: 8px;
        background: var(--bg-primary);
        color: var(--text-primary);
        font-size: 0.875rem;
    }

    .search-input::placeholder {
        color: var(--text-muted);
    }

    .search-input:focus {
        outline: none;
        border-color: var(--accent);
    }

    .btn-clear {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 32px;
        height: 32px;
        border: none;
        background: none;
        color: var(--text-muted);
        cursor: pointer;
        border-radius: 4px;
        margin-left: -44px;
        margin-right: 4px;
    }

    .btn-clear:hover {
        color: var(--text-secondary);
    }

    .search-btn {
        display: flex;
        align-items: center;
        gap: 6px;
        white-space: nowrap;
    }
</style>
