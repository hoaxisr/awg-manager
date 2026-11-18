package config

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
)

func ReadTokenKey(path string) ([sha256.Size]byte, error) {
	var key [sha256.Size]byte
	file, err := os.Open(path)
	if err != nil {
		return key, fmt.Errorf("token_key_file: %w (provision a persistent 32-byte key before starting the relay)", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return key, err
	}
	if !info.Mode().IsRegular() || info.Size() != int64(len(key)) {
		return key, errors.New("token_key_file must be a regular file containing exactly 32 random bytes")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return key, errors.New("token_key_file must not be accessible by group or others")
	}
	_, err = io.ReadFull(file, key[:])
	return key, err
}
