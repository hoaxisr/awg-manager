package namegen

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseLinesStripsBlanksAndWhitespace(t *testing.T) {
	in := "  Иван  \n\nПётр\n\t\nСидор\n"
	got := parseLines(in)
	want := []string{"Иван", "Пётр", "Сидор"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("idx %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestFeminizeSurname(t *testing.T) {
	cases := map[string]string{
		// -ов / -ев / -ёв / -ин / -ын -> +а
		"Иванов":     "Иванова",
		"Лебедев":    "Лебедева",
		"Соловьёв":   "Соловьёва",
		"Никитин":    "Никитина",
		"Солженицын": "Солженицына",
		// -ий / -ый / -ой -> -ая
		"Чайковский": "Чайковская",
		"Белый":      "Белая",
		"Толстой":    "Толстая",
		"Великий":    "Великая",
		"Раевский":   "Раевская",
		// Foreign / meme: unchanged
		"Скайуокер": "Скайуокер",
		"Поттер":    "Поттер",
		"Бонд":      "Бонд",
		"Сноу":      "Сноу",
		"Корлеоне":  "Корлеоне",
		// Edge cases
		"":  "",
		"А": "А",
	}
	for in, want := range cases {
		if got := feminizeSurname(in); got != want {
			t.Errorf("feminizeSurname(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPoolsLoaded(t *testing.T) {
	if len(maleFirstNames) < 100 {
		t.Errorf("male pool too small: %d", len(maleFirstNames))
	}
	if len(femaleFirstNames) < 50 {
		t.Errorf("female pool too small: %d", len(femaleFirstNames))
	}
	if len(lastNames) < 100 {
		t.Errorf("last names pool too small: %d", len(lastNames))
	}
	t.Logf("pools: male=%d female=%d last=%d", len(maleFirstNames), len(femaleFirstNames), len(lastNames))
}

func TestPoolsNoBlankNoDuplicates(t *testing.T) {
	for _, p := range []struct {
		name string
		data []string
	}{
		{"male", maleFirstNames},
		{"female", femaleFirstNames},
		{"last", lastNames},
	} {
		seen := map[string]bool{}
		for _, v := range p.data {
			if v == "" {
				t.Errorf("%s pool: blank entry", p.name)
			}
			if !utf8.ValidString(v) {
				t.Errorf("%s pool: invalid utf8 %q", p.name, v)
			}
			if seen[v] {
				t.Errorf("%s pool: duplicate %q", p.name, v)
			}
			seen[v] = true
		}
	}
}

func TestGenerateProducesPlausibleName(t *testing.T) {
	// Build prefix set of valid first names (may contain spaces, e.g. "Абд аль-Узза").
	firstNameSet := map[string]bool{}
	for _, n := range maleFirstNames {
		firstNameSet[n] = true
	}
	for _, n := range femaleFirstNames {
		firstNameSet[n] = true
	}

	withSurname := 0
	const iters = 2000
	for range iters {
		n := Generate()
		if n == "" {
			t.Fatal("Generate returned empty string")
		}
		// Either the whole string is a first name, or it splits into
		// "<first name> <surname>" at the LAST space.
		if firstNameSet[n] {
			continue
		}
		idx := strings.LastIndex(n, " ")
		if idx < 0 {
			t.Errorf("unrecognized name %q", n)
			continue
		}
		first, last := n[:idx], n[idx+1:]
		if !firstNameSet[first] {
			t.Errorf("unknown first-name prefix in %q", n)
		}
		if last == "" {
			t.Errorf("empty surname in %q", n)
		}
		withSurname++
	}
	// SurnameProbability = 0.7; allow generous slack to avoid flake.
	ratio := float64(withSurname) / float64(iters)
	if ratio < 0.6 || ratio > 0.8 {
		t.Errorf("surname ratio %.2f out of expected band [0.60, 0.80]", ratio)
	}
}

func TestGenerateCoversBothGenders(t *testing.T) {
	maleSet := map[string]bool{}
	for _, n := range maleFirstNames {
		maleSet[n] = true
	}
	femaleSet := map[string]bool{}
	for _, n := range femaleFirstNames {
		femaleSet[n] = true
	}

	sawMale, sawFemale := false, false
	for i := 0; i < 5000 && (!sawMale || !sawFemale); i++ { //nolint:intrange // need early exit
		n := Generate()
		first := strings.SplitN(n, " ", 2)[0]
		if maleSet[first] {
			sawMale = true
		}
		if femaleSet[first] {
			sawFemale = true
		}
	}
	if !sawMale {
		t.Error("no male names seen in 5000 iterations")
	}
	if !sawFemale {
		t.Error("no female names seen in 5000 iterations")
	}
}
