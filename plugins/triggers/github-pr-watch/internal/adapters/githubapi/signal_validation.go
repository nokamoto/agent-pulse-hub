package githubapi

import (
	"context"
	"encoding/json"

	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-pr-watch/internal/domain"
)

func (c *Client) validateSignalAccess(ctx context.Context, config domain.WatchConfig, token string, observed domain.SignalObservation, total *uint64) error {
	// Exercise both check-run filters in the same query. Selecting their fields
	// also checks schema shape when the head has no check suites yet.
	query := `query ValidatePullRequestChecks($owner:String!,$name:String!,$head:String!){repository(owner:$owner,name:$name,followRenames:false){id object(expression:$head){... on Commit {oid checkSuites(first:1){nodes{id app{id} commit{oid}
		all:checkRuns(first:1,filterBy:{checkType:ALL}){nodes{` + checkFields + `} ` + pageInfo + `}
		latest:checkRuns(first:1,filterBy:{checkType:LATEST}){nodes{` + checkFields + `} ` + pageInfo + `}
	} ` + pageInfo + `}}}}}`
	head := observed.Pair.Head
	vars := map[string]any{"owner": config.Owner, "name": config.Repository, "head": head}
	data, err := c.request(ctx, config, token, query, vars, total)
	if err != nil {
		return err
	}
	var repo struct {
		ID     string
		Object *struct {
			OID         string
			CheckSuites json.RawMessage
		}
	}
	if json.Unmarshal(data["repository"], &repo) != nil || repo.ID != observed.RepositoryID || repo.Object == nil {
		return failure("incomplete_retrieval")
	}
	if repo.Object.OID != head {
		return failure("observation_changed_during_acquisition")
	}
	suites, _, _, err := connection(repo.Object.CheckSuites)
	if err != nil {
		return err
	}
	for _, raw := range suites {
		var suiteID string
		var app struct{ ID string }
		var commit struct{ OID string }
		if json.Unmarshal(raw["id"], &suiteID) != nil || suiteID == "" ||
			json.Unmarshal(raw["app"], &app) != nil || app.ID == "" ||
			json.Unmarshal(raw["commit"], &commit) != nil || commit.OID == "" {
			return failure("incomplete_retrieval")
		}
		if commit.OID != head {
			return failure("observation_changed_during_acquisition")
		}
		for _, filter := range []string{"all", "latest"} {
			runs, _, _, err := connection(raw[filter])
			if err != nil {
				return err
			}
			for _, run := range runs {
				if _, err := parseCheck(run, repo.ID, head, suiteID, app.ID); err != nil {
					return err
				}
			}
		}
	}
	statuses, _, err := c.statusPage(ctx, config, token, head, 1, c.budget(), total)
	if err != nil {
		return err
	}
	for _, status := range statuses {
		if _, err := parseStatus(status, repo.ID, head); err != nil {
			return err
		}
	}
	return nil
}
