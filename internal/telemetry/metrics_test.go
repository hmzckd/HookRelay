package telemetry

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrometheusMetricsHaveFixedLabelsAndWellFormedGroups(t *testing.T) {
	s := Snapshot{EventsStored: 1, QueueReady: 2}
	s.Deliveries[0] = 2
	s.Attempts[2] = 1
	var out bytes.Buffer
	if err := WritePrometheus(&out, s); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, expected := range []string{
		"hookrelay_events_stored 1\n", "hookrelay_deliveries{status=\"pending\"} 2\n",
		"hookrelay_attempts{status=\"failed\"} 1\n", "hookrelay_queue_ready 2\n",
		"# TYPE hookrelay_db_pool_waits_total counter\n",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing %q in metrics", expected)
		}
	}
	if !strings.HasSuffix(text, "\n") || strings.Contains(text, "event_id=") ||
		strings.Contains(text, "delivery_id=") || strings.Contains(text, "endpoint_url=") {
		t.Fatalf("invalid exposition: %s", text)
	}
}
