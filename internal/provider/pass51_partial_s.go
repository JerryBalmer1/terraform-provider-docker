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

// docker_compose_publish loads the project and pushes the compose files as
// layers (action_compose_images.go). `docker compose publish` also documents
// --with-env, --resolve-image-digests, --oci-version and --app; the action has
// no argument for any of them (Pass 5 Lane C [ASSUMED]: compose files only).
//
// PARTIAL(pass5) compose:cli-publish env files (`--with-env`), `--resolve-image-digests`, `--oci-version` and `--app` are not implemented; only the compose files are published

// data docker_registry_image accepts insecure_skip_verify for configurations
// migrated from kreuzwerker, where it switched TLS verification off for a
// direct registry read. Here the read goes through the daemon
// (DistributionInspect), so the argument has no effect
// (registry_image_data_source.go). renames.json maps it as implemented.
//
// PARTIAL(pass5) engine:registry-image-data `insecure_skip_verify` is accepted but has no effect (the read goes through the daemon)

// docker_compose (local mode) warns and ignores these service keys
// (compose_local.go composeLocalUnsupported), although each maps to Engine
// API calls the provider already makes elsewhere (exec for the hooks,
// HostConfig for blkio/credential spec, a bind mount for the API socket,
// links). pull_policy daily/weekly/every_<n> fall through to `missing`
// (compose_local_apply.go composeLocalCreate). Not marked: develop (a skipped
// item, compose:service-develop), attach (CLI log streaming), provider
// (runs an external binary; nothing shells out) and models (compose:models).
//
// PARTIAL(pass5) compose:services local mode ignores lifecycle hooks (pre_start/post_start/pre_stop), blkio_config, credential_spec, use_api_socket and external_links (plan warning), and treats pull_policy daily/weekly/every_<n> as missing

// docker_compose records top-level models (the `models` output) but never
// pulls or runs them, and local mode ignores a service's `models` bindings
// (compose_resource.go, compose_local.go).
//
// PARTIAL(pass5) compose:models models are recorded in the `models` output but never pulled or run, and service `models` bindings are ignored

// docker_compose (swarm mode) creates secrets and configs from their file /
// environment / content source but ignores `driver` and `template_driver`
// with a plan warning (compose_swarm.go), which `docker stack deploy` sends
// as SecretSpec.Driver and Templating.
//
// PARTIAL(pass5) compose:secrets swarm mode ignores `driver` and `template_driver` (plan warning)
// PARTIAL(pass5) compose:configs swarm mode ignores `template_driver` (plan warning)
