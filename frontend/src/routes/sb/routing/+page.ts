import { redirect } from '@sveltejs/kit';
import type { PageLoad } from './$types';
import { legacyRoutingTarget } from '$lib/data/legacyRoutingLinks';

// Страницы `/sb/routing` больше нет — подэтап 5D1 разобрал её на группу
// маршрутов. У пользователей остались закладки, поэтому старый адрес раздаёт
// их по новым страницам (таблица — в `legacyRoutingLinks.ts`), а не отдаёт 404.
//
// Редирект в load, а не в компоненте: приложение SPA (`ssr = false`), и при
// заходе по прямой ссылке `navigate({type:'enter'})` идёт с replaceState —
// запись `/sb/routing` замещается целевой, а не добавляется поверх. Тот же
// редирект из `+page.svelte` дал бы вторую запись, и «назад» возвращал бы на
// редирект, который тут же уводил бы вперёд.
export const load: PageLoad = ({ url }) => {
	redirect(308, legacyRoutingTarget(url.searchParams));
};
