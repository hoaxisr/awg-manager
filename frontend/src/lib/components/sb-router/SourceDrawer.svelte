<!--
  Настройки источника трафика (режим «весь роутер» / NDMS policy).
  Открывается кликом по блоку «Источник» в FlowGraph (beginner).
-->
<script lang="ts">
  import { m } from '$lib/i18n';
  import { SideDrawer, Button } from '$lib/components/ui';
  import { singboxRouter as singboxRouterStore } from '$lib/stores/singboxRouter';
  import { notifications } from '$lib/stores/notifications';
  import { sourceDrawerOpen, closeSourceDrawer } from './sourceDrawerStore';
  import TrafficSourceSettings from './TrafficSourceSettings.svelte';
  import { mergeAndSaveSettings } from './settingsActions';
  import type { SingboxRouterSettings } from '$lib/types';

  const status = singboxRouterStore.status;
  const storeSettings = singboxRouterStore.settings;

  let open = $derived($sourceDrawerOpen);
  let s = $derived($status);
  let cfg = $derived($storeSettings);

  async function applyPatch(patch: Partial<SingboxRouterSettings>) {
    if (!cfg) return;
    try {
      await mergeAndSaveSettings(patch);
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      notifications.error(m.sb_router_common_save_failed({ message: msg }));
    }
  }
</script>

<SideDrawer {open} onClose={closeSourceDrawer} title={m.sb_router_source_title()} width={420}>
  {#if cfg}
    <div class="sections">
      <TrafficSourceSettings
        {cfg}
        deviceCount={s?.deviceCount ?? 0}
        policyExists={s?.policyExists !== false}
        variant="beginner"
        onPatch={applyPatch}
      />
    </div>
  {/if}

  {#snippet footer()}
    <Button variant="ghost" size="sm" fullWidth onclick={closeSourceDrawer}>{m.common_close()}</Button>
  {/snippet}
</SideDrawer>

<style>
  .sections { display: flex; flex-direction: column; }
</style>
