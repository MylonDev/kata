package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kata/internal/testenv"
)

// createIssueWithTwoComments seeds an issue with a body, a label, and two
// comments, and returns its short_id.
func createIssueWithTwoComments(t *testing.T, env *testenv.Env, dir string, pid int64) string {
	t.Helper()
	type createResp struct {
		Issue struct {
			ShortID string `json:"short_id"`
		} `json:"issue"`
	}
	created := postJSON[createResp](t, env.URL+"/api/v1/projects/"+itoa(pid)+"/issues",
		map[string]any{
			"actor":  "tester",
			"title":  "Safari callback",
			"body":   "Safari can double-submit the callback.",
			"labels": []string{"bug"},
		})
	ref := created.Issue.ShortID
	runCLI(t, env, dir, "--as", "tester", "comment", ref, "--body", "Reproduced on macOS.")
	runCLI(t, env, dir, "--as", "tester", "comment", ref, "--body", "Also on iOS.")
	return ref
}

func TestShow_NoCommentsHumanOmitsCommentsAndReportsCount(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	ref := createIssueWithTwoComments(t, env, dir, pid)

	out := runCLI(t, env, dir, "show", ref, "--no-comments")

	assert.Contains(t, out, "Safari can double-submit the callback.")
	assert.Contains(t, out, "\n--- comments: 2 omitted ---\n")
	assert.Contains(t, out, "--- labels ---\nbug")
	assert.NotContains(t, out, "Reproduced on macOS.")
	assert.NotContains(t, out, "Also on iOS.")
	assert.NotContains(t, out, "--- comments ---")
}

func TestShow_NoCommentsAgentOmitsCommentsAndReportsCount(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	ref := createIssueWithTwoComments(t, env, dir, pid)

	out := runCLI(t, env, dir, "--agent", "show", ref, "--no-comments")

	assert.Contains(t, out,
		"Body:\n```text\nSafari can double-submit the callback.\n```\nComments-Omitted: 2")
	assert.NotContains(t, out, "Comments:\n")
	assert.NotContains(t, out, "Reproduced on macOS.")
	assert.NotContains(t, out, "Also on iOS.")
}

func TestShow_NoCommentsOnIssueWithoutCommentsPrintsNoCount(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	ref := createIssue(t, env, pid, "quiet issue")

	human := runCLI(t, env, dir, "show", ref, "--no-comments")
	agent := runCLI(t, env, dir, "--agent", "show", ref, "--no-comments")

	assert.NotContains(t, human, "omitted")
	assert.NotContains(t, agent, "Comments-Omitted")
}

func TestShow_NoCommentsRejectsJSON(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	ref := createIssue(t, env, pid, "structured issue")

	_, err := runCLICapture(t, env, dir, "--json", "show", ref, "--no-comments")

	cliErr := requireCLIError(t, err, ExitUsage)
	assert.Equal(t, kindUsage, cliErr.Kind)
	assert.Equal(t, "kata show --no-comments is not available with --json", cliErr.Message)
}

func TestShow_NoCommentsWithRenderOffTerminalMatchesPlain(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	ref := createIssueWithTwoComments(t, env, dir, pid)

	plain := runCLI(t, env, dir, "show", ref, "--no-comments")
	rendered := runCLI(t, env, dir, "show", ref, "--no-comments", "--render")

	require.Contains(t, plain, "--- comments: 2 omitted ---")
	assert.Equal(t, plain, rendered)
}
