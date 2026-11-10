package provider

// Pass 5.1 Lane S: PARTIAL(pass5) status markers for registered types whose
// documented sub-feature is absent or a no-op, where the code lacking it
// belongs to another lane. reference/_gen/merge.py reads every marker in
// internal/provider (test files excluded) and computes the item's status as
// `partial`, with the marker's note as the status note. A marker is a line
// comment that starts with PARTIAL(pass5), then the item id (layer:name, as
// in reference/inventory.json), then a note saying what is missing.
// Remove a marker in the same change that implements what its note names.
// Markers next to the code they describe live in the Lane E (buildx) files.
// Invoke-Build StubReport lists all markers.

// Pass 5.3 Lane R: the engine:registry-image-data marker (insecure_skip_verify
// had no effect) is gone; the data source now reads the registry directly when
// the argument is set (registry_image_data_source.go).
