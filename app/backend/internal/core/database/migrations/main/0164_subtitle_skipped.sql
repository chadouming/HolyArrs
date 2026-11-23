-- Add 'skipped' to subtitles.state and add indexes for subtitle history and slot lookups.
CREATE TABLE subtitles_new (
    id                 INTEGER PRIMARY KEY,
    created_at         TEXT NOT NULL,
    created_by         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,
    updated_by         TEXT NOT NULL,
    file_id            INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    language           TEXT NOT NULL,
    forced             INTEGER NOT NULL DEFAULT 0,
    hearing_impaired   INTEGER NOT NULL DEFAULT 0,
    place              TEXT NOT NULL CHECK (place IN ('built_in', 'beside', 'wish')),
    track_index        INTEGER,
    codec              TEXT,
    relative_path      TEXT,
    site_id            INTEGER REFERENCES subtitle_sites(id) ON DELETE SET NULL,
    site_name          TEXT,
    site_key           TEXT,
    score              INTEGER,
    score_parts        TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(score_parts)),
    origin             TEXT NOT NULL CHECK (origin IN ('found', 'fetched', 'by_hand')),
    state              TEXT NOT NULL CHECK (state IN ('present', 'replaced', 'gone', 'wanted', 'none_good_enough', 'skipped')),
    saved_at           TEXT,
    tries              INTEGER NOT NULL DEFAULT 0,
    first_tried_at     TEXT,
    last_tried_at      TEXT,
    next_try_at        TEXT,
    sync               TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(sync)),
    made               TEXT CHECK (made IN ('translated', 'translated_partial', 'ai_translated', 'machine_translated')),
    transcribed        INTEGER NOT NULL DEFAULT 0,
    integrity_state    TEXT CHECK (integrity_state IN ('ok', 'bad', 'accepted')),
    integrity_issue    TEXT,
    integrity_note     TEXT,
    integrity_coverage REAL,
    integrity_last_ms  INTEGER,
    integrity_video_s  REAL,
    integrity_checked_at TEXT
);

INSERT INTO subtitles_new (
    id, created_at, created_by, updated_at, updated_by,
    file_id, language, forced, hearing_impaired, place,
    track_index, codec, relative_path, site_id, site_name, site_key,
    score, score_parts, origin, state, saved_at, tries,
    first_tried_at, last_tried_at, next_try_at, sync,
    made, transcribed,
    integrity_state, integrity_issue, integrity_note, integrity_coverage,
    integrity_last_ms, integrity_video_s, integrity_checked_at
)
SELECT
    id, created_at, created_by, updated_at, updated_by,
    file_id, language, forced, hearing_impaired, place,
    track_index, codec, relative_path, site_id, site_name, site_key,
    score, score_parts, origin, state, saved_at, tries,
    first_tried_at, last_tried_at, next_try_at, sync,
    made, transcribed,
    integrity_state, integrity_issue, integrity_note, integrity_coverage,
    integrity_last_ms, integrity_video_s, integrity_checked_at
FROM subtitles;

DROP TABLE subtitles;
ALTER TABLE subtitles_new RENAME TO subtitles;

CREATE INDEX subtitles_file ON subtitles(file_id, state);
CREATE INDEX subtitles_due ON subtitles(state, next_try_at) WHERE state IN ('wanted', 'none_good_enough');
CREATE UNIQUE INDEX subtitles_wish_row ON subtitles(file_id, language, forced) WHERE place = 'wish';
CREATE UNIQUE INDEX subtitles_beside_path ON subtitles(file_id, relative_path) WHERE place = 'beside' AND state = 'present';
CREATE INDEX IF NOT EXISTS idx_subtitles_beside_history ON subtitles(place, state, origin, saved_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_subtitles_wish_state ON subtitles(place, state, updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_slots_file_id ON slots(file_id) WHERE file_id IS NOT NULL;
