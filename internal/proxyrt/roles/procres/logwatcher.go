package procres

import (
	"os"
)

type logWatcher interface {
	start()
	stop()
}

func scanLogForFatal(path string, offset *int64) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return false
	}
	size := st.Size()
	if size < *offset {
		*offset = 0 // файл усечён или ротирован
	}
	if size <= *offset {
		return false
	}
	readFrom := *offset
	if size-readFrom > 16384 {
		readFrom = size - 16384
	}
	buf := make([]byte, size-readFrom)
	n, err := f.ReadAt(buf, readFrom)
	*offset = size
	if err != nil && n == 0 {
		return false
	}
	return detectFatalSessionError(string(buf[:n])) != ""
}
