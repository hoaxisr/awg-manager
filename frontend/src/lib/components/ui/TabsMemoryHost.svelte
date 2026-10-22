<script lang="ts">
    import Tabs from './Tabs.svelte';
    import type { Tab } from './tabs-model';
    let { tabs, ready = true, reject = false, persistKey, skipMemorySearchParams }: { tabs: Tab[]; ready?: boolean; reject?: boolean; persistKey?: string; skipMemorySearchParams?: string[] } = $props();
    let active = $state('first');
</script>
<Tabs {tabs} {active} onchange={(id) => { if (!reject) active = id; }} urlParam="tab" memoryReady={ready} {persistKey} {skipMemorySearchParams} />
<p data-testid="active">{active}</p>
