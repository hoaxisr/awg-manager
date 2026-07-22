// Package namegen генерирует случайные русскоязычные display name'ы для
// идентичностей клиента. Пулы имён встроены из data/*.txt.
package namegen

import (
	_ "embed"
	"strings"

	"github.com/samosvalishe/free-turn-proxy/internal/randx"
)

//go:embed data/first_names_male.txt
var rawMaleFirstNames string

//go:embed data/first_names_female.txt
var rawFemaleFirstNames string

//go:embed data/last_names.txt
var rawLastNames string

var (
	maleFirstNames   = parseLines(rawMaleFirstNames)
	femaleFirstNames = parseLines(rawFemaleFirstNames)
	lastNames        = parseLines(rawLastNames)
)

func parseLines(s string) []string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// feminizeSurname переводит мужскую фамилию в женскую форму.
// Работает по рунам - кириллица многобайтовая в UTF-8.
// Фамилии с не-русскими суффиксами возвращаются без изменений
// (иностранные / мемные).
func feminizeSurname(surname string) string {
	rs := []rune(surname)
	n := len(rs)
	if n < 2 {
		return surname
	}
	switch string(rs[n-2:]) {
	case "ий", "ый", "ой":
		return string(rs[:n-2]) + "ая"
	case "ов", "ев", "ёв", "ин", "ын":
		return surname + "а"
	}
	return surname
}

// SurnameProbability - вероятность того, что Generate вернёт имя с фамилией.
const SurnameProbability = 0.7

// Generate возвращает случайное "Имя" или "Имя Фамилия".
// Пол выбирается равномерно; женские фамилии феминизируются.
func Generate() string {
	isFemale := randx.IntN(2) == 0

	pool := maleFirstNames
	if isFemale {
		pool = femaleFirstNames
	}
	fn := pool[randx.IntN(len(pool))]

	if randx.Float64() >= SurnameProbability {
		return fn
	}

	ln := lastNames[randx.IntN(len(lastNames))]
	if isFemale {
		ln = feminizeSurname(ln)
	}
	return fn + " " + ln
}
