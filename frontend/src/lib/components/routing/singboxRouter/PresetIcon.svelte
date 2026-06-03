<script lang="ts">
	import {
		CircleSlash,
		Globe,
		Sparkles,
		Film,
		Gamepad2,
		ShieldCheck,
		ShieldAlert,
		Cpu,
		EyeOff,
		Lock,
		BriefcaseBusiness,
		ShieldOff,
		GlobeLock,
	} from 'lucide-svelte';
	import { brandIcons } from '$lib/generated/brandIcons';
	import { getPresetInlineIcon, type ServiceIconConfig } from '$lib/utils/service-icons';
	import LetterIconTile from '$lib/components/dnsroutes/LetterIconTile.svelte';

	interface Props {
		slug?: string;
		size?: number;
		/** Used for letter monogram when slug has no brand/inline art. */
		label?: string;
	}
	let { slug, size = 36, label = '' }: Props = $props();

	interface BrandIconResolved {
		kind: 'brand';
		path: string;
		hex: string;
	}

	interface LucideIcon {
		kind: 'lucide';
		component: typeof CircleSlash;
		bg: string;
	}

	interface InlineIcon {
		kind: 'inline';
		config: ServiceIconConfig;
	}

	type ResolvedIcon = BrandIconResolved | LucideIcon | InlineIcon | null;

	const lucideMap: Record<string, { component: typeof CircleSlash; bg: string }> = {
		'lucide-circle-slash': { component: CircleSlash, bg: '#dc2626' },
		'lucide-globe': { component: Globe, bg: '#22c55e' },
		'lucide-sparkles': { component: Sparkles, bg: '#8b5cf6' },
		'lucide-film': { component: Film, bg: '#ec4899' },
		'lucide-gamepad-2': { component: Gamepad2, bg: '#14b8a6' },
		'lucide-shield-check': { component: ShieldCheck, bg: '#3b82f6' },
		'lucide-shield-alert': { component: ShieldAlert, bg: '#dc2626' },
		'lucide-cpu': { component: Cpu, bg: '#dc2626' },
		'lucide-eye-off': { component: EyeOff, bg: '#dc2626' },
		'lucide-lock': { component: Lock, bg: '#64748b' },
		'lucide-briefcase-business': { component: BriefcaseBusiness, bg: '#0a66c2' },
		'lucide-shield-off': { component: ShieldOff, bg: '#dc2626' },
		'lucide-globe-lock': { component: GlobeLock, bg: '#64748b' },
	};

	const resolved = $derived.by((): ResolvedIcon => {
		if (!slug) return null;
		const inline = getPresetInlineIcon(slug);
		if (inline) {
			return { kind: 'inline', config: inline };
		}
		const lucide = lucideMap[slug];
		if (lucide) {
			return { kind: 'lucide', component: lucide.component, bg: lucide.bg };
		}
		const brand = brandIcons[slug];
		if (brand) {
			return { kind: 'brand', path: brand.path, hex: '#' + brand.hex };
		}
		return null;
	});

	const inlineInnerSize = $derived.by(() => {
		if (resolved?.kind !== 'inline') return 0;
		const cfg = resolved.config;
		if (cfg.assetSrc && cfg.assetFit === 'cover') return size;
		return Math.round(size * (cfg.scale ?? 0.56));
	});
</script>

<div class="icon-box" style="width:{size}px;height:{size}px">
	{#if resolved === null}
		<LetterIconTile label={label || slug || '?'} {size} />
	{:else if resolved.kind === 'brand'}
		<div class="brand" style="background:{resolved.hex}">
			<svg viewBox="0 0 24 24" width={size * 0.56} height={size * 0.56} fill="white" xmlns="http://www.w3.org/2000/svg">
				<path d={resolved.path} />
			</svg>
		</div>
	{:else if resolved.kind === 'lucide'}
		{@const Component = resolved.component}
		<div class="brand" style="background:{resolved.bg}">
			<Component size={Math.floor(size * 0.56)} color="white" />
		</div>
	{:else if resolved.kind === 'inline'}
		<div class="brand" style="background:{resolved.config.background}">
			{#if resolved.config.assetSrc}
				<img
					class="asset"
					class:cover={resolved.config.assetFit === 'cover'}
					src={resolved.config.assetSrc}
					alt=""
					width={inlineInnerSize}
					height={inlineInnerSize}
					style:filter={resolved.config.assetFilter ?? 'none'}
				/>
			{:else}
				<svg
					viewBox={resolved.config.viewBox ?? '0 0 24 24'}
					width={inlineInnerSize}
					height={inlineInnerSize}
				>
					{@html resolved.config.svg ?? ''}
				</svg>
			{/if}
		</div>
	{/if}
</div>

<style>
	.icon-box {
		flex-shrink: 0;
	}
	.brand {
		width: 100%;
		height: 100%;
		border-radius: 6px;
		display: flex;
		align-items: center;
		justify-content: center;
	}
	.brand .asset {
		object-fit: contain;
	}
	.brand .asset.cover {
		width: 100%;
		height: 100%;
		object-fit: cover;
	}
</style>
