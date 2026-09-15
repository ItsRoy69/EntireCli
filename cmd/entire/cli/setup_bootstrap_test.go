package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const (
	testUser     = "octocat"
	cmdGit       = "git"
	gitCmdCommit = "commit"
	gitCmdConfig = "config"
)

// runBootstrapWith runs the full bootstrap (init + finalize) in one
// call, used by tests that don't need to assert phasing. The real caller
// runs the two phases around agent setup.
func runBootstrapWith(ctx context.Context, w, errW io.Writer, opts BootstrapOptions, runner bootstrapRunner) error {
	state, err := runBootstrapInitWith(ctx, w, errW, opts, runner)
	if err != nil {
		return err
	}
	return runBootstrapFinalize(ctx, w, state)
}

// fakeRunner is a test seam for bootstrapRunner. Each (name, args[0]) pair
// maps to a response.
type fakeRunner struct {
	mu        sync.Mutex
	responses map[string]fakeResponse
	calls     []fakeCall
}

type fakeResponse struct {
	stdout string
	err    error
}

type fakeCall struct {
	dir  string
	name string
	args []string
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		responses: make(map[string]fakeResponse),
	}
}

func (f *fakeRunner) key(name string, args []string) string {
	return name + " " + strings.Join(args, " ")
}

func (f *fakeRunner) set(name string, args []string, stdout string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses[f.key(name, args)] = fakeResponse{stdout: stdout, err: err}
}

func (f *fakeRunner) lookup(name string, args []string) (fakeResponse, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.responses[f.key(name, args)]
	return r, ok
}

func (f *fakeRunner) record(dir, name string, args []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeCall{dir: dir, name: name, args: args})
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	f.record("", name, args)
	if r, ok := f.lookup(name, args); ok {
		return r.stdout, r.err
	}
	return "", fmt.Errorf("fakeRunner: unexpected call %s %v", name, args)
}

func (f *fakeRunner) RunInDir(_ context.Context, dir, name string, args ...string) (string, error) {
	f.record(dir, name, args)
	if r, ok := f.lookup(name, args); ok {
		return r.stdout, r.err
	}
	return "", fmt.Errorf("fakeRunner: unexpected call in %s: %s %v", dir, name, args)
}

// setIdentityConfigured simulates `git config --get user.name/email` returning
// non-empty values, so ensureGitIdentity treats identity as already set.
func (f *fakeRunner) setIdentityConfigured() {
	f.set("git", []string{"config", "--get", "user.name"}, "Test User\n", nil)
	f.set("git", []string{"config", "--get", "user.email"}, "test@example.com\n", nil)
}

// hasCall returns whether any recorded call matches the predicate.
func (f *fakeRunner) hasCall(match func(fakeCall) bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if match(c) {
			return true
		}
	}
	return false
}

func TestGhHelpers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := newFakeRunner()

	r.set("gh", []string{"--version"}, "gh version 2.81.0\n", nil)
	r.set("gh", []string{"auth", "status"}, "Logged in", nil)
	r.set("gh", []string{"api", "user", "--jq", ".login"}, "octocat\n", nil)

	if !ghAvailable(ctx, r) {
		t.Fatal("ghAvailable should be true")
	}
	if !ghAuthenticated(ctx, r) {
		t.Fatal("ghAuthenticated should be true")
	}
	user, err := ghCurrentUser(ctx, r)
	if err != nil || user != testUser {
		t.Fatalf("ghCurrentUser = %q, %v; want octocat", user, err)
	}
}

func TestGhAvailable_Missing(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.set("gh", []string{"--version"}, "", errors.New("not found"))
	if ghAvailable(context.Background(), r) {
		t.Fatal("expected ghAvailable to return false when gh is missing")
	}
}

func TestDoInitialCommit_EmptyFolder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := newFakeRunner()
	r.set("git", []string{"add", "-A"}, "", nil)
	r.set("git", []string{"--no-optional-locks", "status", "--porcelain"}, "", nil)

	committed, err := doInitialCommit(context.Background(), r, dir, "msg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if committed {
		t.Fatal("expected committed=false for empty folder")
	}
}

func TestDoInitialCommit_WithFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := newFakeRunner()
	r.set("git", []string{"add", "-A"}, "", nil)
	r.set("git", []string{"--no-optional-locks", "status", "--porcelain"}, " M README.md\n", nil)
	r.set("git", []string{"-c", "commit.gpgsign=false", "commit", "-m", "msg"}, "", nil)

	committed, err := doInitialCommit(context.Background(), r, dir, "msg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !committed {
		t.Fatal("expected committed=true")
	}
	// Verify gpgsign=false was passed to the commit.
	if !r.hasCall(func(c fakeCall) bool {
		return c.name == cmdGit && len(c.args) >= 3 && c.args[0] == "-c" && c.args[1] == "commit.gpgsign=false" && c.args[2] == gitCmdCommit
	}) {
		t.Fatal("expected commit to pass -c commit.gpgsign=false")
	}
}

func TestRunBootstrap_DeclinedInNonInteractive(t *testing.T) {
	dir := t.TempDir()
	restoreCwd(t, dir)

	err := runBootstrapWith(context.Background(), io.Discard, io.Discard, BootstrapOptions{}, newFakeRunner())
	if !errors.Is(err, errBootstrapDeclined) {
		t.Fatalf("expected errBootstrapDeclined, got %v", err)
	}
}

func TestRunBootstrap_LocalFlow(t *testing.T) {
	dir := t.TempDir()
	restoreCwd(t, dir)

	r := newFakeRunner()
	r.setIdentityConfigured()
	r.set("git", []string{"init"}, "", nil)
	r.set("git", []string{"add", "-A"}, "", nil)
	r.set("git", []string{"--no-optional-locks", "status", "--porcelain"}, " M file\n", nil)
	r.set("git", []string{"-c", "commit.gpgsign=false", "commit", "-m", "First!"}, "", nil)

	opts := BootstrapOptions{
		InitRepo:             true,
		InitialCommitMessage: "First!",
	}
	err := runBootstrapWith(context.Background(), io.Discard, io.Discard, opts, r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify git init ran in the cwd.
	if !r.hasCall(func(c fakeCall) bool {
		return c.name == cmdGit && len(c.args) == 1 && c.args[0] == "init"
	}) {
		t.Fatal("expected git init call")
	}
	// Bootstrap is local-only: it must never shell out to gh.
	if r.hasCall(func(c fakeCall) bool { return c.name == "gh" }) {
		t.Fatal("bootstrap must not invoke gh")
	}
}

func TestResolveCommitMessage_SkipFlag(t *testing.T) {
	t.Parallel()
	msg, commit, err := resolveCommitMessage(BootstrapOptions{SkipInitialCommit: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if commit {
		t.Fatal("commit should be false when SkipInitialCommit is set")
	}
	if msg != "" {
		t.Fatalf("message should be empty when skipping, got %q", msg)
	}
}

func TestResolveCommitMessage_FlagTakesMessage(t *testing.T) {
	t.Parallel()
	msg, commit, err := resolveCommitMessage(BootstrapOptions{InitialCommitMessage: "custom"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !commit {
		t.Fatal("commit should be true with explicit message flag")
	}
	if msg != "custom" {
		t.Fatalf("message = %q, want custom", msg)
	}
}

func TestResolveCommitMessage_NonInteractiveDefault(t *testing.T) {
	msg, commit, err := resolveCommitMessage(BootstrapOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !commit {
		t.Fatal("commit should default to true non-interactively")
	}
	if msg != defaultInitialCommitMessage {
		t.Fatalf("message = %q, want Initial commit", msg)
	}
}

// TestRunBootstrap_CreatesNoRemote is the guard for the invariant that
// `entire enable` bootstrapping is local-only. Creating a repository on a
// forge and publishing a directory's contents are the user's calls to make
// with their own tooling, so bootstrap must never reach the network: no gh
// invocation, and no `git remote`/`git push`. Any future flag that adds one
// back has to break this test first.
func TestRunBootstrap_CreatesNoRemote(t *testing.T) {
	dir := t.TempDir()
	restoreCwd(t, dir)

	r := newFakeRunner()
	r.setIdentityConfigured()
	r.set("git", []string{"init"}, "", nil)
	r.set("git", []string{"add", "-A"}, "", nil)
	r.set("git", []string{"--no-optional-locks", "status", "--porcelain"}, " M f\n", nil)
	r.set("git", []string{"-c", "commit.gpgsign=false", "commit", "-m", defaultInitialCommitMessage}, "", nil)

	// --yes is the most permissive input there is; if any path still creates
	// a remote, this is the one that would.
	if err := runBootstrapWith(context.Background(), io.Discard, io.Discard, BootstrapOptions{Yes: true}, r); err != nil {
		t.Fatalf("bootstrap failed: %v", err)
	}

	if r.hasCall(func(c fakeCall) bool { return c.name == "gh" }) {
		t.Error("bootstrap must not invoke gh")
	}
	for _, sub := range []string{"remote", "push"} {
		if r.hasCall(gitArgsMatch([]string{sub})) {
			t.Errorf("bootstrap must not run git %s", sub)
		}
	}
}

// TestRunBootstrap_InitBeforeFinalize verifies the two-phase split: init
// runs git init up front, finalize creates the commit. A simulated "agent
// setup" step writes a file between the phases; that file must end up in the
// initial commit (i.e. `git add -A` happens after setup, not before).
func TestRunBootstrap_InitBeforeFinalize(t *testing.T) {
	dir := t.TempDir()
	restoreCwd(t, dir)

	r := newFakeRunner()
	r.setIdentityConfigured()
	r.set("git", []string{"init"}, "", nil)
	r.set("git", []string{"add", "-A"}, "", nil)
	r.set("git", []string{"--no-optional-locks", "status", "--porcelain"}, " A .entire/settings.json\n", nil)
	r.set("git", []string{"-c", "commit.gpgsign=false", "commit", "-m", "First"}, "", nil)

	opts := BootstrapOptions{
		InitRepo:             true,
		InitialCommitMessage: "First",
	}

	// Phase 1: init. This must NOT stage or commit.
	state, err := runBootstrapInitWith(context.Background(), io.Discard, io.Discard, opts, r)
	if err != nil {
		t.Fatalf("init failed: %v", err)
	}
	if state == nil {
		t.Fatal("expected non-nil state after init")
	}
	if !r.hasCall(gitArgsMatch([]string{"init"})) {
		t.Fatal("expected git init during phase 1")
	}
	forbidden := [][]string{
		{"add", "-A"},
		{"--no-optional-locks", "status", "--porcelain"},
		{"-c", "commit.gpgsign=false", gitCmdCommit, "-m", "First"},
	}
	for _, args := range forbidden {
		if r.hasCall(gitArgsMatch(args)) {
			t.Fatalf("git %v was called during init; should have been deferred to finalize", args)
		}
	}

	// Phase 2: finalize. Now the commit lands.
	if err := runBootstrapFinalize(context.Background(), io.Discard, state); err != nil {
		t.Fatalf("finalize failed: %v", err)
	}
	if !r.hasCall(gitArgsMatch([]string{"-c", "commit.gpgsign=false", gitCmdCommit, "-m", "First"})) {
		t.Fatal("expected commit during finalize")
	}
}

// gitArgsMatch returns a predicate for hasCall that matches a `git` call
// whose args start with the given slice. Bootstrap shells out to nothing
// else, so the command name is not a parameter.
func gitArgsMatch(args []string) func(fakeCall) bool {
	return func(c fakeCall) bool {
		if c.name != cmdGit || len(c.args) < len(args) {
			return false
		}
		for i, a := range args {
			if c.args[i] != a {
				return false
			}
		}
		return true
	}
}

func TestEnsureGitIdentity_AlreadyConfigured(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.setIdentityConfigured()

	err := ensureGitIdentity(context.Background(), io.Discard, io.Discard, r, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No git config writes should have occurred.
	if r.hasCall(func(c fakeCall) bool {
		return c.name == cmdGit && len(c.args) >= 2 && c.args[0] == gitCmdConfig && (c.args[1] == "user.name" || c.args[1] == "user.email")
	}) {
		t.Fatal("did not expect identity writes when already configured")
	}
}

func TestEnsureGitIdentity_SourcedFromGh(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	// Identity missing locally (empty stdout).
	r.set("git", []string{"config", "--get", "user.name"}, "", errors.New("not set"))
	r.set("git", []string{"config", "--get", "user.email"}, "", errors.New("not set"))
	// gh available and authenticated.
	r.set("gh", []string{"--version"}, "gh", nil)
	r.set("gh", []string{"auth", "status"}, "ok", nil)
	r.set("gh", []string{"api", "user"}, `{"id":42,"login":"octo","name":"Octo Cat","email":"octo@example.com"}`, nil)
	// Expect writes with values from gh.
	r.set("git", []string{"config", "user.name", "Octo Cat"}, "", nil)
	r.set("git", []string{"config", "user.email", "octo@example.com"}, "", nil)

	err := ensureGitIdentity(context.Background(), io.Discard, io.Discard, r, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnsureGitIdentity_GhNoreplyFallback(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.set("git", []string{"config", "--get", "user.name"}, "", errors.New("not set"))
	r.set("git", []string{"config", "--get", "user.email"}, "", errors.New("not set"))
	r.set("gh", []string{"--version"}, "gh", nil)
	r.set("gh", []string{"auth", "status"}, "ok", nil)
	// email is null/missing: should fall back to id+login noreply.
	r.set("gh", []string{"api", "user"}, `{"id":42,"login":"octo","name":"","email":null}`, nil)
	r.set("git", []string{"config", "user.name", "octo"}, "", nil)
	r.set("git", []string{"config", "user.email", "42+octo@users.noreply.github.com"}, "", nil)

	err := ensureGitIdentity(context.Background(), io.Discard, io.Discard, r, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestEnsureGitIdentity_PreservesExistingName covers the partial-config
// case: `user.name` is set globally but `user.email` is missing. We must
// source only the email (from gh) and leave the name untouched — we
// never want to silently replace the user's configured name with a
// gh-derived login.
func TestEnsureGitIdentity_PreservesExistingName(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	// Name is set globally, email is not.
	r.set("git", []string{"config", "--get", "user.name"}, "John Doe\n", nil)
	r.set("git", []string{"config", "--get", "user.email"}, "", errors.New("not set"))
	// gh available and returns both values.
	r.set("gh", []string{"--version"}, "gh", nil)
	r.set("gh", []string{"auth", "status"}, "ok", nil)
	r.set("gh", []string{"api", "user"}, `{"id":42,"login":"johndoe","name":"Johnny Dough","email":"john@example.com"}`, nil)
	// Only the email should be written locally — the name must stay
	// at the user's global value.
	r.set("git", []string{"config", "user.email", "john@example.com"}, "", nil)

	err := ensureGitIdentity(context.Background(), io.Discard, io.Discard, r, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No `git config user.name ...` call should have been made.
	if r.hasCall(func(c fakeCall) bool {
		return c.name == cmdGit && len(c.args) >= 2 && c.args[0] == gitCmdConfig && c.args[1] == "user.name"
	}) {
		t.Fatal("ensureGitIdentity should not write user.name when it's already set globally")
	}
}

// TestEnsureGitIdentity_PreservesExistingEmail mirrors the above for the
// other direction: email set, name missing.
func TestEnsureGitIdentity_PreservesExistingEmail(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.set("git", []string{"config", "--get", "user.name"}, "", errors.New("not set"))
	r.set("git", []string{"config", "--get", "user.email"}, "john@example.com\n", nil)
	r.set("gh", []string{"--version"}, "gh", nil)
	r.set("gh", []string{"auth", "status"}, "ok", nil)
	r.set("gh", []string{"api", "user"}, `{"id":42,"login":"johndoe","name":"Johnny","email":"other@example.com"}`, nil)
	r.set("git", []string{"config", "user.name", "Johnny"}, "", nil)

	err := ensureGitIdentity(context.Background(), io.Discard, io.Discard, r, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.hasCall(func(c fakeCall) bool {
		return c.name == cmdGit && len(c.args) >= 2 && c.args[0] == gitCmdConfig && c.args[1] == "user.email"
	}) {
		t.Fatal("ensureGitIdentity should not write user.email when it's already set globally")
	}
}

func TestEnsureGitIdentity_NonInteractiveNoGh_Errors(t *testing.T) {
	r := newFakeRunner()
	r.set("git", []string{"config", "--get", "user.name"}, "", errors.New("not set"))
	r.set("git", []string{"config", "--get", "user.email"}, "", errors.New("not set"))
	r.set("gh", []string{"--version"}, "", errors.New("not found"))

	err := ensureGitIdentity(context.Background(), io.Discard, io.Discard, r, t.TempDir())
	if err == nil {
		t.Fatal("expected error when identity missing and gh unavailable")
	}
	if !strings.Contains(err.Error(), "git config --global user.name") {
		t.Fatalf("expected guidance to set git config, got %v", err)
	}
}

func TestGhUserIdentity_NameFallsBackToLogin(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.set("gh", []string{"api", "user"}, `{"id":7,"login":"dev","name":"","email":"dev@example.com"}`, nil)
	name, email, err := ghUserIdentity(context.Background(), r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "dev" {
		t.Fatalf("name = %q", name)
	}
	if email != "dev@example.com" {
		t.Fatalf("email = %q", email)
	}
}

// TestBootstrap_FreshMachine_RealGit is an integration-style test that runs
// real git via execRunner on a temp dir isolated from the user's global git
// config. Regression guard for the issue where bootstrap commits failed
// without a configured identity or because of commit.gpgsign=true.
func TestBootstrap_FreshMachine_RealGit(t *testing.T) {
	// Isolate from any global git config: point HOME + GIT_CONFIG_* at
	// empty/missing locations, and force a broken GPG signing config that
	// would fail any commit if we did not pass -c commit.gpgsign=false.
	emptyHome := t.TempDir()
	t.Setenv("HOME", emptyHome)
	t.Setenv("XDG_CONFIG_HOME", "")
	// A global config that demands signing with a non-existent program. If
	// our bootstrap did not override gpgsign for its commit, git would
	// error out here.
	globalCfg := filepath.Join(emptyHome, ".gitconfig")
	globalContent := "[user]\n\tname = Fresh User\n\temail = fresh@example.com\n[commit]\n\tgpgsign = true\n[gpg]\n\tprogram = /does/not/exist\n"
	if err := writeTempFile(globalCfg, globalContent); err != nil {
		t.Fatalf("write global gitconfig: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalCfg)
	// Ensure no system config interferes.
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	projectDir := t.TempDir()
	restoreCwd(t, projectDir)
	// Create a file to commit.
	if err := writeTempFile(filepath.Join(projectDir, "README.md"), "hello\n"); err != nil {
		t.Fatalf("write file: %v", err)
	}

	opts := BootstrapOptions{
		InitRepo:             true,
		InitialCommitMessage: "Initial",
	}
	err := runBootstrapWith(context.Background(), io.Discard, io.Discard, opts, execRunner{})
	if err != nil {
		t.Fatalf("bootstrap failed: %v", err)
	}

	// Verify a commit actually landed on HEAD.
	out, err := execRunner{}.RunInDir(context.Background(), projectDir, "git", "log", "--oneline")
	if err != nil {
		t.Fatalf("git log failed: %v", err)
	}
	if !strings.Contains(out, "Initial") {
		t.Fatalf("expected 'Initial' commit in log, got: %q", out)
	}
}

func writeTempFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

// ghFailingRunner wraps another bootstrapRunner and forces all `gh`
// invocations to fail, while letting real `git` calls through. This
// lets tests deterministically exercise the "gh unavailable" path
// regardless of whether `gh` is installed/authenticated on the host.
type ghFailingRunner struct {
	inner bootstrapRunner
}

func (r ghFailingRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	if name == "gh" {
		return "", errors.New("gh not available (test)")
	}
	return r.inner.Run(ctx, name, args...)
}

func (r ghFailingRunner) RunInDir(ctx context.Context, dir, name string, args ...string) (string, error) {
	if name == "gh" {
		return "", errors.New("gh not available (test)")
	}
	return r.inner.RunInDir(ctx, dir, name, args...)
}

// TestBootstrap_FreshMachine_NoIdentity_RealGit verifies that a fresh
// machine without any git identity configured fails cleanly in
// non-interactive mode with a helpful error message, instead of letting
// `git commit` fail with a confusing "please tell me who you are" stderr.
//
// Uses a gh-failing runner wrapper rather than PATH manipulation so the
// test isn't sensitive to whether `gh` + GH_TOKEN/GITHUB_TOKEN are set
// on the host.
func TestBootstrap_FreshMachine_NoIdentity_RealGit(t *testing.T) {
	emptyHome := t.TempDir()
	t.Setenv("HOME", emptyHome)
	t.Setenv("XDG_CONFIG_HOME", "")
	// Empty global config: no user.name/user.email.
	globalCfg := filepath.Join(emptyHome, ".gitconfig")
	if err := writeTempFile(globalCfg, ""); err != nil {
		t.Fatalf("write global gitconfig: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalCfg)
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	// Belt-and-suspenders: unset any GitHub tokens so a wrapper bypass
	// would still not find credentials.
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	projectDir := t.TempDir()
	restoreCwd(t, projectDir)
	if err := writeTempFile(filepath.Join(projectDir, "README.md"), "hi\n"); err != nil {
		t.Fatalf("write file: %v", err)
	}

	opts := BootstrapOptions{
		InitRepo:             true,
		InitialCommitMessage: "x",
	}
	runner := ghFailingRunner{inner: execRunner{}}
	err := runBootstrapWith(context.Background(), io.Discard, io.Discard, opts, runner)
	if err == nil {
		t.Fatal("expected error when identity missing and gh unavailable")
	}
	if !strings.Contains(err.Error(), "git config --global user.name") {
		t.Fatalf("expected guidance to set git config, got: %v", err)
	}
}

// TestErrSentinels_DistinctPrePostInit documents the contract that the two
// error sentinels signal: errBootstrapDeclined before `git init`,
// errBootstrapInterrupted after. setup.go relies on this to show the
// right user-facing message.
func TestErrSentinels_DistinctPrePostInit(t *testing.T) {
	t.Parallel()
	if errors.Is(errBootstrapDeclined, errBootstrapInterrupted) {
		t.Fatal("errBootstrapDeclined and errBootstrapInterrupted must not match as the same sentinel")
	}
}

func TestEnableCmd_InitCommitMessageFlagsMutuallyExclusive(t *testing.T) {
	setupTestRepo(t)

	cmd := newEnableCmd()
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--initial-commit-message", "foo", "--skip-initial-commit"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when both --initial-commit-message and --skip-initial-commit are set")
	}
	if !strings.Contains(err.Error(), "initial-commit-message") || !strings.Contains(err.Error(), "skip-initial-commit") {
		t.Fatalf("expected error to mention both flags, got: %v", err)
	}
}

func TestEnableCmd_InitRepoFlagsMutuallyExclusive(t *testing.T) {
	setupTestRepo(t)

	cmd := newEnableCmd()
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--init-repo", "--no-init-repo"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when both --init-repo and --no-init-repo are set")
	}
	if !strings.Contains(err.Error(), "init-repo") || !strings.Contains(err.Error(), "no-init-repo") {
		t.Fatalf("expected error to mention both flags, got: %v", err)
	}
}

// withInteractivePromptStdin forces interactive, accessible (text-based)
// prompt mode and feeds input to os.Stdin for the duration of the test, so a
// huh prompt reads a scripted answer instead of opening /dev/tty or blocking
// on a real terminal. ENTIRE_TEST_TTY makes CanPromptInteractively report
// true; ACCESSIBLE makes the form read os.Stdin rather than dial the terminal.
func withInteractivePromptStdin(t *testing.T, input string) {
	t.Helper()
	t.Setenv("ENTIRE_TEST_TTY", "1")
	t.Setenv("ACCESSIBLE", "1")
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pr.Close() })
	go func() {
		pw.WriteString(input) //nolint:errcheck // test helper
		pw.Close()
	}()
	old := os.Stdin
	os.Stdin = pr
	t.Cleanup(func() { os.Stdin = old })
}

// TestConfirmInitRepo_DefaultsToNo verifies that pressing Enter (empty
// input) at the init-repo prompt declines. `entire enable` is often run
// reflexively, so a stray run in a non-repo directory must not initialize
// a repo on the user's behalf. Regression guard for issue #1717.
func TestConfirmInitRepo_DefaultsToNo(t *testing.T) {
	withInteractivePromptStdin(t, "\n")

	proceed, err := confirmInitRepo(io.Discard, t.TempDir(), BootstrapOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if proceed {
		t.Fatal("confirmInitRepo should default to No (decline) on empty input")
	}
}

// TestConfirmInitRepo_ExplicitYesProceeds verifies an explicit "y" still
// opts in, so the safer default doesn't block intentional use.
func TestConfirmInitRepo_ExplicitYesProceeds(t *testing.T) {
	withInteractivePromptStdin(t, "y\n")

	proceed, err := confirmInitRepo(io.Discard, t.TempDir(), BootstrapOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !proceed {
		t.Fatal("confirmInitRepo should proceed when the user explicitly answers yes")
	}
}

func TestPromptBootstrapSetupChoice_DefaultsToLocalInitialCommit(t *testing.T) {
	withInteractivePromptStdin(t, "\n")

	var out bytes.Buffer
	choice, err := promptBootstrapSetupChoice(&out, "/tmp/example")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if choice != bootstrapSetupLocal {
		t.Fatalf("choice = %q, want %q", choice, bootstrapSetupLocal)
	}
	if !strings.Contains(out.String(), "Set one up?") {
		t.Fatalf("expected merged init+setup prompt, got: %s", out.String())
	}
	// The wrong-directory guard: the prompt must show where the repo would
	// be created (issue #1717's concern, carried over from the confirm).
	if !strings.Contains(out.String(), "/tmp/example") {
		t.Fatalf("expected prompt to show the target directory, got: %s", out.String())
	}
}

func TestPromptBootstrapSetupChoice_OffersCustomizeSecond(t *testing.T) {
	withInteractivePromptStdin(t, "2\n")

	choice, err := promptBootstrapSetupChoice(io.Discard, "/tmp/example")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if choice != bootstrapSetupCustom {
		t.Fatalf("choice = %q, want %q", choice, bootstrapSetupCustom)
	}
}

func TestPromptBootstrapSetupChoice_OffersDecline(t *testing.T) {
	// Options are local(1) / customize(2) / No(3).
	withInteractivePromptStdin(t, "3\n")

	choice, err := promptBootstrapSetupChoice(io.Discard, "/tmp/example")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if choice != bootstrapSetupDecline {
		t.Fatalf("choice = %q, want %q", choice, bootstrapSetupDecline)
	}
}

func TestRunBootstrapInit_InteractiveLocalPresetUsesOneSetupAnswer(t *testing.T) {
	dir := t.TempDir()
	restoreCwd(t, dir)
	withInteractivePromptStdin(t, "\n")

	r := newFakeRunner()
	r.setIdentityConfigured()
	r.set("git", []string{"init"}, "", nil)

	var out bytes.Buffer
	state, err := runBootstrapInitWith(
		context.Background(), &out, io.Discard,
		BootstrapOptions{}, r,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !state.commit || state.message != defaultInitialCommitMessage {
		t.Fatalf("local preset commit = %v, message = %q", state.commit, state.message)
	}
	if !strings.Contains(out.String(), "Set one up?") {
		t.Fatalf("expected merged init+setup prompt, got: %s", out.String())
	}
}

// TestRunBootstrapInit_InteractiveDeclineRunsNoGit verifies that
// declining the merged prompt leaves the folder untouched: the select runs
// before `git init`, so "No" must not create a repository.
func TestRunBootstrapInit_InteractiveDeclineRunsNoGit(t *testing.T) {
	dir := t.TempDir()
	restoreCwd(t, dir)
	// The option list is local(1) / customize(2) / No(3).
	withInteractivePromptStdin(t, "3\n")

	r := newFakeRunner()
	_, err := runBootstrapInitWith(
		context.Background(), io.Discard, io.Discard,
		BootstrapOptions{}, r,
	)
	if !errors.Is(err, errBootstrapDeclined) {
		t.Fatalf("err = %v, want errBootstrapDeclined", err)
	}
	if r.hasCall(gitArgsMatch([]string{"init"})) {
		t.Fatal("declining the merged prompt must not run git init")
	}
}

// restoreCwd chdirs into dir for the duration of the test.
func restoreCwd(t *testing.T, dir string) {
	t.Helper()
	// macOS resolves /tmp → /private/tmp; canonicalize for safety.
	canon, err := filepath.EvalSymlinks(dir)
	if err != nil {
		canon = dir
	}
	t.Chdir(canon)
}

func TestRunBootstrap_YesAcceptsAllDefaults(t *testing.T) {
	// --yes should init the repo and commit with the default message,
	// without any interactive prompts. It creates no remote: see
	// TestRunBootstrap_CreatesNoRemote.
	dir := t.TempDir()
	restoreCwd(t, dir)

	r := newFakeRunner()
	r.setIdentityConfigured()
	r.set("git", []string{"init"}, "", nil)
	r.set("git", []string{"add", "-A"}, "", nil)
	r.set("git", []string{"--no-optional-locks", "status", "--porcelain"}, " M f\n", nil)
	r.set("git", []string{"-c", "commit.gpgsign=false", "commit", "-m", defaultInitialCommitMessage}, "", nil)

	var stdout bytes.Buffer
	err := runBootstrapWith(context.Background(), &stdout, io.Discard, BootstrapOptions{Yes: true}, r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !r.hasCall(gitArgsMatch([]string{"init"})) {
		t.Error("expected git init")
	}
	if !r.hasCall(gitArgsMatch([]string{"-c", "commit.gpgsign=false", gitCmdCommit, "-m", defaultInitialCommitMessage})) {
		t.Error("expected commit with default 'Initial commit' message")
	}
}
