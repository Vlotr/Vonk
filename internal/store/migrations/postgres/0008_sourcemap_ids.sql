-- 0008_sourcemap_ids: nothing to do on Postgres.
--
-- The SQLite twin makes source_maps and api_tokens again with
-- AUTOINCREMENT, so their ids (which name parsed-map cache entries and the
-- directories of a token's chunks) are never used again. Postgres identity
-- columns (0007) already never hand out an id twice, so the tables stay as
-- they are; this file keeps the two directories' versions in step.

SELECT 1;
