package metrics

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
)

// Handler exposes only bounded workflow observations in Prometheus text format.
func Handler(c *Collector) http.Handler {
	sem := make(chan struct{}, 2)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		default:
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		s := c.Snapshot()
		fmt.Fprintln(w, "# HELP cairn_history_available Whether retained workflow history could be read.\n# TYPE cairn_history_available gauge")
		fmt.Fprintf(w, "cairn_history_available %d\n", boolValue(s.HistoryAvailable))
		for _, def := range []struct{ name, help, typ string }{
			{"cairn_workflow_events_total", "Locally observed terminal events by outcome since agent start.", "counter"},
			{"cairn_workflow_busy_total", "Locally observed busy rejections since agent start.", "counter"},
			{"cairn_workflow_in_flight", "Locally observed active runs, a lower bound when unknown.", "gauge"},
			{"cairn_workflow_in_flight_known", "Whether the local active count is known.", "gauge"},
			{"cairn_workflow_last_finished_timestamp_seconds", "Latest known workflow terminal time.", "gauge"},
			{"cairn_workflow_last_outcome", "Latest known workflow terminal outcome.", "gauge"},
			{"cairn_workflow_last_duration_seconds", "Duration of latest terminal when known.", "gauge"},
			{"cairn_workflow_last_duration_known", "Whether latest terminal duration is known.", "gauge"},
			{"cairn_workflow_last_success_timestamp_seconds", "Latest known successful workflow completion, not backup age.", "gauge"},
			{"cairn_workflow_last_success_known", "Whether a successful workflow completion is known.", "gauge"},
		} {
			fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", def.name, def.help, def.name, def.typ)
		}
		for _, x := range s.Workflows {
			label := `workflow="` + escapeLabel(x.Name) + `"`
			for _, o := range []struct {
				name  string
				count uint64
			}{{"success", x.Success}, {"warning", x.Warning}, {"failure", x.Failure}, {"interrupted", x.Interrupted}, {"skipped", x.Skipped}} {
				fmt.Fprintf(w, "cairn_workflow_events_total{%s,outcome=%q} %d\n", label, o.name, o.count)
			}
			fmt.Fprintf(w, "cairn_workflow_busy_total{%s} %d\ncairn_workflow_in_flight{%s} %d\ncairn_workflow_in_flight_known{%s} %d\n", label, x.Busy, label, x.InFlight, label, boolValue(x.InFlightKnown))
			if !x.LastFinished.IsZero() {
				fmt.Fprintf(w, "cairn_workflow_last_finished_timestamp_seconds{%s} %s\n", label, seconds(x.LastFinished.UnixNano()))
				switch x.LastOutcome {
				case lifecycle.Success, lifecycle.Warning, lifecycle.Failure, lifecycle.Interrupted, lifecycle.Skipped:
					fmt.Fprintf(w, "cairn_workflow_last_outcome{%s,outcome=%q} 1\n", label, string(x.LastOutcome))
				}
			}
			fmt.Fprintf(w, "cairn_workflow_last_duration_known{%s} %d\n", label, boolValue(x.DurationKnown))
			if x.DurationKnown {
				fmt.Fprintf(w, "cairn_workflow_last_duration_seconds{%s} %s\n", label, strconv.FormatFloat(x.LastDuration.Seconds(), 'f', -1, 64))
			}
			fmt.Fprintf(w, "cairn_workflow_last_success_known{%s} %d\n", label, boolValue(x.SuccessKnown))
			if x.SuccessKnown {
				fmt.Fprintf(w, "cairn_workflow_last_success_timestamp_seconds{%s} %s\n", label, seconds(x.LastSuccess.UnixNano()))
			}
		}
	})
}
func boolValue(b bool) int {
	if b {
		return 1
	}
	return 0
}
func seconds(n int64) string { return strconv.FormatFloat(float64(n)/1e9, 'f', -1, 64) }
func escapeLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`).Replace(s)
}
