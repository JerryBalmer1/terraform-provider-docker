# Docker Hub live probe (Pass 7 Lane 0)

Read-only probe of the Docker Hub API with the user's personal access token (Personal plan, no organization), against the pinned spec `reference/upstream/hub/docs.docker.com-hub-latest.yaml` (info.version 2-beta, sha256 7c280db399f8...). Run 2026-11-11 17:58:07 local time.

- Credentials: `DOCKER_HUB_USERNAME` = jlbalmerjr, `DOCKER_HUB_TOKEN` = [REDACTED]. The token was exchanged at POST /v2/auth/token for a JWT (Bearer [REDACTED]); neither value was printed, logged or written. The probe script asserts neither appears in its output.
- Writes: none. The only POST is the token exchange, which issues a short-lived JWT and changes no Hub state. POST .../immutabletags/verify (read-only by its description) was not called, to keep the probe GET/HEAD only.
- Namespace: jlbalmerjr has 0 repositories (`count=0`), so the repository and tag shapes were checked on the public repository docker/welcome-to-docker (read only), marked below.
- Shape: every key of the JSON response is compared with the documented schema for the returned status (properties, required, types; $ref and allOf resolved; first 3 list entries). `mismatch` = a key the spec does not document, a missing required key, a type difference, or a status the spec does not document. A 403 with a documented 403 schema is `ok` (the shape matched).
- Script (not in the repo; reproducible): stdlib urllib + PyYAML, CA bundle from certifi (the Python 3.10 store has an expired root; TLS verification stayed on).

| # | method | endpoint | status | shape | detail |
|---|---|---|---|---|---|
| 1 | POST | `/v2/auth/token` | 200 | ok | token exchange (identifier + PAT); the JWT is held in memory only |
| 2 | GET | `/v2/access-tokens` | 403 | mismatch | status 403 not documented for this operation — error: token issued from personal access token is not allowed |
| 3 | GET | `/v2/access-tokens/{uuid}` | - | n/a | not probed: the list returned no token uuid |
| 4 | GET | `/v2/auditlogs/{account}/actions` | 403 | ok | error: token issued from personal access token is not allowed; account = personal namespace |
| 5 | GET | `/v2/auditlogs/{account}` | 403 | ok | error: token issued from personal access token is not allowed; account = personal namespace |
| 6 | GET | `/v2/namespaces/{namespace}/repositories` | 200 | ok | count=0 |
| 7 | GET | `/v2/namespaces/{namespace}/repositories/{repository}` | 200 | mismatch | $.registry_region: in response, not in spec — public repository docker/welcome-to-docker (jlbalmerjr has none) |
| 8 | HEAD | `/v2/namespaces/{namespace}/repositories/{repository}` | 200 | ok | (200 documented with an empty response object; corrected by hand, the probe script read an empty object as undocumented) public repository docker/welcome-to-docker (jlbalmerjr has none) |
| 9 | GET | `/v2/namespaces/{namespace}/repositories/{repository}/tags` | 200 | mismatch | $.results[0].media_type: in response, not in spec; $.results[0].content_type: in response, not in spec; $.results[0].digest: in response, not in spec — public repository docker/welcome-to-docker (jlbalmerjr has none); count=1 |
| 10 | HEAD | `/v2/namespaces/{namespace}/repositories/{repository}/tags` | 200 | ok | public repository docker/welcome-to-docker (jlbalmerjr has none) |
| 11 | GET | `/v2/namespaces/{namespace}/repositories/{repository}/tags/{tag}` | 200 | mismatch | $.media_type: in response, not in spec; $.content_type: in response, not in spec; $.digest: in response, not in spec — public repository docker/welcome-to-docker (jlbalmerjr has none) |
| 12 | HEAD | `/v2/namespaces/{namespace}/repositories/{repository}/tags/{tag}` | 200 | ok | public repository docker/welcome-to-docker (jlbalmerjr has none) |
| 13 | GET | `/v2/namespaces/{namespace}/repositories/{repository}` | 200 | mismatch | $.registry_region: in response, not in spec — anonymous (no bearer); public repository docker/welcome-to-docker (jlbalmerjr has none) |
| 14 | GET | `/v2/orgs/{org_name}/settings` | 403 | ok | error: token issued from personal access token is not allowed; org-scoped, personal namespace as org_name |
| 15 | GET | `/v2/orgs/{org_name}/access-tokens` | 403 | ok | error: token issued from personal access token is not allowed; org-scoped, personal namespace as org_name |
| 16 | GET | `/v2/orgs/{org_name}/members` | 403 | ok | error: token issued from personal access token is not allowed; org-scoped, personal namespace as org_name |
| 17 | GET | `/v2/orgs/{org_name}/members/export` | 403 | ok | error: token issued from personal access token is not allowed; org-scoped, personal namespace as org_name |
| 18 | GET | `/v2/orgs/{org_name}/invites` | 403 | ok | error: token issued from personal access token is not allowed; org-scoped, personal namespace as org_name |
| 19 | GET | `/v2/orgs/{org_name}/groups` | 403 | ok | error: token issued from personal access token is not allowed; org-scoped, personal namespace as org_name |

Not probed:

- SCIM (`/v2/scim/2.0/*`, 9 operations): the spec secures them with `scimToken` (an IdP SCIM token from an SSO connection), not a Hub bearer; the account has none.
- `GET /v2/access-tokens/{uuid}`: the list was refused, so no UUID to read.
- Group, group member and org access token reads by name or ID: the list routes were refused, so no name or ID to read.
- Every POST / PUT / PATCH / DELETE (writes).

## Findings

1. **P7-F1 PAT-issued JWTs are refused outside the repository routes.** `/v2/access-tokens`, both audit-log routes and all six org routes answer 403 `token issued from personal access token is not allowed`. So a provider configured with hub_token = a PAT can manage repositories and read tags, but not personal access tokens (docker_hub_access_token / docker_hub_access_tokens), audit logs, or any org item. The spec does not say this: its access-tokens tag says a PAT can be used "instead of a password". Org items need an OAT or a password-issued JWT; personal tokens need a password-issued JWT (naming question P7-Q2).
2. **P7-F2 The 403 on GET /v2/access-tokens is undocumented**: the spec lists no 403 response for that operation.
3. **P7-F3 Tag responses carry `digest`, `media_type` and `content_type`, which the spec's tag schema does not have** (GET .../tags and GET .../tags/{tag}). Lane H keeps the raw body byte-exact, so they are in `repository_tags_json` / `repository_tag_json`; the structured outputs follow the spec until the spec adds them (DriftCheck hub source reports it), or the inventory adds them by decision.
4. **P7-F4 Repository responses carry `registry_region`**, which `repository_info` does not have (GET repository, authenticated and anonymous).
5. Audit logs: the gate for a personal namespace could not be observed: the PAT check refuses the request before any plan or org check. requires stays plan=unknown.
