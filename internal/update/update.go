package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/orron/pano/internal/config"
)

// Default is the build-time switch. Packagers who must not ship a version
// check (Debian, Fedora, …) compile it out with
//
//	-ldflags '-X github.com/orron/pano/internal/update.Default=off'
//
// instead of patching the source.
var Default = "on"

// Repo is the GitHub repository whose releases are checked.
const Repo = "OrRon/pano"

// Interval is how long a check result is trusted before asking again.
const Interval = 24 * time.Hour

// EnvDisable turns the check off for one user or one shell.
const EnvDisable = "PANO_NO_UPDATE_CHECK"

// EnvDoNotTrack is the cross-tool opt-out (https://donottrack.sh). An update
// check is not telemetry, but a user who set it does not want tools calling
// home on their own, so pano honours it.
const EnvDoNotTrack = "DO_NOT_TRACK"

// Info is the result of a check.
type Info struct {
	// Current is the running version, Latest the newest release tag with the
	// leading "v" removed. Available is true when Latest is newer.
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Available bool   `json:"update_available"`
	// URL is the release page; Hint the upgrade command that matches how
	// this binary was installed (brew, go install, or the release page).
	URL  string `json:"url"`
	Hint string `json:"hint"`
}

// Options configure a check. Zero values are filled from the environment.
type Options struct {
	Current   string       // running version (cli.Version())
	StatePath string       // cache file; "" disables caching
	Force     bool         // ignore a fresh cache entry
	Exe       string       // executable path for Hint; default os.Executable()
	HTTP      *http.Client // default: direct (no proxy), 3 s timeout
	Getenv    func(string) string
	Now       func() time.Time
	Endpoint  string        // default https://api.github.com/repos/<Repo>/releases/latest
	Interval  time.Duration // default Interval
}

// state is the on-disk cache: when we last asked, and what we were told.
type state struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
	URL       string    `json:"url"`
}

var describeRE = regexp.MustCompile(`-\d+-g[0-9a-f]{6,}`)

// Disabled returns why an automatic check must not run, or "" when it may.
// It looks at the build, the environment and the config file — everything
// but the terminal and the command, which the caller knows better.
func Disabled(current string, getenv func(string) string, paths config.Paths) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	if Default != "on" {
		return "compiled out"
	}
	if IsDev(current) {
		return "development build"
	}
	if v := getenv(EnvDisable); v != "" && v != "0" {
		return EnvDisable + " is set"
	}
	if v := getenv(EnvDoNotTrack); v != "" && v != "0" {
		return EnvDoNotTrack + " is set"
	}
	if getenv("CI") != "" {
		return "running in CI"
	}
	if paths.Dir != "" {
		cfg, err := config.Load(paths)
		if err != nil || !cfg.Updates.Check {
			return "[updates] check = false"
		}
	}
	return ""
}

// IsDev reports whether v is a local or untagged build: "dev", "(devel)",
// anything git-describe produced past a tag ("v0.3.0-4-gabc1234", "…-dirty").
func IsDev(v string) bool {
	switch v {
	case "", "dev", "(devel)":
		return true
	}
	return strings.HasSuffix(v, "-dirty") || describeRE.MatchString(v)
}

// Check asks for the latest release, honouring the cache, and reports whether
// it is newer than o.Current. A network failure is an error the caller should
// swallow: it must never turn into output on a working command.
func Check(ctx context.Context, o Options) (*Info, error) {
	o = o.defaults()
	now := o.Now()
	st, _ := readState(o.StatePath)
	fresh := st != nil && st.Latest != "" && now.Sub(st.CheckedAt) < o.Interval && now.After(st.CheckedAt)
	if !fresh || o.Force {
		latest, url, err := fetchLatest(ctx, o)
		if err != nil {
			return nil, err
		}
		st = &state{CheckedAt: now, Latest: latest, URL: url}
		if o.StatePath != "" {
			_ = writeState(o.StatePath, *st)
		}
	}
	info := &Info{
		Current:   strings.TrimPrefix(o.Current, "v"),
		Latest:    strings.TrimPrefix(st.Latest, "v"),
		URL:       st.URL,
		Available: Newer(st.Latest, o.Current),
	}
	info.Hint = Hint(o.Exe, o.Getenv)
	return info, nil
}

func (o Options) defaults() Options {
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Interval == 0 {
		o.Interval = Interval
	}
	if o.Endpoint == "" {
		o.Endpoint = "https://api.github.com/repos/" + Repo + "/releases/latest"
	}
	if o.HTTP == nil {
		// Proxy: nil — never through the system proxy, which may be pano.
		o.HTTP = &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	}
	if o.Exe == "" {
		if exe, err := os.Executable(); err == nil {
			if r, err := filepath.EvalSymlinks(exe); err == nil {
				exe = r
			}
			o.Exe = exe
		}
	}
	return o
}

func fetchLatest(ctx context.Context, o Options) (tag, url string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.Endpoint, http.NoBody)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "pano/"+strings.TrimPrefix(o.Current, "v")+" (update check)")
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("update: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", "", errors.New("update: no releases published yet")
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("update: %s: %s", o.Endpoint, resp.Status)
	}
	var rel struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<20)).Decode(&rel); err != nil {
		return "", "", fmt.Errorf("update: decode: %w", err)
	}
	if rel.TagName == "" {
		return "", "", errors.New("update: release has no tag")
	}
	return rel.TagName, rel.HTMLURL, nil
}

func readState(path string) (*state, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var st state
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func writeState(path string, st state) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return config.WriteAtomic(path, b, 0o600)
}

// Hint returns the upgrade command for the way exe was installed: a Homebrew
// Caskroom/Cellar path → brew, a Go bin dir → go install, anything else →
// the release page.
func Hint(exe string, getenv func(string) string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	exe = filepath.ToSlash(exe)
	if strings.Contains(exe, "/Caskroom/") || strings.Contains(exe, "/Cellar/") {
		return "brew upgrade pano"
	}
	goBins := []string{getenv("GOBIN")}
	if gp := getenv("GOPATH"); gp != "" {
		for _, p := range filepath.SplitList(gp) {
			goBins = append(goBins, filepath.Join(p, "bin"))
		}
	} else if home := getenv("HOME"); home != "" {
		goBins = append(goBins, filepath.Join(home, "go", "bin"))
	}
	for _, b := range goBins {
		if b != "" && filepath.ToSlash(filepath.Dir(exe)) == filepath.ToSlash(filepath.Clean(b)) {
			return "go install github.com/orron/pano/cmd/pano@latest"
		}
	}
	return "https://github.com/" + Repo + "/releases/latest"
}

// Newer reports whether semantic version a is newer than b. Tags may carry a
// leading "v"; a pre-release ("-beta.1") sorts before its release; anything
// unparsable is never newer.
func Newer(a, b string) bool {
	va, oka := parse(a)
	vb, okb := parse(b)
	if !oka || !okb {
		return false
	}
	return va.compare(vb) > 0
}

type semver struct {
	nums [3]int
	pre  string
}

func parse(s string) (semver, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	var v semver
	if i := strings.IndexByte(s, '-'); i >= 0 {
		v.pre = s[i+1:]
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v.nums[i] = n
	}
	return v, true
}

func (v semver) compare(o semver) int {
	for i := range v.nums {
		if v.nums[i] != o.nums[i] {
			if v.nums[i] > o.nums[i] {
				return 1
			}
			return -1
		}
	}
	switch {
	case v.pre == o.pre:
		return 0
	case v.pre == "":
		return 1
	case o.pre == "":
		return -1
	}
	return comparePre(v.pre, o.pre)
}

// comparePre orders pre-release identifiers per semver §11: numeric parts
// numerically, others lexically, fewer parts first.
func comparePre(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, aerr := strconv.Atoi(as[i])
		bn, berr := strconv.Atoi(bs[i])
		switch {
		case aerr == nil && berr == nil:
			if an != bn {
				if an > bn {
					return 1
				}
				return -1
			}
		case aerr == nil:
			return -1
		case berr == nil:
			return 1
		default:
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
	}
	switch {
	case len(as) > len(bs):
		return 1
	case len(as) < len(bs):
		return -1
	}
	return 0
}

// Checker runs Check in the background so a command's own work is never
// delayed by the network.
type Checker struct {
	done chan struct{}
	info *Info
}

// Start begins a check; the result is read with Result or Wait.
func Start(ctx context.Context, o Options) *Checker {
	c := &Checker{done: make(chan struct{})}
	go func() {
		defer close(c.done)
		if info, err := Check(ctx, o); err == nil {
			c.info = info
		}
	}()
	return c
}

// Result returns the outcome if the check has finished, else nil. Safe on a
// nil Checker.
func (c *Checker) Result() *Info {
	if c == nil {
		return nil
	}
	select {
	case <-c.done:
		return c.info
	default:
		return nil
	}
}

// Wait blocks up to d for the check to finish and returns Result. Safe on a
// nil Checker.
func (c *Checker) Wait(d time.Duration) *Info {
	if c == nil {
		return nil
	}
	select {
	case <-c.done:
	case <-time.After(d):
	}
	return c.Result()
}
