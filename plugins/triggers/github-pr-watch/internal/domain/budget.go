package domain

import (
	"fmt"
	"strconv"
)

const DefaultByteBudget uint64 = 67_108_864

// ParseByteBudget accepts the canonical positive unsigned decimal used by the
// public resource options. A budget is never interpreted as an item count.
func ParseByteBudget(value string) (uint64, error) {
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil || n == 0 || value == "" || value[0] < '0' || value[0] > '9' {
		return 0, fmt.Errorf("invalid positive byte budget")
	}
	return n, nil
}

type ResourceError struct {
	Code     string
	Option   string
	Budget   uint64
	Observed uint64
}

func (e *ResourceError) Error() string {
	return fmt.Sprintf("%s: %s budget=%d observed=%d", e.Code, e.Option, e.Budget, e.Observed)
}
