package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/history"
	"github.com/spf13/cobra"
)

func newHistoryCommand() *cobra.Command {
	var incomplete bool
	cmd := &cobra.Command{
		Use:   "history",
		Short: "Inspect local run outcomes and execution evidence",
		Long: "Show retained local runs, newest observation first, without loading configuration or contacting Borg.\n\n" +
			"Incomplete means no terminal event was recorded, not active. Partial means recorded events are missing.\n" +
			"Owner and lock observations are independent and momentary. A gone owner does not mean Borg exited;\n" +
			"an unknown lock does not mean free. Missing history is empty; unreadable history is an error.",
		Args:              cobra.NoArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := history.OpenReadOnly()
			if err != nil {
				return &exitError{code: exitFailure, err: history.ErrUnavailable}
			}
			runs, err := store.Inspect()
			closeErr := store.Close()
			if err != nil || closeErr != nil {
				return &exitError{code: exitFailure, err: history.ErrUnavailable}
			}
			if err := writeHistory(cmd.OutOrStdout(), runs, incomplete); err != nil {
				return &exitError{code: exitFailure, err: fmt.Errorf("write local run history output failed")}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&incomplete, "incomplete", false, "Show only runs without a recorded terminal event (not necessarily active)")
	return cmd
}

func writeHistory(out io.Writer, runs []history.Observation, incomplete bool) error {
	shown := false
	for _, run := range runs {
		s := run.Summary
		if incomplete && !s.Incomplete {
			continue
		}
		if !shown {
			if _, err := fmt.Fprintln(out, "Incomplete means no recorded terminal event, not active; partial counts may be incomplete.\nOwner present is not task/Borg liveness; owner gone does not mean Borg exited.\nLock held may belong to another run; lock unknown does not mean free.\nObservations are momentary; age is not liveness evidence."); err != nil {
				return err
			}
			shown = true
		}
		outcome := string(s.Outcome)
		if s.Incomplete || outcome == "" {
			outcome = "unknown"
		}
		config := s.ConfigID
		if config == "" {
			config = "unknown"
		}
		if _, err := fmt.Fprintf(out, "\nrun %q workflow=%q operation=%q source=%s preview=%t config=%s\n"+
			"  outcome=%s incomplete=%t partial=%t reason=%q owner=%s lock=%s\n"+
			"  observed=%s first=%s started=%s finished=%s due=%s sequence=%d\n"+
			"  tasks: success=%d warning=%d failure=%d interrupted=%d skipped=%d\n",
			s.RunID, s.Workflow, s.Operation, s.Source, s.Preview, config,
			outcome, s.Incomplete, s.Partial, s.Reason, run.Owner, run.Lock,
			historyTime(&s.LastObserved), historyTime(&s.FirstObserved), historyTime(s.StartedAt),
			historyTime(s.FinishedAt), historyTime(&s.ScheduledAt), s.Sequence,
			s.Tasks.Success, s.Tasks.Warning, s.Tasks.Failure, s.Tasks.Interrupted, s.Tasks.Skipped); err != nil {
			return err
		}
	}
	if !shown {
		message := "No recorded runs."
		if incomplete {
			message = "No incomplete recorded runs."
		}
		_, err := fmt.Fprintln(out, message)
		return err
	}
	return nil
}

func historyTime(at *time.Time) string {
	if at == nil || at.IsZero() {
		return "unknown"
	}
	return at.UTC().Format(time.RFC3339Nano)
}
