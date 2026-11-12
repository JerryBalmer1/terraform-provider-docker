# Releasing terraform-provider-docker

Releases are built by [GoReleaser](https://goreleaser.com) in `.github/workflows/release.yml`
when a `v*` tag is pushed. The workflow publishes a GitHub release on
`JerryBalmer1/terraform-provider-docker` that the Terraform Registry ingests as
`JerryBalmer1/docker`.

Release assets, for version `X.Y.Z`. These four kinds and nothing else: the Terraform Registry rejects a
release carrying any other asset ("Some release assets are invalid ... invalid file extension found: json"
stopped v0.1.0 until the cosign bundle was deleted from the release by hand).

| Asset | What it is |
|---|---|
| `terraform-provider-docker_X.Y.Z_<os>_<arch>.zip` | one per target: darwin, linux and windows on amd64 and arm64, plus linux arm and linux 386 |
| `terraform-provider-docker_X.Y.Z_manifest.json` | `terraform-registry-manifest.json` (plugin protocol 6.0) |
| `terraform-provider-docker_X.Y.Z_SHA256SUMS` | SHA-256 of every zip and the manifest |
| `terraform-provider-docker_X.Y.Z_SHA256SUMS.sig` | detached GPG signature of the SHA256SUMS (what the registry checks) |

Only GoReleaser uploads release assets. The `sign` job's output stays off the release:

- the cosign keyless signature bundle of the SHA256SUMS (GitHub OIDC),
  `terraform-provider-docker_X.Y.Z_SHA256SUMS.sigstore.json`, is a **workflow artifact** named
  `cosign-bundle-vX.Y.Z` on the release workflow run (kept 90 days), and the signature is also in the
  public Sigstore transparency log (Rekor);
- SLSA build provenance for the zips, the SHA256SUMS and the manifest is stored as
  GitHub attestations on the repository (`gh attestation verify`).

The target matrix lives in `.goreleaser.yml` and in `$ReleaseTargets` in
`build/release.tasks.ps1`; `Invoke-Build ReleaseCheck` fails when they disagree
or when a target does not cross-compile. Its asset-name gate (`tools/releaseassets`) fails when any
step of `release.yml` uploads a file other than the four kinds above to the release (`gh release upload`,
the upload-asset API, `softprops/action-gh-release` and similar), and, against a GoReleaser snapshot in
`dist/` (`Invoke-Build Release` runs the same check), when the files a real release would upload are not
exactly one zip per target, the manifest, the SHA256SUMS and its `.sig`.

## One-time setup

All commands run from the repository root. They need `gpg` (Gpg4win on Windows)
and an authenticated `gh` (`gh auth login`).

### 1. GPG signing key

Generate an RSA 4096 key without an expiry, protected by a passphrase. Interactively:

```sh
gpg --full-generate-key
# kind: (1) RSA and RSA; keysize: 4096; expires: 0; real name / email of the
# signing identity; then a passphrase.
```

or in batch mode (replace the name, email and passphrase):

```sh
cat > gpg-key.params <<'EOF'
Key-Type: RSA
Key-Length: 4096
Subkey-Type: RSA
Subkey-Length: 4096
Name-Real: JerryBalmer1 terraform-provider-docker releases
Name-Email: you@example.com
Expire-Date: 0
Passphrase: CHANGE-ME
%commit
EOF
gpg --batch --generate-key gpg-key.params
rm gpg-key.params
```

Find the fingerprint and export both halves, ASCII-armored:

```sh
gpg --list-secret-keys --keyid-format LONG you@example.com
export FPR=<40-character fingerprint from the line under "sec">
gpg --armor --export-secret-keys "$FPR" > gpg-private.asc
gpg --armor --export "$FPR" > gpg-public.asc
```

### 2. Repository secrets

```sh
gh secret set GPG_PRIVATE_KEY -R JerryBalmer1/terraform-provider-docker < gpg-private.asc
gh secret set PASSPHRASE -R JerryBalmer1/terraform-provider-docker
# paste the passphrase at the prompt
```

Then remove the private key export and keep the key itself backed up offline:

```sh
shred -u gpg-private.asc   # Windows (PowerShell): Remove-Item gpg-private.asc
```

`GITHUB_TOKEN` is provided by Actions; the workflow sets its own per-job permissions.

### 3. Make the repository public

The Terraform Registry only publishes from public repositories.

```sh
gh repo edit JerryBalmer1/terraform-provider-docker --visibility public --accept-visibility-change-consequences
```

### 4. Terraform Registry

1. Sign in to <https://registry.terraform.io> with GitHub (as `JerryBalmer1`).
2. User Settings > Signing Keys: add the GPG public key (paste the contents of `gpg-public.asc`).
3. Publish > Provider: choose `JerryBalmer1/terraform-provider-docker`. The registry installs a
   webhook and picks up every later release by itself.

The repository name must stay `terraform-provider-docker`, and every release must
carry the SHA256SUMS, its `.sig` and the manifest asset; GoReleaser produces all three.

## Per release

1. Run the pre-release gates on a clean tree at the commit to tag:

   ```sh
   pwsh -NoProfile -c "Invoke-Build PreRelease"
   ```

   Optionally build a local snapshot (needs GoReleaser v2:
   `go install github.com/goreleaser/goreleaser/v2@latest`); it never signs or publishes:

   ```sh
   pwsh -NoProfile -c "Invoke-Build ReleaseCheck, Release"
   ```

2. Update `CHANGELOG.md`: move the unreleased entries under a `## X.Y.Z (<date>)` heading,
   and commit that on the branch being released.

3. Tag and push (a signed, annotated tag):

   ```sh
   git tag -s vX.Y.Z -m "vX.Y.Z"
   git push origin vX.Y.Z
   ```

4. Watch the release workflow:

   ```sh
   gh run list -R JerryBalmer1/terraform-provider-docker --workflow release.yml --limit 1
   gh run watch -R JerryBalmer1/terraform-provider-docker <run-id> --exit-status
   ```

5. Verify the release assets (exactly 8 zips, manifest, SHA256SUMS, `.sig`; nothing else):

   ```sh
   gh release view vX.Y.Z -R JerryBalmer1/terraform-provider-docker
   mkdir rel && cd rel
   gh release download vX.Y.Z -R JerryBalmer1/terraform-provider-docker
   sha256sum --check terraform-provider-docker_X.Y.Z_SHA256SUMS
   gpg --verify terraform-provider-docker_X.Y.Z_SHA256SUMS.sig terraform-provider-docker_X.Y.Z_SHA256SUMS
   ```

6. Verify the cosign bundle (keyless, signed by the release workflow on the tag). Download it from the
   release workflow run (the `cosign-bundle-vX.Y.Z` artifact, kept 90 days; keep a copy with the release
   records if you need it longer), into the folder that holds the SHA256SUMS:

   ```sh
   gh run download <run-id> -R JerryBalmer1/terraform-provider-docker -n cosign-bundle-vX.Y.Z
   cosign verify-blob \
     --bundle terraform-provider-docker_X.Y.Z_SHA256SUMS.sigstore.json \
     --certificate-identity-regexp '^https://github\.com/JerryBalmer1/terraform-provider-docker/\.github/workflows/release\.yml@refs/tags/v' \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com \
     terraform-provider-docker_X.Y.Z_SHA256SUMS
   ```

7. Verify the build provenance of each zip (and the SHA256SUMS / manifest the same way):

   ```sh
   gh attestation verify terraform-provider-docker_X.Y.Z_linux_amd64.zip -R JerryBalmer1/terraform-provider-docker
   for f in terraform-provider-docker_X.Y.Z_*.zip; do gh attestation verify "$f" -R JerryBalmer1/terraform-provider-docker; done
   ```

8. Check the registry picked up the version (usually within minutes of the release):

   ```sh
   curl -s https://registry.terraform.io/v1/providers/JerryBalmer1/docker/versions | grep -o '"version":"X.Y.Z"'
   ```

   or open <https://registry.terraform.io/providers/JerryBalmer1/docker/latest>. If it does not
   appear, use Resync on the provider's registry settings page and check the webhook deliveries
   under the repository's Settings > Webhooks.

9. Final step, once the version is public: the registry smoke test. It reads the version from the top
   released heading of `CHANGELOG.md`, writes a `main.tf` requiring `jerrybalmer1/docker` at exactly
   that version under `test/harness/.work/registry-smoke/X.Y.Z`, runs `terraform init` from the public
   registry (with an empty CLI config, so no local mirror or override stands in), and fails unless the
   provider installed and was reported signed by key ID `A18B1B39E1DFDD4C`. It is not in PreRelease,
   which runs before the version exists.

   ```sh
   pwsh -NoProfile -c "Invoke-Build RegistrySmoke"
   # RegistrySmoke: Installed jerrybalmer1/docker vX.Y.Z (self-signed, key ID A18B1B39E1DFDD4C)
   ```

## If a release fails

- GoReleaser failed before publishing: fix the cause on the branch, delete the tag
  (`git push --delete origin vX.Y.Z; git tag -d vX.Y.Z`) and tag again.
- The `sign` job failed after the release was published: re-run it with
  `gh run rerun <run-id> --failed -R JerryBalmer1/terraform-provider-docker` (it re-downloads the
  assets, signs again and stores a new bundle artifact; it never touches the release assets).
- The registry rejects the release for an invalid asset: delete that asset from the GitHub release
  (`gh release delete-asset vX.Y.Z <file> -R JerryBalmer1/terraform-provider-docker`), Resync on the
  registry, and fix whatever uploaded it; `Invoke-Build ReleaseCheck` must catch it next time.
- Never replace assets of a version the registry already ingested; release a new patch version instead.
