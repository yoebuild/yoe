package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/yoebuild/yoe/internal/feed"
	"github.com/yoebuild/yoe/internal/repo"
)

func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", 8765, "TCP port to listen on")
	bind := fs.String("bind", "0.0.0.0", "listen address")
	noMDNS := fs.Bool("no-mdns", false, "skip mDNS advertisement")
	instance := fs.String("service-name", "", "mDNS instance name (default: yoe-<project>)")
	fs.Parse(args)

	proj := loadProject()
	if proj.Name == "" {
		fmt.Fprintf(os.Stderr, "Error: project has no name\n")
		os.Exit(1)
	}

	projRepoDir := repo.RepoDir(proj, projectDir())
	httpRoot := filepath.Dir(projRepoDir)

	archs, err := repo.ArchDirs(projRepoDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: list arch dirs: %v\n", err)
		os.Exit(1)
	}

	srv, err := feed.Start(feed.Config{
		RepoDir:  httpRoot,
		BindAddr: fmt.Sprintf("%s:%d", *bind, *port),
		Project:  proj.Name,
		Archs:    archs,
		Instance: *instance,
		NoMDNS:   *noMDNS,
		LogW:     os.Stderr,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("serving %s/ at %s\n", httpRoot, srv.URL())
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
		fmt.Fprintf(os.Stderr, "shutdown: %v\n", err)
		os.Exit(1)
	}
}
