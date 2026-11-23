-- A subtitle HolyArrs made from a video's audio is marked (transcribed) beside
-- what already says a machine made it, and a file whose transcript failed or
-- cannot be made is remembered (subtitle_transcripts) so it is not tried on
-- every search. Add-only.
ALTER TABLE subtitles ADD COLUMN transcribed INTEGER NOT NULL DEFAULT 0;
CREATE TABLE subtitle_transcripts (
    file_id     INTEGER PRIMARY KEY REFERENCES files(id) ON DELETE CASCADE,
    state       TEXT NOT NULL CHECK (state IN ('failed', 'final')),
    reason      TEXT NOT NULL DEFAULT '',
    tries       INTEGER NOT NULL DEFAULT 0,
    next_try_at TEXT,
    updated_at  TEXT NOT NULL
);
