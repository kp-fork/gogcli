package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMapsPlacesSearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/places:searchText" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("X-Goog-Api-Key"); got != "test-key" {
			t.Fatalf("missing API key header: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"places": []map[string]any{{
				"id":               "ChIJ123",
				"displayName":      map[string]any{"text": "Cafe"},
				"formattedAddress": "1 Main St",
				"googleMapsUri":    "https://maps.example/cafe",
			}},
		})
	}))
	defer srv.Close()
	t.Setenv("GOG_PLACES_API_KEY", "test-key")
	t.Setenv("GOG_PLACES_BASE_URL", srv.URL)

	out := captureStdout(t, func() {
		if err := (&MapsPlacesSearchCmd{Query: []string{"cafe"}}).Run(newCalendarJSONContext(t), &RootFlags{}); err != nil {
			t.Fatalf("Run: %v", err)
		}
	})
	if !strings.Contains(out, "ChIJ123") || !strings.Contains(out, "Cafe") || !strings.Contains(out, "maps.example") {
		t.Fatalf("unexpected output: %s", out)
	}
}
