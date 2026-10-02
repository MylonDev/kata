package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kata/internal/db"
)

func TestList_Parent(t *testing.T) {
	env, dir, projectID := setupCLIWorkspace(t)
	ctx := context.Background()
	parent := createIssue(t, env, projectID, "parent issue")
	openChild := createIssue(t, env, projectID, "open child")
	closedChild := createIssue(t, env, projectID, "closed child")
	createIssue(t, env, projectID, "unrelated issue")
	createLinkViaHTTP(t, env, projectID, openChild, "parent", parent)
	createLinkViaHTTP(t, env, projectID, closedChild, "parent", parent)
	require.NoError(t, closeIssueHTTP(env, projectID, closedChild))

	parentIssue := fetchIssueViaHTTP(t, env, projectID, parent).Issue
	other, err := env.DB.CreateProject(ctx, "other-project")
	require.NoError(t, err)
	foreign, _, err := env.DB.CreateIssue(ctx, db.CreateIssueParams{
		ProjectID: other.ID, Title: "foreign child", Author: "fixture",
	})
	require.NoError(t, err)
	_, err = env.DB.CreateLink(ctx, db.CreateLinkParams{
		FromIssueID: foreign.ID, ToIssueID: parentIssue.ID, Type: "parent", Author: "fixture",
	})
	require.NoError(t, err)

	jsonTitles := func(args ...string) []string {
		t.Helper()
		var response struct {
			Issues []struct {
				Title string `json:"title"`
			} `json:"issues"`
		}
		require.NoError(t, json.Unmarshal([]byte(runCLI(t, env, dir, args...)), &response))
		titles := make([]string, len(response.Issues))
		for i, issue := range response.Issues {
			titles[i] = issue.Title
		}
		return titles
	}

	t.Run("agent rows carry each child's status", func(t *testing.T) {
		out := runCLI(t, env, dir, "--agent", "list", "--parent", parent, "--status", "all")
		assert.Contains(t, out, "OK list count=2\n")
		assert.Regexp(t, `(?m)^- issue=`+openChild+` status=open .*title="open child"`, out)
		assert.Regexp(t, `(?m)^- issue=`+closedChild+` status=closed .*title="closed child"`, out)
		assert.NotContains(t, out, "unrelated issue")
		assert.NotContains(t, out, "parent issue")
		assert.NotContains(t, out, "foreign child")
	})

	t.Run("default status lists open children only", func(t *testing.T) {
		assert.Equal(t, []string{"open child"}, jsonTitles("--json", "list", "--parent", parent))
	})

	t.Run("status closed", func(t *testing.T) {
		assert.Equal(t, []string{"closed child"},
			jsonTitles("--json", "list", "--parent", parent, "--status", "closed"))
	})

	t.Run("human output", func(t *testing.T) {
		out := runCLI(t, env, dir, "list", "--parent", parent, "--status", "all")
		assert.Contains(t, out, "open child")
		assert.Contains(t, out, "closed child")
		assert.NotContains(t, out, "unrelated issue")
		assert.NotContains(t, out, "parent issue")
	})

	t.Run("limit applies after the filter", func(t *testing.T) {
		assert.Equal(t, []string{"open child"},
			jsonTitles("--json", "list", "--parent", parent, "--status", "all", "--sort", "oldest", "--limit", "1"))
	})

	t.Run("parent given as a ULID", func(t *testing.T) {
		assert.Equal(t, []string{"open child"}, jsonTitles("--json", "list", "--parent", parentIssue.UID))
	})

	t.Run("a leaf lists nothing", func(t *testing.T) {
		assert.Equal(t, "OK list count=0", runCLI(t, env, dir, "--agent", "list", "--parent", openChild))
	})

	t.Run("all projects includes children elsewhere", func(t *testing.T) {
		for _, ref := range []string{"kata#" + parent, parentIssue.UID} {
			stdout, stderr, err := executeRootCapture(t, contextWithBaseURL(ctx, env.URL),
				"--json", "list", "--all", "--parent", ref, "--status", "all")
			require.NoError(t, err, "%s", stderr)
			var response struct {
				Issues []struct {
					Title string `json:"title"`
				} `json:"issues"`
			}
			require.NoError(t, json.Unmarshal([]byte(stdout), &response))
			titles := make([]string, len(response.Issues))
			for i, issue := range response.Issues {
				titles[i] = issue.Title
			}
			assert.ElementsMatch(t, []string{"open child", "closed child", "foreign child"}, titles, ref)
		}
	})

	t.Run("unknown parent is not found", func(t *testing.T) {
		for _, ref := range []string{"zzzz9", "01ARZ3NDEKTSV4RRFFQ69G5FAV"} {
			_, err := runCLICapture(t, env, dir, "list", "--parent", ref)
			requireCLIError(t, err, ExitNotFound)
		}
	})

	t.Run("empty parent is rejected", func(t *testing.T) {
		_, err := runCLICapture(t, env, dir, "list", "--parent", "")
		requireCLIError(t, err, ExitValidation)
	})
}
