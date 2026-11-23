CREATE TABLE IF NOT EXISTS transcode_profiles (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL UNIQUE,
    description TEXT    NOT NULL DEFAULT '',
    is_default  INTEGER NOT NULL DEFAULT 0,
    config_json TEXT    NOT NULL,
    created_at  TEXT    NOT NULL,
    updated_at  TEXT    NOT NULL,
    created_by  TEXT    NOT NULL DEFAULT '{"kind":"system"}',
    updated_by  TEXT    NOT NULL DEFAULT '{"kind":"system"}'
);

CREATE TABLE IF NOT EXISTS transcode_library_profiles (
    library_folder_id INTEGER PRIMARY KEY REFERENCES library_folders(id) ON DELETE CASCADE,
    enabled           INTEGER NOT NULL DEFAULT 1,
    profile_id        INTEGER REFERENCES transcode_profiles(id) ON DELETE SET NULL,
    updated_at        TEXT    NOT NULL,
    updated_by        TEXT    NOT NULL DEFAULT '{"kind":"system"}'
);

CREATE TABLE IF NOT EXISTS transcode_items (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    file_id            INTEGER NOT NULL UNIQUE REFERENCES files(id) ON DELETE CASCADE,
    profile_id         INTEGER REFERENCES transcode_profiles(id) ON DELETE SET NULL,
    state              TEXT    NOT NULL DEFAULT 'pending',
    skip_reason        TEXT,
    fileflows_tagged   INTEGER NOT NULL DEFAULT 0,
    file_mtime_ns      INTEGER NOT NULL DEFAULT 0,
    file_size_bytes    INTEGER NOT NULL DEFAULT 0,
    source_codec       TEXT    NOT NULL DEFAULT '',
    source_width       INTEGER NOT NULL DEFAULT 0,
    source_height      INTEGER NOT NULL DEFAULT 0,
    source_hdr         TEXT    NOT NULL DEFAULT '',
    target_codec       TEXT    NOT NULL DEFAULT '',
    selected_q         INTEGER,
    mean_vmaf          REAL,
    gpu_device         TEXT    NOT NULL DEFAULT '',
    input_bytes        INTEGER NOT NULL DEFAULT 0,
    output_bytes       INTEGER NOT NULL DEFAULT 0,
    saved_bytes        INTEGER NOT NULL DEFAULT 0,
    encode_seconds     REAL    NOT NULL DEFAULT 0,
    avg_fps            REAL    NOT NULL DEFAULT 0,
    avg_speed          REAL    NOT NULL DEFAULT 0,
    edl_applied        INTEGER NOT NULL DEFAULT 0,
    tonemapped         INTEGER NOT NULL DEFAULT 0,
    job_id             INTEGER,
    last_error         TEXT,
    probed_at          TEXT,
    completed_at       TEXT,
    created_at         TEXT    NOT NULL,
    updated_at         TEXT    NOT NULL,
    created_by         TEXT    NOT NULL DEFAULT '{"kind":"system"}',
    updated_by         TEXT    NOT NULL DEFAULT '{"kind":"system"}'
);
CREATE INDEX IF NOT EXISTS idx_transcode_items_state ON transcode_items(state, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_transcode_items_completed ON transcode_items(completed_at DESC);
