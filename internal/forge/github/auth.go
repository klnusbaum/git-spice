package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/shurcooL/githubv4"
	"go.abhg.dev/gs/internal/forge"
	"go.abhg.dev/gs/internal/secret"
	"go.abhg.dev/gs/internal/ui"
	"golang.org/x/oauth2"
)

const (
	_oauthAppClientID  = "Ov23lin9rC3LWqd4ks2f"
	_githubAppClientID = "Iv23lifdKaAyYAHQwxNp"
	// (These are not secret.)
)

// AuthenticationToken defines the token returned by the GitHub forge.
type AuthenticationToken struct {
	forge.AuthenticationToken

	// GitHubCLI is true if we should use GitHub CLI for API requests.
	//
	// If true, AccessToken is not used.
	GitHubCLI bool `json:"github_cli,omitempty"`

	// AccessToken is the GitHub access token.
	AccessToken string `json:"access_token,omitempty"`
}

var _ forge.AuthenticationToken = (*AuthenticationToken)(nil)

func (t *AuthenticationToken) githubv4Client(ctx context.Context, apiURL string) (*githubv4.Client, error) {
	graphQLAPIURL, err := url.JoinPath(apiURL, "/graphql")
	if err != nil {
		return nil, fmt.Errorf("build GraphQL API URL: %w", err)
	}

	var tokenSource oauth2.TokenSource
	if t.GitHubCLI {
		tokenSource = &CLITokenSource{}
	} else {
		tokenSource = oauth2.StaticTokenSource(&oauth2.Token{AccessToken: t.AccessToken})
	}

	httpClient := oauth2.NewClient(ctx, tokenSource)
	return githubv4.NewEnterpriseClient(graphQLAPIURL, httpClient), nil
}

func (f *Forge) oauth2Endpoint() (oauth2.Endpoint, error) {
	u, err := url.Parse(f.URL())
	if err != nil {
		return oauth2.Endpoint{}, fmt.Errorf("bad GitHub URL: %w", err)
	}

	return oauth2.Endpoint{
		AuthURL:       u.JoinPath("/login/oauth/authorize").String(),
		TokenURL:      u.JoinPath("/login/oauth/access_token").String(),
		DeviceAuthURL: u.JoinPath("/login/device/code").String(),
	}, nil
}

// AuthenticationFlow prompts the user to authenticate with GitHub.
// This rejects the request if the user is already authenticated
// with a GITHUB_TOKEN environment variable.
func (f *Forge) AuthenticationFlow(ctx context.Context) (forge.AuthenticationToken, error) {
	// Already authenticated with GITHUB_TOKEN.
	// If the user tries to authenticate again, we should error.
	if f.Options.Token != "" {
		// NB: alternatively, we can make this a no-op,
		// and just omit saving it to the stash.
		// Adjust based on user feedback.
		f.Log.Error("Already authenticated with GITHUB_TOKEN.")
		f.Log.Error("Unset GITHUB_TOKEN to login with a different method.")
		return nil, errors.New("already authenticated")
	}

	oauthEndpoint, err := f.oauth2Endpoint()
	if err != nil {
		return nil, fmt.Errorf("get OAuth endpoint: %w", err)
	}

	return (&githubAuthenticator{
		Endpoint: oauthEndpoint,
		Stdin:    os.Stdin,
		Stderr:   os.Stderr,
	}).Authenticate(ctx)
}

// SaveAuthenticationToken saves the given authentication token to the stash.
func (f *Forge) SaveAuthenticationToken(stash secret.Stash, t forge.AuthenticationToken) error {
	ght := t.(*AuthenticationToken)
	if f.Options.Token != "" && f.Options.Token == ght.AccessToken {
		// If the user has set GITHUB_TOKEN,
		// we should not save it to the stash.
		return nil
	}

	bs, err := json.Marshal(ght)
	if err != nil {
		return fmt.Errorf("marshal token: %w", err)
	}

	return stash.SaveSecret(f.URL(), "token", string(bs))
}

// LoadAuthenticationToken loads the authentication token from the stash.
// If the user has set GITHUB_TOKEN, it will be used instead.
func (f *Forge) LoadAuthenticationToken(stash secret.Stash) (forge.AuthenticationToken, error) {
	if f.Options.Token != "" {
		// If the user has set GITHUB_TOKEN, we should use that
		// regardless of what's in the stash.
		return &AuthenticationToken{AccessToken: f.Options.Token}, nil
	}

	tokstr, err := stash.LoadSecret(f.URL(), "token")
	if err != nil {
		return nil, fmt.Errorf("load token: %w", err)
	}

	var tok AuthenticationToken
	if err := json.Unmarshal([]byte(tokstr), &tok); err != nil {
		// Old token format, just use it as the access token.
		return &AuthenticationToken{AccessToken: tokstr}, nil
	}

	return &tok, nil
}

// ClearAuthenticationToken removes the authentication token from the stash.
func (f *Forge) ClearAuthenticationToken(stash secret.Stash) error {
	return stash.DeleteSecret(f.URL(), "token")
}

type authenticationMethod func(context.Context) (forge.AuthenticationToken, error)

// githubAuthenticator presents the user with multiple authentication methods,
// prompts them to choose one, and executes the chosen method.
type githubAuthenticator struct {
	Endpoint oauth2.Endpoint
	Stdin    io.Reader
	Stderr   io.Writer
}

func (a *githubAuthenticator) Authenticate(ctx context.Context) (forge.AuthenticationToken, error) {
	methods := []ui.ListItem[authenticationMethod]{
		{
			Title:       "OAuth",
			Description: _oauthDesc,
			Value: (&DeviceFlowAuthenticator{
				Endpoint: a.Endpoint,
				Stderr:   a.Stderr,
				ClientID: _oauthAppClientID,
				Scopes:   []string{"repo"},
			}).Authenticate,
		},
		{
			Title:       "OAuth: Public repositories only",
			Description: _oauthPublicDesc,
			Value: (&DeviceFlowAuthenticator{
				Endpoint: a.Endpoint,
				Stderr:   a.Stderr,
				ClientID: _oauthAppClientID,
				Scopes:   []string{"public_repo"},
			}).Authenticate,
		},
		{
			Title:       "GitHub App",
			Description: _githubAppDesc,
			Value: (&DeviceFlowAuthenticator{
				Endpoint: a.Endpoint,
				Stderr:   a.Stderr,
				ClientID: _githubAppClientID,
				// No scopes needed for GitHub App.
			}).Authenticate,
		},
		{
			Title:       "Personal Access Token",
			Description: _patDesc,
			Value: (&PATAuthenticator{
				Stdin:  a.Stdin,
				Stderr: a.Stderr,
			}).Authenticate,
		},
	}

	// If the user has gh installed,
	// also offer the option to authenticate with gh.
	if ghExe, err := exec.LookPath("gh"); err == nil {
		methods = append(methods, ui.ListItem[authenticationMethod]{
			Title:       "GitHub CLI",
			Description: _ghDesc,
			Value:       (&CLIAuthenticator{GH: ghExe}).Authenticate,
		})
	}

	var method authenticationMethod
	field := ui.NewList[authenticationMethod]().
		WithTitle("Select an authentication method").
		WithItems(methods...).
		WithValue(&method)
	err := ui.Run(field, ui.WithInput(a.Stdin), ui.WithOutput(a.Stderr))
	if err != nil {
		return nil, err
	}

	return method(ctx)
}

var _oauthDesc = strings.TrimSpace(`
Authorize git-spice to act on your behalf from this device only.
git-spice will get access to all repositories: public and private.
For private repositories, you will need to request installation from a repository owner.
`)

var _oauthPublicDesc = strings.TrimSpace(`
Authorize git-spice to act on your behalf from this device only.
git-spice will only get access to public repositories.
`)

var _githubAppDesc = strings.TrimSpace(`
Authorize git-spice to act on your behalf from this device only.
git-spice will only get access to repositories where the git-spice GitHub App is installed explicitly.
Use https://github.com/apps/git-spice to install the App on repositories.
For private repositories, you will need to request installation from a repository owner.
`)

var _patDesc = strings.TrimSpace(`
Enter a classic or fine-grained Personal Access Token generated from https://github.com/settings/tokens.
Classic tokens need at least one of the following scopes: repo or public_repo.
Fine-grained tokens need read/write access to Repository Contents and Pull requests.
You can use this method if you do not have the ability to install a GitHub or OAuth App on your repositories.
`)

var _ghDesc = strings.TrimSpace(`
Re-use an existing GitHub CLI (https://cli.github.com) session.
You must be logged into gh with 'gh auth login' for this to work.
You can use this if you're just experimenting and don't want to set up a token yet.
`)

// DeviceFlowAuthenticator implements the OAuth device flow for GitHub.
// This is used for OAuth and GitHub App authentication.
type DeviceFlowAuthenticator struct {
	// Endpoint is the OAuth endpoint to use.
	Endpoint oauth2.Endpoint

	// ClientID for the OAuth or GitHub App.
	ClientID string

	// Scopes specifies the OAuth scopes to request.
	Scopes []string

	Stderr io.Writer
}

// Authenticate executes the OAuth authentication flow.
func (a *DeviceFlowAuthenticator) Authenticate(ctx context.Context) (forge.AuthenticationToken, error) {
	cfg := oauth2.Config{
		ClientID:    a.ClientID,
		Endpoint:    a.Endpoint,
		Scopes:      a.Scopes,
		RedirectURL: "http://127.0.0.1/callback",
	}

	resp, err := cfg.DeviceAuth(ctx)
	if err != nil {
		return nil, err
	}

	urlStle := ui.NewStyle().Foreground(ui.Cyan).Bold(true).Underline(true)
	codeStyle := ui.NewStyle().Foreground(ui.Cyan).Bold(true)
	bullet := ui.NewStyle().PaddingLeft(2).Foreground(ui.Gray)
	faint := ui.NewStyle().Faint(true)

	fmt.Fprintf(a.Stderr, "%s Visit %s\n", bullet.Render("1."), urlStle.Render(resp.VerificationURI))
	fmt.Fprintf(a.Stderr, "%s Enter code: %s\n", bullet.Render("2."), codeStyle.Render(resp.UserCode))
	fmt.Fprintln(a.Stderr, faint.Render("The code expires in a few minutes."))
	fmt.Fprintln(a.Stderr, faint.Render("It will take a few seconds to verify after you enter it."))
	// TODO: maybe open browser with flag opt-out

	token, err := cfg.DeviceAccessToken(ctx, resp,
		oauth2.SetAuthURLParam("grant_type", "urn:ietf:params:oauth:grant-type:device_code"))
	if err != nil {
		return nil, err
	}

	return &AuthenticationToken{AccessToken: token.AccessToken}, nil
}

// PATAuthenticator implements PAT authentication for GitHub.
type PATAuthenticator struct {
	Stdin  io.Reader
	Stderr io.Writer
}

// Authenticate prompts the user for a Personal Access Token,
// validates it, and returns the token if successful.
func (a *PATAuthenticator) Authenticate(ctx context.Context) (forge.AuthenticationToken, error) {
	var token string
	err := ui.Run(ui.NewInput().
		WithTitle("Enter Personal Access Token").
		WithValidate(func(input string) error {
			if strings.TrimSpace(input) == "" {
				return errors.New("token is required")
			}
			return nil
		}).WithValue(&token),
		ui.WithInput(a.Stdin),
		ui.WithOutput(a.Stderr),
	)
	if err != nil {
		return nil, err
	}

	// TODO: Should we validate the token by making a request?
	return &AuthenticationToken{AccessToken: token}, nil
}

// CLIAuthenticator implements GitHub CLI authentication flow.
// This doesn't do anything special besides checking if the user is logged in.
type CLIAuthenticator struct {
	GH string

	runCmd func(*exec.Cmd) error
}

// Authenticate checks if the user is authenticated with GitHub CLI.
func (a *CLIAuthenticator) Authenticate(ctx context.Context) (forge.AuthenticationToken, error) {
	runCmd := (*exec.Cmd).Run
	if a.runCmd != nil {
		runCmd = a.runCmd
	}

	if err := runCmd(exec.Command(a.GH, "auth", "token")); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, errors.Join(
				errors.New("gh is not authenticated"),
				fmt.Errorf("stderr: %s", exitErr.Stderr),
			)
		}
		return nil, fmt.Errorf("run gh: %w", err)
	}

	return &AuthenticationToken{GitHubCLI: true}, nil
}