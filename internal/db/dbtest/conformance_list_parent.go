package dbtest

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.kenn.io/kata/internal/db"
)

func checkListParentFilter(t *testing.T, store db.Storage) error {
	t.Helper()
	ctx := context.Background()
	home, err := store.CreateProject(ctx, "list-parent-home")
	if err != nil {
		return fmt.Errorf("create home project: %w", err)
	}
	other, err := store.CreateProject(ctx, "list-parent-other")
	if err != nil {
		return fmt.Errorf("create other project: %w", err)
	}
	newIssue := func(projectID int64, title string) (db.Issue, error) {
		issue, _, createErr := store.CreateIssue(ctx, db.CreateIssueParams{
			ProjectID: projectID, Title: title, Author: "conformance-agent",
		})
		if createErr != nil {
			return db.Issue{}, fmt.Errorf("create %s: %w", title, createErr)
		}
		return issue, nil
	}
	setParent := func(child, parent db.Issue) error {
		_, linkErr := store.CreateLink(ctx, db.CreateLinkParams{
			FromIssueID: child.ID, ToIssueID: parent.ID, Type: "parent", Author: "conformance-agent",
		})
		if linkErr != nil {
			return fmt.Errorf("link %s under %s: %w", child.Title, parent.Title, linkErr)
		}
		return nil
	}

	// parent
	// ├─ open child ── grandchild
	// ├─ closed child
	// ├─ deleted child (soft-deleted)
	// └─ foreign child (other project)
	// sibling parent ── sibling child
	// blocked peer (its blocks link points at parent; not a child)
	issues := map[string]db.Issue{}
	for _, spec := range []struct {
		name      string
		projectID int64
	}{
		{"parent", home.ID},
		{"open child", home.ID},
		{"closed child", home.ID},
		{"deleted child", home.ID},
		{"grandchild", home.ID},
		{"sibling parent", home.ID},
		{"sibling child", home.ID},
		{"blocked peer", home.ID},
		{"foreign child", other.ID},
	} {
		issue, createErr := newIssue(spec.projectID, spec.name)
		if createErr != nil {
			return createErr
		}
		issues[spec.name] = issue
	}
	for _, pair := range [][2]string{
		{"open child", "parent"},
		{"closed child", "parent"},
		{"deleted child", "parent"},
		{"foreign child", "parent"},
		{"grandchild", "open child"},
		{"sibling child", "sibling parent"},
	} {
		if err := setParent(issues[pair[0]], issues[pair[1]]); err != nil {
			return err
		}
	}
	if _, err := store.CreateLink(ctx, db.CreateLinkParams{
		FromIssueID: issues["blocked peer"].ID, ToIssueID: issues["parent"].ID,
		Type: "blocks", Author: "conformance-agent",
	}); err != nil {
		return fmt.Errorf("link blocks: %w", err)
	}
	if _, _, _, err := store.CloseIssue(
		ctx, issues["closed child"].ID, "done", "conformance-agent", "Completed task", nil,
	); err != nil {
		return fmt.Errorf("close child: %w", err)
	}
	if _, _, _, err := store.SoftDeleteIssue(ctx, issues["deleted child"].ID, "conformance-agent"); err != nil {
		return fmt.Errorf("delete child: %w", err)
	}

	titles := func(got []db.Issue) []string {
		out := make([]string, len(got))
		for i, issue := range got {
			out[i] = issue.Title
		}
		return out
	}
	parentUID := issues["parent"].UID

	got, err := store.ListIssues(ctx, db.ListIssuesParams{ProjectID: home.ID, ParentUID: parentUID})
	if err != nil {
		return fmt.Errorf("scoped children: %w", err)
	}
	assert.ElementsMatch(t, []string{"open child", "closed child"}, titles(got), "scoped children, any status")

	got, err = store.ListIssues(ctx, db.ListIssuesParams{ProjectID: home.ID, ParentUID: parentUID, Status: "open"})
	if err != nil {
		return fmt.Errorf("scoped open children: %w", err)
	}
	assert.Equal(t, []string{"open child"}, titles(got), "scoped open children")

	got, err = store.ListIssues(ctx, db.ListIssuesParams{
		ProjectID: home.ID, ParentUID: parentUID, OldestFirst: true, Limit: 1,
	})
	if err != nil {
		return fmt.Errorf("scoped children with limit: %w", err)
	}
	assert.Equal(t, []string{"open child"}, titles(got), "filter applies before limit")

	got, err = store.ListIssues(ctx, db.ListIssuesParams{ProjectID: other.ID, ParentUID: parentUID})
	if err != nil {
		return fmt.Errorf("other project children: %w", err)
	}
	assert.Equal(t, []string{"foreign child"}, titles(got), "scoped list in the child's project")

	got, err = store.ListIssues(ctx, db.ListIssuesParams{ProjectID: home.ID, ParentUID: issues["grandchild"].UID})
	if err != nil {
		return fmt.Errorf("children of a leaf: %w", err)
	}
	assert.Empty(t, titles(got), "a leaf has no children")

	got, err = store.ListAllIssues(ctx, db.ListAllIssuesParams{ParentUID: parentUID})
	if err != nil {
		return fmt.Errorf("global children: %w", err)
	}
	assert.ElementsMatch(t, []string{"open child", "closed child", "foreign child"}, titles(got),
		"global children span projects")

	got, err = store.ListAllIssues(ctx, db.ListAllIssuesParams{ParentUID: parentUID, ProjectID: other.ID})
	if err != nil {
		return fmt.Errorf("global children narrowed: %w", err)
	}
	assert.Equal(t, []string{"foreign child"}, titles(got), "global children narrowed to one project")

	got, err = store.ListAllIssues(ctx, db.ListAllIssuesParams{
		ParentUID: parentUID, Status: "open", OldestFirst: true, Limit: 1,
	})
	if err != nil {
		return fmt.Errorf("global children with limit: %w", err)
	}
	assert.Equal(t, []string{"open child"}, titles(got), "global filter applies before limit")
	return nil
}
