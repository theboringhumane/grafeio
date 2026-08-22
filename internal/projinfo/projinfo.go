// projinfo.go — the current directory's project name and git branch, for the
// top bar's right segment:
//
//	Project — basename of `git rev-parse --show-toplevel`
//	          (fallback: basename of dir itself when git can't answer)
//	Branch  — `git rev-parse --abbrev-ref HEAD`; detached HEAD stands the
//	          short SHA in for a name; "" on any git error (not a repo,
//	          git missing, timeout)
//
// Pure stdlib. Every git call is shelled out under an ~800ms timeout so a
// wedged repo can never stall a render.
package projinfo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Info is one directory's project identity.
type Info struct {
	Project string // repo toplevel basename, or dir basename as fallback
	Branch  string // branch name or short SHA; "" when git can't say
}

// gitTimeout caps every git invocation — a frame must never wait on git.
const gitTimeout = 800 * time.Millisecond

// errNoRepo marks "git answered but there is no repo here".
var errNoRepo = errors.New("projinfo: not a git repository")

// execGit is the test seam: run `git args...` in dir bounded by ctx and
// return trimmed stdout. Tests swap it wholesale to fake git.
var execGit = func(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// git runs one bounded git call; error (and "") on any failure.
func git(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	return execGit(ctx, dir, args...)
}

// resolveDir maps "" to the process working directory.
func resolveDir(dir string) string {
	if dir != "" {
		return dir
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return dir
}

// current is the error-reporting core of Current: err is non-nil when the
// repo probe failed (not a repo, git missing, timeout) so Cache can tell a
// real answer apart from fallback noise. The returned Info is still the
// best-effort fallback either way.
func current(dir string) (Info, error) {
	dir = resolveDir(dir)
	info := Info{Project: filepath.Base(dir)}

	top, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil || top == "" {
		if err == nil {
			err = errNoRepo
		}
		return info, err
	}
	info.Project = filepath.Base(top)

	switch br, berr := git(dir, "rev-parse", "--abbrev-ref", "HEAD"); {
	case berr != nil || br == "":
		// leave Branch "" — unborn HEAD, dubious ownership, whatever git said
	case br == "HEAD": // detached: the short SHA stands in for a branch name
		if sha, serr := git(dir, "rev-parse", "--short", "HEAD"); serr == nil {
			info.Branch = sha
		}
	default:
		info.Branch = br
	}
	return info, nil
}

// Current resolves dir's project identity, best-effort: every git failure
// silently degrades to the fallback Project and an empty Branch.
func Current(dir string) Info {
	info, _ := current(dir)
	return info
}

// Cache memoizes Current per directory so the top bar — recomputed every
// frame — hits git at most once per TTL per dir.
type Cache struct {
	ttl   time.Duration
	mu    sync.Mutex
	items map[string]entry
}

type entry struct {
	info    Info
	fetched time.Time
}

// DefaultTTL is how stale a cached project/branch may get between git calls.
const DefaultTTL = 5 * time.Second

// NewCache returns a Cache that refresh-execs a directory at most once per
// ttl. A non-positive ttl means DefaultTTL.
func NewCache(ttl time.Duration) *Cache {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Cache{ttl: ttl, items: map[string]entry{}}
}

// DefaultCache is the house default: 5s TTL.
func DefaultCache() *Cache { return NewCache(DefaultTTL) }

// Get returns dir's cached Info. Past the TTL it refreshes once; a failed
// refresh keeps the last good Info for that dir (stale-on-error) and notes
// the attempt time so a broken repo isn't re-probed every frame.
func (c *Cache) Get(dir string) Info {
	key := resolveDir(dir)

	c.mu.Lock()
	defer c.mu.Unlock()

	if e, ok := c.items[key]; ok && time.Since(e.fetched) < c.ttl {
		return e.info
	}
	info, err := current(key)
	if err != nil {
		if e, ok := c.items[key]; ok {
			e.fetched = time.Now() // stale, but don't hammer a broken repo
			c.items[key] = e
			return e.info
		}
	}
	c.items[key] = entry{info: info, fetched: time.Now()}
	return info
}
