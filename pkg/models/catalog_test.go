package models

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const catalogJSON = `{
  "models": [
    {"id":"omniparser","kind":"grounding","instances":[{"actual":"stopped"}]},
    {"id":"grounder2","kind":"grounding","instances":[{"actual":"running"}]},
    {"id":"qwen3.8-27b","kind":"chat","instances":[{"actual":"running"}]},
    {"id":"hemmingway","kind":"chat","instances":[{"actual":"stopped"}]},
    {"id":"bge-small","kind":"text-embed","instances":[{"actual":"running"}]}
  ],
  "count": 5,
  "agentHosts": ["a","b"]
}`

func catalogServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/models" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(body))
	}))
}

func TestCatalogParse(t *testing.T) {
	srv := catalogServer(t, catalogJSON)
	defer srv.Close()

	cat, err := newTestClient(t, srv).Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(cat.Models) != 5 {
		t.Fatalf("models = %d, want 5", len(cat.Models))
	}
	if cat.Models[0].ID != "omniparser" || cat.Models[0].Kind != "grounding" {
		t.Errorf("first model = %+v", cat.Models[0])
	}
}

func TestCatalogHelpers(t *testing.T) {
	srv := catalogServer(t, catalogJSON)
	defer srv.Close()
	cat, err := newTestClient(t, srv).Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}

	ground := cat.GroundingModels()
	if len(ground) != 2 {
		t.Fatalf("grounding models = %d, want 2", len(ground))
	}
	chats := cat.ChatModels()
	if len(chats) != 2 {
		t.Fatalf("chat models = %d, want 2", len(chats))
	}
	if ground[0].Running() || ground[1].Running() == false {
		t.Errorf("Running() misreported: %+v", ground)
	}
}

func TestCatalogHelpersNil(t *testing.T) {
	var cat *Catalog
	if cat.GroundingModels() != nil || cat.ChatModels() != nil {
		t.Error("nil catalog should return nil slices")
	}
	if cat.DefaultGrounding() != DefaultGroundingModel {
		t.Error("nil catalog DefaultGrounding should fall back")
	}
	if cat.DefaultPlanner() != "" {
		t.Error("nil catalog DefaultPlanner should be empty")
	}
}

func TestDefaultGroundingPrefersRunning(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "running non-default wins",
			body: `{"models":[{"id":"omniparser","kind":"grounding","instances":[{"actual":"stopped"}]},{"id":"g2","kind":"grounding","instances":[{"actual":"running"}]}]}`,
			want: "g2",
		},
		{
			name: "omniparser running",
			body: `{"models":[{"id":"omniparser","kind":"grounding","instances":[{"actual":"running"}]}]}`,
			want: "omniparser",
		},
		{
			name: "none running falls back",
			body: `{"models":[{"id":"g9","kind":"grounding","instances":[{"actual":"stopped"}]}]}`,
			want: DefaultGroundingModel,
		},
		{
			name: "no grounding at all",
			body: `{"models":[{"id":"x","kind":"chat","instances":[]}]}`,
			want: DefaultGroundingModel,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := catalogServer(t, tc.body)
			defer srv.Close()
			cat, err := newTestClient(t, srv).Catalog(context.Background())
			if err != nil {
				t.Fatalf("Catalog: %v", err)
			}
			if got := cat.DefaultGrounding(); got != tc.want {
				t.Errorf("DefaultGrounding = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDefaultPlanner(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "regex match wins even after another running",
			body: `{"models":[{"id":"foo","kind":"chat","instances":[{"actual":"running"}]},{"id":"qwen3.8-27b","kind":"chat","instances":[{"actual":"running"}]}]}`,
			want: "qwen3.8-27b",
		},
		{
			name: "first running when no regex match",
			body: `{"models":[{"id":"foo","kind":"chat","instances":[{"actual":"running"}]},{"id":"bar","kind":"chat","instances":[{"actual":"running"}]}]}`,
			want: "foo",
		},
		{
			name: "first chat when none running",
			body: `{"models":[{"id":"first","kind":"chat","instances":[{"actual":"stopped"}]},{"id":"second","kind":"chat","instances":[]}]}`,
			want: "first",
		},
		{
			name: "no chat models",
			body: `{"models":[{"id":"g","kind":"grounding","instances":[{"actual":"running"}]}]}`,
			want: "",
		},
		{
			name: "instruct matching stopped regex not used but running nonmatch chosen",
			body: `{"models":[{"id":"instruct-x","kind":"chat","instances":[{"actual":"stopped"}]},{"id":"zzz","kind":"chat","instances":[{"actual":"running"}]}]}`,
			want: "zzz",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := catalogServer(t, tc.body)
			defer srv.Close()
			cat, err := newTestClient(t, srv).Catalog(context.Background())
			if err != nil {
				t.Fatalf("Catalog: %v", err)
			}
			if got := cat.DefaultPlanner(); got != tc.want {
				t.Errorf("DefaultPlanner = %q, want %q", got, tc.want)
			}
		})
	}
}
