package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/yoebuild/yoe/internal/artifact"
	"github.com/yoebuild/yoe/internal/device"
	"github.com/yoebuild/yoe/internal/feed"
)

func cmdDevice(args []string) {
	if len(args) < 1 {
		fail("Usage: %s device <repo> ...", os.Args[0])
	}
	switch args[0] {
	case "repo":
		cmdDeviceRepo(args[1:])
	default:
		fail("Unknown device subcommand: %s", args[0])
	}
}

func cmdDeviceRepo(args []string) {
	if len(args) < 1 {
		fail("Usage: %s device repo <add|remove|list> ...", os.Args[0])
	}
	switch args[0] {
	case "add":
		cmdDeviceRepoAdd(args[1:])
	case "remove":
		cmdDeviceRepoRemove(args[1:])
	case "list":
		cmdDeviceRepoList(args[1:])
	default:
		fail("Unknown device repo subcommand: %s", args[0])
	}
}

func cmdDeviceRepoAdd(args []string) {
	fs := flag.NewFlagSet("device repo add", flag.ExitOnError)
	feedURL := fs.String("feed", "", "explicit feed URL")
	name := fs.String("name", "yoe-dev", "repo file basename")
	pushKey := fs.Bool("push-key", false, "copy project pubkey to /etc/apk/keys/")
	user := fs.String("user", "root", "ssh user (overridden by user@ in target)")
	fs.Parse(args)
	if fs.NArg() < 1 {
		fail("Usage: %s device repo add <[user@]host[:port]> [--feed URL] [--name N] [--push-key] [--user U]", os.Args[0])
	}
	target, err := device.ParseSSHTarget(fs.Arg(0), *user)
	if err != nil {
		fail("Error: %v", err)
	}

	url := *feedURL
	if url == "" {
		discovered, err := discoverFeed(*pushKey)
		if err != nil {
			fail("Error: %v", err)
		}
		url = discovered
	}

	in := device.RepoAddInput{Name: *name, FeedURL: url, Out: os.Stdout}

	if *pushKey {
		keyPath, keyName, err := stagePubKey()
		if err != nil {
			fail("Error: stage pubkey: %v", err)
		}
		defer os.Remove(keyPath)
		in.PushKeyFrom = keyPath
		in.PushKeyTo = "/etc/apk/keys/" + keyName
	}

	ops := device.RepoOps{SSH: device.DefaultSSH, SCP: device.DefaultSCP}
	if err := ops.Add(context.Background(), target, in); err != nil {
		fail("Error: %v", err)
	}
	fmt.Printf("configured %s -> %s on %s\n", *name, url, target.Host)
}

func cmdDeviceRepoRemove(args []string) {
	fs := flag.NewFlagSet("device repo remove", flag.ExitOnError)
	name := fs.String("name", "yoe-dev", "repo file basename")
	user := fs.String("user", "root", "ssh user (overridden by user@ in target)")
	fs.Parse(args)
	if fs.NArg() < 1 {
		fail("Usage: %s device repo remove <[user@]host[:port]> [--name N]", os.Args[0])
	}
	target, err := device.ParseSSHTarget(fs.Arg(0), *user)
	if err != nil {
		fail("Error: %v", err)
	}
	ops := device.RepoOps{SSH: device.DefaultSSH}
	if err := ops.Remove(context.Background(), target, *name, os.Stdout); err != nil {
		fail("Error: %v", err)
	}
}

func cmdDeviceRepoList(args []string) {
	fs := flag.NewFlagSet("device repo list", flag.ExitOnError)
	user := fs.String("user", "root", "ssh user (overridden by user@ in target)")
	fs.Parse(args)
	if fs.NArg() < 1 {
		fail("Usage: %s device repo list <[user@]host[:port]>", os.Args[0])
	}
	target, err := device.ParseSSHTarget(fs.Arg(0), *user)
	if err != nil {
		fail("Error: %v", err)
	}
	ops := device.RepoOps{SSH: device.DefaultSSH}
	if err := ops.List(context.Background(), target, os.Stdout, os.Stderr); err != nil {
		fail("Error: %v", err)
	}
}

// discoverFeed returns the URL of the unique _yoe-feed._tcp instance on the
// LAN that matches the current project. Errors if 0 or >1 results match.
// needProject is true when a project is required (e.g. --push-key); when
// false, falls back to all results when invoked outside a project.
func discoverFeed(needProject bool) (string, error) {
	results, err := feed.BrowseMDNS(1 * time.Second)
	if err != nil {
		return "", fmt.Errorf("mDNS browse: %w", err)
	}

	var projectName string
	if proj := tryLoadProject(); proj != nil {
		projectName = proj.Name
	} else if needProject {
		return "", fmt.Errorf("not in a yoe project — run from a project dir or pass --feed")
	}

	var matches []feed.MDNSResult
	for _, r := range results {
		if projectName != "" && r.Project != projectName {
			continue
		}
		matches = append(matches, r)
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no yoe feed discovered on the LAN — pass --feed URL")
	case 1:
		return matches[0].URL(), nil
	default:
		var hint []string
		for _, m := range matches {
			hint = append(hint, fmt.Sprintf("  %s -> %s", m.Instance, m.URL()))
		}
		return "", fmt.Errorf("found %d feeds:\n%s\npass --feed to disambiguate",
			len(matches), strings.Join(hint, "\n"))
	}
}

// stagePubKey writes the project's public key PEM to a temp file and
// returns the path plus the apk-style key name (e.g. "myproj.rsa.pub").
// Caller is responsible for os.Remove on the returned path.
func stagePubKey() (string, string, error) {
	proj := loadProject()
	signer, err := artifact.LoadOrGenerateSigner(proj.Name, proj.SigningKey)
	if err != nil {
		return "", "", err
	}
	f, err := os.CreateTemp("", signer.KeyName+".*")
	if err != nil {
		return "", "", err
	}
	if _, err := f.Write(signer.PubPEM); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", "", err
	}
	return f.Name(), signer.KeyName, nil
}
