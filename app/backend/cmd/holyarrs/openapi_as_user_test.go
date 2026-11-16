package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/HolyArrs/holyarrs/app/backend/internal/accounts"
)

// The API file lists the X-HolyArrs-User header on exactly the routes the
// sign-in middleware honours it on, so the file cannot promise it elsewhere
// or leave a route out.
func TestOpenAPIAsUserHeader(t *testing.T) {
	t.Parallel()
	var doc struct {
		Paths map[string]map[string]struct {
			Parameters []struct {
				Name        string `json:"name"`
				In          string `json:"in"`
				Required    bool   `json:"required"`
				Description string `json:"description"`
			} `json:"parameters"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(readSpec(t), &doc); err != nil {
		t.Fatal(err)
	}
	var listed []string
	for path, ops := range doc.Paths {
		for method, op := range ops {
			for _, p := range op.Parameters {
				if p.In != "header" || p.Name != accounts.AsUserHeader {
					continue
				}
				route := strings.ToUpper(method) + " " + path
				listed = append(listed, route)
				if p.Required || !strings.Contains(p.Description, "jellyfin:") {
					t.Errorf("%s: the header must be optional and say its jellyfin:<id> form: %+v", route, p)
				}
			}
		}
	}
	want := slices.Clone(accounts.AsUserRoutes)
	slices.Sort(want)
	slices.Sort(listed)
	if !slices.Equal(listed, want) {
		t.Fatalf("the API file lists the header on %v; the middleware honours it on %v", listed, want)
	}
}
