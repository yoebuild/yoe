unit(
    name = "nm-manage-ethernet",
    version = "1.0.0",
    license = "MIT",
    description = "NetworkManager drop-in so it manages (auto-DHCPs) wired ethernet on Ubuntu",
    # Ubuntu's network-manager package ships
    # /usr/lib/NetworkManager/conf.d/10-globally-managed-devices.conf with
    #   unmanaged-devices=*,except:type:wifi,except:type:gsm,except:type:cdma
    # which leaves wired ethernet *unmanaged* — Ubuntu normally delegates the
    # wired NIC to netplan/systemd-networkd. yoe images carry no netplan
    # config, so without this drop-in the ethernet port never comes up (NM
    # sees the device, then ignores it). The companion conf re-includes
    # ethernet in NM's managed set so it auto-DHCPs the wired NIC with zero
    # connection profiles, matching how NetworkManager behaves out of the box
    # on the Debian sibling. /etc wins over /usr/lib and 15- sorts after 10-,
    # so this fully overrides the upstream key. Inert on Debian (NM already
    # manages ethernet there); only the Ubuntu images list it.
    deps = ["toolchain"],
    container = "toolchain",
    container_arch = "target",
    tasks = [
        task("build", steps = [
            "mkdir -p $DESTDIR/etc/NetworkManager/conf.d",
            install_file("15-yoe-manage-ethernet.conf",
                         "$DESTDIR/etc/NetworkManager/conf.d/15-yoe-manage-ethernet.conf",
                         mode = 0o644),
        ]),
    ],
)
