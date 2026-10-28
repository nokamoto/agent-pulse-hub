package modeloutput

import (
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"go.uber.org/mock/gomock"
)

type notificationDirectoryInfo struct{}

func (notificationDirectoryInfo) Name() string       { return "parent" }
func (notificationDirectoryInfo) Size() int64        { return 0 }
func (notificationDirectoryInfo) Mode() os.FileMode  { return os.ModeDir | 0o700 }
func (notificationDirectoryInfo) ModTime() time.Time { return time.Time{} }
func (notificationDirectoryInfo) IsDir() bool        { return true }
func (notificationDirectoryInfo) Sys() any           { return nil }

func TestNotificationModelOutputFirstWriteBoundary(t *testing.T) {
	for _, test := range []struct {
		name               string
		openErr            error
		writeN             int
		writeErr, closeErr error
		status             string
		retry              bool
	}{
		{name: "permission", openErr: os.ErrPermission, status: "unsubmitted"},
		{name: "temporary_resource", openErr: syscall.EMFILE, status: "unsubmitted", retry: true},
		{name: "write_invoked_without_bytes", writeErr: io.ErrClosedPipe, status: "unknown"},
		{name: "partial_write", writeN: 3, writeErr: io.ErrClosedPipe, status: "unknown"},
		{name: "close_failure", closeErr: io.ErrClosedPipe, status: "unknown"},
		{name: "accepted", status: "accepted"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			files := NewMockoutputFiles(ctrl)
			path, err := filepath.Abs(filepath.Join("notification-memory-parent", "events.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			files.EXPECT().Stat(filepath.Dir(path)).Return(notificationDirectoryInfo{}, nil)
			files.EXPECT().Stat(path).Return(nil, os.ErrNotExist)
			if test.openErr != nil {
				files.EXPECT().OpenAppend(path).Return(nil, test.openErr)
			} else {
				file := NewMockoutputFile(ctrl)
				files.EXPECT().OpenAppend(path).Return(file, nil)
				file.EXPECT().Write(gomock.Any()).DoAndReturn(func(data []byte) (int, error) {
					if test.writeErr != nil {
						return test.writeN, test.writeErr
					}
					return len(data), nil
				}).Times(1)
				file.EXPECT().Close().Return(test.closeErr)
			}
			f := testDelivery(destination(path))
			f.RequestID = "11111111-1111-4111-8111-111111111111"
			f.DeliveryID = "22222222-2222-4222-8222-222222222222"
			result := newWithFiles(nil, files).deliverNotification(f)
			if result.Status != test.status || result.RequestID != f.RequestID || result.DeliveryID != f.DeliveryID {
				t.Fatalf("result=%+v", result)
			}
			if result.Status == "unsubmitted" && (result.Retryable == nil || *result.Retryable != test.retry) {
				t.Fatal("invalid retry proof")
			}
			if result.Status != "unsubmitted" && result.Retryable != nil {
				t.Fatal("post-write retry permission")
			}
		})
	}
}
