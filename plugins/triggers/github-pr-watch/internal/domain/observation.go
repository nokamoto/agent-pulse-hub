package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"
)

type Item struct {
	ID             string  `json:"id"`
	Kind           string  `json:"kind"`
	Body           string  `json:"body"`
	Author         *string `json:"author"`
	CreatedAt      *string `json:"created_at"`
	UpdatedAt      *string `json:"updated_at"`
	SubmittedAt    *string `json:"submitted_at,omitempty"`
	URL            string  `json:"url"`
	State          string  `json:"state,omitempty"`
	Commit         *string `json:"commit"`
	OriginalCommit *string `json:"original_commit,omitempty"`
	Review         *string `json:"review,omitempty"`
	ReplyTo        *string `json:"reply_to,omitempty"`
	Thread         string  `json:"thread,omitempty"`
	Resolved       *bool   `json:"resolved,omitempty"`
	Outdated       *bool   `json:"outdated,omitempty"`
	ResolvedBy     *string `json:"resolved_by,omitempty"`
	ReviewerType   string  `json:"reviewer_type,omitempty"`
	Reviewer       string  `json:"reviewer,omitempty"`
	ReviewerID     string  `json:"reviewer_id,omitempty"`
	BodyDigest     string  `json:"body_digest,omitempty"`
}

type Aggregate struct {
	ID       string  `json:"id"`
	URL      string  `json:"url"`
	Head     string  `json:"head"`
	State    string  `json:"state"`
	MergedAt *string `json:"merged_at"`
}

type Observation struct {
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	PR        Aggregate `json:"pr"`
	Items     []Item    `json:"items"`
}

type Fingerprint struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Digest string `json:"digest"`
}

func digest(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func bodyDigest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func fingerprints(o Observation) ([]Fingerprint, error) {
	if len(o.Items) > 32 || o.PR.ID == "" || o.PR.URL == "" || o.PR.Head == "" || o.PR.State == "" || o.EndedAt.Before(o.StartedAt) {
		return nil, errors.New("coverage_capacity_exceeded")
	}
	result := make([]Fingerprint, 0, len(o.Items))
	seen := map[string]bool{}
	for _, item := range o.Items {
		key := item.Kind + ":" + item.ID
		if item.ID == "" || item.Kind == "" || seen[key] {
			return nil, errors.New("incomplete_retrieval")
		}
		seen[key] = true
		// Retrieval timestamps are provenance, not content identity.
		canonical := item
		canonical.CreatedAt = nil
		canonical.UpdatedAt = nil
		canonical.SubmittedAt = nil
		canonical.BodyDigest = ""
		f := Fingerprint{ID: item.ID, Kind: item.Kind, Digest: digest(canonical)}
		encoded, _ := json.Marshal(f)
		if len(encoded) > 192 {
			return nil, errors.New("coverage_capacity_exceeded")
		}
		result = append(result, f)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Kind+result[i].ID < result[j].Kind+result[j].ID })
	return result, nil
}
