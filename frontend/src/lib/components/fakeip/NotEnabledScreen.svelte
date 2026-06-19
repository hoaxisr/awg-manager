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
		/** Show the secondary «переключиться на TPROXY» action (only when fakeip-tun
		 *  is the persisted routing mode, i.e. there is a way back to TPROXY). */
		showSwitchToTproxy?: boolean;
		onSwitchToTproxy?: () => void;
	}

	let { onEnableRequested, showSwitchToTproxy = false, onSwitchToTproxy }: Props = $props();
</script>

<EmptyState
	title="Режим FakeIP не включён"
	description="Сейчас активен другой режим маршрутизации (TPROXY или маршрутизация выключена). Конфигурация и блоки FakeIP появятся после включения движка fakeip-tun."
>
	{#snippet icon()}
		<Network />
	{/snippet}
	{#snippet action()}
		<Button variant="primary" size="md" onclick={onEnableRequested}>
			Включить FakeIP
		</Button>
		{#if showSwitchToTproxy && onSwitchToTproxy}
			<Button variant="secondary" size="md" onclick={onSwitchToTproxy}>
				Переключиться на TPROXY
			</Button>
		{/if}
	{/snippet}
</EmptyState>
