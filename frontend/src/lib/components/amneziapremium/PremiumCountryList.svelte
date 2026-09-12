<script lang="ts">
	import type { AmneziaPremiumCountry, AmneziaPremiumIssuedConfig } from '$lib/types';
	import {
		isPremiumCountryAvailable,
		premiumCountryFlag,
		premiumCountryLabel
	} from '$lib/utils/amneziaPremiumCatalog';

	interface Props {
		countries: AmneziaPremiumCountry[];
		issued: AmneziaPremiumIssuedConfig[];
		/** Туннели с меткой страны подписки: из них берётся метка «туннель awg-nl». */
		countryTunnels: readonly { name: string; amneziaCountry?: string }[];
		selected: string;
		disabled: boolean;
		onselect: (code: string) => void;
	}

	let { countries, issued, countryTunnels, selected, disabled, onselect }: Props = $props();

	// Протокол, которым мастер умеет забрать конфигурацию, решает хелпер:
	// vless-only страну импортировать нечем, и показывать её — обещать
	// действие, которое кончится отказом.
	const usable = $derived(countries.filter(isPremiumCountryAvailable));
</script>

<ul class="premium-countries" role="listbox" aria-label="Страны подписки">
	{#each usable as country (country.code)}
		{@const flag = premiumCountryFlag(country.code)}
		{@const label = premiumCountryLabel(country.code, issued, countryTunnels)}
		<li>
			<button
				type="button"
				class="premium-country"
				class:premium-country--selected={country.code === selected}
				role="option"
				aria-selected={country.code === selected}
				{disabled}
				onclick={() => onselect(country.code)}
			>
				{#if flag}<span class="premium-country-flag">{flag}</span>{/if}
				<span class="premium-country-name">{country.name}</span>
				{#if label}
					<span class="premium-country-label premium-country-label--{label.kind}">{label.text}</span>
				{/if}
			</button>
		</li>
	{:else}
		<li class="premium-countries-empty">Подписка не отдаёт ни одной страны по AmneziaWG.</li>
	{/each}
</ul>

<style>
	/* Список прокручиваемый: в живом ответе портала 21 страна, и без предела
	   по высоте подвал с кнопкой уезжает за край модалки. */
	.premium-countries {
		list-style: none;
		margin: 0;
		padding: 0;
		max-height: 260px;
		overflow-y: auto;
		border: 1px solid var(--border, var(--color-border));
		border-radius: 8px;
	}

	.premium-country {
		display: flex;
		align-items: center;
		gap: 8px;
		width: 100%;
		padding: 8px 10px;
		background: transparent;
		border: none;
		border-bottom: 1px solid var(--border, var(--color-border));
		color: var(--text-primary, var(--color-text-primary));
		font-size: 0.875rem;
		text-align: left;
		cursor: pointer;
	}

	.premium-countries li:last-child .premium-country {
		border-bottom: none;
	}

	.premium-country:hover:not(:disabled) {
		background: var(--bg-secondary, var(--color-bg-secondary));
	}

	.premium-country--selected {
		background: var(--color-accent-tint);
		box-shadow: inset 2px 0 0 var(--accent, var(--color-accent));
	}

	.premium-country:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.premium-country-flag {
		font-size: 1.125rem;
		line-height: 1;
	}

	.premium-country-name {
		flex: 1;
		min-width: 0;
		overflow-wrap: anywhere;
	}

	.premium-country-label {
		flex-shrink: 0;
		padding: 1px 6px;
		border-radius: 999px;
		font-size: 0.6875rem;
		background: var(--bg-secondary, var(--color-bg-secondary));
		color: var(--text-muted, var(--color-text-muted));
	}

	.premium-country-label--stale {
		background: var(--color-warning-tint);
		color: var(--warning, var(--color-warning));
	}

	.premium-country-label--tunnel {
		color: var(--accent, var(--color-accent));
	}

	.premium-countries-empty {
		padding: 12px 10px;
		font-size: 0.8125rem;
		color: var(--text-muted, var(--color-text-muted));
	}
</style>
