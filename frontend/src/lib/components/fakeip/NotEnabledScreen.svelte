<script lang="ts">
	import { EmptyState } from '$lib/components/layout';
	import { Button } from '$lib/components/ui';
	import { Network } from 'lucide-svelte';

	// FE-spec §12.1 state 'not-fakeip' / §7.2 — shown when fakeip-tun is not the
	// active routing mode. The CTA is the entry point into the mode transition;
	// the actual ConfirmSwitch flow lives in task 1E.5, so this component only
	// surfaces the intent via a callback prop.
	interface Props {
		onEnableRequested: () => void;
		/** Причина, по которой режим недоступен: кнопка блокируется, текст показывается. */
		unavailableReason?: string;
	}

	let { onEnableRequested, unavailableReason }: Props = $props();
</script>

<EmptyState
	title="Режим FakeIP не включён"
	description={unavailableReason ??
		'Сейчас активен другой режим маршрутизации (TPROXY или маршрутизация выключена). Конфигурация и блоки FakeIP появятся после включения движка fakeip-tun.'}
>
	{#snippet icon()}
		<Network />
	{/snippet}
	{#snippet action()}
		<Button
			variant="primary"
			size="md"
			disabled={!!unavailableReason}
			title={unavailableReason}
			onclick={onEnableRequested}
		>
			Включить FakeIP
		</Button>
	{/snippet}
</EmptyState>
