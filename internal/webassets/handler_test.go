package webassets_test

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/JamesbbBriz/agent-workflow/internal/webassets"
)

func TestHandlerServesEmbeddedAppAndDelegatesAPI(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"path":"` + r.URL.Path + `"}`))
	})
	handler := webassets.New(api)

	var index string
	for _, path := range []string{"/", "/runs/example"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `<div id="root"></div>`) {
			t.Fatalf("%s did not serve the embedded app: code=%d body=%s", path, response.Code, response.Body.String())
		}
		if response.Header().Get("Content-Security-Policy") == "" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s omitted browser security headers: %v", path, response.Header())
		}
		index = response.Body.String()
	}
	asset := regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`).FindStringSubmatch(index)
	if len(asset) != 2 {
		t.Fatalf("embedded app has no bundled asset: %s", index)
	}
	assetResponse := httptest.NewRecorder()
	handler.ServeHTTP(assetResponse, httptest.NewRequest(http.MethodGet, asset[1], nil))
	if assetResponse.Code != http.StatusOK || assetResponse.Body.Len() == 0 {
		t.Fatalf("embedded asset unavailable: code=%d", assetResponse.Code)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/control-plane", nil))
	if response.Code != http.StatusOK || response.Body.String() != `{"path":"/v1/control-plane"}` {
		t.Fatalf("API request was not delegated: code=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHandlerRejectsNonGetUIRequests(t *testing.T) {
	handler := webassets.New(http.NotFoundHandler())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST / code=%d", response.Code)
	}
}
