# Monitoring and Telemetry

Three units in `module-core` cover the common shape of a connected-product
gateway: something that collects and manages device data, something that stores
the time series it produces, and something that draws them.

| Unit            | Provides                                  | Default port | Data directory            |
| --------------- | ----------------------------------------- | ------------ | ------------------------- |
| `simpleiot-bin` | [Simple IoT](https://simpleiot.org)       | 8118         | `/var/lib/simpleiot`      |
| `victoria-metrics` | [VictoriaMetrics](https://victoriametrics.com) | 8428  | `/var/lib/victoria-metrics` |
| `grafana`       | [Grafana](https://grafana.com)            | 3000         | `/var/lib/grafana`        |

All three install a program that upstream publishes as a release asset rather
than building it from source, so adding them to an image costs a download
rather than a compile.

Add them to an image the same way as anything else:

```python
image(
    name = "gateway-image",
    artifacts = [
        "simpleiot-bin",
        "victoria-metrics",
        "grafana",
    ],
)
```

## Services

Each unit declares its own service, so installing the package is what enables
it — there is no separate step to run at image-assembly time. Each works on the
Alpine base and on the Debian and Ubuntu bases, and the package that reaches a
device carries only the half its init reads:

| | Alpine | Debian / Ubuntu |
| --- | --- | --- |
| service | `/etc/init.d/<name>` | `/lib/systemd/system/<name>.service` |
| settings | `/etc/conf.d/<name>` | `/etc/default/<name>` |

The settings file is the same either way — plain `KEY=VALUE` lines, only the
path differs — so a device behaves the same whichever init is running it. It is
declared as a configuration file, so a copy edited on a device survives a
package upgrade. For how the split is done, see
[Shipping a service on both bases](libc-and-init.md#shipping-a-service-on-both-bases).

VictoriaMetrics and Grafana run under their own service accounts rather than as
root. Under systemd the account is declared to `systemd-sysusers`; under OpenRC
the init script creates it on first start, which is where a package can add an
account given that an image writes `/etc/passwd` as a whole from its `users`
list. Simple IoT runs as root.

## Connecting the three

VictoriaMetrics answers Prometheus queries, so Grafana reads it through the
bundled Prometheus data source with no extra plugin. Point a data source at
`http://localhost:8428` — either through the Grafana UI, or by dropping a YAML
file into `/etc/grafana/provisioning/datasources/`, which Grafana applies at
start-up. The provisioning tree ships with the package:

```
/etc/grafana/provisioning/
    access-control/
    alerting/
    dashboards/
    datasources/
    notifiers/
    plugins/
```

Simple IoT writes to VictoriaMetrics through its Prometheus remote-write
support; configure that in the Simple IoT UI on port 8118.

## Storage

The three data directories sit under `/var/lib`, so they survive a reboot and
grow into whatever space `grow-rootfs` claims at first boot. To keep them across
a rootfs update, point each service's data setting at a separate partition —
`SIOT_DATA`, `VM_STORAGE_DATA_PATH`, and `DATA_DIR` respectively.

## Choosing between `simpleiot` and `simpleiot-bin`

`module-core` carries two Simple IoT units. `simpleiot` builds from source with
the Go toolchain, which is the one to use when you want to carry a patch or
track a branch. `simpleiot-bin` installs the executable upstream publishes,
which is the one to use when you want the release as shipped and a shorter
build.

Both install `/usr/bin/siot` and enable the same `simpleiot` service, so put one
or the other in an image's artifacts list, not both. `simpleiot-bin` records
that it takes ownership of those paths, so it can also replace the source-built
package on a device that already has it.

## Grafana's size

Grafana is large: roughly 930 MB installed, most of it the server program and
the thirteen bundled data sources, each of which carries its own backend. It has
been growing steadily — the 13.x releases are more than twice the size of 11.x.
The unit already removes what only someone developing Grafana would read:
JavaScript source maps, the Swagger UI, the bundled documentation, and the
packaging and container helper scripts.

If that is still too much for the device, the next place to look is
`/usr/share/grafana/data/plugins-bundled`, where each data source you never
query costs 25–50 MB. Removing some of them is a per-image decision, which
means the resulting package is no longer the one every other image shares —
worth it on a space-constrained board, not worth it by default.
