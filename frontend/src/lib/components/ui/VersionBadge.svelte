<script lang="ts" module>
  export type VersionBadgeKind = 'backend' | 'awg';
  export type BackendValue = 'kernel' | 'nativewg' | string;
  export type AwgValue = 'awg2.0' | 'awg1.5' | 'awg1.0' | 'wg' | string;
</script>

<script lang="ts">
  interface Props {
    kind: VersionBadgeKind;
    value: BackendValue | AwgValue;
  }

  let { kind, value }: Props = $props();

  const label = $derived.by(() => {
    if (kind === 'backend') return value === 'nativewg' ? 'NativeWG' : 'Kernel';
    return ({
      'awg2.0': 'AWG 2.0',
      'awg1.5': 'AWG 1.5',
      'awg1.0': 'AWG 1.0',
      'wg': 'WG',
    } as Record<string, string>)[value as string] ?? '';
  });

  const tone = $derived(kind === 'awg' && value !== 'wg' ? 'accent' : 'muted');
</script>

{#if label}
  <span class="vb tone-{tone}">{label}</span>
{/if}

<style>
  .vb {
    display: inline-flex;
    align-items: center;
    font-family: var(--font-mono);
    font-size: 10px;
    font-weight: 600;
    letter-spacing: 0.3px;
    padding: 2px 7px;
    border-radius: var(--radius-pill);
    white-space: nowrap;
  }

  .tone-accent {
    background: var(--color-accent-tint);
    color: var(--color-accent);
  }

  .tone-muted {
    background: var(--color-bg-tertiary);
    color: var(--color-text-muted);
  }
</style>
