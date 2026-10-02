// Runtime-валидация ответов API против схем, сгенерированных из swagger
// (schemas.gen.ts). Вызывается из центральной точки ApiClient.request():
// невалидный ответ (присутствующее значение неверного типа) — это ошибка
// запроса и в dev, и в prod. Отсутствующие/null-поля валидны by design —
// см. дизайн-решения в scripts/gen-api-schemas.mjs.
import { m } from '$lib/i18n';
import * as v from 'valibot';
import { RESPONSE_SCHEMAS } from './schemas.gen';

export class ApiValidationError extends Error {
	readonly endpoint: string;
	readonly issues: string[];

	constructor(endpoint: string, issues: string[]) {
		super(m.api_invalid_response({ endpoint, issues: issues.join('; ') }));
		this.name = 'ApiValidationError';
		this.endpoint = endpoint;
		this.issues = issues;
	}
}

type TemplatedEntry = {
	method: string;
	segments: string[]; // '{...}' — wildcard-сегмент
	schema: v.GenericSchema;
};

// Статические ключи матчатся O(1) по строке; шаблонные (/servers/{name}/…)
// — посегментно. Разбиение считается один раз на старте модуля.
const templated: TemplatedEntry[] = [];
const staticSchemas = new Map<string, v.GenericSchema>();

for (const [key, schema] of Object.entries(RESPONSE_SCHEMAS)) {
	if (key.includes('{')) {
		const [method, path] = key.split(' ', 2);
		templated.push({ method, segments: path.split('/'), schema });
	} else {
		staticSchemas.set(key, schema);
	}
}

/**
 * Схема 2xx-ответа для метода и endpoint'а клиента (path без /api,
 * query-строка игнорируется). undefined — эндпоинта нет в спеке.
 */
export function findResponseSchema(method: string, endpoint: string): v.GenericSchema | undefined {
	const path = endpoint.split('?', 1)[0];
	const direct = staticSchemas.get(`${method} ${path}`);
	if (direct) return direct;

	const segments = path.split('/');
	outer: for (const entry of templated) {
		if (entry.method !== method || entry.segments.length !== segments.length) continue;
		for (let i = 0; i < segments.length; i++) {
			const pattern = entry.segments[i];
			if (pattern.startsWith('{')) {
				if (!segments[i]) continue outer; // пустой сегмент — не совпадение
			} else if (pattern !== segments[i]) {
				continue outer;
			}
		}
		return entry.schema;
	}
	return undefined;
}

function formatIssue(issue: v.BaseIssue<unknown>): string {
	const path = (issue.path ?? [])
		.map((p) => String((p as { key?: unknown }).key ?? '?'))
		.join('.');
	const got = issue.received;
	return m.api_validate_issue({ path: path || '<root>', expected: String(issue.expected), received: got });
}

/**
 * Проверяет конверт ответа по схеме эндпоинта. Нет схемы — ответ проходит
 * без проверки (эндпоинт вне спеки). Несовпадение — ApiValidationError со
 * списком точных путей расхождения (первые 5).
 */
export function validateApiResponse(method: string, endpoint: string, body: unknown): void {
	const schema = findResponseSchema(method.toUpperCase(), endpoint);
	if (!schema) return;

	const result = v.safeParse(schema, body);
	if (result.success) return;

	const issues = result.issues.slice(0, 5).map(formatIssue);
	if (result.issues.length > 5) {
		issues.push(m.api_validate_more({ count: result.issues.length - 5 }));
	}
	throw new ApiValidationError(endpoint, issues);
}
