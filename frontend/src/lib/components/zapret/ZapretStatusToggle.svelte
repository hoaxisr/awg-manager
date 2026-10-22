<script lang="ts">
	import { m } from '$lib/i18n';
	import type { ZapretStatus } from '$lib/types';
	import { Toggle } from '$lib/components/ui';

	interface Props {
		status: ZapretStatus | null;
		loading?: boolean;
		busy?: boolean;
		oncontrol: (action: 'start' | 'stop') => void;
	}

	let { status, loading = false, busy = false, oncontrol }: Props = $props();
	let installed = $derived(status?.installed === true);
	let running = $derived(status?.running === true);
	let unavailable = $derived(loading || busy || !installed || !!status?.conflict || !status?.initScript);
	let label = $derived(running ? m.zapret_toggle_stop_aria() : m.zapret_toggle_start_aria());
	let title = $derived.by(() => {
		if (loading || busy) return m.zapret_toggle_loading_title();
		if (!installed || !status?.initScript) return m.zapret_toggle_unavailable_title();
		if (status.conflict) return m.zapret_toggle_conflict_title();
		return running ? m.zapret_toggle_stop_title() : m.zapret_toggle_start_title();
	});
	function toggle() {
		if (!unavailable) oncontrol(running ? 'stop' : 'start');
	}
</script>

<span {title} aria-busy={loading || busy}>
	<Toggle
		checked={running}
		controlled
		variant="flip"
		ariaLabel={label}
		disabled={unavailable}
		loading={loading || busy}
		onchange={toggle}
	/>
</span>
