package workflow

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
)

func TestOutputSharedAcrossExecutorsAndReports(t *testing.T) {
	var buffer bytes.Buffer
	output := NewOutput(&buffer, &buffer)
	var workers sync.WaitGroup
	for i := range 20 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			name := fmt.Sprintf("workflow-%d", i)
			executor := Executor{Output: output, Runner: runnerFunc(func(_ context.Context, req borg.Request) (borg.Result, error) {
				for range 10 {
					if _, err := fmt.Fprintln(req.Diagnostics, name); err != nil {
						return borg.Result{Outcome: borg.Failure}, err
					}
				}
				return borg.Result{Outcome: borg.Success}, nil
			})}
			result, err := executor.Run(t.Context(), Plan{Name: name, Stages: [][]Task{{
				{Type: "compact", Repository: "local", Scope: name},
			}}})
			if err != nil || result.Outcome != borg.Success {
				t.Errorf("run: %+v %v", result, err)
			}
			fmt.Fprintln(output, "agent report")
		}()
	}
	workers.Wait()
	for _, line := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
		if line == "agent report" {
			continue
		}
		var index int
		if _, err := fmt.Sscanf(line, `[workflow "workflow-%d"`, &index); err != nil {
			t.Fatalf("unattributed output: %q", line)
		}
		prefix := fmt.Sprintf(`[workflow "workflow-%d" stage 1 task 1 compact repository "local"] `, index)
		if line != prefix+fmt.Sprintf("workflow-%d", index) && line != prefix+"completed" {
			t.Fatalf("interleaved label/payload: %q", line)
		}
	}
}
