package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/boubonming/portfolio-dashboard/internal/auth"
)

func TestFeedbackHTTPBrowserAppendReplay(t *testing.T) {
	handler, database, submission, _ := reviewBoundaryFixture(t)
	defer database.Close()
	review := submitReviewFixture(t, database, submission, auth.MachinePrincipal{PrincipalID: "review-boundary-machine", PortfolioID: "portfolio-api", PortfolioRead: true, ReviewsWrite: true}, "feedback-http-review-key-0001")
	login := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/auth/login", strings.NewReader(`{"username":"fixture-user","password":"fixture-only-value"}`))
	login.Header.Set("Content-Type", "application/json")
	login.Header.Set("Origin", "https://example.test")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	var session struct {
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(loginResponse.Body.Bytes(), &session); err != nil || session.CSRFToken == "" {
		t.Fatalf("session=%s err=%v", loginResponse.Body.String(), err)
	}
	cookie := loginResponse.Result().Cookies()[0]
	target := "/api/v1/portfolios/portfolio-api/reviews/" + review.ReviewID + "/feedback"
	request := httptest.NewRequest(http.MethodPost, "https://example.test"+target, bytes.NewBufferString(`{"decision":"agree"}`))
	request.AddCookie(cookie)
	request.Header.Set("Origin", "https://example.test")
	request.Header.Set(auth.CSRFHeaderName, session.CSRFToken)
	request.Header.Set("Idempotency-Key", "feedback-http-key-0001")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("feedback status=%d body=%s", response.Code, response.Body.String())
	}
	var first struct {
		FeedbackID string  `json:"feedback_id"`
		Note       *string `json:"note"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &first); err != nil || first.FeedbackID == "" || first.Note != nil {
		t.Fatalf("feedback=%s err=%v", response.Body.String(), err)
	}
	replay := httptest.NewRecorder()
	replayRequest := httptest.NewRequest(http.MethodPost, "https://example.test"+target, bytes.NewBufferString(`{"decision":"agree","note":null}`))
	replayRequest.AddCookie(cookie)
	replayRequest.Header.Set("Origin", "https://example.test")
	replayRequest.Header.Set(auth.CSRFHeaderName, session.CSRFToken)
	replayRequest.Header.Set("Idempotency-Key", "feedback-http-key-0001")
	replayRequest.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(replay, replayRequest)
	if replay.Code != http.StatusCreated || replay.Body.String() == "" {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	var count int
	if err := database.DB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM review_feedback`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("feedback rows=%d err=%v", count, err)
	}
}
