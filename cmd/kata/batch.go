package main

import (
	"bufio"
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

// batchAllowedCommands is keyed by the command path cobra resolves a line
// to, not by the line's first word, so "label" alone or "comment edit" cannot
// slip through on a prefix.
var batchAllowedCommands = map[string]bool{
	"create":    true,
	"comment":   true,
	"close":     true,
	"edit":      true,
	"label add": true,
	"label rm":  true,
}

// batchRefusedFlags cannot appear in a line: the batch owns the output mode
// and stdin, and a help request would run nothing.
var batchRefusedFlags = []string{"json", "agent", "format", "quiet", "body-stdin", "help"}

// batchForwardedFlags are the global flags a line inherits from the batch
// invocation when the user passed them.
var batchForwardedFlags = []string{"project", "workspace", "as", "daemon", "teammate"}

// errBatchResultUnreadable marks a line that ran without error but whose
// output could not be read back. Its write has landed.
var errBatchResultUnreadable = errors.New("the command succeeded but its result could not be read")

type batchLine struct {
	number  int
	args    []string
	command string
	// project is the --project that applies to the line, its own or the
	// batch's. Results of such a line render qualified, because a bare
	// short_id would resolve against the workspace's project.
	project string
	dryRun  bool
}

type batchResult struct {
	Line    int            `json:"line"`
	Command string         `json:"command"`
	Result  jsontext.Value `json:"result"`

	issue   string
	changed *bool
	reused  bool
	dryRun  bool
}

func newBatchCmd() *cobra.Command {
	var fromLine int
	cmd := &cobra.Command{
		Use:   "batch",
		Short: "run create, comment, close, edit, and label commands read from stdin",
		Long: `Run several write commands in one call.

Each line of stdin is a JSON array holding one command's arguments:

  ["create", "Retry on 429", "--label", "debt", "--idempotency-key", "k1"]
  ["comment", "abc4", "--body", "Deferred the retry work."]

Allowed commands are create, comment, close, edit, label add, and label rm.
Every line is checked for form before any line runs. Lines then run in order
with each command's usual validation, and the batch stops at the first line
that fails. Every create line needs --idempotency-key.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runBatch(cmd, fromLine)
		},
	}
	cmd.Flags().IntVar(&fromLine, "from-line", 1, "skip the lines before this line number (to resume after a failure)")
	return cmd
}

func batchUsageError(format string, args ...any) error {
	return &cliError{Message: fmt.Sprintf(format, args...), Kind: kindUsage, ExitCode: ExitUsage}
}

func runBatch(cmd *cobra.Command, fromLine int) error {
	// Checking and running a line builds a fresh root command, which resets
	// the package-level CLI state and lets the line's flags write into it.
	// Restore the batch's own state however this returns, so its output mode
	// and error reporting are those of `kata batch`.
	saved, savedCommandName := flags, errorCommandName
	defer func() {
		flags, runEEntered, errorCommandName = saved, true, savedCommandName
	}()
	// Read before any line is parsed: every root command binds its global
	// flags to the same package-level storage.
	inherited := batchInheritedArgs(cmd)

	if fromLine < 1 {
		return batchUsageError("--from-line must be 1 or greater")
	}
	lines, err := readBatchLines(cmd.InOrStdin())
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		return batchUsageError("no commands on stdin")
	}
	if last := lines[len(lines)-1].number; fromLine > last {
		return batchUsageError("--from-line %d is past the last line (%d)", fromLine, last)
	}
	for len(lines) > 0 && lines[0].number < fromLine {
		lines = lines[1:]
	}
	for i := range lines {
		if err := checkBatchLine(&lines[i]); err != nil {
			return err
		}
		if lines[i].project == "" {
			lines[i].project = saved.Project
		}
	}

	results := make([]batchResult, 0, len(lines))
	for i, line := range lines {
		result, runEReached, err := runBatchLine(cmd, inherited, line)
		if err != nil {
			return batchLineError(line, err, runEReached, results, lines[i+1:])
		}
		results = append(results, result)
	}

	// The deferred restore runs too late for the output below, which reads
	// the batch's own output mode and quiet setting.
	flags = saved
	return printBatchResults(cmd.OutOrStdout(), results)
}

// readBatchLines parses stdin into numbered argument lists. Blank lines are
// skipped but still counted, so line numbers match the input file.
func readBatchLines(r io.Reader) ([]batchLine, error) {
	var lines []batchLine
	reader := bufio.NewReader(r)
	for number := 1; ; number++ {
		raw, readErr := reader.ReadBytes('\n')
		if text := bytes.TrimSpace(raw); len(text) > 0 {
			// Decoding into []*string tells a JSON null apart from "".
			var elements []*string
			if text[0] != '[' || json.Unmarshal(text, &elements) != nil {
				return nil, batchUsageError("line %d: not a JSON array of strings", number)
			}
			args := make([]string, len(elements))
			for i, element := range elements {
				if element == nil {
					return nil, batchUsageError("line %d: not a JSON array of strings", number)
				}
				args[i] = *element
			}
			lines = append(lines, batchLine{number: number, args: args})
		}
		if errors.Is(readErr, io.EOF) {
			return lines, nil
		}
		if readErr != nil {
			return nil, fmt.Errorf("read stdin: %w", readErr)
		}
	}
}

// checkBatchLine validates a line's form with cobra's own command lookup,
// flag parsing, and argument-count rules, without running it. Value rules
// that live inside a command's RunE are checked only when the line runs.
func checkBatchLine(line *batchLine) error {
	root := newRootCmd()
	sub, rest, err := root.Find(line.args)
	if err != nil {
		return batchUsageError("line %d: %s", line.number, err)
	}
	if sub == root {
		return batchUsageError("line %d: no command", line.number)
	}
	line.command = strings.TrimPrefix(sub.CommandPath(), root.Name()+" ")
	if !batchAllowedCommands[line.command] {
		return batchUsageError("line %d: %q is not allowed in a batch", line.number, line.command)
	}
	sub.InitDefaultHelpFlag()
	if err := sub.ParseFlags(rest); err != nil {
		return batchUsageError("line %d: %s", line.number, sub.FlagErrorFunc()(sub, err))
	}
	for _, name := range batchRefusedFlags {
		if f := sub.Flags().Lookup(name); f != nil && f.Changed {
			return batchUsageError("line %d: --%s is not allowed in a batch line", line.number, name)
		}
	}
	if err := sub.ValidateArgs(sub.Flags().Args()); err != nil {
		return batchUsageError("line %d: %s", line.number, err)
	}
	if line.command == "create" {
		// create sends the header only for a non-blank key, so a blank one
		// would not protect a rerun.
		key, _ := sub.Flags().GetString("idempotency-key")
		if strings.TrimSpace(key) == "" {
			return batchUsageError("line %d: create needs --idempotency-key so a rerun cannot duplicate it", line.number)
		}
	}
	if f := sub.Flags().Lookup("project"); f != nil && f.Changed {
		line.project = f.Value.String()
	}
	if f := sub.Flags().Lookup("dry"); f != nil && f.Changed {
		line.dryRun = f.Value.String() == "true"
	}
	return nil
}

// batchInheritedArgs returns the global flags passed to `kata batch` itself,
// which every line inherits. Only flags the user set are forwarded: an
// explicit empty --teammate, for one, means something different from none.
func batchInheritedArgs(cmd *cobra.Command) []string {
	var args []string
	for _, name := range batchForwardedFlags {
		if f := cmd.Flags().Lookup(name); f != nil && f.Changed {
			args = append(args, "--"+name+"="+f.Value.String())
		}
	}
	return args
}

// runBatchLine runs one line through the ordinary command tree, so it gets
// exactly the validation and behavior of the standalone command. The line's
// own arguments come last and so override the inherited global flags. The
// bool reports whether the command's RunE was reached, which decides how an
// untyped error is classified.
func runBatchLine(cmd *cobra.Command, inherited []string, line batchLine) (batchResult, bool, error) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetIn(strings.NewReader(""))
	args := append([]string{"--json"}, inherited...)
	root.SetArgs(append(args, line.args...))
	if err := root.ExecuteContext(cmd.Context()); err != nil {
		return batchResult{}, runEEntered, err
	}
	result := batchResult{
		Line: line.number, Command: line.command,
		Result: jsontext.Value(bytes.TrimSpace(out.Bytes())), dryRun: line.dryRun,
	}
	var decoded struct {
		Issue struct {
			ShortID string `json:"short_id"`
		} `json:"issue"`
		Changed *bool `json:"changed"`
		Reused  bool  `json:"reused"`
	}
	if err := json.Unmarshal(result.Result, &decoded); err != nil {
		return batchResult{}, true, errBatchResultUnreadable
	}
	result.issue = decoded.Issue.ShortID
	if line.project != "" {
		result.issue = line.project + "#" + decoded.Issue.ShortID
	}
	result.changed, result.reused = decoded.Changed, decoded.Reused
	return result, true, nil
}

// batchLineError reports the line that stopped the batch. kata's error line
// has no hint field, so the applied and not-run lines ride in the message;
// JSON output carries the same detail in error.data. The failing command's
// kind and exit code are kept. Its Code is not: agent rendering reads Data by
// Code, and this Data has the batch's shape with the command's error nested.
func batchLineError(line batchLine, err error, runEReached bool, applied []batchResult, later []batchLine) error {
	failed := cliErrorForErr(err, runEReached)
	var message strings.Builder
	fmt.Fprintf(&message, "line %d (%s): %s", line.number, line.command, firstLine(failed.Message))

	type appliedLine struct {
		Line    int    `json:"line"`
		Command string `json:"command"`
		Issue   string `json:"issue"`
	}
	data := struct {
		FailedLine     int           `json:"failed_line"`
		OutcomeUnknown bool          `json:"outcome_unknown,omitzero"`
		Applied        []appliedLine `json:"applied"`
		NotRun         []int         `json:"not_run"`
		Error          struct {
			Kind     errKind        `json:"kind"`
			Code     string         `json:"code,omitempty"`
			Message  string         `json:"message"`
			ExitCode int            `json:"exit_code"`
			Data     jsontext.Value `json:"data,omitempty"`
		} `json:"error"`
	}{
		FailedLine:     line.number,
		OutcomeUnknown: batchOutcomeUnknown(err, failed),
		Applied:        []appliedLine{},
		NotRun:         []int{},
	}
	data.Error.Kind, data.Error.Code = failed.Kind, failed.Code
	data.Error.Message, data.Error.ExitCode, data.Error.Data = failed.Message, failed.ExitCode, failed.Data

	numbers, issues := make([]string, len(applied)), make([]string, len(applied))
	for i, r := range applied {
		data.Applied = append(data.Applied, appliedLine{Line: r.Line, Command: r.Command, Issue: r.issue})
		numbers[i], issues[i] = fmt.Sprint(r.Line), r.issue
	}
	switch len(applied) {
	case 0:
		message.WriteString("; no lines applied")
	case 1:
		fmt.Fprintf(&message, "; line %s applied (%s)", numbers[0], issues[0])
	default:
		fmt.Fprintf(&message, "; lines %s applied (%s)", strings.Join(numbers, ", "), strings.Join(issues, ", "))
	}
	for _, l := range later {
		data.NotRun = append(data.NotRun, l.number)
	}
	switch len(later) {
	case 0:
	case 1:
		fmt.Fprintf(&message, "; line %d not run", later[0].number)
	default:
		fmt.Fprintf(&message, "; lines %d-%d not run", later[0].number, later[len(later)-1].number)
	}
	if data.OutcomeUnknown {
		// The write may have landed, so rerunning from this line could
		// apply it twice.
		fmt.Fprintf(&message, "; line %d may have been applied: check its issue before rerunning", line.number)
	} else {
		fmt.Fprintf(&message, "; fix line %d and rerun with --from-line %d", line.number, line.number)
	}

	encoded, marshalErr := json.Marshal(data)
	if marshalErr != nil {
		encoded = nil
	}
	return &cliError{Message: message.String(), Kind: failed.Kind, ExitCode: failed.ExitCode, Data: encoded}
}

// batchOutcomeUnknown reports failures after which the line's write may
// already exist: a connection lost mid-request, a mutation whose workspace
// repair or follow-up --comment failed, or a result that could not be read.
// The follow-up --comment failure has no type or code, only its message.
func batchOutcomeUnknown(err error, failed *cliError) bool {
	if errors.Is(err, errBatchResultUnreadable) {
		return true
	}
	if transportErr, ok := errors.AsType[*daemonTransportError](err); ok && transportErr.mutationOutcomeUnknown() {
		return true
	}
	return failed.Code == "create_outcome_unknown" || failed.Code == "workspace_repair_failed" ||
		strings.Contains(failed.Message, "issue mutation succeeded")
}

// printBatchResults writes one row per applied line. --quiet drops the
// header and the close reminder but keeps the issue refs, as it does for a
// single create.
func printBatchResults(w io.Writer, results []batchResult) error {
	mode := currentOutputMode()
	if mode == outputJSON {
		var buf bytes.Buffer
		if err := emitJSON(&buf, struct {
			Results []batchResult `json:"results"`
		}{Results: results}); err != nil {
			return err
		}
		_, err := fmt.Fprint(w, buf.String())
		return err
	}
	closed := false
	if mode == outputAgent && !flags.Quiet {
		if _, err := fmt.Fprintf(w, "OK batch count=%d\n", len(results)); err != nil {
			return err
		}
	}
	for _, r := range results {
		closed = closed || (r.Command == "close" && !r.dryRun && r.changed != nil && *r.changed)
		if mode != outputAgent {
			text := fmt.Sprintf("line %d: %s %s", r.Line, r.Command, r.issue)
			if r.dryRun {
				text += " (dry run)"
			}
			if flags.Quiet {
				text = r.issue
			}
			if _, err := fmt.Fprintln(w, text); err != nil {
				return err
			}
			continue
		}
		fields := []agentField{
			agentRowField("line", fmt.Sprint(r.Line)),
			agentRowField("command", r.Command),
			agentRowField("issue", r.issue),
		}
		if r.reused {
			fields = append(fields, agentRowField("reused", "true"))
		}
		if r.changed != nil {
			fields = append(fields, agentRowField("changed", fmt.Sprint(*r.changed)))
		}
		if r.dryRun {
			fields = append(fields, agentRowField("dry_run", "true"))
		}
		if err := writeAgentKVRow(w, fields...); err != nil {
			return err
		}
	}
	if !closed || flags.Quiet {
		return nil
	}
	if mode == outputAgent {
		return writeAgentField(w, "Reminder", agentValue(closeReminderText))
	}
	_, err := fmt.Fprintf(w, "Reminder: %s\n", closeReminderText)
	return err
}
