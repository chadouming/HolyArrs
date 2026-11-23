-- Repair non-JSON actor defaults written by earlier transcode tables and error inserts.
UPDATE transcode_items SET created_by = '{"kind":"system"}' WHERE NOT json_valid(created_by);
UPDATE transcode_items SET updated_by = '{"kind":"system"}' WHERE NOT json_valid(updated_by);
UPDATE transcode_profiles SET created_by = '{"kind":"system"}' WHERE NOT json_valid(created_by);
UPDATE transcode_profiles SET updated_by = '{"kind":"system"}' WHERE NOT json_valid(updated_by);
UPDATE transcode_library_profiles SET updated_by = '{"kind":"system"}' WHERE NOT json_valid(updated_by);
