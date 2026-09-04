package config

import (
	"errors"
	"fmt"
	"os"
)

const (
	PrivateDirMode  = 0o700
	PrivateFileMode = 0o600
)

// EnsurePrivateDir creates an application-owned directory and repairs its mode.
func EnsurePrivateDir(path string) error {
	if err := os.MkdirAll(path, PrivateDirMode); err != nil {
		return fmt.Errorf("create private directory %s: %w", path, err)
	}
	if err := os.Chmod(path, PrivateDirMode); err != nil {
		return fmt.Errorf("restrict private directory %s: %w", path, err)
	}
	return nil
}

// OpenPrivateFile opens a sensitive file and repairs the mode of an existing file.
func OpenPrivateFile(path string, flags int) (*os.File, error) {
	file, err := os.OpenFile(path, flags, PrivateFileMode)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(PrivateFileMode); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("restrict private file %s: %w", path, err)
	}
	return file, nil
}

// WritePrivateFile writes sensitive data without leaving an existing file public.
func WritePrivateFile(path string, data []byte) error {
	file, err := OpenPrivateFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// RestrictPrivateFiles repairs existing sensitive files. Missing sidecars are valid.
func RestrictPrivateFiles(paths ...string) error {
	for _, path := range paths {
		if err := os.Chmod(path, PrivateFileMode); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("restrict private file %s: %w", path, err)
		}
	}
	return nil
}
