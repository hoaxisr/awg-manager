<script lang="ts">
    // Кнопка «Поиск» разделов маршрутизации (шапка /router/ndms и
    // /services/hrneo). Результат чужого раздела открывается кросс-переходом
    // ?edit={id} — целевая страница поднимает модалку правила.
    import { goto } from '$app/navigation';
    import { Search } from 'lucide-svelte';
    import type { DnsRoute, StaticRouteList, RoutingTunnel } from '$lib/types';
    import { Button, Modal } from '$lib/components/ui';
    import RoutingSearch from './RoutingSearch.svelte';

    interface Props {
        dnsRoutes: DnsRoute[];
        staticRoutes: StaticRouteList[];
        tunnels: RoutingTunnel[];
    }

    let { dnsRoutes, staticRoutes, tunnels }: Props = $props();

    let open = $state(false);

    function handleRuleClick(id: string, type: 'dns' | 'ip') {
        open = false;
        // dnsRoutes держит правила обоих движков; HR-правило редактируется
        // только на своей странице — NDMS их отфильтровывает.
        const path =
            type === 'ip'
                ? '/router/ip'
                : dnsRoutes.find((r) => r.id === id)?.backend === 'hydraroute'
                  ? '/services/hrneo'
                  : '/router/ndms';
        void goto(`${path}?edit=${encodeURIComponent(id)}`);
    }
</script>

<Button variant="secondary" size="sm" onclick={() => (open = true)} iconBefore={searchIcon}>
    Поиск
</Button>

<Modal
    {open}
    onclose={() => (open = false)}
    title="Поиск по правилам маршрутизации NDMS"
    size="xl"
>
    <RoutingSearch {dnsRoutes} {staticRoutes} {tunnels} onRuleClick={handleRuleClick} />
</Modal>

{#snippet searchIcon()}
    <Search size={16} strokeWidth={2} aria-hidden="true" />
{/snippet}
