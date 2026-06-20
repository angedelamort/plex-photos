# Roadmap (V2)

Planned work for the next major iteration. See the [README](README.md) for the
current feature set.

## Scan pipeline

- Split metadata into its own labelled phase/progress bar in the UI
  (`index → thumbnails → metadata`); today metadata is folded into the
  "thumbnails" phase even though the work runs as its own per-photo step.
- Surface the deep scan as an admin button, and retire the now-redundant
  standalone "Regenerate thumbnails" / "Cleanup orphaned thumbnails" actions
  (a deep scan already covers both).

## Search

A search box to find photos across every library the user can access, querying
the indexed metadata rather than scanning the filesystem on each request. The
`photo_meta` / `photo_people` index already populated at scan time (capture
date, dimensions, GPS, geocoded city/country, person tags) makes this feasible
without re-reading EXIF per query.

Sketch:

- A search endpoint that filters indexed photos by free text (filename, place,
  person) and structured facets (date range, camera/lens, has-GPS, orientation),
  scoped to the caller's accessible libraries.
- Tokenize person/place names for partial matches; consider SQLite FTS5 for the
  text columns if simple `LIKE` proves too limited.
- A search affordance in the header that opens a results grid reusing the
  existing photo tiles and viewer, plus an "open as slideshow" action.
- Clickable tags as a faceted entry point: surface a photo's **person** and
  **location** (geocoded place) tags — e.g. in the viewer's Details panel — as
  links that run a pre-filled search for that person or place. Clicking a tag
  opens the results grid for "all photos of this person" or "all photos taken
  here," turning the indexed `photo_people` / `photo_meta` place data into
  one-click navigation.
- Naturally complements smart collections below (a saved search ≈ a rule-based
  collection).

## Smart collections

Rule-based collections that populate automatically instead of being manually
curated (analogous to Plex smart collections). Selected by filters such as date
range, EXIF camera/lens, GPS area, filename pattern, or favorited status — and
optionally combined with manual playlists.

Sketch:

- Persist a rule definition (e.g. JSON criteria) per smart collection.
- Evaluate rules at query time over the indexed photo metadata (`photo_meta` /
  `photo_people`: capture date, GPS, geocoded place, dimensions, person tags),
  which is already populated during scans.
- Surface them alongside playlists in the UI.
