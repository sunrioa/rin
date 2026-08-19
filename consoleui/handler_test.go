package consoleui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesEmbeddedConsoleWithLocalCSP(t *testing.T) {
	handler := NewHandler()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/console/", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Rin Console") {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "新增技能") ||
		!strings.Contains(response.Body.String(), "skillDetail") {
		t.Fatal("embedded Console is missing Skill management controls")
	}
	if csp := response.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "connect-src 'self'") {
		t.Fatalf("CSP = %q", csp)
	}
}

func TestHandlerRedirectsCanonicalConsolePath(t *testing.T) {
	response := httptest.NewRecorder()
	NewHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/console", nil))
	if response.Code != http.StatusPermanentRedirect || response.Header().Get("Location") != "/console/" {
		t.Fatalf("redirect = %d %q", response.Code, response.Header().Get("Location"))
	}
}
