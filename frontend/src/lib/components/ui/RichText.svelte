<script lang="ts">
	/**
	 * Фраза из словаря с простой разметкой внутри: <strong>, <code>, <a>, <span>,
	 * у каждого — необязательный class="…". Разметка выводится элементами Svelte,
	 * содержимое тегов и всё вокруг — текстом: подставленные в сообщение значения
	 * (имена, адреса) не могут превратиться в HTML. Так фраза остаётся целиком в
	 * одном ключе, а не склеивается из кусков вокруг <strong>.
	 */
	interface Props {
		text: string;
		/** Адрес для <a>…</a> во фразе. */
		href?: string;
	}

	let { text, href }: Props = $props();

	type Tag = 'strong' | 'code' | 'a' | 'span';
	interface Seg {
		tag: Tag | null;
		cls?: string;
		text: string;
	}

	const MARKUP = /<(strong|code|a|span)(?: class="([\w -]+)")?>([\s\S]*?)<\/\1>/g;

	function segments(source: string): Seg[] {
		const out: Seg[] = [];
		let last = 0;
		for (const hit of source.matchAll(MARKUP)) {
			const at = hit.index ?? 0;
			if (at > last) out.push({ tag: null, text: source.slice(last, at) });
			out.push({ tag: hit[1] as Tag, cls: hit[2], text: hit[3] });
			last = at + hit[0].length;
		}
		if (last < source.length) out.push({ tag: null, text: source.slice(last) });
		return out;
	}

	const parts = $derived(segments(text));
</script>

{#each parts as seg, i (i)}{#if seg.tag === 'strong'}<strong class={seg.cls}>{seg.text}</strong>{:else if seg.tag === 'code'}<code class={seg.cls}>{seg.text}</code>{:else if seg.tag === 'span'}<span class={seg.cls}>{seg.text}</span>{:else if seg.tag === 'a'}<a class={seg.cls} {href} target="_blank" rel="noopener noreferrer">{seg.text}</a>{:else}{seg.text}{/if}{/each}
