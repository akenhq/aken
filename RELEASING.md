# Release Aken

To release, push a version tag. The
[release workflow](.github/workflows/release.yml) builds and signs the
binaries, publishes the GitHub release, and stages the npm packages. You then
approve the npm packages and verify the result. The version comes from the
tag, so there is no version number to edit in the source.

This guide is for maintainers. You need:

- push access to `akenhq/aken`;
- maintainer access, with two-factor authentication, to `aken-mcp` and the
  three `@akenhq/mcp-*` packages on npm;
- the GitHub CLI (`gh`), [cosign](https://github.com/sigstore/cosign/releases),
  and npm 11.15.0 or later, which has the `npm stage` commands.

## What a release contains

The workflow publishes 26 assets to the GitHub release and four npm packages:

- Nine binaries: `aken`, `aken-mcp`, and `aken-relay`, each for linux/amd64,
  linux/arm64, and darwin/arm64. Each binary has an SPDX SBOM and a GitHub
  build provenance attestation.
- `install.sh`, `run.sh`, and `install-mcp.sh`, rendered with the tag and the
  SHA-256 of every binary. Each script has its own Sigstore bundle.
- `SHA256SUMS`, which lists the binaries, the SBOMs, and the scripts, and
  `SHA256SUMS.sigstore.json`.
- The npm launcher `aken-mcp` and the platform packages
  `@akenhq/mcp-linux-x64`, `@akenhq/mcp-linux-arm64`, and
  `@akenhq/mcp-darwin-arm64`. Their version is the tag without the leading `v`.

`aken.dev/install.sh`, `aken.dev/run.sh`, and `aken.dev/install-mcp.sh`
redirect to the assets of the latest GitHub release. The website needs no
change for a release.

## Choose the version

Tags have the form `v<major>.<minor>.<patch>`, for example `v0.4.0`. Before
1.0, raise the minor version for features and protocol changes, and the patch
version for fixes.

For a prerelease, add a hyphen and a suffix, for example `v0.4.0-rc.1`. The
workflow marks the GitHub release as a prerelease and stages the npm packages
under the `next` dist-tag instead of `latest`. GitHub's latest release skips
prereleases, so the `aken.dev` installers keep installing the last full
release.

The suffix can contain letters, digits, dots, and hyphens.
`packaging/npm.sh` rejects any other tag format, and the workflow then fails
before it publishes anything.

Do not move or reuse a tag after you push it. The signatures name the tag, and
the Go module proxy keeps the first commit that it sees for a version. If a
tag is wrong, release the next patch version.

## Before you tag

1. Merge every change for the release into `main`.
2. Confirm that CI passed on the commit you plan to tag:

   ```sh
   gh run list --workflow ci.yml --branch main --limit 1
   ```

3. Check that the status paragraph in [README.md](README.md) and the pages in
   `docs/` describe what this release does.
4. Optional: run `make check` locally.

## Tag and push

Replace `<tag>` with the version you chose:

```sh
git switch main
git pull --ff-only
git tag <tag>
git push origin <tag>
```

The push starts the workflow. To follow the run, list it and then watch it by
its ID:

```sh
gh run list --workflow release.yml --limit 1
gh run watch <run-id>
```

The workflow runs these steps in order. When a run fails, the step tells you
what is already public.

1. Build every target twice and compare the hashes (`make repro`), then build
   the release binaries (`make release`).
2. Write an SBOM for each binary.
3. Write `SHA256SUMS`, render the three scripts, and build the npm packages.
   `make npm` checks each `aken-mcp` binary against `SHA256SUMS`.
4. Sign the three scripts, add them to `SHA256SUMS`, and sign `SHA256SUMS`.
5. Attest build provenance for the binaries.
6. Publish the GitHub release. The notes are
   [.github/RELEASE_NOTES_HEADER.md](.github/RELEASE_NOTES_HEADER.md) followed
   by GitHub's generated notes.
7. Stage the three platform packages and then `aken-mcp` on npm. The workflow
   authenticates through trusted publishing (OIDC). The repository stores no
   npm token.

## Approve the npm packages

A staged version cannot be installed until a maintainer approves it. Approve
the three platform packages before `aken-mcp`, because `aken-mcp` depends on
them at the exact version.

1. List the staged versions:

   ```sh
   npm stage list
   ```

2. Optional: inspect a package before you approve it. `bin/aken-mcp` in a
   platform package must match the `aken-mcp_<os>_<arch>` line in the
   release's `SHA256SUMS`.

   ```sh
   npm stage view <stage-id>
   npm stage download <stage-id>
   ```

3. Approve each platform package, then `aken-mcp`. npm asks for your second
   factor.

   ```sh
   npm stage approve <stage-id>
   ```

You can also approve staged versions on npmjs.com.

## Verify the release

1. Download every asset into an empty directory, then verify the signature,
   the checksums, and one attestation:

   ```sh
   gh release download <tag> --repo akenhq/aken
   cosign verify-blob \
     --bundle SHA256SUMS.sigstore.json \
     --certificate-identity "https://github.com/akenhq/aken/.github/workflows/release.yml@refs/tags/<tag>" \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com \
     SHA256SUMS
   sha256sum -c SHA256SUMS
   gh attestation verify aken_linux_amd64 --repo akenhq/aken
   ```

   Without `--ignore-missing`, `sha256sum` also fails when a listed asset is
   absent from the release.

2. For a full release, confirm that the latest release and the installer point
   at the new tag:

   ```sh
   curl -fsSLI -o /dev/null -w '%{url_effective}\n' https://github.com/akenhq/aken/releases/latest
   curl -fsSL https://aken.dev/install.sh | grep -m1 AKEN_VERSION=
   ```

3. Install the npm package into a temporary directory and run it. Replace
   `<version>` with the tag without the leading `v`:

   ```sh
   dir=$(mktemp -d)
   npm install --prefix "$dir" aken-mcp@<version>
   "$dir/node_modules/.bin/aken-mcp" version
   ```

   The command prints the tag. The registry can take a few minutes to serve a
   version after you approve it. Wait and retry before you treat a missing
   version as a failure.

4. Read the release notes on GitHub. To change them, run
   `gh release edit <tag> --notes-file <file>`.

## After the release

If the release changes `aken-relay`, the `relay/` package, or the relay API,
deploy the new relay to `relay.aken.dev`. A separate deployment repository
builds `aken-relay` from this repository. Follow the runbook in that
repository, then run the conformance suite against the deployed relay as
[spec/relay-api.md](spec/relay-api.md) describes.

## If the workflow fails

**The run failed before the step "Publish the GitHub release".** Nothing is
public. If the cause is temporary, such as a failed download, run the job
again:

```sh
gh run rerun <run-id> --failed
```

If the fix needs a code change, merge the fix into `main` and tag the next
patch version. Leave the failed tag in place.

**The run failed at the step "Stage npm packages".** The GitHub release is
complete and valid. Do not run the job again, because `gh release create`
fails when the release exists. Publish the npm packages by hand instead.

## Publish the npm packages by hand

Use this procedure when the GitHub release exists but the npm packages do
not. Versions 0.2.0 and 0.3.0 went out this way, and the staging step has not
yet completed on a real tag. Packages that you publish by hand have no npm
provenance.

1. Check for versions that the workflow staged before it failed:

   ```sh
   npm stage list
   ```

   Approve a staged version instead of publishing that package again.

2. Check out the tag and remove old build output. The launcher and the
   package metadata come from the working tree.

   ```sh
   git switch --detach <tag>
   make clean
   ```

3. Download the MCP binaries and the signed checksum file into `dist/`, then
   verify them:

   ```sh
   gh release download <tag> --repo akenhq/aken --dir dist \
     --pattern 'aken-mcp_*' --pattern 'SHA256SUMS*'
   cosign verify-blob \
     --bundle dist/SHA256SUMS.sigstore.json \
     --certificate-identity "https://github.com/akenhq/aken/.github/workflows/release.yml@refs/tags/<tag>" \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com \
     dist/SHA256SUMS
   (cd dist && sha256sum -c --ignore-missing SHA256SUMS)
   ```

   If a check fails, stop.

4. Build the packages into `dist-npm/`:

   ```sh
   make npm VERSION=<tag>
   ```

5. Log in with `npm login`, then publish the platform packages before the
   launcher. Keep the `./` prefix, because npm reads a path without it as a
   GitHub repository. For a prerelease, add `--tag next`.

   ```sh
   for package in ./dist-npm/akenhq-mcp-*.tgz ./dist-npm/aken-mcp-*.tgz; do
     npm publish "$package" --access public
   done
   ```

6. Return to `main` with `git switch main`, then continue with
   [Verify the release](#verify-the-release).
