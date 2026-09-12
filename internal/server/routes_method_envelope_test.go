package server

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Отказ по методу у ручек /api обязан приезжать КОНВЕРТОМ API, а не plain
// text: фронт разбирает тело как JSON (`response.MethodNotAllowed`), а
// `http.Error` отдаёт строку — и разбор падает уже на ответе об ошибке,
// подменяя причину.
//
// Страж статический, потому что альтернатива — поднимать весь сервер ради
// проверки четырёх закрытий маршрутов. Тот же приём, что у
// TestResourceKeys_NoLiteralPublishers.
//
// Единственное исключение — спецификация OpenAPI: она отдаёт YAML, её
// потребитель JSON-конверта не ждёт вовсе, и подсовывать ему чужую форму
// смысла нет. Исключение ИМЕННОЕ: список из одного элемента заставляет
// объяснять каждое следующее.
func TestRouteClosures_MethodRefusalUsesAPIEnvelope(t *testing.T) {
	src, err := os.ReadFile("server_routes.go")
	if err != nil {
		t.Fatalf("чтение server_routes.go: %v", err)
	}
	lines := strings.Split(string(src), "\n")

	bad := regexp.MustCompile(`http\.Error\(\s*w\s*,\s*"Method not allowed"`)
	var offenders []string
	for i, line := range lines {
		if !bad.MatchString(line) {
			continue
		}
		if isOpenAPISpecClosure(lines, i) {
			continue
		}
		offenders = append(offenders, lines[i])
	}
	if len(offenders) > 0 {
		t.Fatalf("отказ по методу отдаётся plain text вместо конверта API (%d шт.): %v\n"+
			"используйте response.MethodNotAllowed(w)", len(offenders), offenders)
	}
}

// isOpenAPISpecClosure — принадлежит ли строка закрытию, отдающему
// спецификацию OpenAPI. Опознаётся по соседству, а не по номеру строки:
// номер уедет при первой же правке файла.
func isOpenAPISpecClosure(lines []string, at int) bool {
	const window = 12
	from := at - window
	if from < 0 {
		from = 0
	}
	to := at + window
	if to > len(lines) {
		to = len(lines)
	}
	ctx := strings.Join(lines[from:to], "\n")
	return strings.Contains(ctx, "openAPIHandler") || strings.Contains(ctx, "application/yaml")
}
