package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/entireio/auth-go/tokenmanager"
	"github.com/entireio/cli/cmd/entire/cli/api"
	cliauth "github.com/entireio/cli/cmd/entire/cli/auth"
	"github.com/entireio/cli/cmd/entire/cli/interactive"
	"github.com/entireio/cli/internal/entireclient/contexts"
)

var (
	errEntireLoginRequired    = errors.New("entire login required")
	errEntireEnvTokenRejected = errors.New("entire token rejected")
)

type identityGuidanceError string

func (e identityGuidanceError) Error() string { return string(e) }

const unattendedIdentityGuidance = "Git identity is missing, and Entire authentication is required.\n" +
	"This unattended environment cannot complete sign-in automatically.\n" +
	"Run `entire login` in an interactive shell, then rerun `entire enable`.\n" +
	"For unattended use, provide a valid user token in ENTIRE_TOKEN."

const envTokenIdentityGuidance = "ENTIRE_TOKEN could not authenticate an Entire user profile.\n" +
	"ENTIRE_TOKEN overrides stored logins, so automatic sign-in cannot repair this session.\n" +
	"Fix or unset ENTIRE_TOKEN, then rerun `entire enable`."

type gitIdentityResolver func(context.Context) (*authProfile, error)
type identityResolverFactory func(io.Writer, io.Writer, bool) gitIdentityResolver

type identityProfileDependencies struct {
	lookupEnv     func(string) (string, bool)
	contexts      contextsProvider
	resolveLogin  loginTokenResolver
	fetchProfile  profileFetcher
	allowInsecure bool
}

type identityProfileResult struct {
	profile     *authProfile
	loginServer string
}

type identityRecoveryDependencies struct {
	resolve      func(context.Context) (identityProfileResult, error)
	login        func(context.Context, io.Writer, io.Writer, string, bool) error
	isUnattended func() bool
}

func defaultIdentityProfileDependencies(insecure bool) identityProfileDependencies {
	return identityProfileDependencies{
		lookupEnv:     os.LookupEnv,
		contexts:      cliauth.Contexts,
		resolveLogin:  cliauth.RefreshedLoginToken,
		fetchProfile:  defaultFetchProfile,
		allowInsecure: insecure,
	}
}

func defaultIdentityRecoveryDependencies(insecure bool) identityRecoveryDependencies {
	profileDeps := defaultIdentityProfileDependencies(insecure)
	return identityRecoveryDependencies{
		resolve: func(ctx context.Context) (identityProfileResult, error) {
			return resolveEntireIdentityProfile(ctx, profileDeps)
		},
		login: func(ctx context.Context, outW, errW io.Writer, server string, insecure bool) error {
			return runLoginCommand(ctx, outW, errW, server, insecure, false)
		},
		isUnattended: interactive.IsKnownUnattended,
	}
}

func newEntireGitIdentityResolver(outW, errW io.Writer, insecure bool) gitIdentityResolver {
	deps := defaultIdentityRecoveryDependencies(insecure)
	return func(ctx context.Context) (*authProfile, error) {
		applyInsecureHTTPAuth(insecure)
		return recoverGitIdentity(ctx, outW, errW, insecure, deps)
	}
}

func gitIdentityFromEntireProfile(profile *authProfile, existingName, existingEmail string) (string, string, error) {
	if profile == nil {
		return "", "", errors.New("entire profile does not contain a verified Git name and email")
	}

	name := strings.TrimSpace(existingName)
	handle := strings.TrimSpace(profile.Handle)
	if name == "" {
		name = strings.TrimSpace(profile.DisplayName)
		if name == "" {
			name = handle
		}
	}

	email := strings.TrimSpace(existingEmail)
	provider := strings.TrimSpace(profile.Provider)
	providerUserID := strings.TrimSpace(profile.ProviderUserID)
	if email == "" {
		email = strings.TrimSpace(profile.Email)
		if email == "" && provider == "github" && providerUserID != "" && handle != "" {
			email = fmt.Sprintf("%s+%s@users.noreply.github.com", providerUserID, handle)
		}
	}

	if name == "" || email == "" {
		return "", "", errors.New("entire profile does not contain a verified Git name and email")
	}
	return name, email, nil
}

func ensureGitIdentity(
	ctx context.Context,
	w io.Writer,
	runner bootstrapRunner,
	dir string,
	resolve gitIdentityResolver,
) error {
	existingName, nameSet, err := readGitIdentityField(ctx, runner, dir, "user.name")
	if err != nil {
		return err
	}
	existingEmail, emailSet, err := readGitIdentityField(ctx, runner, dir, "user.email")
	if err != nil {
		return err
	}
	if nameSet && emailSet {
		return nil
	}

	profile, err := resolve(ctx)
	if err != nil {
		return err
	}
	name, email, err := gitIdentityFromEntireProfile(profile, existingName, existingEmail)
	if err != nil {
		return err
	}

	if !nameSet {
		if _, err := runner.RunInDir(ctx, dir, "git", "config", "user.name", name); err != nil {
			return wrapExecError("git config user.name", err)
		}
	}
	if !emailSet {
		if _, err := runner.RunInDir(ctx, dir, "git", "config", "user.email", email); err != nil {
			return wrapExecError("git config user.email", err)
		}
	}
	fmt.Fprintf(w, "  Using git identity: %s <%s>\n", name, email)
	return nil
}

func readGitIdentityField(ctx context.Context, runner bootstrapRunner, dir, key string) (string, bool, error) {
	out, err := runner.RunInDir(ctx, dir, "git", "config", "--get", key)
	if err != nil {
		var exitErr interface{ ExitCode() int }
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", false, nil
		}
		return "", false, wrapExecError("read git config "+key, err)
	}
	value := strings.TrimSpace(out)
	return value, value != "", nil
}

func resolveEntireIdentityProfile(ctx context.Context, deps identityProfileDependencies) (identityProfileResult, error) {
	if raw, ok := deps.lookupEnv(cliauth.EnvTokenVar); ok {
		target, err := resolveEnvTokenStatusTarget(raw)
		if err != nil {
			return identityProfileResult{}, fmt.Errorf("%w: %w", errEntireEnvTokenRejected, err)
		}
		profile, err := deps.fetchProfile(ctx, target.coreURL, target.token)
		if err != nil {
			if isKeychainTokenRejected(err) {
				return identityProfileResult{loginServer: target.coreURL}, fmt.Errorf("%w: %w", errEntireEnvTokenRejected, err)
			}
			return identityProfileResult{loginServer: target.coreURL}, err
		}
		return identityProfileResult{profile: profile, loginServer: target.coreURL}, nil
	}

	all, current, err := deps.contexts()
	if err != nil {
		return identityProfileResult{}, err
	}
	result := identityProfileResult{loginServer: api.DefaultAuthBaseURL}
	var active *contexts.Context
	for _, candidate := range all {
		if candidate != nil && candidate.Name == current {
			active = candidate
			break
		}
	}
	if active == nil {
		return result, errEntireLoginRequired
	}
	if strings.TrimSpace(active.CoreURL) != "" {
		result.loginServer = active.CoreURL
	}
	if !deps.allowInsecure {
		if err := api.RequireSecureURL(active.CoreURL); err != nil {
			return result, fmt.Errorf("context login server URL check: %w", err)
		}
	}
	token, err := deps.resolveLogin(ctx, active)
	if err != nil {
		if errors.Is(err, cliauth.ErrNotLoggedIn) || errors.Is(err, tokenmanager.ErrReauthRequired) {
			return result, fmt.Errorf("%w: %w", errEntireLoginRequired, err)
		}
		return result, err
	}
	if strings.TrimSpace(token) == "" {
		return result, errEntireLoginRequired
	}
	profile, err := deps.fetchProfile(ctx, active.CoreURL, token)
	if err != nil {
		if isKeychainTokenRejected(err) {
			return result, fmt.Errorf("%w: %w", errEntireLoginRequired, err)
		}
		return result, err
	}
	result.profile = profile
	return result, nil
}

func recoverGitIdentity(
	ctx context.Context,
	outW, errW io.Writer,
	insecure bool,
	deps identityRecoveryDependencies,
) (*authProfile, error) {
	result, err := deps.resolve(ctx)
	if err == nil {
		return result.profile, nil
	}
	if errors.Is(err, errEntireEnvTokenRejected) {
		return nil, identityGuidanceError(envTokenIdentityGuidance)
	}
	if !errors.Is(err, errEntireLoginRequired) {
		return nil, err
	}
	if deps.isUnattended() {
		return nil, identityGuidanceError(unattendedIdentityGuidance)
	}
	if err := deps.login(ctx, outW, errW, result.loginServer, insecure); err != nil {
		return nil, err
	}
	result, err = deps.resolve(ctx)
	if err != nil {
		if errors.Is(err, errEntireEnvTokenRejected) {
			return nil, identityGuidanceError(envTokenIdentityGuidance)
		}
		return nil, fmt.Errorf("resolve Entire profile after login: %w", err)
	}
	return result.profile, nil
}
