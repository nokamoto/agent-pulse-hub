package githubapi

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-pr-watch/internal/domain"
)

const (
	signalPRFields = `id url state headRefOid mergedAt baseRepository { id } baseRefName baseRefOid baseRef { id target { oid } } mergeable isDraft`
	checkFields    = `id name status conclusion startedAt completedAt detailsUrl url`
)

func requiredFields(raw map[string]json.RawMessage, keys ...string) bool {
	for _, key := range keys {
		if _, ok := raw[key]; !ok {
			return false
		}
	}
	return true
}

func parseSignalPR(data map[string]json.RawMessage) (domain.Aggregate, domain.SignalObservation, error) {
	pr, err := pull(data)
	if err != nil {
		return domain.Aggregate{}, domain.SignalObservation{}, err
	}
	aggregate, err := parseAggregate(pr)
	if err != nil {
		return aggregate, domain.SignalObservation{}, err
	}
	var repository struct{ ID string }
	if json.Unmarshal(data["repository"], &repository) != nil || repository.ID == "" {
		return aggregate, domain.SignalObservation{}, failure("incomplete_retrieval")
	}
	var value struct {
		BaseRepository *struct{ ID string }
		BaseRefName    string
		BaseRefOid     string
		BaseRef        *struct {
			ID     string
			Target *struct{ OID string }
		}
		Mergeable string
		IsDraft   *bool
	}
	encoded, _ := json.Marshal(pr)
	if json.Unmarshal(encoded, &value) != nil || !requiredFields(pr, "baseRepository", "baseRefName", "baseRefOid", "baseRef", "mergeable", "isDraft") || value.BaseRepository == nil || value.BaseRepository.ID == "" || value.BaseRefName == "" || !validCommitOID(aggregate.Head) || !validCommitOID(value.BaseRefOid) || value.IsDraft == nil {
		return aggregate, domain.SignalObservation{}, failure("incomplete_retrieval")
	}
	if value.BaseRef != nil && (value.BaseRef.ID == "" || value.BaseRef.Target == nil || value.BaseRef.Target.OID != value.BaseRefOid) {
		return aggregate, domain.SignalObservation{}, failure("observation_changed_during_acquisition")
	}
	observation := domain.SignalObservation{RepositoryID: repository.ID, Pair: domain.ConflictPair{PRID: aggregate.ID, Head: aggregate.Head, BaseRepositoryID: value.BaseRepository.ID, BaseRef: value.BaseRefName, BaseSHA: value.BaseRefOid}, BaseExists: value.BaseRef != nil, Mergeable: value.Mergeable, Draft: *value.IsDraft, CI: []domain.CIResult{}}
	if err := observation.Validate(); err != nil {
		return aggregate, observation, err
	}
	return aggregate, observation, nil
}

func validCommitOID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, ch := range s {
		if !(ch >= '0' && ch <= '9') && !(ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}

func parseCheck(raw map[string]json.RawMessage, repository, head, suite, app string) (domain.CIResult, error) {
	var value struct {
		ID          string
		Name        string
		Status      string
		Conclusion  *string
		StartedAt   *string
		CompletedAt *string
		DetailsURL  *string
		URL         *string
	}
	encoded, _ := json.Marshal(raw)
	if json.Unmarshal(encoded, &value) != nil || !requiredFields(raw, "id", "name", "status", "conclusion", "startedAt", "completedAt", "detailsUrl", "url") || value.ID == "" || value.Name == "" {
		return domain.CIResult{}, failure("incomplete_retrieval")
	}
	result := domain.CIResult{Source: "ci_check", ID: value.ID, RepositoryID: repository, Head: head, SuiteID: suite, AppID: app, Name: value.Name, Status: value.Status, Conclusion: value.Conclusion, StartedAt: value.StartedAt, CompletedAt: value.CompletedAt, DetailsURL: value.DetailsURL, URL: value.URL}
	if _, err := result.FailureClass(); err != nil {
		return result, err
	}
	return result, nil
}

func parseStatus(raw map[string]json.RawMessage, repository, head string) (domain.CIResult, error) {
	var value struct {
		ID          json.Number `json:"id"`
		Context     string      `json:"context"`
		State       string      `json:"state"`
		CreatedAt   *string     `json:"created_at"`
		UpdatedAt   *string     `json:"updated_at"`
		TargetURL   *string     `json:"target_url"`
		Description *string     `json:"description"`
	}
	encoded, _ := json.Marshal(raw)
	if json.Unmarshal(encoded, &value) != nil || !requiredFields(raw, "id", "context", "state", "created_at", "updated_at", "target_url", "description") || value.Context == "" || !positiveDecimal(string(raw["id"])) {
		return domain.CIResult{}, failure("incomplete_retrieval")
	}
	result := domain.CIResult{Source: "ci_status", ID: string(value.ID), RepositoryID: repository, Head: head, Name: value.Context, Status: value.State, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt, DetailsURL: value.TargetURL, Description: value.Description}
	if _, err := result.FailureClass(); err != nil {
		return result, err
	}
	return result, nil
}

func positiveDecimal(s string) bool {
	if len(s) == 0 || s[0] < '1' || s[0] > '9' {
		return false
	}
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	_, err := strconv.ParseUint(s, 10, 64)
	return err == nil
}

func statusEndpoint(config domain.WatchConfig, head string, page uint64) string {
	authority, prefix := config.Host, "/api/v3/repos/"
	if config.Host == "github.com" {
		authority, prefix = "api.github.com", "/repos/"
	}
	return "https://" + authority + prefix + url.PathEscape(config.Owner) + "/" + url.PathEscape(config.Repository) + "/commits/" + head + "/statuses?per_page=100&page=" + strconv.FormatUint(page, 10)
}

// nextStatusPage validates the complete Link header, but returns only a locally
// reconstructed page number. API-provided URLs are never request destinations.
func nextStatusPage(link string, config domain.WatchConfig, head string, current uint64) (uint64, error) {
	if strings.TrimSpace(link) == "" {
		return 0, nil
	}
	expected, _ := url.Parse(statusEndpoint(config, head, current))
	next := uint64(0)
	seenRels := map[string]bool{}
	for _, part := range strings.Split(link, ",") {
		part = strings.TrimSpace(part)
		end := strings.IndexByte(part, '>')
		if !strings.HasPrefix(part, "<") || end < 2 {
			return 0, failure("pagination_failure")
		}
		parsed, err := url.Parse(part[1:end])
		if err != nil || parsed.Scheme != "https" || parsed.Host != expected.Host || parsed.User != nil || parsed.Fragment != "" || parsed.EscapedPath() != expected.EscapedPath() || parsed.Opaque != "" {
			return 0, failure("pagination_failure")
		}
		query, err := url.ParseQuery(parsed.RawQuery)
		if err != nil || len(query) != 2 || len(query["per_page"]) != 1 || query.Get("per_page") != "100" || len(query["page"]) != 1 || !positiveDecimal(query.Get("page")) {
			return 0, failure("pagination_failure")
		}
		page, _ := strconv.ParseUint(query.Get("page"), 10, 64)
		relation := ""
		for _, attribute := range strings.Split(part[end+1:], ";") {
			attribute = strings.TrimSpace(attribute)
			if attribute == "" {
				continue
			}
			if strings.HasPrefix(attribute, "rel=") && relation == "" {
				relation = strings.TrimPrefix(attribute, "rel=")
			} else {
				return 0, failure("pagination_failure")
			}
		}
		if len(relation) < 3 || relation[0] != '"' || relation[len(relation)-1] != '"' {
			return 0, failure("pagination_failure")
		}
		for _, rel := range strings.Fields(relation[1 : len(relation)-1]) {
			if seenRels[rel] {
				return 0, failure("pagination_failure")
			}
			seenRels[rel] = true
			switch rel {
			case "next":
				if page <= current {
					return 0, failure("pagination_failure")
				}
				next = page
			case "first", "prev", "last":
			default:
				return 0, failure("pagination_failure")
			}
		}
	}
	return next, nil
}
