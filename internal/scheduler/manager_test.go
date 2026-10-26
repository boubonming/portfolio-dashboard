package scheduler

import (
	"context"
	"sync"
	"testing"

	"github.com/boubonming/portfolio-dashboard/internal/config"
	"github.com/boubonming/portfolio-dashboard/internal/marketrefresh"
)

func managerSchedule(t *testing.T, portfolioID, expression string) config.MarketRefreshConfig {
	t.Helper()
	value, err := config.ParseMarketRefreshConfig(expression, "Asia/Singapore", portfolioID, "USD")
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestManagerRestoresMultiplePortfoliosAndSerializesTransitions(t *testing.T) {
	manager := NewManager(marketrefresh.New(nil, nil, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	disabled := managerSchedule(t, "portfolio-a", "")
	enabledA := managerSchedule(t, "portfolio-a", "0 5 * * 1-5")
	enabledB := managerSchedule(t, "portfolio-b", "30 6 * * *")
	if err := manager.Start(ctx, disabled); err != nil {
		t.Fatal(err)
	}
	if manager.schedulers["portfolio-a"] == nil || manager.schedulers["portfolio-a"].Enabled() {
		t.Fatal("startup did not restore disabled portfolio schedule")
	}
	if err := manager.Reconfigure(ctx, enabledA); err != nil {
		t.Fatal(err)
	}
	if !manager.schedulers["portfolio-a"].Enabled() {
		t.Fatal("disabled-to-enabled transition did not start schedule")
	}

	var wait sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errs <- manager.Reconfigure(ctx, enabledA)
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.Reconfigure(ctx, disabled); err != nil {
		t.Fatal(err)
	}
	if manager.schedulers["portfolio-a"].Enabled() {
		t.Fatal("enabled-to-disabled transition did not stop schedule")
	}
	if err := manager.Reconfigure(ctx, enabledB); err != nil {
		t.Fatal(err)
	}
	if len(manager.schedulers) != 2 || !manager.schedulers["portfolio-b"].Enabled() {
		t.Fatalf("multiple portfolio restoration=%d schedulers=%+v", len(manager.schedulers), manager.schedulers)
	}
	if err := manager.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}
