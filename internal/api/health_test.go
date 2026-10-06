package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

type fakeDB struct {
	pingErr         error
	versionErr      error
	current, latest int64
}

func (f fakeDB) Ping(context.Context) error { return f.pingErr }

func (f fakeDB) SchemaVersions(context.Context) (int64, int64, error) {
	return f.current, f.latest, f.versionErr
}

func TestDBHealth(t *testing.T) {
	secret := errors.New("dial tcp 10.0.0.5:5432: password authentication failed for user synergy")
	tests := []struct {
		name       string
		db         DBHealth
		wantCode   int
		wantStatus string
		wantVer    int64
	}{
		{"healthy", fakeDB{current: 1, latest: 1}, http.StatusOK, dbStatusOK, 1},
		{"schema ahead of binary still serves", fakeDB{current: 2, latest: 1}, http.StatusOK, dbStatusOK, 2},
		{"migrations pending", fakeDB{current: 0, latest: 1}, http.StatusServiceUnavailable, dbStatusMigrationsPending, 0},
		{"ping fails", fakeDB{pingErr: secret}, http.StatusServiceUnavailable, dbStatusUnavailable, -1},
		{"version query fails", fakeDB{versionErr: secret}, http.StatusServiceUnavailable, dbStatusUnavailable, -1},
		{"not configured", nil, http.StatusServiceUnavailable, dbStatusUnavailable, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(Options{DB: tt.db})
			rec := do(t, s, http.MethodGet, "/health/db", nil)

			if rec.Code != tt.wantCode {
				t.Fatalf("status code = %d, want %d", rec.Code, tt.wantCode)
			}
			body := decode[dbHealthResponse](t, rec)
			if body.Status != tt.wantStatus {
				t.Errorf("status = %q, want %q", body.Status, tt.wantStatus)
			}
			if tt.wantVer >= 0 && (body.SchemaVersion == nil || *body.SchemaVersion != tt.wantVer) {
				t.Errorf("schema_version = %v, want %d", body.SchemaVersion, tt.wantVer)
			}
			if strings.Contains(rec.Body.String(), "10.0.0.5") || strings.Contains(rec.Body.String(), "password") {
				t.Errorf("response leaks connection details: %s", rec.Body.String())
			}
		})
	}
}

func TestDBHealthLogsFailureDetails(t *testing.T) {
	s, logs := newTestServer(t)
	s.db = fakeDB{pingErr: errors.New("connection refused")}
	do(t, s, http.MethodGet, "/health/db", nil)

	if !strings.Contains(logs.String(), "connection refused") {
		t.Errorf("failure cause not logged: %s", logs.String())
	}
}

func TestFailingDBProbeDoesNotLogAtError(t *testing.T) {
	s, logs := newTestServer(t)
	s.db = fakeDB{pingErr: errors.New("connection refused")}
	do(t, s, http.MethodGet, "/health/db", nil)

	if strings.Contains(logs.String(), `"level":"ERROR"`) {
		t.Errorf("failing health probe logged at ERROR: %s", logs.String())
	}
}
