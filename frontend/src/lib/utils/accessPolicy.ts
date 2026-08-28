/** Built-in NDMS access policy (Policy0..PolicyN). */
export function isStandardAccessPolicyName(name: string): boolean {
	return /^Policy\d+$/.test(name);
}

/** HydraRoute Neo / other subsystem policy (custom NDMS name). */
export function isHydraRouteAccessPolicy(policy: { name: string; isStandard?: boolean }): boolean {
	return policy.isStandard === false || !isStandardAccessPolicyName(policy.name);
}

type PolicyMembership = {
	name: string;
	interfaces?: { name: string; denied?: boolean }[];
};

/**
 * Обратный поиск «интерфейс → политика» по ответу GET /access-policies:
 * отдельной ручки «в какой я политике» у бэкенда нет, состав политик читается
 * только целиком. Запрещённый интерфейс (`no permit`) в политике числится, но
 * трафик через неё не идёт — членством не считается. Интерфейс может стоять
 * в нескольких политиках; побеждает первая по порядку ответа.
 *
 * Возвращается политика целиком: показывать пользователю нужно её описание, а
 * не внутреннее NDMS-имя (`description || name`, как на «Маршрутизации»).
 */
export function findPolicyForInterface<P extends PolicyMembership>(
	policies: P[],
	ifaceName: string
): P | null {
	const wanted = ifaceName.trim().toLowerCase();
	if (!wanted) return null;
	for (const policy of policies) {
		for (const iface of policy.interfaces ?? []) {
			// Регистр имени: в конфигурации попадается legacy-написание opkgtunN.
			if (!iface.denied && iface.name.toLowerCase() === wanted) return policy;
		}
	}
	return null;
}
