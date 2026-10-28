package modeloutput

import (
	"errors"
	"syscall"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
)

func (p *Plugin) deliverNotification(f pluginprotocol.Frame) pluginprotocol.Frame {
	p.deliveryMu.Lock()
	defer p.deliveryMu.Unlock()
	r := pluginprotocol.Frame{Type: "notification_result", RequestID: f.RequestID, DeliveryID: f.DeliveryID, Status: "unsubmitted", Reason: "model_destination_invalid", Message: "Output destination is invalid.", Retryable: boolPointer(false)}
	if err := p.validate(f.DestinationArgs); err != nil {
		return r
	}
	line, err := encodeRecord(f, time.Now().UTC())
	if err != nil {
		return r
	}
	file, err := p.files.OpenAppend(destinationPath(f.DestinationArgs))
	if err != nil {
		r.Reason = "model_open_failed"
		r.Message = "Output file could not be opened."
		if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) || errors.Is(err, syscall.ENOMEM) {
			r.Reason = "model_open_resource"
			r.Retryable = boolPointer(true)
		}
		return r
	}
	_, writeErr := writeRecord(file, append(line, '\n'))
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		r.Status = "unknown"
		r.Reason = "model_write_unknown"
		r.Message = "Output write or close did not confirm acceptance."
		r.Retryable = nil
		return r
	}
	r.Status = "accepted"
	r.Reason = ""
	r.Message = ""
	r.Retryable = nil
	return r
}
