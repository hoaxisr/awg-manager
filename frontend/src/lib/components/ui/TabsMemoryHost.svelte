<script lang="ts">
    // Test host for tab-memory.test.ts: owns `active` the way a page does.
    import Tabs from './Tabs.svelte';
    import type { Tab } from './tabs-model';

    interface Props {
        tabs: Tab[];
        ready?: boolean;
        reject?: boolean;
        persistKey?: string;
        skipMemorySearchParams?: string[];
        restoreMemory?: boolean;
    }

    let { tabs, ready = true, reject = false, persistKey, skipMemorySearchParams, restoreMemory }: Props = $props();
    let active = $state('first');
</script>
<Tabs {tabs} {active} onchange={(id) => { if (!reject) active = id; }} urlParam="tab" memoryReady={ready} {persistKey} {skipMemorySearchParams} {restoreMemory} />
<p data-testid="active">{active}</p>
