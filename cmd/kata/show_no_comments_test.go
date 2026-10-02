package main

import (
	"bytes"
	"context"
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

// The rendered path receives the response after --no-comments has dropped the
// comments, so only the description reaches the Markdown renderer and the
// omitted count still prints.
func TestShow_NoCommentsRenderedOutputRendersOnlyDescription(t *testing.T) {
	var response showResponseForCLI
	response.Issue.ShortID = "abc4"
	response.Issue.Title = "rendered fields"
	response.Issue.Status = "open"
	response.Issue.Author = "user-a"
	response.Issue.Body = "## Description"
	response.CommentsOmitted = 2
	renderer := &recordingShowMarkdownRenderer{}
	var out bytes.Buffer

	require.NoError(t, renderAndPrintShowHuman(
		context.Background(), &out, response, "example-project", renderer, 80,
	))

	require.Len(t, renderer.calls, 1)
	assert.Equal(t, markdownDescription, renderer.calls[0].kind)
	assert.Contains(t, out.String(), "\x1b[1mDescription\x1b[0m")
	assert.Contains(t, out.String(), "\n--- comments: 2 omitted ---\n")
}

func TestShow_NoCommentsWithRenderFlagKeepsOmittedCount(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	ref := createIssueWithTwoComments(t, env, dir, pid)

	out := runCLI(t, env, dir, "show", ref, "--no-comments", "--render")

	assert.Contains(t, out, "--- comments: 2 omitted ---")
	assert.NotContains(t, out, "Reproduced on macOS.")
}
