---
date: 2026-05-18
topic: mirror-alpine-keep-keys
---

# Mirror Alpine packages while keeping Alpine's signatures

## Summary

Caching Alpine packages into the project repo and re-signing them with the
project key are two independent decisions that today's `RepackAPK` conflates.
This design separates them: mirror Alpine apks **byte-for-byte** (Alpine's
embedded signature intact), index them alongside project-built packages, and let
the target verify each apk against whichever key its `.SIGN.RSA.<keyname>`
segment names. The device trusts two keysets — Alpine's build-host keyring for
mirrored packages, the project key for built packages and the index. The fragile
fetch → split → strip → re-sign → reconcatenate path collapses to a verbatim
copy.

---

## Problem Frame

`internal/artifact/apk.go` (`RepackAPK`, lines ~122-191) fetches each upstream
Alpine apk, splits its gzip streams, strips the leading `.SIGN.RSA.alpine*`
segment, keeps control + data unchanged, re-signs the control stream with the
project key, and reconcatenates. The only reason the signature is swapped is
that yoe chose a **single trust root**: one project key under `/etc/apk/keys/`,
so `apk add` / `apk upgrade` work without `--allow-untrusted` (see
`docs/signing.md`, `docs/apk-passthrough.md`).

Mirroring does not require that swap. In apk v2 the `.SIGN.RSA.<keyname>`
segment is a **separate prepended gzip segment**, not part of the control tar.
The `C:` field in `APKINDEX` is the SHA-1 of the control segment only. So an
Alpine apk copied verbatim produces the **identical `C:`** that Alpine's own
index carries, and its embedded signature still verifies against Alpine's
keyring — the bytes the index addresses and the bytes the signature covers are
independent of who is hosting the file.

The cost of the current conflation: `RepackAPK` is the most intricate artifact
path in the tree, it re-signs ~1000 packages on every cache miss, and it
silently re-roots trust away from upstream. The benefit it was reaching for
(single key, offline, reproducible) is achievable by mirroring alone — keys are
orthogonal.

This spec is a runtime/trust companion to
`docs/specs/2026-05-13-feeds-as-modules.md`, which addresses the build-time
`.star` duplication problem but explicitly _keeps_ project-key re-signing (that
spec's "Dependencies / Assumptions", line ~316). The two are complementary:
feeds-as-modules decides how the resolver _sees_ Alpine packages; this decides
how the device _trusts_ them.

---

## Actors

- A1. Project user: writes `PROJECT.star`, points devices at the project repo,
  flashes/upgrades images. Wants offline installs that work without
  `--allow-untrusted` and reproducible package sets.
- A2. Yoe build pipeline: fetches upstream apks, publishes them into the project
  repo, generates and signs `APKINDEX.tar.gz`.
- A3. Yoe image assembler: stages `base-files`, which carries the trusted public
  keys into `/etc/apk/keys/`.
- A4. Operator of a connected or air-gapped fleet: relies on the project repo
  URL being the single host devices contact.
- A5. apk-tools on the target: verifies the index signature and each apk's
  embedded signature against the keys in `/etc/apk/keys/`.

---

## Key Flows

- F1. Mirror an upstream Alpine apk verbatim
  - **Trigger:** A unit resolves to an upstream Alpine package (passthrough /
    synthetic-feed entry) during `yoe build`.
  - **Actors:** A2.
  - **Steps:** Fetch the upstream apk against its pinned checksum. Copy it into
    `repo/<project>/<arch>/` **unchanged** — no stream split, no signature
    strip, no re-sign. Extract PKGINFO from the control segment for indexing
    only (read-only; the apk bytes are not rewritten).
  - **Outcome:** The mirrored apk is byte-identical to upstream; its
    `.SIGN.RSA.<alpine-keyname>` segment and its `C:` checksum are unchanged.
  - **Covered by:** R1, R2, R3.

- F2. Generate and sign the merged index
  - **Trigger:** Publish step after F1, or any project-built apk landing in the
    repo.
  - **Actors:** A2.
  - **Steps:** Scan all apks (mirrored Alpine + project-built) in the per-arch
    dir plus the `noarch/` sibling. Build one `APKINDEX` covering all of them,
    `C:` taken from each apk's control segment as-is. Tar + gzip + sign the
    index with the **project** key.
  - **Outcome:** One signed `APKINDEX.tar.gz` per arch; index trust roots to the
    project key, individual apk trust roots to each apk's own signer.
  - **Covered by:** R4, R5.

- F3. Ship both keysets into the rootfs
  - **Trigger:** `yoe build <image-unit>` stages `base-files`.
  - **Actors:** A3.
  - **Steps:** Extract the Alpine build-host public keys from the pinned
    `alpine-keys` apk at build time and install them into `/etc/apk/keys/`
    alongside the project key, both via `base-files`.
  - **Outcome:** The target trusts the project key (index + project apks) and
    Alpine's keyring (mirrored apks). No runtime bootstrap dependency on
    fetching `alpine-keys` first.
  - **Covered by:** R6, R7.

- F4. Target installs a mirrored package offline
  - **Trigger:** `apk add <pkg>` on a device pointed only at the project repo
    URL (no route to Alpine's CDN).
  - **Actors:** A4, A5.
  - **Steps:** apk fetches the project `APKINDEX.tar.gz`, verifies it against
    the project key. Resolves `<pkg>` to a mirrored Alpine apk, fetches it from
    the project repo, verifies its embedded `.SIGN.RSA.<alpine-keyname>` against
    the Alpine keyring in `/etc/apk/keys/`. Installs without
    `--allow-untrusted`.
  - **Outcome:** Offline install of an upstream-signed package served from the
    project's single host.
  - **Covered by:** R5, R7, R8.

---

## Requirements

**Verbatim mirroring**

- R1. The passthrough path copies upstream Alpine apks into the project repo
  byte-for-byte. The signature segment is not stripped, the control stream is
  not re-signed, and the gzip segments are not re-split or recompressed.
- R2. PKGINFO extraction for indexing reads the control segment without
  modifying the apk. The on-disk apk after mirroring is bit-identical to the
  fetched upstream file.
- R3. The mirrored apk's `C:` checksum equals the upstream APKINDEX `C:` for the
  same package. A regression test asserts byte-identity and `C:` equality
  against a known upstream apk + APKINDEX pair.

**Merged signed index**

- R4. `internal/repo/index.go` generates one `APKINDEX` per arch covering both
  mirrored and project-built apks, with each entry's `C:` taken from that apk's
  control segment as-is (no recomputation that would diverge from upstream for
  mirrored entries).
- R5. The merged `APKINDEX.tar.gz` is signed with the project key. The index
  signer is independent of, and unaffected by, the per-apk signers. noarch
  routing and per-arch reindex-on-publish behavior (see
  `docs/apk-passthrough.md`) are preserved unchanged.

**Dual keyring on target**

- R6. `base-files` installs the project public key **and** the Alpine build-host
  public key(s) into `/etc/apk/keys/`. The Alpine keys are extracted from the
  pinned `alpine-keys` apk at build time, not fetched at runtime.
- R7. The set of Alpine key files installed matches the keyname(s) referenced by
  the mirrored apks' `.SIGN.RSA.<keyname>` segments for the active arch(es) and
  pinned Alpine release. `apk add` / `apk upgrade` / image-time install of a
  mirrored package succeed without `--allow-untrusted`.

**Reproducibility and pinning**

- R8. Mirrored apks remain pinned by checksum exactly as the current passthrough
  path pins them; the project repo's retention (not Alpine's CDN lifetime) is
  the reproducibility guarantee. Removing the re-sign step does not change which
  bytes a given build resolves to.

**Documentation**

- R9. `docs/signing.md` and `docs/apk-passthrough.md` are updated in the same
  change: the "what's signed" section reflects that mirrored apks carry upstream
  signatures, and the dual-keyring model is documented next to the existing
  single-key flow. A CHANGELOG entry leads with the user-visible effect.

---

## Acceptance Examples

- AE1. **Covers R1, R2, R3.** Given upstream `busybox-1.36.1-r0.apk` and
  Alpine's `v3.21/main/x86_64/APKINDEX`, when yoe mirrors the apk into
  `repo/<project>/x86_64/`, the on-disk file is `cmp`-identical to the fetched
  upstream apk and yoe's generated index lists the same `C:` value as Alpine's
  index for `busybox`.

- AE2. **Covers R4, R5.** Given a repo containing one mirrored Alpine apk
  (Alpine-signed) and one project-built apk (project-signed), when the index is
  generated, `APKINDEX.tar.gz` carries a single `.SIGN.RSA.<project-keyname>`
  segment and both packages appear with their respective `C:` checksums.

- AE3. **Covers R6, R7.** Given an image built for the pinned Alpine release,
  when it boots, `/etc/apk/keys/` contains both the project `.rsa.pub` and the
  Alpine build-host `.rsa.pub`(s), and `apk add <mirrored-pkg>` succeeds with no
  `--allow-untrusted` flag and no network route to `dl-cdn.alpinelinux.org`.

- AE4. **Covers R7.** Given a mirrored apk whose embedded signature names an
  Alpine keyname **not** present in `/etc/apk/keys/`, when `apk add` runs, it
  fails loudly with a signature/trust error (silent untrusted install is not an
  acceptable degradation — surfaces the keyring-sync gap).

- AE5. **Covers R8.** Given two builds of the same project at the same Alpine
  pin, when both run, each resolves the identical mirrored apk bytes; deleting
  the re-sign step changes no resolved checksum versus the pre-change baseline
  for project-built units.

---

## Success Criteria

- A device pointed at only the project repo URL installs both project-built and
  upstream Alpine packages offline, without `--allow-untrusted`.
- Mirrored Alpine apks on disk are byte-identical to upstream; their signatures
  verify against Alpine's keyring.
- `RepackAPK`'s signature-swap logic is deleted; the passthrough path is a copy
  plus a read-only PKGINFO extract.
- Index generation is unchanged in behavior for project-built packages and
  correctly includes mirrored packages with upstream `C:` values.
- Reproducibility is unchanged: the project repo's retention guarantees the
  package set, independent of Alpine CDN lifetime.
- An implementer reading this document can plan the change without inventing
  trust or layout behavior.

---

## Scope Boundaries

- The monolithic-vs-split conflict (source-built `util-linux` vs Alpine's split
  `libuuid` / `libmount` / `libblkid`) is unchanged — orthogonal to who signs.
- Install scripts and triggers (`.pre-install`, `.post-install`, `.trigger`) are
  still not executed at image-build time; this design does not change extraction
  semantics.
- This spec does not decide _which_ packages are mirrored vs. source-built —
  that is `prefer_modules` / feeds-as-modules territory. It governs only how a
  mirrored apk is signed and trusted.
- The merged-index shape (one repo line on the device, mixed apk signatures) is
  the recommended default. The fully separate-feed shape (two repo lines, one
  Alpine-signed index mirrored verbatim, one project-signed index) is recorded
  in Key Decisions as the considered alternative, not specified in detail here.
- Key rotation for the **project** key is unchanged (`docs/signing.md`). Alpine
  keyring refresh is folded into the existing Alpine-release-bump procedure
  (`docs/module-alpine.md`), not given a separate rotation flow.
- No change to bootstrap apks (`yoe bootstrap`), which remain unsigned and
  container-internal.

---

## Key Decisions

- **Mirror verbatim, keep upstream signatures.** The signature segment is
  separable from indexable content, so re-signing was never required for
  mirroring. Removing it deletes the most fragile artifact path and restores
  upstream's trust attestation instead of masking it.
- **Merged single index over separate feeds.** One repo line on the device is
  the smallest operational delta and reuses yoe's existing publish/index
  pipeline untouched. The separate-feed alternative (mirror Alpine's tree _and_
  its signed index verbatim, list it as a second repo) also works and keeps even
  the index upstream-signed, but adds a second repo line and a second index-sync
  surface for no trust benefit the merged shape lacks.
- **Alpine keys baked from the pinned `alpine-keys` apk at build time.** Avoids
  a runtime chicken-and-egg (can't fetch `alpine-keys` to trust the feed that
  serves `alpine-keys`). The keyring is a build-time input, shipped via
  `base-files` exactly as the project key already is.
- **Alpine keyring lifecycle rides the release bump.** Alpine rotates build-host
  keys per release; `docs/module-alpine.md` already requires keyring sync at
  release-bump time. Making it "ship these `.pub` files" is more explicit than
  "re-sign everything with our key," not more work.
- **Project key still signs the index.** The index is yoe-generated content;
  signing it with the project key is correct and unchanged. Only per-apk
  signatures for mirrored packages change (from project to upstream).

---

## Dependencies / Assumptions

- apk-tools v2 verifies the `APKINDEX.tar.gz` signature and each apk's embedded
  control-segment signature **independently**, against any matching key in
  `/etc/apk/keys/`; differing keynames across packages in one repo are
  supported. (Native Alpine behavior; confirm against the apk-tools version yoe
  targets.)
- The `.SIGN.RSA.<keyname>` segment is not included in the control-segment SHA-1
  that `APKINDEX` `C:` addresses, so verbatim mirroring yields
  upstream-identical `C:`. (Stated as fact above; assert with the AE1 regression
  test before code commits.)
- `alpine-keys` for the pinned release ships exactly the build-host public
  key(s) used to sign that release's `main`/`community` apks. Verify the keyname
  set for each active arch.
- `internal/repo/index.go` already reads PKGINFO from the control segment
  without rewriting the apk; the only change is removing the re-sign call in the
  passthrough path, not reworking index extraction.
- `internal/source/fetch.go` checksum pinning for passthrough apks is unaffected
  — pinning is upstream-byte-based and removing re-sign keeps those bytes.

---

## Outstanding Questions

### Resolve Before Planning

- [Affects Scope, Key Decisions][User decision] Confirm the merged-index shape
  as the shipped design (recommended), or elect the fully separate-feed shape.
  Determines whether the device gets one repo line or two and whether the Alpine
  index is mirrored or regenerated.
- [Affects R6, R7][User decision] Bake the Alpine keyring into `base-files`
  unconditionally for every image, or gate it on the project actually mirroring
  Alpine packages? Unconditional is simpler and matches "explicit over
  implicit"; gating avoids shipping unused keys on pure-source images.

### Deferred to Planning

- [Affects R7][Technical, Needs research] Enumerate the exact Alpine build-host
  keyname(s) for the pinned release across `x86_64` / `aarch64` / `noarch`;
  confirm `alpine-keys` is the authoritative source and how its `.pub` files map
  to `.SIGN.RSA.<keyname>` segments.
- [Affects R1][Technical] Audit every current `RepackAPK` responsibility beyond
  signing (e.g. any normalization, recompression, or metadata fix-up) to ensure
  "verbatim copy" loses nothing the downstream relies on.
- [Affects R4][Technical] Confirm `index.go`'s `C:` derivation already matches
  upstream byte-for-byte for an untouched apk, or identify the exact step that
  would diverge and pin it.
- [Affects R9][Technical] Decide whether `docs/apk-passthrough.md` is rewritten
  or superseded by a new doc, given how much of it describes the re-sign
  mechanism that this change removes.
- [Affects feeds-as-modules][Technical] Reconcile with
  `docs/specs/2026-05-13-feeds-as-modules.md` line ~316, which assumes
  project-key re-signing; that assumption should be updated or cross-referenced
  when both land.
