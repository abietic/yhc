package plugins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMarketplaceClientSearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/plugins" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		q := r.URL.Query().Get("q")
		if q != "git" {
			t.Errorf("query = %q, want git", q)
		}

		resp := MarketplaceListResponse{
			Plugins: []MarketplaceSearchResult{
				{Name: "git-tools", Version: "1.0.0", Description: "Git helpers", Author: "dev"},
			},
			TotalCount: 1,
			Page:       1,
			PageSize:   20,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := NewMarketplaceClient(srv.URL)
	result, err := client.Search(context.Background(), "git", 1, 20)
	if err != nil {
		t.Fatal(err)
		return
	}
	if len(result.Plugins) != 1 {
		t.Fatalf("got %d plugins, want 1", len(result.Plugins))
	}
	if result.Plugins[0].Name != "git-tools" {
		t.Errorf("name = %q", result.Plugins[0].Name)
	}
}

func TestMarketplaceClientGetPlugin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/plugins/my-plugin" {
			http.NotFound(w, r)
			return
		}
		resp := MarketplaceSearchResult{
			Name: "my-plugin", Version: "2.1.0", Description: "A test plugin",
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := NewMarketplaceClient(srv.URL)

	// Found
	result, err := client.GetPlugin(context.Background(), "my-plugin")
	if err != nil {
		t.Fatal(err)
		return
	}
	if result.Version != "2.1.0" {
		t.Errorf("version = %q", result.Version)
	}

	// Not found
	_, err = client.GetPlugin(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent plugin")
	}
}

func TestMarketplaceClientHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("server error"))
	}))
	defer srv.Close()

	client := NewMarketplaceClient(srv.URL)
	_, err := client.Search(context.Background(), "", 0, 0)
	if err == nil {
		t.Error("expected error on 500")
	}
}
