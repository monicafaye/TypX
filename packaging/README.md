# TypX Packaging & Distribution

This directory holds package manager manifests and the deterministic generator used by [.github/workflows/release.yaml](../.github/workflows/release.yaml) on tagged `v*.*.*` releases.

## Release Archive Contract

Tagged releases publish 6 archives, 4 native Linux packages, and `SHA256SUMS.txt`:

- `typx-<version>-windows-amd64.zip` (flattened: `typx.exe`, `LICENSE*`, and `README.md` at archive root)
- `typx-<version>-windows-arm64.zip` (flattened)
- `typx-<version>-darwin-arm64.tar.gz`
- `typx-<version>-darwin-amd64.tar.gz`
- `typx-<version>-linux-amd64.tar.gz`
- `typx-<version>-linux-arm64.tar.gz`
- `typx_<version>_amd64.deb` and `typx_<version>_arm64.deb` (built via `nfpm` from `packaging/nfpm.yaml`)
- `typx-<version>-1.x86_64.rpm` and `typx-<version>-1.aarch64.rpm` (built via `nfpm` from `packaging/nfpm.yaml`)

Linux and macOS users can also install directly from GitHub Releases with SHA-256 verification via `scripts/install.sh`.

## Enabling Automated Publishing

All external publishing steps in `.github/workflows/release.yaml` are gated on repository secrets so normal GitHub Releases succeed even before external package repos are configured.

### 1. WinGet (`monicafaye.TypX`)
1. Submit the initial portable-zip manifest to `microsoft/winget-pkgs` using `wingetcreate new` against a tagged release with flattened Windows `.zip` archives.
2. Add a GitHub Personal Access Token (`public_repo` scope on your `winget-pkgs` fork) as repository secret `WINGET_TOKEN`.
3. Subsequent `v*.*.*` tags automatically open an update PR via `vedantmgoyal9/winget-releaser@v2`.

### 2. Scoop (`monicafaye/scoop-bucket`) and Homebrew (`monicafaye/homebrew-tap`)
1. Create public GitHub repositories:
   - `monicafaye/scoop-bucket` (manifest lives at `bucket/typx.json`)
   - `monicafaye/homebrew-tap` (formula lives at `Formula/typx.rb`)
2. Add a PAT with write access to those two repos as repository secret `PACKAGING_TOKEN`.
3. Subsequent `v*.*.*` tags render `typx.json` and `typx.rb` from `SHA256SUMS.txt` via `packaging/scripts/update-manifests.sh` and push commits to both repos.

### 3. AUR (`typx-bin`)
Render the updated `PKGBUILD` from a release's `SHA256SUMS.txt`:

```bash
bash packaging/scripts/update-manifests.sh 0.4.0 SHA256SUMS.txt /tmp/typx-pkg
cp /tmp/typx-pkg/aur/PKGBUILD /path/to/aur-typx-bin/PKGBUILD
cd /path/to/aur-typx-bin
makepkg --printsrcinfo > .SRCINFO
git commit -am "Update to 0.4.0" && git push
```
