package store

import (
	"context"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	"synergy/internal/domain"
)

// TestUnreachableDatabaseIsUnavailable needs no PostgreSQL: it points the
// store at a closed port and checks connection failures surface as
// domain.ErrUnavailable through both plain queries and transactions.
func TestUnreachableDatabaseIsUnavailable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // nothing listens here now

	ctx := context.Background()
	url := "postgres://synergy:pw@127.0.0.1:" + strconv.Itoa(port) + "/synergy?sslmode=disable"
	st, err := Open(ctx, url, Options{ConnectTimeout: time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	if _, err := st.GetSource(ctx, newID()); !errors.Is(err, domain.ErrUnavailable) {
		t.Errorf("GetSource error = %v, want ErrUnavailable", err)
	}
	if _, err := st.ListItems(ctx, domain.ItemFilter{}); !errors.Is(err, domain.ErrUnavailable) {
		t.Errorf("ListItems error = %v, want ErrUnavailable", err)
	}
	if _, err := st.StartFetchRun(ctx, newID(), domain.TriggerCLI); !errors.Is(err, domain.ErrUnavailable) {
		t.Errorf("StartFetchRun (transaction) error = %v, want ErrUnavailable", err)
	}
	if errors.Is(mapErr(errors.New("syntax error")), domain.ErrUnavailable) {
		t.Error("ordinary errors must not map to ErrUnavailable")
	}
}
