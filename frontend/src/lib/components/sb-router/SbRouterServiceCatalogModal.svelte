<script lang="ts">
  import { m } from '$lib/i18n';
  import { ServiceCatalogModal } from '$lib/components/dnsroutes';
  import type { CatalogPreset } from '$lib/types';
  import { singboxRouterCatalogPresetFilter } from '$lib/utils/catalog-preset';
  import {
    templatesOpen,
    templatesSelection,
    dismissTemplatesModal,
    catalogIdsFromTemplatesSelection,
    setServiceTemplateSelection,
  } from './templatesStore';

  interface Props {
    /** rule-set tags already in config: member-of-added-composite mark (#450)
     *  + non-disabling «набор уже есть» reuse badge (wizard reuses existing sets). */
    existingRuleSetTags?: string[];
  }

  let { existingRuleSetTags = [] }: Props = $props();

  const initialSelectedIds = $derived(catalogIdsFromTemplatesSelection($templatesSelection));

  function handleConfirm(presets: CatalogPreset[]) {
    setServiceTemplateSelection(presets.map((p) => p.id));
    dismissTemplatesModal();
  }

  function handleClose() {
    dismissTemplatesModal();
  }
</script>

<ServiceCatalogModal
  open={$templatesOpen}
  title={m.dns_routes_catalog_title()}
  presetFilter={singboxRouterCatalogPresetFilter}
  footer="none"
  multiple
  warnLargeDnsLists={false}
  markExisting={false}
  {existingRuleSetTags}
  {initialSelectedIds}
  onclose={handleClose}
  onconfirm={handleConfirm}
/>
