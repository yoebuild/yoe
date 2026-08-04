package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/yoebuild/yoe/internal/feed"
)

func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", 8765, "TCP port to listen on")
	bind := fs.String("bind", "0.0.0.0", "listen address")
	noMDNS := fs.Bool("no-mdns", false, "skip mDNS advertisement")
	instance := fs.String("service-name", "", "mDNS instance name (default: yoe-<project>)")
	fs.Parse(args)

	proj := loadProject()

	cfg, err := feed.ConfigForProject(proj, projectDir())
	if err != nil {
		fail("Error: %v", err)
	}
	cfg.BindAddr = fmt.Sprintf("%s:%d", *bind, *port)
	cfg.Instance = *instance
	cfg.NoMDNS = *noMDNS
	cfg.LogW = os.Stderr

	srv, err := feed.Start(cfg)
	if err != nil {
		fail("Error: %v", err)
	}

	fmt.Printf("serving %s/ at %s\n", cfg.RepoDir, srv.URL())
	if !*noMDNS {
		instName := *instance
		if instName == "" {
			instName = "yoe-" + proj.Name
		}
		fmt.Printf("mDNS:  _yoe-feed._tcp.local. instance=%s\n", instName)
	}
	fmt.Println("press ctrl-c to stop")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	fmt.Println("\nshutting down...")
	if err := srv.Stop(); err != nil {
		fail("shutdown: %v", err)
	}
}
