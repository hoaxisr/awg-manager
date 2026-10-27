<script lang="ts">
    import ServiceCatalogModal from './ServiceCatalogModal.svelte';
    import type { RoutingTunnel, CatalogPreset } from '$lib/types';

    interface Props {
        open: boolean;
        existingNames: string[];
        tunnels: RoutingTunnel[];
        onclose: () => void;
        oncreate: (presets: CatalogPreset[], tunnelId: string) => void;
    }

    let {
        open = $bindable(false),
        existingNames,
        tunnels,
        onclose,
        oncreate,
    }: Props = $props();

    let creating = $state(false);

    $effect(() => {
        if (!open) creating = false;
    });

    function handleCreate(presets: CatalogPreset[], tunnelId: string) {
        creating = true;
        oncreate(presets, tunnelId);
    }
</script>

<ServiceCatalogModal
    bind:open
    markExisting
    {existingNames}
    footer="tunnel"
    {tunnels}
    submitting={creating}
    {onclose}
    oncreate={handleCreate}
/>
