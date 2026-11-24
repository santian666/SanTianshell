package diaglog

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

const maxLogSize = 5 << 20

func Open(directory string) (*os.File, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(directory, "app.log")
	if info, err := os.Stat(path); err == nil && info.Size() >= maxLogSize {
		backup := path + ".1"
		_ = os.Remove(backup)
		if err = os.Rename(path, backup); err != nil {
			return nil, err
		}
	}
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
}

func Logger(file io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.MultiWriter(os.Stderr, file), &slog.HandlerOptions{Level: slog.LevelInfo}))
}
