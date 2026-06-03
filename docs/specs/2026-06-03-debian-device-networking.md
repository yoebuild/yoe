<!--
Spec: Debian device networking (NetworkManager + wifi + cellular)
Date: 2026-06-03
-->

# Debian device networking: NetworkManager, wifi, and cellular

## Summary

The Debian backend targets real field devices, which carry wifi and cellular
radios in addition to wired ports and must reconfigure their networks at runtime
(field wifi provisioning, SIM/APN changes, failover between links, VPN). That
use profile picks the connection manager: **NetworkManager**, not
systemd-networkd or ifupdown. networkd is static, declarative, and
headless-server-shaped; it has no story for runtime reconfiguration, wifi
association, cellular auto-connect, or VPN. NetworkManager owns exactly those,
with `nmcli` / D-Bus for runtime control and per-connection keyfile profiles for
provisioning.

This spec adopts NetworkManager as the standard connection manager for Debian
device images, with **wpa_supplicant** as the wifi backend and **ModemManager**
(+ libqmi/libmbim) for cellular. DNS goes through the **systemd-resolved**
already present in the image. The wired QEMU `dev-image` comes online with zero
connection profiles because NM auto-manages unconfigured ethernet with DHCP.

## Why NetworkManager (not networkd / ifupdown)

| Need on a field device                        | networkd | ifupdown         | NetworkManager   |
| --------------------------------------------- | -------- | ---------------- | ---------------- |
| Wired DHCP                                    | ✓        | ✓ (needs config) | ✓ (zero config)  |
| Wifi association + roaming                    | ✗        | ✗ (wpa glue)     | ✓                |
| Field wifi provisioning (`nmcli`)             | ✗        | ✗                | ✓                |
| Cellular auto-connect (APN, failover)         | ✗        | ✗                | ✓ (ModemManager) |
| VPN, link priority/metering, runtime reconfig | ✗        | ✗                | ✓                |

The images already ship `ifupdown` + `isc-dhcp-client`, but nothing writes
`/etc/network/interfaces`, so the NIC is never brought up — that is the current
"no network" symptom. Rather than configure ifupdown (which still can't do wifi
or cellular), the Debian images switch to NetworkManager and drop ifupdown +
isc-dhcp-client.

## Stack

All components are upstream Debian packages already present in `module-debian`'s
feed; yoe pulls them as passthrough `.deb`s. None is built from source.

| Package                        | Role                                                                |
| ------------------------------ | ------------------------------------------------------------------- |
| `network-manager`              | the NM daemon, `nmcli`, keyfile plugin — wired + orchestration      |
| `wpasupplicant`                | wifi backend NM drives over D-Bus (WPA2/WPA3, incl. enterprise EAP) |
| `wireless-regdb`               | regulatory database so wifi may use legal channels/power            |
| `modemmanager`                 | cellular: modem detection, SIM/APN, connection (mmcli)              |
| `libqmi` / `libmbim` utilities | modern QMI/MBIM modem protocols (pulled as ModemManager deps)       |
| `usb-modeswitch`               | flips multi-mode USB modems out of mass-storage into modem mode     |

## Decisions

- **wpa_supplicant over iwd.** wpa_supplicant is NM's default backend, mature,
  and has complete WPA2/WPA3-Enterprise (EAP-TLS/PEAP/TTLS) support — table
  stakes for industrial deployments behind corporate or carrier wifi. iwd is
  lighter and rolls faster, but its EAP coverage and NM+iwd production track
  record are thinner. Revisit iwd if a size- or roaming-critical product line
  justifies it; it is an NM backend swap, not an architectural change.
- **ModemManager for cellular**, with libqmi/libmbim for QMI/MBIM modems and
  `usb-modeswitch` for the mode-switch dance. This is effectively the only real
  NM cellular stack.
- **DNS via systemd-resolved.** It is already enabled in the image and owns
  `/etc/resolv.conf` (the stub symlink). NM auto-detects the resolved stub and
  routes DNS through it — no extra configuration.
- **Enablement is postinst-driven, not yoe-`services`.** Debian maintainer
  scripts run `deb-systemd-helper enable` during mmdebstrap assembly (confirmed:
  the build log already shows resolved/fstrim symlinks being created). So
  NetworkManager and ModemManager enable themselves by being installed — no
  Alpine-style `<svc>-enable.star` companion is needed. This is a meaningful
  divergence from the Alpine path and should stay documented so nobody adds
  redundant enable units.

## Packaging in yoe

The stack is split into opt-in layers so a minimal image pays only for what it
uses, while a product image pulls the lot:

- **Base** — `network-manager`. Gets wired DHCP and the NM daemon. This alone
  brings the QEMU `dev-image` online and SSH-reachable.
- **Wifi** — `wpasupplicant` + `wireless-regdb`.
- **Cellular** — `modemmanager` + `usb-modeswitch` (libqmi/libmbim arrive as
  deps).

For the QEMU `dev-image` and `base-image`, only the base layer is meaningful
(QEMU has no wifi/cellular hardware), so they list `network-manager` and drop
`ifupdown` + `isc-dhcp-client`. Product/device images add the wifi and cellular
packages. Whether these become a single bundling unit in `module-debian` (one
name that `Depends:` the layer) or stay as explicit per-image package lists is
an open question below; the explicit list is the starting point.

## Connection profile provisioning

- **Wired**: none needed — NM's default "auto" behavior DHCPs any unmanaged
  ethernet.
- **Wifi / cellular**: NM keyfile profiles under
  `/etc/NetworkManager/system-connections/<name>.nmconnection` (mode 0600).
  These are per-device secrets (PSKs, EAP identities, APNs), so they are
  provisioned — baked per machine, dropped by a first-boot/cloud-init step, or
  set at runtime with `nmcli` — not committed into a shared image. The mechanism
  is out of scope here; this spec only guarantees the stack that consumes them
  is present.

## Phasing

1. **Base NM (wired).** Swap `ifupdown`/`isc-dhcp-client` → `network-manager` in
   the Debian `base-image` and `dev-image`. Verify in QEMU: NM up,
   `eth0`/`enp0s2` gets a DHCP lease, SSH works. (This phase also closes the
   current "no network" bug.)
2. **Wifi.** Add `wpasupplicant` + `wireless-regdb` to device images. Assembles
   in QEMU; functional verification needs real wifi hardware.
3. **Cellular.** Add `modemmanager` + `usb-modeswitch`. Assembles in QEMU;
   functional verification needs a modem.

## Open Questions

- **base-image weight.** NetworkManager is heavier than networkd. Should
  `base-image` stay lean on systemd-networkd (zero added cost, wired-only) and
  reserve NM for device/product images, or is NM the right device-foundation
  default even for base? Starting point: NM in both, since base is the device
  images' foundation and "Debian targets devices."
- **Bundling unit vs explicit lists.** A `module-debian` unit that `Depends:`
  each layer would DRY up product images; explicit per-image lists are simpler
  to start.
- **Regulatory domain.** `wireless-regdb` ships the database, but the active
  country (`REGDOMAIN`/`iw reg set`) is per-deployment — where does it get set?
- **iwd reconsideration** for a size- or roaming-critical product line.

## Acceptance Criteria

- The Debian `dev-image` boots, NetworkManager is running and enabled, the wired
  NIC obtains a DHCP lease, and SSH over the forwarded port succeeds.
- `ifupdown` and `isc-dhcp-client` are gone from the Debian images; no
  `/etc/network/interfaces` is required for connectivity.
- The wifi and cellular packages install and assemble into a device image
  without conflicts (radio-functional verification deferred to hardware).
- No redundant systemd-enable companion units exist for NM/ModemManager — the
  Debian postinsts are the single enablement path.
