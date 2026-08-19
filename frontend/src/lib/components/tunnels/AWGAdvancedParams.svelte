<script lang="ts">
	import { ASCEditor } from '$lib/components/asc';

	interface AWGFormFields {
		mtu: number;
		jc: number;
		jmin: number;
		jmax: number;
		s1: number;
		s2: number;
		s3: number;
		s4: number;
		h1: string;
		h2: string;
		h3: string;
		h4: string;
		i1: string;
		i2: string;
		i3: string;
		i4: string;
		i5: string;
		headerProtectionKey?: string;
		contentPaddingAddition?: string;
		rekeyAfterTime?: string;
		rekeyTimeout?: string;
		rejectAfterTime?: string;
		keepaliveTimeout?: string;
		maxHandshakeAttempts?: string;
		[key: string]: unknown;
	}

	interface AWGErrorFields {
		jc?: string[];
		jmin?: string[];
		jmax?: string[];
		s1?: string[];
		s2?: string[];
		s3?: string[];
		s4?: string[];
		h1?: string[];
		h2?: string[];
		h3?: string[];
		h4?: string[];
		i1?: string[];
		i2?: string[];
		i3?: string[];
		i4?: string[];
		i5?: string[];
		[key: string]: string[] | undefined;
	}

	let {
		form = $bindable(),
		errors,
		hints = undefined,
		compact = false,
		awg3 = false,
	}: {
		form: AWGFormFields;
		errors: AWGErrorFields;
		hints?: Record<string, string>;
		compact?: boolean;
		awg3?: boolean;
	} = $props();
</script>

<ASCEditor
	bind:params={form}
	extended
	{awg3}
	mtu={form.mtu}
	{errors}
	{hints}
	{compact}
/>
