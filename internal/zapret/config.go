package zapret

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Файл config — обычный shell-исходник: init-скрипт делает `. config` и
// разбирает опции демона оттуда. Опции лежат в присваивании вида
//
//	NFQWS2_OPT="
//	--filter-tcp=80 --lua-desync=multisplit --new
//	--filter-tcp=443 --lua-desync=fake --new
//	"
//
// Значение может быть многострочным (обычно так и есть) или однострочным.
// Мы не исполняем файл — eval/shell вставки тут были бы дырой, — а
// вырезаем блок присваивания текстом и меняем только его.

// optBlock — сырое содержимое value переменной опций вместе с позицией
// в файле, чтобы запись была точечной.
type optBlock struct {
	value string
	// start — смещение начала «NFQWS2_OPT=" в файле, end — смещение
	// сразу за закрывающей кавычкой. Между ними в файле лежит ровно
	// то, что мы заменим при записи.
	start, end int
	multiline  bool
}

var (
	// assignmentRe ищет начало присваивания в начале строки:
	// «^\s*NAME=». Якорь на \s* + допуск пустой строки перед — потому
	// что config может начинаться с BOM или с пустых строк.
	assignmentRe = func(name string) *regexp.Regexp {
		return regexp.MustCompile(`(?m)^[ \t]*` + regexp.QuoteMeta(name) + `=`)
	}
)

// readOptBlock вырезает значение переменной optVar из configPath.
func readOptBlock(configPath, optVar string) (optBlock, bool) {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return optBlock{}, false
	}
	body := string(raw)

	loc := assignmentRe(optVar).FindStringIndex(body)
	if loc == nil {
		return optBlock{}, false
	}
	rest := body[loc[1]:]

	// Присваивание без кавычек (NFQWS_OPT=--filter-tcp=80 ...) —
	// валидный shell, разбираем до конца строки.
	if !strings.HasPrefix(strings.TrimLeft(rest, " \t"), `"`) {
		line, _ := readLogicalLine(rest)
		value := strings.TrimSpace(line)
		// Хвост комментария у однострочного варианта отбрасываем:
		// иначе в опции демона уедут символы «# …».
		if i := strings.Index(value, " #"); i >= 0 {
			value = strings.TrimSpace(value[:i])
		}
		return optBlock{
			value:     value,
			start:     loc[0],
			end:       loc[1] + len(line),
			multiline: false,
		}, true
	}

	// Многострочное: идём от открывающей кавычки и ищем закрывающую,
	// замыкающую присваивание. Кавычка завершает значение, только если
	// после неё до конца строки — пусто или комментарий. Иначе это
	// кавычка внутри значения, и присваивание ещё не кончилось.
	_, valueStart, valueEnd, consumed, ok := scanQuotedValue(rest)
	if !ok {
		// Незакрытая кавычка — битый конфиг. Не гадаем: отдаём
		// однострочную трактовку, пусть зритель увидит мусор и
		// починит сам, а не получит молчаливую потерю хвоста.
		line, c := readLogicalLine(rest)
		return optBlock{value: strings.TrimSpace(line), start: loc[0], end: loc[1] + c}, true
	}

	// Многострочность определяем по самому значению: если в нём есть
	// перевод строки, исходная форма была многострочной и при записи её
	// надо восстановить (иначе весь блок схлопнется в одну длинную
	// строку и диффы конфига у пользователя станут нечитаемыми).
	return optBlock{
		value:     rest[valueStart:valueEnd],
		start:     loc[0],
		end:       loc[1] + consumed,
		multiline: strings.Contains(rest[valueStart:valueEnd], "\n"),
	}, true
}

// scanQuotedValue ищет закрывающую кавычку присваивания, начиная с
// первого непробельного символа rest (имет открывающую кавычку).
func scanQuotedValue(rest string) (closeIdx, valueStart, valueEnd, consumed int, ok bool) {
	i := 0
	for i < len(rest) && (rest[i] == ' ' || rest[i] == '\t') {
		i++
	}
	if i >= len(rest) || rest[i] != '"' {
		return 0, 0, 0, 0, false
	}
	openIdx := i
	valueStart = openIdx + 1

	for j := valueStart; j < len(rest); j++ {
		if rest[j] == '\\' && j+1 < len(rest) {
			j++
			continue
		}
		if rest[j] == '"' && closesAssignment(rest[j:]) {
			return j, valueStart, j, j + 1, true
		}
	}
	return 0, 0, 0, 0, false
}

// closesAssignment сообщает, завершает ли кавычка в позиции p значение
// переменной: дальше до перевода строки должно быть пусто либо комментарий.
func closesAssignment(tail string) bool {
	line := tail[1:]
	if nl := strings.IndexByte(line, '\n'); nl >= 0 {
		line = line[:nl]
	}
	line = strings.TrimSpace(line)
	return line == "" || strings.HasPrefix(line, "#")
}

// readLogicalLine возвращает первую строку и число съеденных байт.
func readLogicalLine(s string) (line string, consumed int) {
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		return s[:nl], nl + 1
	}
	return s, len(s)
}

// findAssignment достаёт значение переменной без учёта позиций — нужно
// только чтобы понять, «задана ли она» (см. detectOptVar).
func findAssignment(body, name string) string {
	blk, ok := readOptBlockFromString(body, name)
	if !ok {
		return ""
	}
	return strings.TrimSpace(blk.value)
}

func readOptBlockFromString(body, name string) (optBlock, bool) {
	loc := assignmentRe(name).FindStringIndex(body)
	if loc == nil {
		return optBlock{}, false
	}
	rest := body[loc[1]:]
	if !strings.HasPrefix(strings.TrimLeft(rest, " \t"), `"`) {
		line, _ := readLogicalLine(rest)
		return optBlock{value: strings.TrimSpace(line), start: loc[0], end: loc[1] + len(line)}, true
	}
	_, vs, ve, consumed, ok := scanQuotedValue(rest)
	if !ok {
		return optBlock{}, false
	}
	return optBlock{
		value:     rest[vs:ve],
		start:     loc[0],
		end:       loc[1] + consumed,
		multiline: strings.Contains(rest[vs:ve], "\n"),
	}, true
}

// splitProfiles делит значение опций по маркеру --new. Порядок
// получившихся строк = порядок проверки фильтров в nfqws2, и он же
// приоритет: выигрывает первый подошедший. Поэтому перестановка
// карточек в UI обязана менять файл, а не только вид. Пустые профили
// (после двух подряд --new или от хвостового перевода строки) отбрасываем:
// это артефакт форматирования, а не стратегия. Значение уже в памяти,
// поэтому режем по строкам без сканера: у того предел длины строки, и
// длинный профиль молча обрезал бы список.
func splitProfiles(value string) []string {
	var out []string
	var cur strings.Builder
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// --new может стоять в середине строки (все опции профиля
		// склеены в одну), поэтому режем всю строку, а не префикс.
		chunks := splitOnNewMarker(line)
		for i, chunk := range chunks {
			if chunk = strings.TrimSpace(chunk); chunk != "" {
				cur.WriteString(chunk)
				cur.WriteString(" ")
			}
			// Every separator closes the preceding profile, even when
			// another profile starts on the same physical line.
			if i < len(chunks)-1 {
				if profile := strings.TrimSpace(cur.String()); profile != "" {
					out = append(out, profile)
				}
				cur.Reset()
			}
		}
	}
	if tail := strings.TrimSpace(cur.String()); tail != "" {
		out = append(out, tail)
	}
	return out
}

// newMarkerToken — «--new» либо «--new=имя», ровно отдельным токеном.
// Утверждение с lookahead в Go/re2 недоступно, поэтому границу слова
// проверяем вручную в findNewMarkers.
var newMarkerRe = regexp.MustCompile(`(?:^|\s)--new(?:=[^\s]+)?`)

// markerEndsToken сообщает, что совпадение marker заняло весь остаток
// строки — то есть это последний токен, а не начало «--newline».
func markerEndsToken(line string, end int) bool {
	return end >= len(line) || line[end] == ' ' || line[end] == '\t'
}

// findNewMarkers возвращает позиции маркеров «--new», отбрасывая
// ложные срабатывания вроде «--newline=1».
func findNewMarkers(line string) [][]int {
	var out [][]int
	for _, loc := range newMarkerRe.FindAllStringIndex(line, -1) {
		if markerEndsToken(line, loc[1]) {
			out = append(out, loc)
		}
	}
	return out
}

// splitOnNewMarker режет строку по всем маркерам --new.
func splitOnNewMarker(line string) []string {
	locs := findNewMarkers(line)
	if len(locs) == 0 {
		return []string{line}
	}
	var out []string
	prev := 0
	for _, l := range locs {
		out = append(out, line[prev:l[0]])
		prev = l[1]
	}
	return append(out, line[prev:])
}

// serializeProfiles склеивает профили обратно в тело переменной опций.
// Каждый профиль — своя строка, --new в конце, кроме последнего: ровно
// та форма, которую пишут install_easy.sh и KZM2, поэтому диффы
// конфига у пользователя останутся читаемыми.
func serializeProfiles(items []string) string {
	if len(items) == 0 {
		return "\n"
	}
	var b strings.Builder
	for i, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		b.WriteString(item)
		if i < len(items)-1 {
			b.WriteString(" --new")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// writeOptBlock заменяет ТОЛЬКО блок присваивания, оставляя остальной
// конфиг байт в байт. Так же поступает KZM2: почему — обновление z2k
// переписывает файл целиком, но и наша точечная правка не должна
// затирать ключи, которых мы не знаем.
func writeOptBlock(configPath, optVar, value string, mode os.FileMode) error {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read zapret config: %w", err)
	}
	body := string(raw)

	blk, ok := readOptBlockFromString(body, optVar)
	if !ok {
		return fmt.Errorf("zapret config has no %s assignment", optVar)
	}

	// Raw profiles are shell templates, not executable shell fragments. Keep
	// simple variable references used by installed configs, but quote everything
	// that could close the assignment or perform command substitution.
	value = quoteOptTemplate(value)
	replacement := optVar + "="
	if blk.multiline {
		replacement += "\"\n" + strings.TrimRight(value, "\n") + "\n\""
	} else {
		replacement += "\"" + strings.TrimSpace(value) + "\""
	}

	next := body[:blk.start] + replacement + body[blk.end:]
	return writeAtomic(configPath, []byte(next), mode)
}

// quoteOptTemplate returns the contents of a double-quoted shell assignment.
// Preserve existing shell escapes so repeated saves do not accumulate them.
// Only $NAME and ${NAME} may expand; substitutions and parameter operators
// remain literal text. Raw приходит из уже записанного конфига, поэтому его
// экранирование при повторной записи сохраняется как есть.
func quoteOptTemplate(value string) string {
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch c {
		case '\\':
			if i+1 < len(value) && strings.ContainsRune("\\\"$`", rune(value[i+1])) {
				out.WriteByte(c)
				i++
				out.WriteByte(value[i])
			} else {
				out.WriteString(`\\`)
			}
		case '"', '`':
			out.WriteByte('\\')
			out.WriteByte(c)
		case '$':
			if n := simpleVariableLength(value[i:]); n > 0 {
				out.WriteString(value[i : i+n])
				i += n - 1
			} else {
				out.WriteString(`\$`)
			}
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}

func simpleVariableLength(value string) int {
	start := 1
	braced := len(value) > 1 && value[1] == '{'
	if braced {
		start++
	}
	if start >= len(value) || !shellNameStart(value[start]) {
		return 0
	}
	i := start + 1
	for i < len(value) && (shellNameStart(value[i]) || value[i] >= '0' && value[i] <= '9') {
		i++
	}
	if braced {
		if i >= len(value) || value[i] != '}' {
			return 0
		}
		i++
	}
	return i
}

func shellNameStart(c byte) bool {
	return c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}

// writeAtomic кладёт содержимое рядом с целью и переименовывает: на
// умирающем NAND (а это часто внутренняя память Keenetic) прямая
// запись по месту оставляет обрезанный файл при потере питания, и тогда
// zapret не стартует вовсе. Так же пишет сам KZM2.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".zapret-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace: %w", err)
	}
	return nil
}

// validateProfiles отсекает мусор до записи: профиль без опций демона
// почти наверняка означает, что мы разрезали строку не там, а параметр с
// переводом строки внутри испортит разбор getopt у nfqws2.
func validateProfiles(items []string) error {
	if len(items) == 0 {
		return fmt.Errorf("no strategies supplied")
	}
	if len(items) > maxProfiles {
		return fmt.Errorf("too many strategies: %d (maximum %d)", len(items), maxProfiles)
	}
	for i, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			return fmt.Errorf("strategy %d is empty", i+1)
		}
		if strings.ContainsAny(item, "\n\r\x00") {
			return fmt.Errorf("strategy %d contains a line break or NUL", i+1)
		}
		if strings.HasPrefix(item, "-") && !strings.HasPrefix(item, "--") {
			return fmt.Errorf("strategy %d starts with an incomplete option: %q", i+1, item)
		}
		if !strings.Contains(item, "--") {
			return fmt.Errorf("strategy %d contains no daemon options", i+1)
		}
	}
	return nil
}

// fileMode возвращает режим существующего файла, чтобы запись его не
// изменила: у пользователя могут быть выставлены нестандартные права.
func fileMode(path string) os.FileMode {
	st, err := os.Stat(path)
	if err != nil {
		return 0o644
	}
	return st.Mode().Perm()
}
