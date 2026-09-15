package agentimport

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	formatcfg "github.com/go-git/go-git/v6/plumbing/format/config"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/storage/memory"

	"github.com/entireio/cli/cmd/entire/cli/agent"
	"github.com/entireio/cli/cmd/entire/cli/agent/types"
)

func TestValidateAnchorCommit_ObjectFormats(t *testing.T) {
	t.Parallel()
	for _, format := range []formatcfg.ObjectFormat{formatcfg.SHA1, formatcfg.SHA256} {
		t.Run(string(format), func(t *testing.T) {
			t.Parallel()
			// The filesystem test helper initializes only the default format.
			// An in-memory repo permits both formats without touching user config.
			repo, err := git.Init(memory.NewStorage(), git.WithWorkTree(memfs.New()), git.WithObjectFormat(format))
			if err != nil {
				t.Fatal(err)
			}
			wt, err := repo.Worktree()
			if err != nil {
				t.Fatal(err)
			}
			signature := &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()}
			commit, err := wt.Commit("anchor", &git.CommitOptions{Author: signature, Committer: signature, AllowEmptyCommits: true})
			if err != nil {
				t.Fatal(err)
			}
			got, err := ValidateAnchorCommit(repo, strings.ToUpper(commit.String()))
			if err != nil || got != commit.String() {
				t.Fatalf("ValidateAnchorCommit = %q, %v; want %q", got, err, commit)
			}
			// A length accepted by another object format is not a full ID here.
			wrongSize := formatcfg.SHA256.HexSize()
			if format == formatcfg.SHA256 {
				wrongSize = formatcfg.SHA1.HexSize()
			}
			if _, err := ValidateAnchorCommit(repo, strings.Repeat("a", wrongSize)); err == nil {
				t.Fatal("accepted another object format's length")
			}
		})
	}
}

func repoHeadSHA(t *testing.T, repo *git.Repository) string {
	t.Helper()
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	return head.Hash().String()
}

// Snapshot files as well as refs: opening a writable store must not initialize
// checkpoint data or session state before the invalid anchor is rejected.
func importGitFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := make(map[string]string)
	err := filepath.WalkDir(filepath.Join(dir, ".git"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[path] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestRun_RejectsInvalidAnchorBeforeWrites(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"import", "dry-run", "already-imported"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			for _, name := range []string{"empty", "short", "overlong", "nonhex", "revision", "expression", "missing", "tree", "blob", "tag", "hex-ref"} {
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					repo, dir := initRepoWithCommit(t)
					sha := repoHeadSHA(t, repo)
					commit, err := repo.CommitObject(plumbing.NewHash(sha))
					if err != nil {
						t.Fatal(err)
					}
					file, err := commit.File("f.txt")
					if err != nil {
						t.Fatal(err)
					}
					tag, err := repo.CreateTag("anchor-tag", commit.Hash, &git.CreateTagOptions{
						Tagger: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()}, Message: "anchor tag",
					})
					if err != nil {
						t.Fatal(err)
					}
					missing := strings.Repeat("a", len(sha))
					if err := repo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName(missing), commit.Hash)); err != nil {
						t.Fatal(err)
					}
					inputs := map[string]string{
						"empty": "", "short": sha[:8], "overlong": sha + "00", "nonhex": strings.Repeat("z", len(sha)),
						"revision": "HEAD", "expression": "HEAD~1", "missing": strings.Repeat("0", len(sha)),
						"tree": commit.TreeHash.String(), "blob": file.Hash.String(), "tag": tag.Hash().String(), "hex-ref": missing,
					}
					transcripts := t.TempDir()
					writeFixtureSession(t, transcripts, "anchor.jsonl")
					opts := Options{RepoRoot: dir, OverridePath: transcripts, Now: time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC), LinkCommitSHA: sha}
					if mode == "already-imported" {
						if _, err := Run(t.Context(), repo, claudeImporter{}, opts); err != nil {
							t.Fatal(err)
						}
					}
					before := importGitFiles(t, dir)
					opts.LinkCommitSHA = inputs[name]
					opts.DryRun = mode == "dry-run"
					if _, err := Run(context.Background(), repo, claudeImporter{}, opts); err == nil {
						t.Errorf("expected invalid anchor %q to fail", opts.LinkCommitSHA)
					}
					if after := importGitFiles(t, dir); !reflect.DeepEqual(before, after) {
						t.Error("invalid anchor changed git/checkpoint/session files")
					}
				})
			}
		})
	}
}

// discoverFatalImporter fails the test if Run reaches transcript discovery.
type discoverFatalImporter struct{ t *testing.T }

func (discoverFatalImporter) Name() string               { return string(agent.AgentNameClaudeCode) }
func (discoverFatalImporter) AgentType() types.AgentType { return agent.AgentTypeClaudeCode }
func (i discoverFatalImporter) Discover(_, _ string, _ time.Time, _ []string) ([]SessionFile, error) {
	i.t.Fatal("an invalid anchor must be rejected before transcript discovery")
	return nil, nil
}
func (discoverFatalImporter) SplitTurns(_ SessionFile, _ []byte) ([]Turn, error) { return nil, nil }

// The no-writes assertion above cannot prove the ordering the contract states:
// the checkpoint store is opened after discovery, and in a remoteless temp repo
// opening it reads without leaving a trace, so moving the check below Open
// would still pass there. Stopping before Discover is what pins "before opening
// a writable checkpoint store".
func TestRun_RejectsInvalidAnchorBeforeDiscovery(t *testing.T) {
	t.Parallel()
	// The messages are asserted here because an unset field and a mistyped one
	// are different mistakes: a caller that never set LinkCommitSHA is not
	// helped by advice about hexadecimal length.
	for _, tc := range []struct{ name, sha, wantErr string }{
		{"unset", "", "import anchor is required"},
		{"nonexistent", strings.Repeat("0", 40), "does not resolve to a commit object"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo, dir := initRepoWithCommit(t)
			_, err := Run(t.Context(), repo, discoverFatalImporter{t: t}, Options{
				RepoRoot: dir, Now: time.Now(), LinkCommitSHA: tc.sha,
			})
			if err == nil {
				t.Fatalf("expected anchor %q to fail", tc.sha)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}
