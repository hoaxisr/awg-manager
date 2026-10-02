import { m } from '$lib/i18n';
// Подсказка к тумблеру «DNS роутера» в модалках пира (issue #847): запросы к
// роутеру отвечает его резолвер, политика доступа сервера на них не действует.
export const routerDnsHint = (): string => m.servers_router_dns_hint();
