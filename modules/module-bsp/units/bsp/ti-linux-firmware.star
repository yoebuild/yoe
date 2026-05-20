unit(
    name = "ti-linux-firmware",
    version = "12.00.02",
    source = "https://git.ti.com/git/processor-firmware/ti-linux-firmware.git",
    branch = "ti-linux-firmware",
    license = "TI-TFL",
    description = "TI K3 SYSFW (TIFS) + DM firmware blobs for AM62x and friends",
    deps = ["toolchain-musl"],
    container = "toolchain-musl",
    container_arch = "target",
    tasks = [
        task("build", steps=[
            # Prebuilt blobs — no compile. Install everything U-Boot SPL,
            # TF-A, and the running kernel might pull in. The path layout
            # matches what TI's own scripts expect when these land in a
            # sysroot under /lib/firmware/.
            #
            # ti-sysfw/   — TIFS / SYSFW images, consumed by R5 SPL binman
            # ti-dm/      — Device Manager firmware, consumed by A53 SPL
            # Everything else (cadence, am65x-pru, etc.) is shipped too so
            # downstream rootfs units can pick what they need without
            # re-cloning the repo.
            "install -d $DESTDIR/lib/firmware",
            "cp -a ti-sysfw   $DESTDIR/lib/firmware/",
            "cp -a ti-dm      $DESTDIR/lib/firmware/",
            "cp -a ti-fs      $DESTDIR/lib/firmware/ 2>/dev/null || true",
            "cp -a cadence    $DESTDIR/lib/firmware/ 2>/dev/null || true",
        ]),
    ],
)
