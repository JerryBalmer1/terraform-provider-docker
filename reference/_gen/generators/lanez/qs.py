import sys; sys.path.insert(0, sys.argv[1])
from frag import load, save
import json
CLOSE={'B':['Raw output for service/task logs','Service/task `inspect` is sensitive as a whole'],
       'C':['docker_compose_logs raw output'],
       'D':['Whole-Dockerfile outputs (`dockerfile`, `stages`) are sensitive'],
       'E':['buildkit_version (builder) is a version','cli-only items (stack, manifest list) mirror docker CLI']}
for L,pfx in CLOSE.items():
    d=load(L); qs=d['naming_questions']; keep=[q for q in qs if not any(q.startswith(p) for p in pfx)]
    assert len(qs)-len(keep)==len(pfx),(L,len(qs),len(keep))
    d['naming_questions']=keep; save(L,d); print(L,'closed',len(pfx),'left',len(keep))
z=load('Z')
z['notes'].append("Decided by the user 2026-11-08 (Pass 4.5 Lane Z), applied to A-E by Lane Z; the four cross-lane questions and the lane questions they answer "
  "(B: logs name, service/task inspect sensitivity; C: compose logs output; D: whole-Dockerfile sensitivity; E: buildkit_version, docker_cli_version) are closed.")
z['notes'].append("Logs: every logs item has both `logs` (plain text) and `logs_json` (JSON array of entries) as raw outputs, each with its own hash "
  "(`logs_sha256`, `logs_json_sha256`, per the two-raw hash rule): engine:container-logs, swarm:service-logs, swarm:task-logs (logs_text renamed to logs), compose:cli-logs (logs added).")
z['notes'].append("Sixth category `result`: the outcome of running an action or a descriptive value about how data was produced, not the identity or state of a lasting object. "
  "Every structured output of an action is now `result` (prune results, container state after start/stop/..., exec/wait/run results, service update_status/update_response/mode/service_status, node spec, "
  "swarm tls_info/swarm_info/join_tokens, buildx bake/metadata/status/result); plus root_rotation_in_progress, force_update, image created, dockerfile reconstructed/fidelity, stack service_status. "
  "Identity handoff values (deleted IDs, tokens, digests) stay identity. V2 still asks resources and data sources for a structured output; V4 also applies to object-typed result outputs.")
z['notes'].append("Sensitivity is per field (new rule V5): a structured or result output is sensitive as a whole only with a `sensitive_reason` (only the three join_tokens outputs, where every field is a secret). "
  "Otherwise the output is not sensitive and the secret-carrying FIELDs carry `sensitive: true`. Raw outputs (JSON/text strings) stay sensitive as before; hashes never are. "
  "Secret channels marked: container/service/task env, plugin settings/config env, image config env, volume driver options (CIFS/NFS passwords), compose environment/env_file/secrets/build args/ssh/secrets/dockerfile_inline/configs content/environment-sourced values, Dockerfile ENV values/ARG defaults/history created_by, build provenance, attestation statements, buildx driver_opts, bake args/secret/ssh/dockerfile_inline, log lines, exec/run stdout/stderr, registry identity_token. "
  "A `dynamic` field cannot hide nested values, so the whole field is marked (stack service spec, image config blobs, provenance).")
z['notes'].append("Sensitivity judgment calls by Lane Z, reversible: (1) command lines (container args/cmd/entrypoint, service Args/Command, compose ps Command, compose top processes) are NOT a secret channel, following Lane A and CONTRACT.md's list; B and C had treated them as sensitive, so compose:cli-ps containers and compose:cli-top processes are now plain. "
  "(2) Lane C's parent-level marks on the whole `secrets` and `configs` maps were dropped (they hid every field); their environment/content children stay marked. "
  "(3) compose networks/models slices carry no secret channel and are now plain. Accepted risk: a secret interpolated (${VAR}) into a field that is not a secret channel is no longer hidden in plans; secrets belong in environment/secrets.")
z['notes'].append("Version names: V3 now allows eight names: the six plus `buildkit_version` (buildx:builder, buildx:builder-inspect, moved from identity) and `docker_cli_version` "
  "(sources_md pin docker_cli, added to the five cli-only items: stack, stacks, stack-services, manifest-list, manifest-inspect).")
z['naming_questions']=[]
save('Z',z)
