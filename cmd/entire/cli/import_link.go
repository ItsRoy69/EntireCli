package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/entireio/cli/cmd/entire/cli/agentimport"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"

	"github.com/entireio/cli/cmd/entire/cli/logging"
	"github.com/entireio/cli/cmd/entire/cli/strategy"
)

// resolveImportLinkCommitSHA returns the commit SHA imported checkpoints are
// anchored to: the default branch's head at import time. Preference order:
// origin's tip of the default branch (the commit most likely already known to
// the server), then the local branch tip, then HEAD. Each target must resolve
// to a commit object; an empty repo cannot import until it has a valid anchor.
// This function is the source of truth for the order; the architecture docs
// describe it but defer here.
//
// Every rejection is logged, and the final error names the refs tried. A
// candidate is now skippable rather than merely absent — origin's tip can be
// present as a ref but missing as an object after a prune or a partial fetch —
// so without this an import silently anchors to local HEAD instead, and the
// single Debug line at the call site records only the winner. Import decisions
// are one-shot: nothing re-derives them later, so an unlogged demotion
// destroys the only evidence of why a checkpoint points where it does.
func resolveImportLinkCommitSHA(ctx context.Context, repo *git.Repository) (string, error) {
	var refs []plumbing.ReferenceName
	if name := strategy.GetDefaultBranchName(repo); name != "" {
		refs = append(refs, plumbing.NewRemoteReferenceName("origin", name), plumbing.NewBranchReferenceName(name))
	}
	refs = append(refs, plumbing.HEAD)

	tried := make([]string, 0, len(refs))
	for _, name := range refs {
		tried = append(tried, name.Short())
		ref, err := repo.Reference(name, true)
		if err != nil {
			logging.Debug(ctx, "import: anchor candidate unresolved", "ref", name.String(), "error", err)
			continue
		}
		sha, err := agentimport.ValidateAnchorCommit(repo, ref.Hash().String())
		if err != nil {
			logging.Debug(ctx, "import: anchor candidate rejected", "ref", name.String(), "target", ref.Hash().String(), "error", err)
			continue
		}
		return sha, nil
	}
	return "", fmt.Errorf("cannot import sessions without a valid anchor commit (tried %s): create a commit or fetch the repository's code history, then retry", strings.Join(tried, ", "))
}
