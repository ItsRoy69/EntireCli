package agentimport

import (
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
)

// ValidateAnchorCommit requires a full hexadecimal object ID naming a commit
// in repo and returns its canonical lowercase ID. It never interprets the input
// as a ref or revision expression, or peels a tag into a commit.
func ValidateAnchorCommit(repo *git.Repository, sha string) (string, error) {
	// Named separately from the length check below: Run is exported, so an
	// unset field is a caller that forgot one, and "must be a full
	// 40-character hexadecimal commit ID" describes a typo instead.
	if sha == "" {
		return "", errors.New("import anchor is required: no commit ID was resolved for this import")
	}
	cfg, err := repo.Config()
	if err != nil {
		return "", fmt.Errorf("read anchor repository config: %w", err)
	}
	if len(sha) != cfg.Extensions.ObjectFormat.HexSize() {
		return "", fmt.Errorf("import anchor must be a full %d-character hexadecimal commit ID", cfg.Extensions.ObjectFormat.HexSize())
	}
	if _, err := hex.DecodeString(sha); err != nil {
		return "", fmt.Errorf("import anchor must be hexadecimal: %w", err)
	}
	commit, err := repo.CommitObject(plumbing.NewHash(sha))
	if err != nil {
		return "", fmt.Errorf("import anchor %q does not resolve to a commit object: %w", sha, err)
	}
	return commit.Hash.String(), nil
}
