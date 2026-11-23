-- The names that point at one part of a show, as JSON
-- [{"names": [...], "season": 4, "offset": 0, "source": "anime_map"}]:
-- episode N of the part is episode offset+N of the season. A Release named
-- after a part counts its running number inside that part. Filled when a
-- show is added or refreshed; NULL (every row so far) means none.
ALTER TABLE titles ADD COLUMN part_names TEXT CHECK (part_names IS NULL OR json_valid(part_names));
