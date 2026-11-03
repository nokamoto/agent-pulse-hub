package domain

type selection struct {
	through   uint64
	prior     uint64
	attempted uint64
	positions []Position
}

// StartContinuous converts validated legacy progress without using assessment
// positions to advance selection.
func (t *Tracker) StartContinuous() {
	if t.continuous {
		return
	}
	t.cursor = max(t.Saved.Confirmed, t.Saved.Attempted)
	t.Saved.Comparison = nil
	t.continuous = true
}

// Select retains the previous progress until the emitted range receives its
// terminal notice. A second range cannot be selected while that notice is due.
func (t *Tracker) Select() (string, uint64, error) {
	if t.pendingSelection != nil {
		return "", 0, nil
	}
	prior := selection{prior: t.cursor, attempted: t.Saved.Attempted, positions: t.Saved.Positions}
	context, through, err := t.context()
	if err != nil || context == "" {
		return context, through, err
	}
	prior.through = through
	t.pendingSelection = &prior
	t.cursor = through
	return context, through, nil
}

func (t *Tracker) FinishSelection(through uint64, status string) {
	pending := t.pendingSelection
	if pending == nil || pending.through != through {
		return
	}
	switch status {
	case "discarded":
		t.cursor = pending.prior
		t.Saved.Attempted = pending.attempted
		t.Saved.Positions = pending.positions
	case "accepted":
		t.Saved.Confirmed = through
	case "failed", "unknown":
	default:
		return
	}
	t.pendingSelection = nil
}
