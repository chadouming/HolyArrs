-- Integrity check state and details for beside subtitles. Add-only.
ALTER TABLE subtitles ADD COLUMN integrity_state TEXT CHECK (integrity_state IN ('ok', 'bad', 'accepted'));
ALTER TABLE subtitles ADD COLUMN integrity_issue TEXT;
ALTER TABLE subtitles ADD COLUMN integrity_note TEXT;
ALTER TABLE subtitles ADD COLUMN integrity_coverage REAL;
ALTER TABLE subtitles ADD COLUMN integrity_last_ms INTEGER;
ALTER TABLE subtitles ADD COLUMN integrity_video_s REAL;
ALTER TABLE subtitles ADD COLUMN integrity_checked_at TEXT;
