<script lang="ts">
  import type { Snippet } from 'svelte';

  interface Props {
    children: Snippet;
  }

  let { children }: Props = $props();
</script>

<!-- Единая ширина всех страниц (nav-v3, подэтап 5A): контейнер отдаёт контенту
     всю доступную ширину оболочки. Единственный рычаг сужения — тумблер
     «Компактный режим» (data-layout-compact, 960px) в настройках. -->
<div class="page-container">
  {@render children()}
</div>

<style>
  .page-container {
    flex: 1;
    margin: 0 auto;
    padding: var(--layout-gutter-y-top) var(--layout-gutter-x) var(--layout-gutter-y-bottom);
    width: 100%;
    box-sizing: border-box;
    min-width: 0;
  }
</style>
