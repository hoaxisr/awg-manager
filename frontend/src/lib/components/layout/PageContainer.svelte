<script lang="ts" module>
  import type { Snippet } from 'svelte';
  export type PageWidth = 'narrow' | 'wide' | 'full';
</script>

<script lang="ts">
  interface Props {
    width?: PageWidth;
    children: Snippet;
  }

  let { width = 'wide', children }: Props = $props();
</script>

<div
  class="page-container"
  class:width-narrow={width === 'narrow'}
  class:width-wide={width === 'wide'}
  class:width-full={width === 'full'}
>
  {@render children()}
</div>

<style>
  .page-container {
    flex: 1;
    margin: 0 auto;
    padding: 1.5rem;
    width: 100%;
  }

  .width-narrow { max-width: 960px; }
  .width-wide { max-width: 1440px; }
  .width-full { max-width: 100%; }
</style>
