<script lang="ts">
	import { m } from '$lib/i18n';
	import { Modal } from '$lib/components/ui';
	import TunnelDiagnosticsPanel from './TunnelDiagnosticsPanel.svelte';

	type DiagnosticsKind = 'awg' | 'system' | 'singbox' | 'subscription';
	type DiagnosticsSubject = 'tunnel' | 'subscription';

	interface Props {
		open: boolean;
		kind: DiagnosticsKind;
		targetId: string;
		displayName: string;
		subject: DiagnosticsSubject;
		iface?: string;
		loading?: boolean;
		unavailableReason?: string;
		onclose: () => void;
	}

	let {
		open,
		kind,
		targetId,
		displayName,
		subject,
		iface,
		loading = false,
		unavailableReason,
		onclose,
	}: Props = $props();

	let diagnosticsTitlePrefix = $derived.by(() => {
		if (kind === 'awg') return 'AWG';
		if (kind === 'system') return 'AWG';
		if (kind === 'singbox') return 'Sing-box';
		return 'Subscription';
	});
	let modalTitle = $derived(m.tunnel_test_title({ prefix: diagnosticsTitlePrefix, name: displayName }));
</script>

<Modal
	{open}
	{onclose}
	title={modalTitle}
	size="xl"
>
	<TunnelDiagnosticsPanel
		{kind}
		{targetId}
		{displayName}
		backHref=""
		backLabel=""
		{subject}
		{iface}
		{loading}
		{unavailableReason}
		mode="modal"
	/>
</Modal>
