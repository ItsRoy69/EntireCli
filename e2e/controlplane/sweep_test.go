//go:build e2e

package controlplane

import (
	"strings"
	"testing"
	"time"
)

const (
	// namePrefix marks every resource this suite creates.
	namePrefix = "e2e-cp-"
	// leakedAfter is how old a suite org must be before the sweep treats it
	// as left behind rather than owned by a run still in progress elsewhere.
	leakedAfter = 30 * time.Minute
)

// sweepLeaked deletes the orgs, and everything under them, that earlier runs
// left behind by ending without cleanup: a killed process, a cancelled job, a
// lost runner. The test account may own three orgs, so without this one such
// run would block every later one until someone cleaned up by hand.
func sweepLeaked(t *testing.T, dir string) {
	t.Helper()
	stdout, _ := mustRunEntire(t, dir, "org", "list", "--json")
	orgs := decodeJSON[[]struct {
		ID        string    `json:"id"`
		Name      string    `json:"name"`
		CreatedAt time.Time `json:"createdAt"`
	}](t, stdout)
	for _, org := range orgs {
		if !strings.HasPrefix(org.Name, namePrefix) || time.Since(org.CreatedAt) < leakedAfter {
			continue
		}
		stdout, _ = mustRunEntire(t, dir, "project", "list", "--org", org.ID, "--json")
		for _, project := range decodeJSON[[]struct {
			ID string `json:"id"`
		}](t, stdout) {
			stdout, _ = mustRunEntire(t, dir, "repo", "list", project.ID, "--json")
			for _, repo := range decodeJSON[[]struct {
				ID string `json:"id"`
			}](t, stdout) {
				deleteResource(t, dir, "repo", repo.ID)
			}
			deleteResource(t, dir, "project", project.ID)
		}
		deleteResource(t, dir, "org", org.ID)
		t.Logf("swept leaked org %s", org.Name)
	}
}
