package github

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractRepoInfo(t *testing.T) {
	tests := []struct {
		name      string
		give      string
		githubURL string

		wantOwner string
		wantRepo  string
	}{
		{
			name:      "https",
			give:      "https://github.com/example/repo",
			wantOwner: "example",
			wantRepo:  "repo",
		},
		{
			name:      "ssh",
			give:      "git@github.com:example/repo",
			wantOwner: "example",
			wantRepo:  "repo",
		},
		{
			name:      "ssh with git protocol",
			give:      "ssh://git@github.com/example/repo",
			wantOwner: "example",
			wantRepo:  "repo",
		},
		{
			name:      "https/trailing slash",
			give:      "https://github.com/example/repo/",
			wantOwner: "example",
			wantRepo:  "repo",
		},
		{
			name:      "ssh/.git",
			give:      "git@github.com:example/repo.git",
			wantOwner: "example",
			wantRepo:  "repo",
		},
		{
			name:      "https/.git/trailing slash",
			give:      "https://github.com/example/repo.git/",
			wantOwner: "example",
			wantRepo:  "repo",
		},
		{
			name:      "https/custom URL",
			give:      "https://example.com/example/repo",
			githubURL: "https://example.com",
			wantOwner: "example",
			wantRepo:  "repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := Forge{Options: Options{URL: tt.githubURL}}
			owner, repo, err := extractRepoInfo(f.URL(), tt.give)
			require.NoError(t, err)

			assert.Equal(t, tt.wantOwner, owner, "owner")
			assert.Equal(t, tt.wantRepo, repo, "repo")
		})
	}
}

func TestExtractRepoInfoErrors(t *testing.T) {
	tests := []struct {
		name      string
		give      string
		githubURL string

		wantErr []string
	}{
		{
			name:      "bad github URL",
			give:      "https://github.com/example/repo",
			githubURL: "NOT\tA\nVALID URL",
			wantErr:   []string{"bad base URL"},
		},
		{
			name:    "bad remote URL",
			give:    "NOT\tA\nVALID URL",
			wantErr: []string{"parse remote URL"},
		},
		{
			name: "host mismatch",
			give: "https://example.com/example/repo",
			wantErr: []string{
				"not a GitHub URL",
				`expected host "github.com"`,
			},
		},
		{
			name:    "no owner",
			give:    "https://github.com/repo",
			wantErr: []string{"does not contain a GitHub repository"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := Forge{Options: Options{URL: tt.githubURL}}
			_, _, err := extractRepoInfo(f.URL(), tt.give)
			require.Error(t, err)

			for _, want := range tt.wantErr {
				assert.ErrorContains(t, err, want)
			}
		})
	}
}
