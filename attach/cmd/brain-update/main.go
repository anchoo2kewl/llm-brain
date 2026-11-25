// brain-update installs stable desktop releases from the project's GitHub repository.
// Usage: brain-update <running version> <target>. An empty target only checks. The target is the Mac .app bundle,
// the Linux AppImage, or the path where Windows keeps the verified NSIS setup until the restart runs it.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const repo = "https://github.com/anchoo2kewl/llm-brain"
const maxBytes = 512 << 20

type asset struct {
	Name, Digest string
	URL          string `json:"browser_download_url"`
	Size         int64
}
type release struct {
	Tag               string `json:"tag_name"`
	Draft, Prerelease bool
	Assets            []asset
}

func version(s string) ([3]int, bool) {
	var v [3]int
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, e := strconv.Atoi(p)
		if e != nil || n < 0 || strconv.Itoa(n) != p {
			return v, false
		}
		v[i] = n
	}
	return v, true
}
func newer(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

// latest gives the newest published client release above installed. Drafts and prereleases never count:
// a release is visible to clients only once the release workflow publishes it.
func latest(releases []release, installed [3]int) (release, bool) {
	best, chosen := installed, release{}
	for _, r := range releases {
		if !strings.HasPrefix(r.Tag, "client-v") || r.Draft || r.Prerelease {
			continue
		}
		if v, ok := version(strings.TrimPrefix(r.Tag, "client-v")); ok && newer(v, best) {
			best, chosen = v, r
		}
	}
	return chosen, chosen.Tag != ""
}

// assetName is the release file for this system. Linux assets have a dot for the space in "Brain Hub".
func assetName(goos, goarch, next string) (string, error) {
	switch {
	case goos == "darwin":
		return "BrainHub-mac-" + next + ".zip", nil
	case goos == "linux" && goarch == "amd64":
		return "Brain Hub_" + next + "_amd64.AppImage", nil
	case goos == "windows" && goarch == "amd64":
		return "Brain Hub_" + next + "_x64-setup.exe", nil
	}
	return "", fmt.Errorf("no release build for %s/%s", goos, goarch)
}

// targetOK checks the install location. A Windows setup path may not exist yet.
func targetOK(goos, target string, st os.FileInfo, statErr error) bool {
	switch goos {
	case "darwin":
		return statErr == nil && st.IsDir() && filepath.Ext(target) == ".app"
	case "windows":
		return strings.EqualFold(filepath.Ext(target), ".exe") && (os.IsNotExist(statErr) || statErr == nil && st.Mode().IsRegular())
	}
	return statErr == nil && st.Mode().IsRegular()
}

func get(ctx context.Context, u string) (*http.Response, error) {
	req, e := http.NewRequestWithContext(ctx, "GET", u, nil)
	if e != nil {
		return nil, e
	}
	client := http.Client{CheckRedirect: func(r *http.Request, via []*http.Request) error {
		h := r.URL.Hostname()
		if len(via) > 5 || r.URL.Scheme != "https" || (h != "github.com" && !strings.HasSuffix(h, ".githubusercontent.com")) {
			return fmt.Errorf("unexpected release redirect")
		}
		return nil
	}}
	res, e := client.Do(req)
	if e != nil {
		return nil, e
	}
	if res.StatusCode != 200 {
		res.Body.Close()
		return nil, fmt.Errorf("release service: %s", res.Status)
	}
	return res, nil
}
func run(current, target string) (string, error) {
	installed, ok := version(current)
	if !ok {
		return "", fmt.Errorf("invalid installed version")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	res, e := get(ctx, "https://api.github.com/repos/anchoo2kewl/llm-brain/releases?per_page=100")
	if e != nil {
		return "", e
	}
	var releases []release
	e = json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&releases)
	res.Body.Close()
	if e != nil {
		return "", e
	}
	chosen, found := latest(releases, installed)
	if !found {
		return "You’re up to date.", nil
	}
	next := strings.TrimPrefix(chosen.Tag, "client-v")
	if target == "" {
		return "Version " + next + " is available: " + repo + "/releases/tag/" + chosen.Tag, nil
	}
	if !filepath.IsAbs(target) {
		return "", fmt.Errorf("invalid install location")
	}
	st, e := os.Lstat(target)
	if !targetOK(runtime.GOOS, target, st, e) {
		return "", fmt.Errorf("unsupported install location")
	}
	name, e := assetName(runtime.GOOS, runtime.GOARCH, next)
	if e != nil {
		return "", e
	}
	var file asset
	for _, a := range chosen.Assets {
		if a.Name == name || a.Name == strings.Replace(name, " ", ".", 1) {
			file = a
			break
		}
	}
	if file.Name == "" || file.Size <= 0 || file.Size > maxBytes {
		return "", fmt.Errorf("missing or invalid release asset for %s", next)
	}
	expected, e := hex.DecodeString(strings.TrimPrefix(file.Digest, "sha256:"))
	if !strings.HasPrefix(file.Digest, "sha256:") || e != nil || len(expected) != sha256.Size {
		return "", fmt.Errorf("release has no valid SHA-256 digest")
	}
	u, e := url.Parse(file.URL)
	if e != nil || u.Scheme != "https" || u.Host != "github.com" || u.Path != "/anchoo2kewl/llm-brain/releases/download/"+chosen.Tag+"/"+file.Name {
		return "", fmt.Errorf("unexpected download URL")
	}
	parent := filepath.Dir(target)
	unlock, e := lockDir(parent)
	if e != nil {
		return "", fmt.Errorf("another update is active: %w", e)
	}
	defer unlock()
	temp, e := os.MkdirTemp(parent, ".brain-update-")
	if e != nil {
		return "", fmt.Errorf("install directory is read-only; use the release download: %w", e)
	}
	defer os.RemoveAll(temp)
	path := filepath.Join(temp, "download")
	out, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if e != nil {
		return "", e
	}
	res, e = get(ctx, file.URL)
	if e != nil {
		out.Close()
		return "", e
	}
	hash := sha256.New()
	n, e := io.Copy(io.MultiWriter(out, hash), io.LimitReader(res.Body, file.Size+1))
	res.Body.Close()
	closeErr := out.Close()
	if e != nil || closeErr != nil || n != file.Size || hex.EncodeToString(hash.Sum(nil)) != hex.EncodeToString(expected) {
		return "", fmt.Errorf("download verification failed")
	}
	if e = install(path, target, next); e != nil {
		return "", e
	}
	if runtime.GOOS == "windows" {
		return "Downloaded " + next + ". Restart Brain Hub to install it.", nil
	}
	return "Installed " + next + ". Restart Brain Hub to use it.", nil
}
func main() {
	if len(os.Args) != 3 {
		os.Exit(2)
	}
	msg, e := run(os.Args[1], os.Args[2])
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	fmt.Println(msg)
}
