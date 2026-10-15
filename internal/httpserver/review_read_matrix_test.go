package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
	"github.com/boubonming/portfolio-dashboard/internal/store"
)

func TestReviewHTTPListAndDetailReadContract(t *testing.T) {
	handler, database, submission, _ := reviewBoundaryFixture(t)
	defer database.Close()
	body := reviewBoundaryBody(t, submission)
	var created domain.AdvisoryReview
	for index := 0; index < 2; index++ {
		response := reviewBoundaryRequest(handler, http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret, "read-contract-key-"+string(rune('a'+index))+"123456", "application/json", body)
		if response.Code != http.StatusCreated {
			t.Fatalf("create[%d] status=%d body=%s", index, response.Code, response.Body.String())
		}
		if index == 0 && json.Unmarshal(response.Body.Bytes(), &created) != nil {
			t.Fatal("created response is not a review")
		}
	}

	list := reviewBoundaryRequest(handler, http.MethodGet, "/api/v1/portfolios/portfolio-api/reviews?limit=1", reviewBoundarySecret, "", "", nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	var page struct {
		Items      []domain.AdvisoryReview `json:"items"`
		HasMore    bool                    `json:"has_more"`
		NextCursor string                  `json:"next_cursor"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("unexpected first page: %+v", page)
	}
	if page.Items[0].ReviewID == "" || page.Items[0].Lifecycle.CurrentSnapshotID == nil || page.Items[0].Lifecycle.Outdated {
		t.Fatalf("list item lifecycle/review id: %+v", page.Items[0])
	}

	next := reviewBoundaryRequest(handler, http.MethodGet, "/api/v1/portfolios/portfolio-api/reviews?limit=1&cursor="+page.NextCursor, reviewBoundarySecret, "", "", nil)
	if next.Code != http.StatusOK {
		t.Fatalf("next page status=%d body=%s", next.Code, next.Body.String())
	}
	var final struct {
		Items      []domain.AdvisoryReview `json:"items"`
		HasMore    bool                    `json:"has_more"`
		NextCursor string                  `json:"next_cursor"`
	}
	if err := json.Unmarshal(next.Body.Bytes(), &final); err != nil {
		t.Fatal(err)
	}
	if len(final.Items) != 1 || final.HasMore || final.NextCursor != "" || final.Items[0].ReviewID == page.Items[0].ReviewID {
		t.Fatalf("unexpected final page: %+v", final)
	}

	detail := reviewBoundaryRequest(handler, http.MethodGet, "/api/v1/portfolios/portfolio-api/reviews/"+created.ReviewID, reviewBoundarySecret, "", "", nil)
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	var got domain.AdvisoryReview
	if err := json.Unmarshal(detail.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mustJSON(t, got), mustJSON(t, created)) {
		t.Fatalf("detail differs from submission response: detail=%s created=%s", mustJSON(t, got), mustJSON(t, created))
	}

	for _, target := range []string{
		"/api/v1/portfolios/portfolio-api/reviews?limit=0",
		"/api/v1/portfolios/portfolio-api/reviews?limit=51",
		"/api/v1/portfolios/portfolio-api/reviews?limit=1&limit=2",
		"/api/v1/portfolios/portfolio-api/reviews?unknown=x",
		"/api/v1/portfolios/portfolio-api/reviews?status=complete&status=incomplete",
		"/api/v1/portfolios/portfolio-api/reviews?outdated=maybe",
		"/api/v1/portfolios/portfolio-api/reviews?cursor=not-a-valid-cursor",
		"/api/v1/portfolios/portfolio-api/reviews?%6cimit=1",
		"/api/v1/portfolios/portfolio-api/reviews?limit=%31",
		"/api/v1/portfolios/portfolio-api/reviews?limit=+1",
		"/api/v1/portfolios/portfolio-api/reviews?limit=01",
		"/api/v1/portfolios/portfolio-api/reviews?limit=1%20",
		"/api/v1/portfolios/portfolio-api/reviews?",
	} {
		response := reviewBoundaryRequest(handler, http.MethodGet, target, reviewBoundarySecret, "", "", nil)
		assertPhaseDError(t, response, http.StatusBadRequest, "invalid_request")
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodHead} {
		response := reviewBoundaryRequest(handler, method, "/api/v1/portfolios/portfolio-api/reviews/"+created.ReviewID, reviewBoundarySecret, "", "", nil)
		assertPhaseDError(t, response, http.StatusMethodNotAllowed, "method_not_allowed")
		if response.Header().Get("Allow") != http.MethodGet {
			t.Fatalf("detail Allow=%q", response.Header().Get("Allow"))
		}
	}
	response := reviewBoundaryRequest(handler, http.MethodPut, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret, "", "", nil)
	assertPhaseDError(t, response, http.StatusMethodNotAllowed, "method_not_allowed")
	if response.Header().Get("Allow") != "GET, POST" {
		t.Fatalf("collection Allow=%q", response.Header().Get("Allow"))
	}

	unauthenticated := httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-api/reviews?limit=0", nil)
	unauthenticatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticatedResponse, unauthenticated)
	assertPhaseDError(t, unauthenticatedResponse, http.StatusUnauthorized, "unauthorized")
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestReviewStoreReadDoesNotUseSnapshotPayload(t *testing.T) {
	handler, database, submission, snapshotID := reviewBoundaryFixture(t)
	defer database.Close()
	body := reviewBoundaryBody(t, submission)
	created := reviewBoundaryRequest(handler, http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret, "payload-independent-key", "application/json", body)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	if _, err := database.DB.ExecContext(context.Background(), "DROP TRIGGER snapshots_immutable_update"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(context.Background(), "UPDATE snapshots SET payload=? WHERE id=?", "{corrupt", snapshotID); err != nil {
		t.Fatal(err)
	}
	var review domain.AdvisoryReview
	if err := json.Unmarshal(created.Body.Bytes(), &review); err != nil {
		t.Fatal(err)
	}
	read := reviewBoundaryRequest(handler, http.MethodGet, "/api/v1/portfolios/portfolio-api/reviews/"+review.ReviewID, reviewBoundarySecret, "", "", nil)
	if read.Code != http.StatusOK {
		t.Fatalf("read status=%d body=%s", read.Code, read.Body.String())
	}
}

func TestReviewHTTPReadAcceptsValidUnreviewedCurrentSnapshot(t *testing.T) {
	handler, database, submission, _ := reviewBoundaryFixture(t)
	defer database.Close()

	created := reviewBoundaryRequest(handler, http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret, "unreviewed-current-key", "application/json", reviewBoundaryBody(t, submission))
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var review domain.AdvisoryReview
	if err := json.Unmarshal(created.Body.Bytes(), &review); err != nil {
		t.Fatal(err)
	}

	// A snapshot can be persisted before any review targets it. Its normalized
	// metadata is sufficient for lifecycle evaluation; payload is not consulted.
	const currentSnapshotID = "snapshot-unreviewed-current"
	if _, err := database.DB.Exec(`INSERT INTO snapshots(id,portfolio_id,calculation_version,payload,created_at,input_hash) VALUES(?,?,?,?,?,?)`, currentSnapshotID, "portfolio-api", "review.v2", "{not-read}", "2099-01-01T00:00:00.000000000Z", "unreviewed-current-hash"); err != nil {
		t.Fatal(err)
	}

	list := reviewBoundaryRequest(handler, http.MethodGet, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret, "", "", nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	var page struct {
		Items []domain.AdvisoryReview `json:"items"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ReviewID != review.ReviewID {
		t.Fatalf("unexpected list page: %+v", page)
	}
	if page.Items[0].Lifecycle.CurrentSnapshotID == nil || *page.Items[0].Lifecycle.CurrentSnapshotID != currentSnapshotID || !page.Items[0].Lifecycle.Outdated {
		t.Fatalf("unreviewed current snapshot lifecycle: %+v", page.Items[0].Lifecycle)
	}

	detail := reviewBoundaryRequest(handler, http.MethodGet, "/api/v1/portfolios/portfolio-api/reviews/"+review.ReviewID, reviewBoundarySecret, "", "", nil)
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	var got domain.AdvisoryReview
	if err := json.Unmarshal(detail.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Lifecycle.CurrentSnapshotID == nil || *got.Lifecycle.CurrentSnapshotID != currentSnapshotID || !got.Lifecycle.Outdated {
		t.Fatalf("unreviewed current snapshot detail lifecycle: %+v", got.Lifecycle)
	}
}

func TestReviewHTTPCurrentSnapshotAssociationCorruptionFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, *store.Store)
	}{
		{
			name: "mismatched-version",
			mutate: func(t *testing.T, database *store.Store) {
				if _, err := database.DB.Exec(`DROP TRIGGER IF EXISTS phase_d_review_snapshots_update`); err != nil {
					t.Fatal(err)
				}
				if _, err := database.DB.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
					t.Fatal(err)
				}
				if _, err := database.DB.Exec(`UPDATE review_snapshots SET calculation_version='wrong-version'`); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "dangling-association",
			mutate: func(t *testing.T, database *store.Store) {
				for _, trigger := range []string{"phase_d_review_snapshots_delete", "snapshots_immutable_delete"} {
					if _, err := database.DB.Exec("DROP TRIGGER IF EXISTS " + trigger); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := database.DB.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
					t.Fatal(err)
				}
				if _, err := database.DB.Exec(`DELETE FROM snapshots`); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, database, submission, _ := reviewBoundaryFixture(t)
			defer database.Close()
			created := reviewBoundaryRequest(handler, http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret, "snapshot-corruption-"+tc.name+"-key", "application/json", reviewBoundaryBody(t, submission))
			if created.Code != http.StatusCreated {
				t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
			}
			var review domain.AdvisoryReview
			if err := json.Unmarshal(created.Body.Bytes(), &review); err != nil {
				t.Fatal(err)
			}
			tc.mutate(t, database)
			for _, path := range []string{"/api/v1/portfolios/portfolio-api/reviews", "/api/v1/portfolios/portfolio-api/reviews/" + review.ReviewID} {
				response := reviewBoundaryRequest(handler, http.MethodGet, path, reviewBoundarySecret, "", "", nil)
				assertPhaseDError(t, response, http.StatusInternalServerError, "internal_error")
				if bytes.Contains(response.Body.Bytes(), []byte(review.ReviewID)) || bytes.Contains(response.Body.Bytes(), []byte("rationale")) {
					t.Fatalf("corruption leaked content: %s", response.Body.String())
				}
			}
		})
	}
}

func TestReviewHTTPReadOnlyConcurrentListDetailAndCancellation(t *testing.T) {
	handler, database, submission, _ := reviewBoundaryFixture(t)
	defer database.Close()
	created := reviewBoundaryRequest(handler, http.MethodPost, "/api/v1/portfolios/portfolio-api/reviews", reviewBoundarySecret, "concurrent-read-key", "application/json", reviewBoundaryBody(t, submission))
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var review domain.AdvisoryReview
	if err := json.Unmarshal(created.Body.Bytes(), &review); err != nil {
		t.Fatal(err)
	}

	tables := []string{
		"snapshots", "review_snapshots", "snapshot_holdings", "advisory_reviews", "review_targets",
		"review_risks", "review_counterarguments", "review_conditions", "review_metrics", "review_evidence",
		"review_sourced_facts", "review_fact_evidence", "review_submission_permission_snapshots", "review_idempotency",
		"review_feedback", "feedback_idempotency", "feedback_permission_snapshots", "audit_events", "audit_event_portfolios", "activity_events",
	}
	counts := func() map[string]int {
		result := make(map[string]int, len(tables))
		for _, table := range tables {
			var count int
			if err := database.DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
				t.Fatal(err)
			}
			result[table] = count
		}
		return result
	}
	before := counts()

	type readResult struct{ status int }
	results := make(chan readResult, 20)
	var group sync.WaitGroup
	for index := 0; index < 20; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			path := "/api/v1/portfolios/portfolio-api/reviews"
			if index%2 == 1 {
				path += "/" + review.ReviewID
			}
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.Header.Set("Authorization", "Bearer "+reviewBoundarySecret)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			results <- readResult{status: response.Code}
		}(index)
	}
	group.Wait()
	close(results)
	completed := 0
	for result := range results {
		completed++
		if result.status != http.StatusOK {
			t.Fatalf("concurrent read status=%d", result.status)
		}
	}
	if completed != 20 {
		t.Fatalf("collected %d concurrent read results", completed)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	canceledRequest := httptest.NewRequest(http.MethodGet, "/api/v1/portfolios/portfolio-api/reviews/"+review.ReviewID, nil).WithContext(ctx)
	canceledRequest.Header.Set("Authorization", "Bearer "+reviewBoundarySecret)
	canceledResponse := httptest.NewRecorder()
	handler.ServeHTTP(canceledResponse, canceledRequest)
	if canceledResponse.Code != http.StatusInternalServerError {
		t.Fatalf("canceled detail status=%d body=%s", canceledResponse.Code, canceledResponse.Body.String())
	}
	if after := counts(); !reflect.DeepEqual(after, before) {
		t.Fatalf("read changed normalized accounting before=%v after=%v", before, after)
	}
}
