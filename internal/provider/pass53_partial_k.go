package provider

// Pass 5.3 Lane K: the compose PARTIAL(pass5) markers that remain (format:
// see pass51_partial_s.go).
//
// Removed in Pass 5.3: compose:services (local mode now runs the lifecycle
// hooks, sends blkio_config, credential_spec, use_api_socket, links /
// external_links, mac_address, umask and volume labels, and honours
// pull_policy refresh / daily / weekly / every_<d>; compose_local_k.go,
// compose_hooks.go), compose:secrets and compose:configs (swarm mode sends
// SecretSpec.Driver and Templating; compose_swarm.go).

// docker_compose_publish pushes the compose files, the env_file rewrite,
// the `--with-env` env file layers, the `--resolve-image-digests` layer and
// the `--oci-version` manifest forms (action_compose_images.go,
// compose_publish_k.go). `--app` also copies every service image into the
// target repository and pushes an image index whose subject is the compose
// manifest (compose v5.7.0 pkg/compose/publish.go:140-194
// pushApplicationIndex, oci.Copy in internal/oci/resolver.go:132-162): that
// needs a registry-to-registry manifest and blob copy client the provider
// does not have (its registry client only pushes the artifact's own blobs).
//
// PARTIAL(pass5) compose:cli-publish `--app` is not implemented: the service images are not copied into the repository and no image index is pushed

// docker_compose records top-level models (the `models` output) but never
// pulls or runs them, and a service's `models` bindings are ignored (plan
// warning, compose_local.go). Docker Model Runner is not part of the Engine
// API: the v1.56 swagger (reference/upstream/engine-api/api/swagger.yaml)
// has no model endpoint, and docker compose v5.7.0 drives models by running
// the `docker model` CLI plugin binary (pkg/compose/model.go:85 GetPlugin,
// :127 `model pull`, :191 `model configure`, :210 `model status`), which
// the provider cannot do: nothing shells out.
//
// PARTIAL(pass5) compose:models models are recorded in the `models` output but never pulled or run (Engine API 1.56 has no model endpoint; compose runs the docker-model CLI plugin), and service `models` bindings are ignored
