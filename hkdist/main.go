// Command hkdist provides services for distributing hk binaries and updates.
//
// It has three sub-commands: build, web, and gen.
//
//   $ hkdist build [all|<platform> ...]
//
// This command builds cross-compiled binaries. For the current platform (or
// all specified platforms), it first fetches source code from github, builds a
// binary executable, uploads the binary to an S3 bucket, and posts its SHA-256
// hash to the hk distribution server (hk.heroku.com in production).
//
//   $ hkdist web
//
// This command provides directory service for hk binary hashes.
//
//   $ hkdist gen
//
// This command polls the distribution server to learn about new releases,
// then generates byte-sequence patches between each pair of releases on
// each platform. It puts these patches in an S3 bucket so the hk client
// can use them for self-update instead of downloading a (much larger) full
// release.
package main

import (
	"fmt"
	"github.com/kr/s3"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var (
	distURL    = os.Getenv("DISTURL")
	s3DistURL  = os.Getenv("S3DISTURL")
	s3PatchURL = os.Getenv("S3PATCHURL")
	buildName  = os.Getenv("BUILDNAME")
	netrcPath  = filepath.Join(os.Getenv("HOME"), ".netrc")
	branch     = os.Getenv("BUILDBRANCH")
	s3keys     = s3.Keys{
		AccessKey: os.Getenv("S3_ACCESS_KEY"),
		SecretKey: os.Getenv("S3_SECRET_KEY"),
	}
)

type release struct {
	Plat, Cmd, Ver string
	Sha256         []byte
}

func (r release) Name() string {
	return r.Cmd + "-" + r.Ver + "-" + r.Plat
}

func (r release) Gzname() string {
	return r.Name() + ".gz"
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage: hkdist (web|gen|build [all|<platform> ...])")
	os.Exit(2)
}

var allPlatforms = []string{
	"darwin-386",
	"darwin-amd64",
	"freebsd-386",
	"freebsd-amd64",
	"freebsd-arm",
	"linux-386",
	"linux-amd64",
	"linux-arm",
	"windows-386",
	"windows-amd64",
}

func main() {
	log.SetFlags(log.Lshortfile)
	if len(os.Args) < 2 {
		usage()
	} else if os.Args[1] != "build" && len(os.Args) != 2 {
		usage()
	}
	switch os.Args[1] {
	case "gen":
		gen()
	case "web":
		web()
	case "build":
		mustHaveEnv("S3DISTURL")
		mustHaveEnv("S3_ACCESS_KEY")
		mustHaveEnv("S3_SECRET_KEY")
		mustHaveEnv("DISTURL")
		mustHaveEnv("BUILDBRANCH")
		mustHaveEnv("BUILDNAME")

		platforms := make([]string, 0)
		if len(os.Args) > 2 {
			if os.Args[2] == "all" {
				platforms = allPlatforms
			} else {
				list := os.Args[2:]
				for _, platform := range list {
					if !isValidPlatform(platform) {
						fmt.Fprintln(os.Stderr, "Allowed platforms:", strings.Join(allPlatforms, " "))
						usage()
					}
				}
				platforms = list
			}
		} else {
			platforms = []string{runtime.GOOS + "-" + runtime.GOARCH}
		}
		// run Build for each platform
		for _, platform := range platforms {
			sepIndex := strings.Index(platform, "-")
			b := &Build{
				Repo:   "https://github.com/kr/hk.git",
				Dir:    "hk",
				Branch: branch,
				Name:   buildName,
				OS:     platform[:sepIndex],
				Arch:   platform[sepIndex+1:],
			}
			err := b.Run()
			if err != nil {
				log.Printf("Error building %s on %s for %s-%s: %s", b.Name, b.Branch, b.OS, b.Arch, err)
			}
		}
	}
}

func isValidPlatform(platform string) bool {
	for _, allowed := range allPlatforms {
		if allowed == platform {
			return true
		}
	}
	return true
}
