package health

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandler(t *testing.T) {
	tests := []struct {
		method   string
		wantCode int
		wantBody string
	}{
		{http.MethodGet, http.StatusOK, `{"status":"ok"}`},
		{http.MethodHead, http.StatusOK, ""},
		{http.MethodPost, http.StatusMethodNotAllowed, "method not allowed"},
	}
	for _, tc := range tests {
		t.Run(tc.method, func(t *testing.T) {
			rec := httptest.NewRecorder()
			Handler().ServeHTTP(rec, httptest.NewRequest(tc.method, Path, nil))
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != tc.wantBody {
				t.Fatalf("body = %q, want %q", got, tc.wantBody)
			}
		})
	}
}
