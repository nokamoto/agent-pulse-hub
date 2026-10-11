//go:build windows

package windowsprocess

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/mock/gomock"
	"golang.org/x/sys/windows"
)

func TestContainedStartOrdersAssignmentBeforeResumeAndCleansPartialFailures(t *testing.T) {
	for _, stage := range []string{"job", "start", "assign", "resume", "success"} {
		t.Run(stage, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			system, process := NewMocknativeSystem(ctrl), NewMocknativeProcess(ctrl)
			failure := errors.New(stage)
			job := windows.Handle(123)
			var calls []any
			jobErr := error(nil)
			if stage == "job" {
				jobErr = failure
			}
			calls = append(calls, system.EXPECT().CreateJob().Return(job, jobErr))
			if stage != "job" {
				calls = append(calls, system.EXPECT().NewCommand(gomock.Any(), "child", []string{"arg"}, nil, nil, nil).Return(process), process.EXPECT().SetCancel(gomock.Any()))
				startErr := error(nil)
				if stage == "start" {
					startErr = failure
				}
				calls = append(calls, process.EXPECT().Start().Return(startErr))
				if stage != "start" {
					assignErr := error(nil)
					if stage == "assign" {
						assignErr = failure
					}
					calls = append(calls, process.EXPECT().PID().Return(uint32(5)), system.EXPECT().Assign(job, uint32(5)).Return(assignErr))
					if stage != "assign" {
						resumeErr := error(nil)
						if stage == "resume" {
							resumeErr = failure
						}
						calls = append(calls, process.EXPECT().PID().Return(uint32(5)), system.EXPECT().Resume(uint32(5)).Return(resumeErr))
					}
					if stage != "success" {
						calls = append(calls, system.EXPECT().TerminateJob(job).Return(nil), process.EXPECT().Kill().Return(nil), process.EXPECT().Wait().Return(nil))
					}
				}
				calls = append(calls, system.EXPECT().TerminateJob(job).Return(nil), system.EXPECT().CloseJob(job).Return(nil))
			}
			gomock.InOrder(calls...)
			child, err := start(context.Background(), "child", []string{"arg"}, nil, nil, nil, system)
			if stage == "success" {
				if err != nil {
					t.Fatal(err)
				}
				child.Close()
				child.Close()
			} else if !errors.Is(err, failure) {
				t.Fatalf("error %v want %v", err, failure)
			}
		})
	}
}
