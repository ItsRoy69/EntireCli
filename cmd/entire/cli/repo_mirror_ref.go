package cli

import (
	"fmt"
	"strings"
)

const mirrorRepoRefHelp = "Repository references name their forge: /gh/<owner>/<repo> for GitHub, " +
	"/et/<project>/<repo> for Entire. This operation currently supports GitHub mirrors only. " +
	"GitHub URLs are also accepted."

// parseGitHubMirrorRepoRef separates repository syntax from the forges that
// mirror operations support. Keep host-qualified GitHub URLs working, but
// require a forge on path references so a bare pair cannot select one implicitly.
func parseGitHubMirrorRepoRef(ref string) (owner, repo string, err error) {
	ref = strings.TrimSpace(ref)
	if declaresForge(ref, nativeCloneForge) {
		if _, _, err := parseNativeCloneRef(ref); err != nil {
			return "", "", fmt.Errorf("invalid <repo> %q: %w", ref, err)
		}
		return "", "", fmt.Errorf("this operation does not support Entire repository %q; it currently supports GitHub mirrors only", ref)
	}
	if declaresForge(ref, mirrorCloneForge) {
		_, owner, repo, err = parseMirrorCloneRef(ref)
		if err != nil {
			return "", "", fmt.Errorf("invalid <repo> %q: %w", ref, err)
		}
		return owner, repo, nil
	}
	if owner, repo, err = parseHostedGitHubURL(ref); err == nil {
		return owner, repo, nil
	}
	if suggestions := bareRefSuggestions(ref); len(suggestions) > 0 {
		return "", "", fmt.Errorf("invalid <repo>: repository reference must name its forge; did you mean %s?", strings.Join(suggestions, " or "))
	}
	return "", "", fmt.Errorf("invalid <repo>: expected a forge-qualified repository reference such as /gh/owner/repo or /et/project/repo, got %q", ref)
}
