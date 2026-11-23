package webserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInternal(t *testing.T) {
	cases := []struct {
		name       string
		who        string
		err        error
		wantLogged bool
		wantStatus int
	}{
		{
			name:       "bare context canceled",
			who:        "api",
			err:        context.Canceled,
			wantLogged: false,
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "wrapped context canceled",
			who:        "api",
			err:        fmt.Errorf("select downloads: %w", context.Canceled),
			wantLogged: false,
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "context deadline exceeded",
			who:        "discover",
			err:        context.DeadlineExceeded,
			wantLogged: true,
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "normal error",
			who:        "api",
			err:        errors.New("database locked"),
			wantLogged: true,
			wantStatus: http.StatusInternalServerError,
		},
	}

	prevOut := log.Writer()
	prevFlags := log.Flags()
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			log.SetOutput(&logs)
			log.SetFlags(0)

			rec := httptest.NewRecorder()
			Internal(rec, tc.who, tc.err)

			if !tc.wantLogged {
				if logs.Len() != 0 {
					t.Fatalf("expected no log output, got %q", logs.String())
				}
			} else {
				wantLog := fmt.Sprintf("%s: %v", tc.who, tc.err)
				if !strings.Contains(logs.String(), wantLog) {
					t.Fatalf("log %q does not contain %q", logs.String(), wantLog)
				}
			}
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", ct)
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("invalid JSON body %q: %v", rec.Body.String(), err)
			}
			if body["error"] != "something went wrong; see the log" {
				t.Fatalf("unexpected error field, got %v", body)
			}
		})
	}
}
