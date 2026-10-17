// This external-executable fixture qualifies process mechanics only. It is not
// GitHub CLI and does not establish compatibility with a saved GitHub login.
package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	const host = "ghes.example.test"
	want := []string{"auth", "token", "--hostname", host}
	if len(os.Args) != len(want)+1 {
		os.Exit(2)
	}
	for i, value := range want {
		if os.Args[i+1] != value {
			os.Exit(2)
		}
	}
	blocked := map[string]bool{}
	for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST"} {
		_, blocked[key] = os.LookupEnv(key)
	}
	row := struct {
		Args    []string        `json:"argv"`
		Blocked map[string]bool `json:"blocked_environment_present"`
	}{Args: os.Args[1:], Blocked: blocked}
	data, err := json.Marshal(row)
	if err != nil || os.WriteFile(os.Getenv("APH_GRR_FIXTURE_JOURNAL"), data, 0o600) != nil {
		os.Exit(3)
	}
	fmt.Println("fixture-saved-token")
}
