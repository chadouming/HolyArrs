package subtitles

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/HolyArrs/holyarrs/app/backend/internal/core/webserver"
)

// HistoryItem is one completed or user-skipped subtitle entry in subtitle history.
type HistoryItem struct {
	ID              int64     `json:"id"`
	FileID          int64     `json:"file_id"`
	TitleID         int64     `json:"title_id"`
	Title           string    `json:"title"`
	MediaKind       string    `json:"media_kind"`
	Season          *int      `json:"season,omitempty"`
	Episode         *int      `json:"episode,omitempty"`
	RelativePath    string    `json:"relative_path,omitempty"`
	Language        string    `json:"language"`
	Forced          bool      `json:"forced"`
	HearingImpaired bool      `json:"hearing_impaired"`
	State           string    `json:"state"`
	How             string    `json:"how"`
	Source          string    `json:"source"`
	At              time.Time `json:"at"`
}

// HistoryResult is the paginated response returned by ListHistory.
type HistoryResult struct {
	Items        []HistoryItem `json:"items"`
	Total        int           `json:"total"`
	DoneCount    int           `json:"done_count"`
	SkippedCount int           `json:"skipped_count"`
	DoneCounts   DoneCounts    `json:"done_counts"`
	Limit        int           `json:"limit"`
	Offset       int           `json:"offset"`
}

// ListHistory returns SQL-paginated completed and user-skipped subtitle history.
func (s *Service) ListHistory(ctx context.Context, stateFilter, query string, limit, offset int) (HistoryResult, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	state := strings.ToLower(strings.TrimSpace(stateFilter))
	if state == "" {
		state = "all"
	}
	items, total, doneCount, skippedCnt, dc, err := queryHistory(ctx, s.Store.Read(), state, query, limit, offset)
	if err != nil {
		return HistoryResult{}, err
	}
	return HistoryResult{
		Items:        items,
		Total:        total,
		DoneCount:    doneCount,
		SkippedCount: skippedCnt,
		DoneCounts:   dc,
		Limit:        limit,
		Offset:       offset,
	}, nil
}

func (a *Handlers) historySubtitles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := strings.ToLower(strings.TrimSpace(q.Get("state")))
	switch state {
	case "", "all", "done", "skipped":
	default:
		webserver.Fail(w, http.StatusBadRequest, "state must be all, done or skipped")
		return
	}
	limit := waitingLimit(r, 50, 200)
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}
	res, err := a.svc.ListHistory(r.Context(), state, q.Get("q"), limit, offset)
	if err != nil {
		webserver.Internal(w, "api", err)
		return
	}
	webserver.WriteJSON(w, http.StatusOK, res)
}
