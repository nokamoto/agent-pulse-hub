package main

import (
	"context"
	"errors"
	"io"
	"strconv"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/control"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
)

func runNotification(args []string, stdout, stderr io.Writer) int {
	op := args[0]
	required, optional := []string{"--notification-id"}, []string{"--address"}
	if op == "notifications" {
		required = []string{"--subscription-id"}
		optional = append(optional, "--after", "--limit")
	}
	flags, err := parseFlags(args[1:], required, optional)
	if err != nil {
		return report(stderr, op+": "+err.Error())
	}
	id := flags[required[0]]
	if !domain.ValidNotificationID(id) {
		return report(stderr, op+": a canonical non-nil lowercase UUID is required")
	}
	limit := 20
	if value, ok := flags["--limit"]; ok {
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 20 || strconv.Itoa(limit) != value {
			return report(stderr, op+": limit must be an integer from 1 through 20")
		}
	}
	if value, ok := flags["--after"]; ok && !domain.ValidNotificationID(value) {
		return report(stderr, op+": after must be a canonical non-nil lowercase UUID")
	}
	ctx, cancel := context.WithTimeout(context.Background(), controlDeadline)
	defer cancel()
	var data []byte
	if op == "notification" {
		data, err = control.Notification(ctx, addressOrDefault(flags["--address"]), id)
	} else {
		data, err = control.Notifications(ctx, addressOrDefault(flags["--address"]), id, flags["--after"], limit)
	}
	if err != nil {
		var de *control.DaemonError
		if errors.As(err, &de) {
			if writeAll(stdout, de.Wire) != nil {
				return report(stderr, op+": could not write daemon response")
			}
			return 1
		}
		return report(stderr, op+": "+err.Error())
	}
	if writeAll(stdout, data) != nil {
		return report(stderr, op+": could not write inspection")
	}
	return 0
}
