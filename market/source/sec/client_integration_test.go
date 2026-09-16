//go:build integration

package sec

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestLiveSECFilings(t *testing.T) {
	if os.Getenv("RUN_SEC_INTEGRATION") != "1" {
		t.Skip("set RUN_SEC_INTEGRATION=1 and your SEC_USER_AGENT to opt in")
	}
	client, err := New(Config{UserAgent: os.Getenv("SEC_USER_AGENT")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	rows, err := client.RecentFilings(ctx, "AAPL", "0000320193")
	if err != nil || len(rows) == 0 {
		t.Fatal("SEC acceptance failed", err)
	}
	t.Logf("retrieved %d recent filing metadata rows", len(rows))
}
