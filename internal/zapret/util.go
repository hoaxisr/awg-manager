package zapret

import (
	"os"
	"path/filepath"
	"strings"
)

// findHostlistRef ищет ссылку на список среди файлов каталога zapret.
// Нужна для относительных путей из конфига: nfqws2 резолвит их через
// realpath() относительно каталога демона, и мы обязаны повторить
// именно это, иначе покажем «файл не найден» для существующего списка.
func findHostlistRef(base, ref string) (string, bool) {
	if strings.HasPrefix(ref, "/") {
		st, err := os.Stat(ref)
		if err != nil || st.IsDir() {
			return ref, false
		}
		return ref, true
	}
	candidate := filepath.Join(base, ref)
	if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
		return candidate, true
	}
	return ref, false
}

// refIsExclude отличает список исключений от включающего: у zapret
// «exclude» есть и в имени файла, и в названии ipsets, и в переменной
// конфига nozapret.
func refIsExclude(p string) bool {
	lower := strings.ToLower(p)
	return strings.Contains(lower, "exclude") || strings.Contains(lower, "nozapret")
}
