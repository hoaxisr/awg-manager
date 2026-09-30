<script lang="ts">
	import { Modal } from '$lib/components/ui';
	import type { MihomoDiagnosticResourceKind } from '$lib/types';
	import TunnelDiagnosticsPanel from './TunnelDiagnosticsPanel.svelte';

	type DiagnosticsKind = 'awg' | 'system' | 'singbox' | 'subscription' | 'mihomo';
	type DiagnosticsSubjectLabel = 'туннель' | 'подписку';

	interface Props {
		open: boolean;
		kind: DiagnosticsKind;
		targetId: string;
		displayName: string;
		subjectLabel: DiagnosticsSubjectLabel;
		iface?: string;
		resourceKind?: MihomoDiagnosticResourceKind;
		loading?: boolean;
		unavailableReason?: string;
		onclose: () => void;
	}

	let {
		open,
		kind,
		targetId,
		displayName,
		subjectLabel,
		iface,
		resourceKind,
		loading = false,
		unavailableReason,
		onclose,
	}: Props = $props();

	let diagnosticsTitlePrefix = $derived.by(() => {
		if (kind === 'awg') return 'AWG';
		if (kind === 'system') return 'AWG';
		if (kind === 'singbox') return 'Sing-box';
		if (kind === 'mihomo') return 'Mihomo';
		return 'Subscription';
	});
	let modalTitle = $derived(`${diagnosticsTitlePrefix} тестирование: ${displayName}`);
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
		{subjectLabel}
		{iface}
		{resourceKind}
		{loading}
		{unavailableReason}
		mode="modal"
	/>
</Modal>
