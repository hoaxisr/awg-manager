package hydraroute

import (
	"fmt"
	"os"
)

// readOrEmpty reads path or returns empty string if the file does not exist.
// A missing file is a valid state (HR Neo installed but nothing written yet).
func readOrEmpty(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(data), nil
}
