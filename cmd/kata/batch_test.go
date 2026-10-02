package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kata/internal/testenv"
)

const batchCloseMessage = "Completed the example work and checked the result by hand."

// execBatch feeds stdin to `kata batch` in the bound workspace. globalArgs go
// before the command (output mode); batchArgs after it.
func execBatch(
	t *testing.T, env *testenv.Env, dir, stdin string, globalArgs []string, batchArgs ...string,
) (stdout, stderr string, err error) {
	t.Helper()
	args := append([]string{"--workspace", dir}, globalArgs...)
	args = append(args, "batch")
	args = append(args, batchArgs...)
	return executeRootCaptureWithInput(contextWithBaseURL(context.Background(), env.URL), t, stdin, args...)
}

func batchInput(t *testing.T, args ...string) string {
	t.Helper()
	bs, err := json.Marshal(args)
	require.NoError(t, err)
	return string(bs) + "\n"
}

func batchIssueTitles(t *testing.T, env *testenv.Env, projectID int64) []string {
	t.Helper()
	got := getJSON[struct {
		Issues []struct {
			Title string `json:"title"`
		} `json:"issues"`
	}](t, env.URL+"/api/v1/projects/"+itoa(projectID)+"/issues")
	titles := make([]string, len(got.Issues))
	for i, issue := range got.Issues {
		titles[i] = issue.Title
	}
	return titles
}

func batchCommentBodies(t *testing.T, env *testenv.Env, projectID int64, ref string) []string {
	t.Helper()
	issue := fetchIssueViaHTTPWithComments(t, env, projectID, ref)
	bodies := make([]string, len(issue.Comments))
	for i, c := range issue.Comments {
		bodies[i] = c.Body
	}
	return bodies
}

func TestBatch_RejectsMalformedInputBeforeRunningAnything(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	ref := createIssue(t, env, pid, "existing issue")
	validFirst := batchInput(t, "create", "must not be created", "--idempotency-key", "batch-precheck-1")

	tests := []struct {
		name    string
		second  string
		wantMsg string
	}{
		{"not JSON", "create something\n", "line 2: not a JSON array of strings"},
		{"JSON object", `{"op":"create"}` + "\n", "line 2: not a JSON array of strings"},
		{"array with a number", `["comment","` + ref + `",7]` + "\n", "line 2: not a JSON array of strings"},
		{"empty array", "[]\n", "line 2: no command"},
		{"command outside the allowlist", batchInput(t, "delete", ref), `line 2: "delete" is not allowed in a batch`},
		{"read command", batchInput(t, "show", ref), `line 2: "show" is not allowed in a batch`},
		{"comment edit", batchInput(t, "comment", "edit", ref, "01ARZ3NDEKTSV4RRFFQ69G5FAV", "--body", "x"),
			`line 2: "comment edit" is not allowed in a batch`},
		{"label without a subcommand", batchInput(t, "label", ref, "bug"), `line 2: "label" is not allowed in a batch`},
		{"only global flags", batchInput(t, "--project", "kata"), "line 2: no command"},
		{"unknown command", batchInput(t, "cretae", "title"), `line 2: unknown command "cretae" for "kata"`},
		{"null element", `["comment","` + ref + `","--body",null]` + "\n", "line 2: not a JSON array of strings"},
		{"blank idempotency key", batchInput(t, "create", "blank key", "--idempotency-key", " "),
			"line 2: create needs --idempotency-key"},
		{"output flag", batchInput(t, "comment", ref, "--body", "x", "--json"), "line 2: --json is not allowed in a batch line"},
		{"quiet flag", batchInput(t, "comment", ref, "--body", "x", "-q"), "line 2: --quiet is not allowed in a batch line"},
		{"stdin body", batchInput(t, "comment", ref, "--body-stdin"), "line 2: --body-stdin is not allowed in a batch line"},
		{"help flag", batchInput(t, "create", "--help"), "line 2: --help is not allowed in a batch line"},
		{"unknown flag", batchInput(t, "comment", ref, "--bodyy", "x"), "line 2: unknown flag: --bodyy"},
		{"missing argument", batchInput(t, "close"), "line 2: accepts 1 arg(s), received 0"},
		{"create without a key", batchInput(t, "create", "no key"), "line 2: create needs --idempotency-key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, _, err := execBatch(t, env, dir, validFirst+tt.second, nil)
			ce := requireCLIError(t, err, ExitUsage)
			assert.Contains(t, ce.Message, tt.wantMsg)
			assert.Empty(t, stdout)
			assert.Equal(t, []string{"existing issue"}, batchIssueTitles(t, env, pid))
		})
	}
}

func TestBatch_EmptyInputIsRejected(t *testing.T) {
	env, dir, _ := setupCLIWorkspace(t)

	for _, stdin := range []string{"", "\n\n"} {
		_, _, err := execBatch(t, env, dir, stdin, nil)
		ce := requireCLIError(t, err, ExitUsage)
		assert.Equal(t, "no commands on stdin", ce.Message)
	}
}

func TestBatch_BodyTextThatLooksLikeAFlagIsAccepted(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	ref := createIssue(t, env, pid, "existing issue")

	_, stderr, err := execBatch(t, env, dir, batchInput(t, "comment", ref, "--body", "--json"), nil)

	require.NoError(t, err, "%s", stderr)
	assert.Equal(t, []string{"--json"}, batchCommentBodies(t, env, pid, ref))
}

func TestBatch_AppliesEveryCommandInOrder(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	ref := createIssue(t, env, pid, "existing issue")
	stdin := batchInput(t, "create", "follow-up work", "--idempotency-key", "batch-apply-1",
		"--body", "Line one\nline `two` with \"quotes\"") +
		"\n" + // blank lines are skipped but still counted
		batchInput(t, "comment", ref, "--body", "Deferred the retry work.") +
		batchInput(t, "edit", ref, "--title", "renamed issue") +
		batchInput(t, "label", "add", ref, "needs-review") +
		batchInput(t, "close", ref, "--done", "--message", batchCloseMessage, "--commit", "deadbeef")

	stdout, stderr, err := execBatch(t, env, dir, stdin, []string{"--agent"})

	require.NoError(t, err, "%s", stderr)
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	require.Len(t, lines, 7, stdout)
	assert.Equal(t, "OK batch count=5", lines[0])
	assert.Regexp(t, `^- line=1 command=create issue=[0-9a-z]+ changed=true$`, lines[1])
	assert.Equal(t, "- line=3 command=comment issue="+ref+" changed=true", lines[2])
	assert.Equal(t, "- line=4 command=edit issue="+ref+" changed=true", lines[3])
	assert.Equal(t, `- line=5 command="label add" issue=`+ref+" changed=true", lines[4])
	assert.Equal(t, "- line=6 command=close issue="+ref+" changed=true", lines[5])
	assert.Equal(t, `Reminder: "`+closeReminderText+`"`, lines[6])

	got := fetchIssueViaHTTPWithComments(t, env, pid, ref)
	assert.Equal(t, "renamed issue", got.Issue.Title)
	assert.Equal(t, "closed", got.Issue.Status)
	require.Len(t, got.Labels, 1)
	assert.Equal(t, "needs-review", got.Labels[0].Label)
	assert.Equal(t, []string{"Deferred the retry work."}, batchCommentBodies(t, env, pid, ref))
	assert.ElementsMatch(t, []string{"renamed issue", "follow-up work"}, batchIssueTitles(t, env, pid))
	created := strings.Fields(lines[1])[3]
	assert.Equal(t, "Line one\nline `two` with \"quotes\"",
		issueBodyForBatch(t, env, pid, strings.TrimPrefix(created, "issue=")))
}

func issueBodyForBatch(t *testing.T, env *testenv.Env, pid int64, ref string) string {
	t.Helper()
	got := getJSON[struct {
		Issue struct {
			Body string `json:"body"`
		} `json:"issue"`
	}](t, env.URL+"/api/v1/projects/"+itoa(pid)+"/issues/"+ref)
	return got.Issue.Body
}

func TestBatch_JSONOutputCarriesEachCommandResult(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	ref := createIssue(t, env, pid, "existing issue")
	stdin := batchInput(t, "comment", ref, "--body", "first") +
		batchInput(t, "label", "add", ref, "bug")

	stdout, stderr, err := execBatch(t, env, dir, stdin, []string{"--json"})

	require.NoError(t, err, "%s", stderr)
	var got struct {
		Results []struct {
			Line    int    `json:"line"`
			Command string `json:"command"`
			Result  struct {
				Issue struct {
					ShortID string `json:"short_id"`
				} `json:"issue"`
				Changed bool `json:"changed"`
			} `json:"result"`
		} `json:"results"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &got), stdout)
	require.Len(t, got.Results, 2)
	assert.Equal(t, 1, got.Results[0].Line)
	assert.Equal(t, "comment", got.Results[0].Command)
	assert.Equal(t, ref, got.Results[0].Result.Issue.ShortID)
	assert.True(t, got.Results[0].Result.Changed)
	assert.Equal(t, 2, got.Results[1].Line)
	assert.Equal(t, "label add", got.Results[1].Command)
}

func TestBatch_HumanOutputListsEachLine(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	ref := createIssue(t, env, pid, "existing issue")

	stdout, stderr, err := execBatch(t, env, dir, batchInput(t, "comment", ref, "--body", "first"), nil)

	require.NoError(t, err, "%s", stderr)
	assert.Equal(t, "line 1: comment "+ref+"\n", stdout)
}

func TestBatch_StopsAtTheFirstFailingLine(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	ref := createIssue(t, env, pid, "existing issue")
	// Line 2 is well formed but close refuses it at run time: no reason.
	stdin := batchInput(t, "comment", ref, "--body", "applied before the failure") +
		batchInput(t, "close", ref) +
		batchInput(t, "comment", ref, "--body", "must not be applied") +
		batchInput(t, "label", "add", ref, "bug")

	stdout, stderr, err := execBatch(t, env, dir, stdin, []string{"--agent"})

	// A standalone `kata close <ref>` without a reason exits ExitInternal.
	ce := requireCLIError(t, err, ExitInternal)
	assert.Equal(t,
		"line 2 (close): --reason is required (one of: done, wontfix, duplicate, superseded, audit-no-change)"+
			"; line 1 applied ("+ref+"); lines 3-4 not run; fix line 2 and rerun with --from-line 2",
		ce.Message)
	assert.Empty(t, stdout)
	assert.Regexp(t, `^ERR batch internal: line 2 \(close\): --reason is required`, stderr)
	assert.Equal(t, []string{"applied before the failure"}, batchCommentBodies(t, env, pid, ref))
	assert.Empty(t, fetchIssueViaHTTP(t, env, pid, ref).Labels)
}

func TestBatch_JSONErrorDataNamesAppliedAndNotRunLines(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	ref := createIssue(t, env, pid, "existing issue")
	stdin := batchInput(t, "comment", ref, "--body", "applied") +
		batchInput(t, "comment", "zzzz9", "--body", "no such issue") +
		batchInput(t, "comment", ref, "--body", "not run")

	stdout, stderr, err := execBatch(t, env, dir, stdin, []string{"--json"})

	ce := requireCLIError(t, err, ExitNotFound)
	assert.Contains(t, ce.Message, "line 2 (comment): ")
	assert.Contains(t, ce.Message, "; line 1 applied ("+ref+"); line 3 not run; fix line 2 and rerun with --from-line 2")
	assert.Empty(t, stdout)
	var got struct {
		Error struct {
			Kind string `json:"kind"`
			Data struct {
				FailedLine int `json:"failed_line"`
				Applied    []struct {
					Line    int    `json:"line"`
					Command string `json:"command"`
					Issue   string `json:"issue"`
				} `json:"applied"`
				NotRun []int `json:"not_run"`
				Error  struct {
					Kind string `json:"kind"`
				} `json:"error"`
			} `json:"data"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(stderr), &got), stderr)
	assert.Equal(t, "not_found", got.Error.Kind)
	assert.Equal(t, 2, got.Error.Data.FailedLine)
	require.Len(t, got.Error.Data.Applied, 1)
	assert.Equal(t, 1, got.Error.Data.Applied[0].Line)
	assert.Equal(t, "comment", got.Error.Data.Applied[0].Command)
	assert.Equal(t, ref, got.Error.Data.Applied[0].Issue)
	assert.Equal(t, []int{3}, got.Error.Data.NotRun)
	assert.Equal(t, "not_found", got.Error.Data.Error.Kind)
}

func TestBatch_FromLineSkipsEarlierLines(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	ref := createIssue(t, env, pid, "existing issue")
	stdin := batchInput(t, "comment", ref, "--body", "already applied") +
		batchInput(t, "comment", ref, "--body", "second") +
		batchInput(t, "comment", ref, "--body", "third")

	stdout, stderr, err := execBatch(t, env, dir, stdin, []string{"--agent"}, "--from-line", "2")

	require.NoError(t, err, "%s", stderr)
	assert.Equal(t,
		"OK batch count=2\n"+
			"- line=2 command=comment issue="+ref+" changed=true\n"+
			"- line=3 command=comment issue="+ref+" changed=true\n",
		stdout)
	assert.Equal(t, []string{"second", "third"}, batchCommentBodies(t, env, pid, ref))

	_, _, err = execBatch(t, env, dir, stdin, nil, "--from-line", "4")
	ce := requireCLIError(t, err, ExitUsage)
	assert.Equal(t, "--from-line 4 is past the last line (3)", ce.Message)
}

func TestBatch_RerunDoesNotDuplicateACreate(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	stdin := batchInput(t, "create", "follow-up work", "--idempotency-key", "batch-rerun-1")

	first, stderr, err := execBatch(t, env, dir, stdin, []string{"--agent"})
	require.NoError(t, err, "%s", stderr)
	second, stderr, err := execBatch(t, env, dir, stdin, []string{"--agent"})
	require.NoError(t, err, "%s", stderr)

	assert.Equal(t, []string{"follow-up work"}, batchIssueTitles(t, env, pid))
	assert.Regexp(t, `(?m)^- line=1 command=create issue=[0-9a-z]+ changed=true$`, first)
	assert.Regexp(t, `(?m)^- line=1 command=create issue=[0-9a-z]+ reused=true changed=false$`, second)
}

func TestBatch_LineCanTargetAnotherProject(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	other, err := env.DB.CreateProject(context.Background(), "other-project")
	require.NoError(t, err)
	stdin := batchInput(t, "create", "filed elsewhere", "--project", "other-project",
		"--idempotency-key", "batch-other-1")

	stdout, stderr, err := execBatch(t, env, dir, stdin, []string{"--agent"})

	require.NoError(t, err, "%s", stderr)
	assert.Regexp(t, `(?m)^- line=1 command=create issue=other-project#[0-9a-z]+ changed=true$`, stdout)
	assert.Equal(t, []string{"filed elsewhere"}, batchIssueTitles(t, env, other.ID))
	assert.Empty(t, batchIssueTitles(t, env, pid))
}

func TestBatch_BatchLevelProjectAppliesAndQualifiesRefs(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	other, err := env.DB.CreateProject(context.Background(), "other-project")
	require.NoError(t, err)
	stdin := batchInput(t, "create", "filed elsewhere", "--idempotency-key", "batch-level-project-1")

	stdout, stderr, err := execBatch(t, env, dir, stdin, []string{"--agent", "--project", "other-project"})

	require.NoError(t, err, "%s", stderr)
	assert.Regexp(t, `(?m)^- line=1 command=create issue=other-project#[0-9a-z]+ changed=true$`, stdout)
	assert.Equal(t, []string{"filed elsewhere"}, batchIssueTitles(t, env, other.ID))
	assert.Empty(t, batchIssueTitles(t, env, pid))
}

func TestBatch_GlobalFlagBeforeTheCommandInALine(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	ref := createIssue(t, env, pid, "existing issue")

	_, stderr, err := execBatch(t, env, dir,
		batchInput(t, "--as", "line-actor", "comment", ref, "--body", "flag first"), nil)

	require.NoError(t, err, "%s", stderr)
	comments := fetchIssueViaHTTPWithComments(t, env, pid, ref).Comments
	require.Len(t, comments, 1)
	assert.Equal(t, "line-actor", comments[0].Author)
}

func TestBatch_AcceptsCRLFInput(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	ref := createIssue(t, env, pid, "existing issue")
	stdin := strings.ReplaceAll(
		batchInput(t, "comment", ref, "--body", "first")+batchInput(t, "comment", ref, "--body", "second"),
		"\n", "\r\n")

	_, stderr, err := execBatch(t, env, dir, stdin, nil)

	require.NoError(t, err, "%s", stderr)
	assert.Equal(t, []string{"first", "second"}, batchCommentBodies(t, env, pid, ref))
}

func TestBatch_DryCloseIsMarkedAndLeavesTheIssueOpen(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	ref := createIssue(t, env, pid, "existing issue")
	stdin := batchInput(t, "close", ref, "--done", "--message", batchCloseMessage, "--commit", "deadbeef", "--dry")

	stdout, stderr, err := execBatch(t, env, dir, stdin, []string{"--agent"})

	require.NoError(t, err, "%s", stderr)
	assert.Equal(t, "OK batch count=1\n- line=1 command=close issue="+ref+" changed=false dry_run=true\n", stdout)
	assert.Equal(t, "open", fetchIssueViaHTTP(t, env, pid, ref).Issue.Status)
}

func TestBatch_QuietKeepsTheIssueRefs(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	ref := createIssue(t, env, pid, "existing issue")
	stdin := batchInput(t, "comment", ref, "--body", "first") +
		batchInput(t, "close", ref, "--done", "--message", batchCloseMessage, "--commit", "deadbeef")

	agent, stderr, err := execBatch(t, env, dir, stdin, []string{"--agent", "--quiet"})
	require.NoError(t, err, "%s", stderr)
	assert.Equal(t, "- line=1 command=comment issue="+ref+" changed=true\n"+
		"- line=2 command=close issue="+ref+" changed=true\n", agent)

	human, stderr, err := execBatch(t, env, dir, batchInput(t, "comment", ref, "--body", "second"), []string{"--quiet"})
	require.NoError(t, err, "%s", stderr)
	assert.Equal(t, ref+"\n", human)
}

func TestBatchLineError_OutcomeUnknownSaysToCheckBeforeRerunning(t *testing.T) {
	applied := []batchResult{{Line: 1, Command: "comment", issue: "abc4"}}
	later := []batchLine{{number: 3, command: "comment"}}
	failed := batchLine{number: 2, command: "create"}

	for _, cause := range []error{
		&cliError{Message: "create timed out", Kind: kindDaemonUnavail, Code: "create_outcome_unknown", ExitCode: ExitDaemonUnavail},
		errBatchResultUnreadable,
	} {
		err := batchLineError(failed, cause, true, applied, later)

		var ce *cliError
		require.ErrorAs(t, err, &ce)
		assert.Contains(t, ce.Message, "; line 1 applied (abc4); line 3 not run; "+
			"line 2 may have been applied: check its issue before rerunning")
		assert.NotContains(t, ce.Message, "--from-line")
		var data struct {
			FailedLine     int   `json:"failed_line"`
			OutcomeUnknown bool  `json:"outcome_unknown"`
			NotRun         []int `json:"not_run"`
		}
		require.NoError(t, json.Unmarshal(ce.Data, &data))
		assert.Equal(t, 2, data.FailedLine)
		assert.True(t, data.OutcomeUnknown)
		assert.Equal(t, []int{3}, data.NotRun)
	}
}

func TestBatch_BatchLevelActorAppliesToEveryLine(t *testing.T) {
	env, dir, pid := setupCLIWorkspace(t)
	ref := createIssue(t, env, pid, "existing issue")
	stdin := batchInput(t, "comment", ref, "--body", "from the batch actor") +
		batchInput(t, "comment", ref, "--body", "from the line actor", "--as", "line-actor")

	_, stderr, err := execBatch(t, env, dir, stdin, []string{"--as", "batch-actor"})

	require.NoError(t, err, "%s", stderr)
	comments := fetchIssueViaHTTPWithComments(t, env, pid, ref).Comments
	require.Len(t, comments, 2)
	assert.Equal(t, "batch-actor", comments[0].Author)
	assert.Equal(t, "line-actor", comments[1].Author)
}
