package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/entireio/cli/cmd/entire/cli/agentimport"
	"github.com/entireio/cli/cmd/entire/cli/testutil"
	"github.com/stretchr/testify/require"
)

func TestRunSelectedImports_PreservesTurnAnchorSelection(t *testing.T) {
	// Not parallel: onboarding resolves repository and state from CWD.
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	testutil.WriteFile(t, dir, "f.txt", "first")
	testutil.GitAdd(t, dir, "f.txt")
	testutil.GitCommit(t, dir, "first")
	recorded := testutil.GetHeadHash(t, dir)
	testutil.WriteFile(t, dir, "f.txt", "second")
	testutil.GitAdd(t, dir, "f.txt")
	testutil.GitCommit(t, dir, "second")
	fallback := testutil.GetHeadHash(t, dir)
	t.Chdir(dir)

	transcripts := t.TempDir()
	content := strings.Join([]string{
		`{"type":"user","uuid":"u1","timestamp":"2026-06-20T00:00:00Z","message":{"role":"user","content":"first"}}`,
		fmt.Sprintf(`{"type":"user","toolUseResult":{"gitOperation":{"commit":{"sha":%q,"kind":"committed"}}},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"committed"}]}}`, recorded),
		`{"type":"user","uuid":"u2","timestamp":"2026-06-20T00:01:00Z","message":{"role":"user","content":"second"}}`,
	}, "\n") + "\n"
	testutil.WriteFile(t, transcripts, "mixed.jsonl", content)
	imp := importerForAgent(fakeAgent{typ: testAgentClaude})
	require.NotNil(t, imp)
	selected := fixedDiscoverImporter{Importer: imp, sessions: []agentimport.SessionFile{{
		Path: filepath.Join(transcripts, "mixed.jsonl"), SessionID: "mixed",
	}}}
	var out bytes.Buffer
	runSelectedImports(context.Background(), &out, dir, []eligibleImport{{imp: selected, displayName: "Claude Code"}})
	require.Contains(t, out.String(), "Imported 2 turn(s)")
	for turn, want := range map[string]string{"u1": recorded, "u2": fallback} {
		cid := agentimport.DeriveCheckpointID("mixed", turn)
		for _, suffix := range []string{"/metadata.json", "/0/metadata.json"} {
			raw := testutil.RunGit(t, dir, "show", "entire/checkpoints/v1:"+cid.Path()+suffix)
			var metadata struct {
				CommitSHA string `json:"commit_sha"`
			}
			require.NoError(t, json.Unmarshal([]byte(raw), &metadata))
			require.Equal(t, want, metadata.CommitSHA, "%s%s", cid, suffix)
		}
	}
}

func TestRunSelectedImports_EmptyRepoSkipsBeforeDiscovery(t *testing.T) {
	// Not parallel: onboarding resolves the repository from CWD.
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	t.Chdir(dir)
	imp := importerForAgent(fakeAgent{typ: testAgentClaude})
	require.NotNil(t, imp)
	selected := fixedDiscoverImporter{Importer: imp, onDiscover: func() {
		t.Fatal("anchorless import must stop before transcript discovery")
	}}
	var out bytes.Buffer
	runSelectedImports(context.Background(), &out, dir, []eligibleImport{{imp: selected, displayName: "Claude Code"}})
	require.Contains(t, out.String(), "skipping agent history import")
	require.Contains(t, out.String(), "without a valid anchor commit")
	// The offer is first-run only, so "retry" must not be read as "re-run
	// enable": the note has to name the command that can still import.
	require.Contains(t, out.String(), "entire import <agent>")
	require.NotContains(t, out.String(), "Imported ")
	require.Empty(t, testutil.RunGit(t, dir, "for-each-ref", "--format=%(refname)", "refs/entire", "refs/heads/entire"))
}
