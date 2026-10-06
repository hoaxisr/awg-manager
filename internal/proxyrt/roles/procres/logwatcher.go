package procres

import (
	"bytes"
	"os"
)

type logWatcher interface {
	start()
	stop()
}

// scanLogForFatal проверяет дозаписанную часть журнала на наличие фатальных
// сигнатур сбоя сессии. Смещение *offset сдвигается строго до последнего перевода
// строки (\n), чтобы строка, записанная клиентом в два приёма, не разрезалась между
// блоками чтения.
func scanLogForFatal(path string, offset *int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	size := st.Size()
	if size < *offset {
		*offset = 0 // файл усечён или ротирован
	}
	if size <= *offset {
		return ""
	}
	readFrom := *offset
	if size-readFrom > 16384 {
		readFrom = size - 16384
	}
	buf := make([]byte, size-readFrom)
	n, err := f.ReadAt(buf, readFrom)
	if err != nil && n == 0 {
		return ""
	}
	data := buf[:n]
	lastNL := bytes.LastIndexByte(data, '\n')
	var toCheck []byte
	if lastNL >= 0 {
		toCheck = data[:lastNL+1]
		*offset = readFrom + int64(lastNL+1)
	} else if len(data) >= 16384 {
		toCheck = data
		*offset = size
	} else {
		// Неполная строка без перевода строки: смещение не продвигаем,
		// чтобы следующий опрос прочитал строку целиком после дозаписи.
		return ""
	}
	return detectFatalSessionError(string(toCheck))
}
