package registration

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"github.com/nokamoto/agent-pulse-hub/plugins/actions/codex-queue/internal/application"
)

type TempDestinationFiles struct{}

var _ application.DestinationFileFactory = TempDestinationFiles{}

func (TempDestinationFiles) Create(threadID string) (string, func() error, error) {
	directory, err := os.MkdirTemp("", "agent-pulse-codex-queue-")
	if err != nil {
		return "", nil, fmt.Errorf("create private destination directory: %w", err)
	}
	path := filepath.Join(directory, "destination.json")
	value := jsonvalue.NewObject(map[string]*jsonvalue.Value{"thread_id": jsonvalue.NewString(threadID)})
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = os.Remove(directory)
		return "", nil, fmt.Errorf("create destination file: %w", err)
	}
	writeErr := writeAll(file, value.Marshal())
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(path)
		_ = os.Remove(directory)
		return "", nil, fmt.Errorf("write destination file: %w", err)
	}
	cleanup := func() error {
		fileErr := os.Remove(path)
		if errors.Is(fileErr, os.ErrNotExist) {
			fileErr = nil
		}
		directoryErr := os.Remove(directory)
		if errors.Is(directoryErr, os.ErrNotExist) {
			directoryErr = nil
		}
		return errors.Join(fileErr, directoryErr)
	}
	return path, cleanup, nil
}

func writeAll(file *os.File, value []byte) error {
	for len(value) > 0 {
		written, err := file.Write(value)
		if err != nil {
			return err
		}
		if written == 0 {
			return errors.New("short write")
		}
		value = value[written:]
	}
	return nil
}
