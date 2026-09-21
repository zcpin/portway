package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/byteporter/portway/internal/app"
)

func TestBatchOperationsReportPartialResultsAndAreIdempotent(t *testing.T) {
	a, s := snapshotTestServer(t)
	if err := a.AddTunnel(snapshotTestTunnel("first", 15432)); err != nil {
		t.Fatal(err)
	}
	if err := a.AddTunnel(snapshotTestTunnel("second", 15433)); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"start", "start", "stop", "stop"} {
		req := httptest.NewRequest(http.MethodPost, "/api/tunnels/batch", strings.NewReader(
			`{"action":"`+action+`","names":["first","missing","second","first"]}`))
		req.Header.Set("X-Auth-Token", snapshotTestToken)
		res := httptest.NewRecorder()
		s.routes().ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("batch: %d %s", res.Code, res.Body)
		}
		var results []app.BatchResult
		if err := json.Unmarshal(res.Body.Bytes(), &results); err != nil {
			t.Fatal(err)
		}
		if len(results) != 3 || !results[0].OK || results[1].OK || results[1].Error == "" || !results[2].OK {
			t.Fatalf("partial results: %+v", results)
		}
		for _, running := range a.GetStatus() {
			if running != (action == "start") {
				t.Fatalf("incorrect state: %v", a.GetStatus())
			}
		}
	}
}

func TestInvalidBatchIsRejectedBeforeAnyAction(t *testing.T) {
	a, s := snapshotTestServer(t)
	if err := a.AddTunnel(snapshotTestTunnel("first", 15432)); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"action":"invalid","names":["first"]}`, `{"action":"start","names":["first",""]}`, `{"action":"start","names":[]}`} {
		req := httptest.NewRequest(http.MethodPost, "/api/tunnels/batch", strings.NewReader(body))
		req.Header.Set("X-Auth-Token", snapshotTestToken)
		res := httptest.NewRecorder()
		s.routes().ServeHTTP(res, req)
		if res.Code != http.StatusBadRequest || a.GetStatus()["first"] {
			t.Fatalf("invalid batch changed state: %d", res.Code)
		}
	}
}
