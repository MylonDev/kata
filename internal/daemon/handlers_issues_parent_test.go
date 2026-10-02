package daemon_test

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kata/internal/db"
	"go.kenn.io/kata/internal/testenv"
)

func TestListIssuesParentUID(t *testing.T) {
	env := testenv.New(t)
	ctx := context.Background()
	home, err := env.DB.CreateProject(ctx, "list-parent-home")
	require.NoError(t, err)
	other, err := env.DB.CreateProject(ctx, "list-parent-other")
	require.NoError(t, err)
	newIssue := func(projectID int64, title string) db.Issue {
		issue, _, createErr := env.DB.CreateIssue(ctx, db.CreateIssueParams{
			ProjectID: projectID, Title: title, Author: "fixture",
		})
		require.NoError(t, createErr)
		return issue
	}
	parent := newIssue(home.ID, "parent issue")
	newIssue(home.ID, "unrelated issue")
	for _, child := range []db.Issue{
		newIssue(home.ID, "home child"),
		newIssue(other.ID, "foreign child"),
	} {
		_, err = env.DB.CreateLink(ctx, db.CreateLinkParams{
			FromIssueID: child.ID, ToIssueID: parent.ID, Type: "parent", Author: "fixture",
		})
		require.NoError(t, err)
	}

	get := func(path string, query url.Values) (int, []byte) {
		request, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, env.URL+path+"?"+query.Encode(), nil)
		require.NoError(t, reqErr)
		response, doErr := env.HTTP.Do(request)
		require.NoError(t, doErr)
		defer func() { _ = response.Body.Close() }()
		body, readErr := io.ReadAll(response.Body)
		require.NoError(t, readErr)
		return response.StatusCode, body
	}
	scopedPath := "/api/v1/projects/" + strconv.FormatInt(home.ID, 10) + "/issues"
	byParent := url.Values{"parent_uid": {parent.UID}}

	status, body := get(scopedPath, byParent)
	require.Equal(t, http.StatusOK, status, string(body))
	assert.Equal(t, []string{"home child"}, listIssueTitles(t, body))

	status, body = get("/api/v1/issues", byParent)
	require.Equal(t, http.StatusOK, status, string(body))
	assert.ElementsMatch(t, []string{"home child", "foreign child"}, listIssueTitles(t, body))

	for _, path := range []string{scopedPath, "/api/v1/issues"} {
		status, body = get(path, url.Values{"parent_uid": {"not-a-uid"}})
		assert.Equal(t, http.StatusBadRequest, status, string(body))
		assert.Contains(t, string(body), "parent_uid must be an issue UID")
	}
}
