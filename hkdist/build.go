package main

import (
	"bytes"
	"code.google.com/p/go-netrc/netrc"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

func mustHaveEnv(name string) {
	if os.Getenv(name) == "" {
		log.Fatal("need env: " + name)
	}
}

type Build struct {
	Repo   string
	Dir    string
	Branch string
	Name   string
	Arch   string
	OS     string
	ver    string
}

func (b *Build) Platform() string {
	return b.OS + "-" + b.Arch
}

func (b *Build) Run() error {
	if err := os.RemoveAll(b.Dir); err != nil {
		return err
	}
	if _, err := cmd("git", "clone", "-b", b.Branch, b.Repo, b.Dir); err != nil {
		return fmt.Errorf("clone failure: ", err)
	}

	if err := os.Chdir(b.Dir); err != nil {
		return err
	}
	err := b.cloneAndBuild()
	if err != nil {
		return err
	}
	body, err := os.Open(b.Name)
	if err != nil {
		return fmt.Errorf("open failure: %s", err)
	}

	h := sha256.New()
	if _, err := io.Copy(h, body); err != nil {
		return err
	}
	shasum := h.Sum(nil)

	_, err = body.Seek(int64(0), 0)
	if err != nil {
		return err
	}

	if err = b.upload(body); err != nil {
		return fmt.Errorf("upload failure: %s", err)
	}
	if err = b.register(shasum); err != nil {
		return fmt.Errorf("registration failure: %s", err)
	}
	if err = b.setCurVersion(); err != nil {
		return fmt.Errorf("release failure: %s", err)
	}
	if err := os.Chdir(".."); err != nil {
		return err
	}
	return nil
}

const relverGo = `
// +build release

package main
const Version = %q
`

func (b *Build) cloneAndBuild() (err error) {
	tagb, err := cmd("git", "describe")
	if err != nil {
		return fmt.Errorf("error listing tags: %s", err)
	}
	tag := string(bytes.TrimSpace(tagb))
	if tag[0] != 'v' {
		return fmt.Errorf("bad tag name: %s", tag)
	}
	ver := tag[1:]
	if strings.IndexFunc(ver, badVersionRune) >= 0 {
		return fmt.Errorf("bad tag name: %s", tag)
	}
	// TODO(kr): verify signature
	url := distURL + b.Name + "-" + ver + "-" + b.OS + "-" + b.Arch + ".json"
	if _, err := fetchBytes(url); err == nil {
		return fmt.Errorf("already built: %s", ver)
	}

	f, err := os.Create("relver.go")
	if err != nil {
		return fmt.Errorf("error writing relver.go: %s", err)
	}
	_, err = fmt.Fprintf(f, relverGo, ver)
	if err != nil {
		return fmt.Errorf("error writing relver.go: %s", err)
	}
	log.Printf("GOOS=%s GOARCH=%s go build -tags release -o %s\n", b.OS, b.Arch, b.Name)
	cmd := exec.Command("go", "build", "-tags", "release", "-o", b.Name)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append([]string{fmt.Sprintf("GOOS=%s", b.OS), fmt.Sprintf("GOARCH=%s", b.Arch)}, os.Environ()...)
	err = cmd.Run()
	if err != nil {
		return fmt.Errorf("go build -tags release: ", err)
	}
	b.ver = ver
	return nil
}

func (b *Build) upload(r io.Reader) error {
	buf := new(bytes.Buffer)
	gz, _ := gzip.NewWriterLevel(buf, gzip.BestCompression)
	gz.Name = b.Name + "-" + b.ver
	if _, err := io.Copy(gz, r); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}

	filename := b.Name + "-" + b.ver + "-" + b.OS + "-" + b.Arch + ".gz"
	if err := s3put(buf, s3DistURL+filename); err != nil {
		return err
	}
	return nil
}

func cmd(arg ...string) ([]byte, error) {
	log.Println(strings.Join(arg, " "))
	cmd := exec.Command(arg[0], arg[1:]...)
	cmd.Stderr = os.Stderr
	return cmd.Output()
}

func getCreds(u *url.URL) (user, pass string) {
	if u.User != nil {
		pw, _ := u.User.Password()
		return u.User.Username(), pw
	}

	m, err := netrc.FindMachine(netrcPath, u.Host)
	if err != nil {
		log.Fatalf("netrc error (%s): %v", u.Host, err)
	}

	return m.Login, m.Password
}

func (b *Build) register(sha256 []byte) error {
	url := distURL + b.Name + "-" + b.ver + "-" + b.OS + "-" + b.Arch + ".json"
	buf := new(bytes.Buffer)
	err := json.NewEncoder(buf).Encode(struct{ Sha256 []byte }{sha256})
	if err != nil {
		return err
	}
	r, err := http.NewRequest("PUT", url, buf)
	if err != nil {
		return err
	}
	r.SetBasicAuth(getCreds(r.URL))
	r.Header.Set("Date", time.Now().UTC().Format(http.TimeFormat))
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		return err
	}
	if resp.StatusCode != 201 {
		body, _ := ioutil.ReadAll(resp.Body)
		return fmt.Errorf("http status %v putting %q: %q", resp.Status, r.URL, string(body))
	}
	return nil
}

func (b *Build) setCurVersion() error {
	url := distURL + b.Name + "-" + b.Platform() + ".json"
	buf := new(bytes.Buffer)
	err := json.NewEncoder(buf).Encode(struct{ Version string }{b.ver})
	if err != nil {
		return err
	}
	r, err := http.NewRequest("PUT", url, buf)
	if err != nil {
		return err
	}
	r.SetBasicAuth(getCreds(r.URL))
	r.Header.Set("Date", time.Now().UTC().Format(http.TimeFormat))
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		body, _ := ioutil.ReadAll(resp.Body)
		return fmt.Errorf("http status %v putting %q: %q", resp.Status, r.URL, string(body))
	}
	return nil
}
