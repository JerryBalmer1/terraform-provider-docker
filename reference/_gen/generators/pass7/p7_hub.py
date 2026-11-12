"""Pass 7 Lane 0: write reference/_gen/fragments/hub.json and reference/_gen/fragments/outputs/hub.json from the pinned
Docker Hub API spec (reference/upstream/hub/docs.docker.com-hub-latest.yaml, SOURCES.md pin hub_api).

Run from the repo root: python -I reference/_gen/generators/pass7/p7_hub.py  (needs PyYAML, like z1_build.py)

The spec is the only naming source (DECISIONS.md): type names come from the spec's tags, paths and operation summaries,
attribute names are the spec's property names snake_cased (CONTRACT.md "Naming"). Every operation gets exactly one
primary home; every tag is covered by an item or listed in INFO_TAGS with a reason. Exits 1 on an unplaced operation or
an uncovered tag. Provenance: once a fragment is edited by hand, re-running this overwrites the edit."""
import json, os, re, sys

import yaml

ROOT = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "..", ".."))
SPEC_REL = "upstream/hub/docs.docker.com-hub-latest.yaml"
SPEC = yaml.safe_load(open(os.path.join(ROOT, "reference", SPEC_REL), encoding="utf-8"))
OPS = {}
for _p, _it in SPEC["paths"].items():
    for _m, _op in _it.items():
        if _m in ("get", "post", "put", "patch", "delete", "head"):
            OPS[_op["operationId"]] = (_m.upper(), _p, _op, _it.get("parameters", []))

# Tags with no operations (kind: info in the spec). Each is covered here, with what it means for the provider.
INFO_TAGS = {
    "changelog": "Info tag, no operations: points at the Hub API changelog. DriftCheck watches the spec itself (hub source kind).",
    "resources": "Info tag, no operations: links the experimental hub-tool CLI. Not a naming source (DECISIONS.md: the spec only).",
    "rate-limiting": "Info tag, no operations: X-RateLimit-Limit / -Remaining / -Reset headers. Lane H's Hub client must honour them (retry after Reset on 429); no Terraform type.",
    "authentication": "Info tag, no operations: password / PAT / OAT auth types. Covered by ephemeral docker_hub_token (the /v2/auth/token exchange) and the provider arguments hub_username / hub_token (Lane H).",
}

V_HUB = {"name": "hub_api_version", "tf_type": "string", "category": "version", "source": {"type": "sources_md", "pin": "hub_api"},
         "known_after_apply": False, "sensitive": False,
         "description": "The Docker Hub API spec version the client follows (info.version of the pinned spec)."}
SECRET_PROPS = {"token", "access_token", "secret", "password", "scim_token"}

# requires (Pass 7 decision): "org" = the path is org-scoped; "plan=<name>" = a subscription the spec names;
# "plan=unknown" = a gated feature (orgs, groups, members, org settings, OATs, SCIM, audit logs, invites) whose plan
# the spec does not state. No entry = reachable on a Personal plan as far as the spec says.
ORG_UNKNOWN = ["org", "plan=unknown"]

# (id, kind, name, update, tag, [operationId, ...] (primary unless prefixed "~"), requires, summary, extra)
ITEMS = [
    ("hub:token", "ephemeral_resource", "docker_hub_token", None, "authentication-api", ["AuthCreateAccessToken"], [],
     "Exchange a username and a password, PAT or OAT for a short-lived Hub bearer JWT (POST /v2/auth/token); the JWT is sensitive and never stored.", {}),
    ("hub:users-login", "skip", None, None, "authentication-api", ["PostUsersLogin"], [],
     "Legacy login (POST /v2/users/login).",
     {"skip_reason": "Legacy login route; the spec documents POST /v2/auth/token (ephemeral docker_hub_token) as the exchange, with the same identifier and secret."}),
    ("hub:users-2fa-login", "skip", None, None, "authentication-api", ["PostUsers2FALogin"], [],
     "Second-factor login (POST /v2/users/2fa-login).",
     {"skip_reason": "Needs an interactive TOTP code after a password login; Terraform runs unattended. A PAT or OAT through docker_hub_token needs no second factor."}),
    ("hub:access-token", "resource", "docker_hub_access_token", "in_place", "access-tokens",
     ["postV2AccessTokens", "getV2AccessTokensByUuid", "patchV2AccessTokensByUuid", "deleteV2AccessTokensByUuid"], [],
     "A personal access token. Label and is_active update in place; scopes and expires_at force replacement. The token value is returned once, by create: stored as the sensitive `token` and optionally written to secret_output_path.",
     {"secret_output_path": True}),
    ("hub:access-tokens", "data_source", "docker_hub_access_tokens", None, "access-tokens", ["getV2AccessTokens"], [],
     "The personal access tokens of the authenticated user (paginated list; never the token values).", {"plural": True}),
    ("hub:access-token-data", "data_source", "docker_hub_access_token", None, "access-tokens", ["~getV2AccessTokensByUuid"], [],
     "One personal access token by UUID (metadata only; the value is never readable again).", {}),
    ("hub:audit-log-actions", "data_source", "docker_hub_audit_log_actions", None, "audit-logs", ["AuditLogs_ListAuditActions"], ["plan=unknown"],
     "The audit log action names of a namespace, for filtering docker_hub_audit_logs (the spec returns a map by category).", {}),
    ("hub:audit-logs", "data_source", "docker_hub_audit_logs", None, "audit-logs", ["AuditLogs_ListAuditLogs"], ["plan=unknown"],
     "Audit log events of a namespace, filtered by action, name, actor and a from/to window; page_size bounds state.", {"plural": True}),
    ("hub:org-settings", "resource", "docker_hub_org_settings", "in_place", "org-settings",
     ["getV2OrgsByNameSettings", "putV2OrgsByNameSettings"], ["org", "plan=business"],
     "An organization's settings (restricted_images). A singleton: create adopts the existing settings and PUTs them, delete only removes it from state (the spec has no delete). The spec: restricted_images is only used on a business subscription.", {}),
    ("hub:org-settings-data", "data_source", "docker_hub_org_settings", None, "org-settings", ["~getV2OrgsByNameSettings"], ["org", "plan=unknown"],
     "An organization's settings, read only.", {}),
    ("hub:org-access-token", "resource", "docker_hub_org_access_token", "in_place", "org-access-tokens",
     ["postV2OrgsByNameAccessTokens", "getV2OrgsByOrgNameAccessTokensByAccessTokenId",
      "patchV2OrgsByOrgNameAccessTokensByAccessTokenId", "deleteV2OrgsByOrgNameAccessTokensByAccessTokenId"], ORG_UNKNOWN,
     "An organization access token (OAT). A separate item from docker_hub_access_token: org-scoped, with resource scopes. The value is returned once, by create: sensitive `token` and optional secret_output_path.",
     {"secret_output_path": True}),
    ("hub:org-access-tokens", "data_source", "docker_hub_org_access_tokens", None, "org-access-tokens", ["getV2OrgsByNameAccessTokens"], ORG_UNKNOWN,
     "The access tokens of an organization (paginated; never the values).", {"plural": True}),
    ("hub:org-access-token-data", "data_source", "docker_hub_org_access_token", None, "org-access-tokens",
     ["~getV2OrgsByOrgNameAccessTokensByAccessTokenId"], ORG_UNKNOWN, "One organization access token by ID (metadata only).", {}),
    ("hub:repository", "resource", "docker_hub_repository", "replace", "repositories", ["CreateRepository", "GetRepository"], [],
     "A Hub repository in a namespace (name, description, full_description, registry, is_private). The spec has create and read only: no update and no delete route, so every change replaces and destroy can only remove it from state (finding P7-F5).", {}),
    ("hub:repositories", "data_source", "docker_hub_repositories", None, "repositories", ["listNamespaceRepositories"], [],
     "The repositories of a namespace (user or organization), filtered by name and ordered.", {"plural": True}),
    ("hub:repository-data", "data_source", "docker_hub_repository", None, "repositories", ["~GetRepository", "CheckRepository"], [],
     "One repository of a namespace; public repositories read anonymously.", {}),
    ("hub:repository-tags", "data_source", "docker_hub_repository_tags", None, "repositories",
     ["ListRepositoryTags", "headV2NamespacesByNamespaceRepositoriesByRepositoryTags"], [],
     "The tags of a repository (paginated).", {"plural": True}),
    ("hub:repository-tag", "data_source", "docker_hub_repository_tag", None, "repositories",
     ["GetRepositoryTag", "headV2NamespacesByNamespaceRepositoriesByRepositoryTagsByTag"], [],
     "One tag of a repository: its images per platform, size, status and push/pull times.", {}),
    ("hub:repository-immutable-tags", "resource", "docker_hub_repository_immutable_tags", "in_place", "repositories",
     ["UpdateRepositoryImmutableTags", "~GetRepository"], [],
     "A repository's immutable tags setting (enabled + rules). Read from the repository's immutable_tags_settings; delete PATCHes it off. Repository admin only (the spec).", {}),
    ("hub:repository-immutable-tags-verify", "data_source", "docker_hub_repository_immutable_tags_verify", None, "repositories",
     ["VerifyRepositoryImmutableTags"], [],
     "Validate an immutable-tags regex and list the repository's tags it matches. A POST that changes nothing (the spec: validates and returns), so a data source.", {}),
    ("hub:repository-group", "resource", "docker_hub_repository_group", "replace", "repositories", ["CreateRepositoryGroup"], ORG_UNKNOWN,
     "An organization group's permission on a repository. The spec has only the assign route: no read and no delete (finding P7-F5).", {}),
    ("hub:org-members", "data_source", "docker_hub_org_members", None, "orgs", ["getV2OrgsByOrgNameMembers"], ORG_UNKNOWN,
     "The members of an organization, filtered by search, type and role.", {"plural": True}),
    ("hub:org-members-export", "data_source", "docker_hub_org_members_export", None, "orgs", ["getV2OrgsByOrgNameMembersExport"], ORG_UNKNOWN,
     "The organization's members as the CSV the Hub exports (raw text only).", {}),
    ("hub:org-member", "resource", "docker_hub_org_member", "in_place", "orgs",
     ["putV2OrgsByOrgNameMembersByUsername", "deleteV2OrgsByOrgNameMembersByUsername", "~getV2OrgsByOrgNameMembers"], ORG_UNKNOWN,
     "An existing member's role in an organization. Members join through invites (docker_hub_invite), so create adopts the member and PUTs the role; delete removes the member from the organization.", {}),
    ("hub:org-invites", "data_source", "docker_hub_org_invites", None, "invites", ["getV2OrgsByOrgNameInvites"], ORG_UNKNOWN,
     "The pending invites of an organization.", {"plural": True}),
    ("hub:invite", "resource", "docker_hub_invite", "replace", "invites",
     ["postV2InvitesBulk", "deleteV2InvitesById", "~getV2OrgsByOrgNameInvites"], ORG_UNKNOWN,
     "One invite of a Docker ID or email to an organization (and optionally a group, with a role), sent through the bulk route with one invitee; delete cancels it. Read finds it in the pending list.", {}),
    ("hub:invite-resend", "action", "docker_hub_invite_resend", None, "invites", ["patchV2InvitesByIdResend"], ORG_UNKNOWN,
     "Resend a pending invite.", {}),
    ("hub:group", "resource", "docker_hub_group", "in_place", "groups",
     ["postV2OrgsByOrgNameGroups", "getV2OrgsByOrgNameGroupsByGroupName", "putV2OrgsByOrgNameGroupsByGroupName",
      "patchV2OrgsByOrgNameGroupsByGroupName", "deleteV2OrgsByOrgNameGroupsByGroupName"], ORG_UNKNOWN,
     "A group of an organization (the spec's name; the Hub UI says team). Name and description update in place.", {}),
    ("hub:groups", "data_source", "docker_hub_groups", None, "groups", ["getV2OrgsByOrgNameGroups"], ORG_UNKNOWN,
     "The groups of an organization, filtered by username or search.", {"plural": True}),
    ("hub:group-data", "data_source", "docker_hub_group", None, "groups", ["~getV2OrgsByOrgNameGroupsByGroupName"], ORG_UNKNOWN,
     "One group of an organization.", {}),
    ("hub:group-member", "resource", "docker_hub_group_member", "replace", "groups",
     ["postV2OrgsByOrgNameGroupsByGroupNameMembers", "deleteV2OrgsByOrgNameGroupsByGroupNameMembersByUsername",
      "~getV2OrgsByOrgNameGroupsByGroupNameMembers"], ORG_UNKNOWN,
     "A user's membership in a group. Read finds the member in the group's member list.", {}),
    ("hub:group-members", "data_source", "docker_hub_group_members", None, "groups", ["getV2OrgsByOrgNameGroupsByGroupNameMembers"], ORG_UNKNOWN,
     "The members of a group.", {"plural": True}),
    ("hub:scim-service-provider-config", "data_source", "docker_hub_scim_service_provider_config", None, "scim",
     ["getV2Scim20ServiceProviderConfig"], ORG_UNKNOWN, "The SCIM service provider configuration (SCIM token auth).", {}),
    ("hub:scim-resource-types", "data_source", "docker_hub_scim_resource_types", None, "scim", ["getV2Scim20ResourceTypes"], ORG_UNKNOWN,
     "The SCIM resource types.", {"plural": True}),
    ("hub:scim-resource-type", "data_source", "docker_hub_scim_resource_type", None, "scim", ["getV2Scim20ResourceTypesByName"], ORG_UNKNOWN,
     "One SCIM resource type by name.", {}),
    ("hub:scim-schemas", "data_source", "docker_hub_scim_schemas", None, "scim", ["getV2Scim20Schemas"], ORG_UNKNOWN,
     "The SCIM schemas.", {"plural": True}),
    ("hub:scim-schema", "data_source", "docker_hub_scim_schema", None, "scim", ["getV2Scim20SchemasById"], ORG_UNKNOWN,
     "One SCIM schema by ID.", {}),
    ("hub:scim-users", "data_source", "docker_hub_scim_users", None, "scim", ["getV2Scim20Users"], ORG_UNKNOWN,
     "The SCIM users of the organization (filter, sort, startIndex/count).", {"plural": True}),
    ("hub:scim-user", "resource", "docker_hub_scim_user", "in_place", "scim",
     ["postV2Scim20Users", "getV2Scim20UsersById", "putV2Scim20UsersById"], ORG_UNKNOWN,
     "A SCIM-provisioned user. The spec has no delete: destroy PUTs active=false (deactivate), the spec's documented way to remove access.", {}),
]

PATH_INPUTS = {"namespace", "repository", "org_name", "group_name", "username", "tag"}


def resolve(s, depth=0):
    while isinstance(s, dict) and "$ref" in s and depth < 30:
        n = SPEC
        for part in s["$ref"].lstrip("#/").split("/"):
            n = n[part.replace("~1", "/").replace("~0", "~")]
        s, depth = n, depth + 1
    if isinstance(s, dict) and "allOf" in s:
        m = {"type": "object", "properties": {}, "required": []}
        for sub in s["allOf"]:
            r = resolve(sub, depth + 1)
            m["properties"].update(r.get("properties", {}))
            m["required"] += r.get("required", [])
        return m
    return s or {}


def ref_ptr(s, fallback):
    return s["$ref"] if isinstance(s, dict) and "$ref" in s else fallback


def esc(p):
    return p.replace("~", "~0").replace("/", "~1")


def snake(name):
    s = re.sub(r"[^A-Za-z0-9]+", "_", name)
    s = re.sub(r"([a-z0-9])([A-Z])", r"\1_\2", s)
    s = re.sub(r"([A-Z]+)([A-Z][a-z])", r"\1_\2", s)
    return s.strip("_").lower()


def scalar(s):
    t = s.get("type")
    if isinstance(t, list):
        t = next((x for x in t if x != "null"), "string")
    return {"integer": "number", "number": "number", "boolean": "bool", "string": "string"}.get(t)


def fields_of(s, ptr, depth, complete, secret=False):
    """FIELD list for an object schema; complete[0] goes False when a level is cut off."""
    s = resolve(s)
    out = []
    for prop, sub in (s.get("properties") or {}).items():
        sp = ref_ptr(sub, ptr + "/properties/" + esc(prop))
        r = resolve(sub)
        f = {"name": snake(prop), "upstream": prop, "pointer": sp}
        sc = scalar(r)
        t = r.get("type")
        if isinstance(t, list):
            t = next((x for x in t if x != "null"), None)
        if sc and t != "object" and t != "array":
            f["tf_type"] = sc
        elif t == "array" or "items" in r:
            it = resolve(r.get("items") or {})
            isc = scalar(it)
            if it.get("properties") and depth < 3:
                f["tf_type"] = "list(object({...}))"
                f["fields"] = fields_of(r["items"], ref_ptr(r.get("items"), sp + "/items"), depth + 1, complete)
            elif isc:
                f["tf_type"] = "list(%s)" % isc
            else:
                f["tf_type"] = "list(string)"
                complete[0] = complete[0] and not it.get("properties")
        elif r.get("properties") and depth < 3:
            f["tf_type"] = "object({...})"
            f["fields"] = fields_of(sub, sp, depth + 1, complete)
        elif r.get("additionalProperties") not in (None, False):
            ap = resolve(r["additionalProperties"]) if isinstance(r["additionalProperties"], dict) else {}
            f["tf_type"] = "map(%s)" % (scalar(ap) or "string")
        else:
            f["tf_type"] = "dynamic" if depth == 0 else "string"
            if r.get("properties"):
                complete[0] = False
        if prop in SECRET_PROPS and not secret:
            f["sensitive"] = True
        out.append(f)
    return out


def response(opid):
    """(schema, pointer, media type) of the first 2xx response with a body."""
    m, p, op, _ = OPS[opid]
    for code, r in sorted((op.get("responses") or {}).items()):
        if not str(code).startswith("2"):
            continue
        rptr = ref_ptr(r, "#/paths/%s/%s/responses/%s" % (esc(p), m.lower(), code))
        r = resolve(r)
        for ct, media in (r.get("content") or {}).items():
            if media.get("schema") is not None:
                return media["schema"], ref_ptr(media["schema"], rptr + "/content/" + esc(ct) + "/schema"), ct
    return None, None, None


def params(opid):
    m, p, op, shared = OPS[opid]
    out = []
    for prm in list(shared) + list(op.get("parameters") or []):
        ptr = prm.get("$ref") or "#/paths/%s/%s/parameters/%s" % (esc(p), m.lower(), prm.get("name"))
        r = resolve(prm)
        out.append((r.get("in"), r.get("name"), ptr, r))
    return out


def writable(opid):
    """Writable leaf fields of a write operation: request body leaves (readOnly skipped) and path/query inputs."""
    m, p, op, _ = OPS[opid]
    out = []
    for where, name, ptr, r in params(opid):
        if where == "query" or (where == "path" and name in PATH_INPUTS):
            out.append({"pointer": ptr, "upstream": name, "attr": snake(name), "in": where, "op": opid})
    rb = resolve(op.get("requestBody") or {})
    for ct, media in (rb.get("content") or {}).items():
        base = ref_ptr(media.get("schema"), "#/paths/%s/%s/requestBody/content/%s/schema" % (esc(p), m.lower(), esc(ct)))

        def walk(s, ptr, prefix, attr_prefix):
            s = resolve(s)
            if s.get("type") == "array" and "items" in s:
                walk(s["items"], ref_ptr(s["items"], ptr + "/items"), prefix + "[]", attr_prefix)
                return
            props = s.get("properties") or {}
            if not props:
                out.append({"pointer": ptr, "upstream": prefix or "(body)", "attr": attr_prefix.rstrip(".") or "body", "in": "body", "op": opid})
                return
            for prop, sub in props.items():
                r = resolve(sub)
                if r.get("readOnly"):
                    continue
                sp = ref_ptr(sub, ptr + "/properties/" + esc(prop))
                if r.get("properties") or (r.get("type") == "array" and resolve(r.get("items") or {}).get("properties")):
                    walk(sub, sp, (prefix + "." if prefix else "") + prop, attr_prefix + snake(prop) + ".")
                else:
                    out.append({"pointer": sp, "upstream": (prefix + "." if prefix else "") + prop,
                                "attr": attr_prefix + snake(prop), "in": "body", "op": opid})
        walk(media.get("schema"), base, "", "")
        break
    return out


def outputs_for(iid, kind, name, ops, extra):
    prim = [o for o in ops if not o.startswith("~")]
    allops = [o.lstrip("~") for o in ops]
    reads = [o for o in allops if OPS[o][0] == "GET"] or [o for o in allops if OPS[o][0] in ("POST", "PUT", "PATCH")]
    sch = ptr = ct = None
    for o in reads + allops:
        sch, ptr, ct = response(o)
        if sch is not None:
            break
    src = lambda p: {"type": "openapi", "file": SPEC_REL, "pointer": p}
    kaa = kind != "data_source"
    stem = name[len("docker_hub_"):]
    outs, entry = [], {"uses_cli": False}
    if kind == "action":
        s = resolve(sch) if sch is not None else {}
        flds = fields_of(sch, ptr, 0, [True]) if s.get("properties") else []
        res = {"name": "result", "tf_type": "object({...})", "category": "result", "known_after_apply": True, "sensitive": False,
               "source": src(ptr) if ptr else {"type": "openapi", "file": SPEC_REL, "pointer": "#/paths/%s/%s/responses" % (esc(OPS[allops[0]][1]), OPS[allops[0]][0].lower())},
               "description": "The action's outcome: the HTTP status the Hub answered (the resend route returns 204 with no body) and the invite ID.",
               "fields": flds or [{"name": "id", "upstream": "id", "tf_type": "string", "pointer": params(allops[0])[0][2] if params(allops[0]) else "#/paths"},
                                  {"name": "status_code", "upstream": "(HTTP status)", "tf_type": "number", "pointer": "#/paths/%s/patch/responses/204" % esc(OPS[allops[0]][1])}]}
        outs = [res, {"name": "id", "tf_type": "string", "category": "identity", "known_after_apply": False, "sensitive": False,
                      "source": src("#/paths/%s/patch/parameters" % esc(OPS[allops[0]][1])), "description": "The invite ID acted on."}, dict(V_HUB)]
        entry["outputs"] = outs
        return entry
    if kind == "ephemeral_resource":
        s = resolve(sch)
        flds = fields_of(sch, ptr, 0, [True], secret=True)
        outs = [{"name": "identifier", "tf_type": "string", "category": "identity", "known_after_apply": False, "sensitive": False,
                 "source": src("#/paths/%s/post/requestBody" % esc(OPS[allops[0]][1])),
                 "description": "The username (or OAT organization identifier) the token was issued for."},
                {"name": "access_token", "tf_type": "string", "category": "identity", "known_after_apply": True, "sensitive": True,
                 "source": src(ptr + "/properties/access_token" if "$ref" not in str(ptr) else ptr + "/properties/access_token"),
                 "description": "The bearer JWT. Sensitive; ephemeral, so never in plan or state."},
                dict(V_HUB)]
        entry["outputs"] = outs
        entry["no_raw_reason"] = "The response is one secret value (access_token); a raw copy would only duplicate it."
        entry["no_structured_reason"] = "The response has the single field access_token, exposed as its own output."
        return entry
    if ct and "csv" in ct:
        outs = [{"name": "members_csv", "tf_type": "string", "category": "raw", "known_after_apply": False, "sensitive": False,
                 "source": src(ptr), "description": "The CSV as the Hub returns it (Name, Username, Email, Type, Role, Teams, Date Joined, ...)."},
                {"name": "content_sha256", "tf_type": "string", "category": "hash", "known_after_apply": False, "sensitive": False,
                 "source": {"type": "derived", "from": "members_csv", "how": "sha256 of the UTF-8 bytes"}, "hash_of": "members_csv"},
                {"name": "org_name", "tf_type": "string", "category": "identity", "known_after_apply": False, "sensitive": False,
                 "source": src([p for w, n, p, _ in params(allops[0]) if n == "org_name"][0])}, dict(V_HUB)]
        entry["outputs"] = outs
        entry["no_structured_reason"] = "The response is text/csv, not a JSON schema; docker_hub_org_members has the same members structured."
        return entry
    complete = [True]
    plural = extra.get("plural")
    s = resolve(sch) if sch is not None else {}
    list_key = None
    if plural:
        props = s.get("properties") or {}
        list_key = next((k for k in ("results", "data", "Resources", "actions", "logs") if k in props), None)
        if list_key is None:
            list_key = next((k for k, v in props.items() if resolve(v).get("type") == "array"), None)
    struct_name = stem.replace("-", "_")
    raw_name = struct_name + "_json"
    if plural and list_key:
        item_s = resolve(s["properties"][list_key]).get("items") or {}
        iptr = ref_ptr(item_s, ptr + "/properties/%s/items" % list_key)
        flds = fields_of(item_s, iptr, 0, complete)
        structured = {"name": struct_name, "tf_type": "list(object({...}))", "category": "structured", "known_after_apply": kaa,
                      "sensitive": False, "source": src(iptr), "fields": flds, "fields_complete": complete[0],
                      "description": "Every entry of the response's `%s` list, all pages read." % list_key}
        names = [f for f in flds if f["upstream"] in ("name", "username", "uuid", "id")]
        ident = []
        for f in names[:1]:
            nm = {"name": "names", "username": "usernames", "uuid": "ids", "id": "ids"}[f["upstream"]]
            ident.append({"name": nm, "tf_type": "list(string)", "category": "identity", "known_after_apply": kaa, "sensitive": False,
                          "source": src(f["pointer"]), "description": "The `%s` of every entry (DECISIONS.md Q36)." % f["upstream"]})
    else:
        flds = fields_of(sch, ptr, 0, complete) if s.get("properties") else []
        if not flds:
            flds = [{"name": "value", "upstream": "(body)", "tf_type": "dynamic", "pointer": ptr or "#"}]
            complete[0] = False
        structured = {"name": struct_name, "tf_type": "object({...})", "category": "structured", "known_after_apply": kaa,
                      "sensitive": False, "source": src(ptr), "fields": flds, "fields_complete": complete[0]}
        ident = []
        for up in ("uuid", "id", "name", "namespace", "username"):
            f = next((f for f in flds if f["upstream"] == up), None)
            if f and not any(x["name"] == ("id" if up == "uuid" else f["name"]) for x in ident):
                ident.append({"name": "id" if up == "uuid" else f["name"], "tf_type": "string", "category": "identity",
                              "known_after_apply": kaa, "sensitive": False, "source": src(f["pointer"])})
        if not ident and not any(w == "path" for o in allops for w, _, _, _ in params(o)):
            m0, p0 = OPS[allops[0]][0], OPS[allops[0]][1]
            ident.append({"name": "id", "tf_type": "string", "category": "identity", "known_after_apply": False, "sensitive": False,
                          "source": {"type": "derived", "from": raw_name, "how": "the route itself, %s (one object per SCIM token's organization)" % p0},
                          "description": "The SCIM route read; the object has no ID of its own."})
        if not ident:
            path_ids =[(n, p) for o in allops for w, n, p, _ in params(o) if w == "path"]
            seen = set()
            for n, p in path_ids:
                if snake(n) not in seen:
                    seen.add(snake(n))
                    ident.append({"name": snake(n), "tf_type": "string", "category": "identity", "known_after_apply": False,
                                  "sensitive": False, "source": src(p)})
    raw_sensitive = any(f.get("sensitive") for f in walk_fields(structured["fields"]))
    raw = {"name": raw_name, "tf_type": "string", "category": "raw", "known_after_apply": kaa, "sensitive": raw_sensitive,
           "source": src(ptr), "serializes": struct_name,
           "description": "The Hub's JSON response body%s." % (" (every page, the lists concatenated)" if plural else "")}
    outs = [raw, structured,
            {"name": "content_sha256", "tf_type": "string", "category": "hash", "known_after_apply": kaa, "sensitive": False,
             "source": {"type": "derived", "from": raw_name, "how": "sha256 of the UTF-8 bytes"}, "hash_of": raw_name}] + ident + [dict(V_HUB)]
    if extra.get("secret_output_path"):
        outs.append({"name": "token", "tf_type": "string", "category": "identity", "known_after_apply": True, "sensitive": True,
                     "source": src(response(allops[0])[1] + "/properties/token"),
                     "description": "The token value, returned once by create and kept in state (sensitive). With secret_output_path set, also written to that file."})
        entry["inputs"] = [{"name": "secret_output_path", "tf_type": "string", "default": None,
                            "upstream": "(provider)", "source": {"type": "derived", "from": "token", "how": "written once after create, like an action's result_path (CONTRACT.md Action results): UTF-8, mode 0600, the value and a trailing newline"},
                            "description": "Optional local file the token value is written to after create (it cannot be read back from the Hub)."}]
    if iid == "hub:audit-logs":
        entry["inputs"] = [{"name": "page_size", "tf_type": "number", "default": 100, "upstream": "page_size",
                            "source": src("#/components/parameters/page_size"), "description": "Events per page."},
                           {"name": "max_events", "tf_type": "number", "default": 1000, "upstream": "(provider)",
                            "source": {"type": "derived", "from": "audit_logs_json", "how": "stop paging after this many events"},
                            "description": "Upper bound on events kept in state (the logs bound of rule L1, applied to audit events)."}]
    entry["outputs"] = outs
    return entry


def walk_fields(fl):
    for f in fl:
        yield f
        yield from walk_fields(f.get("fields") or [])


def main():
    used, items, outputs, tags_cov = {}, [], {}, {}
    for iid, kind, name, update, tag, ops, requires, summary, extra in ITEMS:
        srcs = []
        for o in ops:
            primary = not o.startswith("~")
            o = o.lstrip("~")
            if o not in OPS:
                sys.exit("unknown operationId %s in %s" % (o, iid))
            m, p, op, _ = OPS[o]
            if tag not in op.get("tags", []):
                sys.exit("%s: %s is tagged %s, not %s" % (iid, o, op.get("tags"), tag))
            s = {"type": "openapi", "spec": "hub", "file": SPEC_REL, "operationId": o, "method": m, "path": p}
            if primary:
                s["primary"] = True
                used.setdefault(o, []).append(iid)
            srcs.append(s)
        it = {"id": iid, "layer": "hub", "kind": kind, "name": name, "summary": summary, "sources": srcs, "min_api": None,
              "hub_tag": tag}
        if kind != "skip":
            it["planned"] = "pass7"
            it["requires"] = requires
        if update:
            it["update"] = update
        if extra.get("plural"):
            it["plural"] = True
        if extra.get("skip_reason"):
            it["skip_reason"] = extra["skip_reason"]
        if kind in ("resource", "action", "ephemeral_resource"):
            wf = []
            for o in ops:
                if o.startswith("~") or OPS[o][0] not in ("POST", "PUT", "PATCH"):
                    continue
                wf += writable(o)
            seen, dedup = set(), []
            for f in wf:
                if f["pointer"] not in seen:
                    seen.add(f["pointer"])
                    dedup.append(f)
            it["writable_fields"] = dedup
        items.append(it)
        tags_cov.setdefault(tag, []).append(iid)
        if kind != "skip":
            outputs[iid] = outputs_for(iid, kind, name, ops, extra)
    errs = []
    for o in OPS:
        if len(used.get(o, [])) != 1:
            errs.append("operation %s has %d primary homes %s" % (o, len(used.get(o, [])), used.get(o, [])))
    spec_tags = [t["name"] for t in SPEC.get("tags", [])]
    for t in spec_tags:
        if t not in tags_cov and t not in INFO_TAGS:
            errs.append("tag %s is not covered" % t)
    if errs:
        sys.exit("\n".join(errs))
    frag = {
        "lane": "P7-0",
        "notes": [
            "Pass 7 Lane 0: every operation of the Docker Hub API spec (SOURCES.md pin hub_api, info.version 2-beta) has exactly one primary home; every spec tag is covered (hub_tag on the items, info_tags below). Names come only from the spec (DECISIONS.md).",
            "planned: pass7 marks an item the Pass 7 build lanes implement: merge.py computes status `planned` while it is unregistered, registrygen and contractgen leave it out, validate.py S1 lists it without failing. The lane that registers the type removes the key (merge.py fails on a registered item that still says planned).",
            "requires: org = the route is org-scoped; plan=<name> = a subscription the spec names; plan=unknown = a gated feature whose plan the spec does not state. No entry = reachable on a Personal plan as far as the spec says (the live probe, reference/_gen/pass7/hub-probe.md, can narrow it).",
            "Provider block (Lane H, no inventory item): hub_username and hub_token, falling back to DOCKER_HUB_USERNAME and DOCKER_HUB_TOKEN; hub_token is sensitive. Every hub type exchanges them through POST /v2/auth/token (the docker_hub_token exchange) and caches the JWT per provider instance.",
            "No owner label on Hub objects: no Hub object has a label field (CONTRACT.md Owner label, Excluded types).",
            "Groups, not teams: the spec's tag and paths say group (the summaries add '(Team)'); every name uses group.",
            "Docker Hardened Images and Docker Scout: the pinned spec has no tag or operation for either, so there is no item (DECISIONS.md: the spec is the only naming source). Re-harvest when the spec adds them (DriftCheck hub source).",
        ],
        "info_tags": INFO_TAGS,
        "naming_questions": [
            "P7-Q1 docker_hub_repository and docker_hub_repository_group: the spec has no delete (and no update) route. Keep them as resources whose destroy only forgets (current), make create an action (docker_hub_repository_create), or defer both until the spec adds the routes?",
            "P7-Q2 docker_hub_access_token: a JWT issued from a PAT is refused by /v2/access-tokens (403 'token issued from personal access token is not allowed', live probe). Managing PATs needs a password-issued JWT. Add a write-only hub_password provider argument for it, or ship the type documented as password-auth only?",
        ],
        "items": items,
    }
    out_frag = {"lane": "hub", "notes": ["Pass 7 Lane 0 outputs for the docker_hub_* items, derived from the pinned Hub spec's response schemas by reference/_gen/generators/pass7/p7_hub.py."], "items": outputs}
    for rel, obj in (("fragments/hub.json", frag), ("fragments/outputs/hub.json", out_frag)):
        with open(os.path.join(ROOT, "reference", "_gen", rel), "w", encoding="utf-8", newline="\n") as f:
            json.dump(obj, f, indent=2, ensure_ascii=False)
            f.write("\n")
    kinds = {}
    for it in items:
        k = it["kind"] + ("(plural)" if it.get("plural") else "")
        kinds[k] = kinds.get(k, 0) + 1
    print("items", len(items), kinds, "ops", len(OPS), "gated", sum(1 for it in items if it.get("requires")))


if __name__ == "__main__":
    main()
