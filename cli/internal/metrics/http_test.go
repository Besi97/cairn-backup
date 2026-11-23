package metrics

import (
	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandlerVisibilityAndEscaping(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	c, _ := New("secret-config-identity", []string{"daily\"\\\nname"})
	req := httptest.NewRequest("GET", "/metrics", nil)
	get := func() string {
		w := httptest.NewRecorder()
		Handler(c).ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
		return w.Body.String()
	}
	body := get()
	if !strings.Contains(body, "# HELP cairn_workflow_last_success_timestamp_seconds Timestamp of the latest known successful workflow completion.\n") {
		t.Fatal("missing last success metric help")
	}
	if !strings.Contains(body, `workflow="daily\"\\\nname"`) || !strings.Contains(body, `cairn_workflow_last_success_known{workflow="daily\"\\\nname"} 0`) || strings.Contains(body, "secret-config-identity") || strings.Contains(body, "cairn_workflow_last_success_timestamp_seconds{") {
		t.Fatal(body)
	}
	now := time.Unix(1700000000, 0)
	c.Observe(lifecycle.Event{ConfigID: "secret-config-identity", Workflow: "daily\"\\\nname", RunID: "run", Kind: lifecycle.RunStarted, Time: now})
	c.Observe(lifecycle.Event{ConfigID: "secret-config-identity", Workflow: "daily\"\\\nname", RunID: "run", Kind: lifecycle.RunFinished, Outcome: lifecycle.Success, Time: now.Add(time.Second)})
	body = get()
	for _, want := range []string{`outcome="success"} 1`, `cairn_workflow_last_success_timestamp_seconds{workflow="daily\"\\\nname"} 1700000001`, `cairn_workflow_last_duration_seconds{workflow="daily\"\\\nname"} 1`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in %s", want, body)
		}
	}
	w := httptest.NewRecorder()
	Handler(c).ServeHTTP(w, httptest.NewRequest("GET", "/other", nil))
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
}
