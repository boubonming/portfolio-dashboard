package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/boubonming/portfolio-dashboard/internal/domain"
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
