// projinfo_test.go — the Current contract: resolve the repo toplevel project
// and branch (short SHA when detached), degrade silently everywhere git
// can't answer; and the Cache contract: exec at most once per TTL per dir,
// stale-on-error, never hammering a broken repo.
package projinfo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not on PATH: %v", err)
	}
}

// initRepo turns dir into a real repo on branch main with one commit.
func initRepo(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "seed.txt")
	run("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "seed")
}

func TestCurrent(t *testing.T) {
	needGit(t)

	t.Run("plain dir is not a repo", func(t *testing.T) {
		dir := t.TempDir()
		info := Current(dir)
		if want := filepath.Base(dir); info.Project != want {
			t.Errorf("Project = %q, want dir basename %q", info.Project, want)
		}
		if info.Branch != "" {
			t.Errorf("Branch = %q, want empty outside a repo", info.Branch)
		}
	})

	t.Run("real repo resolves toplevel project and branch", func(t *testing.T) {
		dir := t.TempDir()
		initRepo(t, dir)
		info := Current(dir)
		if want := filepath.Base(dir); info.Project != want {
			t.Errorf("Project = %q, want toplevel basename %q", info.Project, want)
		}
		if info.Branch != "main" {
			t.Errorf("Branch = %q, want main", info.Branch)
		}
	})

	t.Run("nested dir still reports the repo root project", func(t *testing.T) {
		dir := t.TempDir()
		initRepo(t, dir)
		sub := filepath.Join(dir, "a", "b")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		info := Current(sub)
		if want := filepath.Base(dir); info.Project != want {
			t.Errorf("Project = %q, want repo root %q", info.Project, want)
		}
		if info.Branch != "main" {
			t.Errorf("Branch = %q, want main", info.Branch)
		}
	})

	t.Run("detached HEAD stands in the short SHA", func(t *testing.T) {
		dir := t.TempDir()
		initRepo(t, dir)
		run := func(args ...string) string {
			cmd := exec.Command("git", args...)
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
			}
			return strings.TrimSpace(string(out))
		}
		run("checkout", "--detach", "HEAD")
		sha := run("rev-parse", "--short", "HEAD")
		if info := Current(dir); info.Branch != sha {
			t.Errorf("Branch = %q, want short SHA %q when detached", info.Branch, sha)
		}
	})

	t.Run("missing dir degrades to its own basename", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "nope")
		info := Current(missing)
		if info.Project != "nope" {
			t.Errorf("Project = %q, want %q", info.Project, "nope")
		}
		if info.Branch != "" {
			t.Errorf("Branch = %q, want empty for a missing dir", info.Branch)
		}
	})

	t.Run("empty dir means the working directory", func(t *testing.T) {
		wd, err := os.Getwd()
		if err != nil {
			t.Skip(err)
		}
		out, err := exec.Command("git", "-C", wd, "rev-parse", "--show-toplevel").Output()
		if err != nil {
			t.Skipf("package dir not inside a repo: %v", err)
		}
		if want := filepath.Base(strings.TrimSpace(string(out))); Current("").Project != want {
			t.Errorf("Project = %q, want %q (working-dir fallback)", Current("").Project, want)
		}
	})
}

func TestCacheMaxOneExecPerTTLPerDir(t *testing.T) {
	dir := t.TempDir()

	orig := execGit
	t.Cleanup(func() { execGit = orig })

	var fail atomic.Bool
	var calls atomic.Int32
	execGit = func(_ context.Context, _ string, args ...string) (string, error) {
		calls.Add(1)
		if fail.Load() {
			return "", errors.New("git exploded")
		}
		switch {
		case len(args) == 2 && args[1] == "--show-toplevel":
			return dir, nil
		case len(args) == 3 && args[1] == "--abbrev-ref":
			return "main", nil
		default:
			return "", errors.New("unexpected git args: " + strings.Join(args, " "))
		}
	}

	c := NewCache(60 * time.Millisecond)
	want := Info{Project: filepath.Base(dir), Branch: "main"}

	if got := c.Get(dir); got != want {
		t.Fatalf("first Get = %+v, want %+v", got, want)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("first Get ran %d git execs, want 2 (toplevel + branch)", n)
	}

	if got := c.Get(dir); got != want {
		t.Fatalf("cached Get = %+v, want %+v", got, want)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("second Get within TTL ran git again (calls=%d, want 2)", n)
	}

	time.Sleep(80 * time.Millisecond)
	if got := c.Get(dir); got != want {
		t.Fatalf("refreshed Get = %+v, want %+v", got, want)
	}
	if n := calls.Load(); n != 4 {
		t.Fatalf("post-TTL Get must refresh exactly once (calls=%d, want 4)", n)
	}

	// Git breaks: the refresh keeps the last good Info, and the failure is
	// memoized too — a broken repo must not be re-probed every frame.
	fail.Store(true)
	time.Sleep(80 * time.Millisecond)
	if got := c.Get(dir); got != want {
		t.Fatalf("failed refresh = %+v, want stale %+v", got, want)
	}
	if n := calls.Load(); n != 5 { // toplevel probe fails -> single exec
		t.Fatalf("failed refresh ran %d git execs total, want 5", n)
	}
	if got := c.Get(dir); got != want {
		t.Fatalf("post-failure Get = %+v, want stale %+v", got, want)
	}
	if n := calls.Load(); n != 5 {
		t.Fatalf("TTL must apply to failures too (calls=%d, want 5)", n)
	}
}

func TestDefaultCacheTTL(t *testing.T) {
	if DefaultCache().ttl != 5*time.Second {
		t.Fatalf("DefaultCache TTL = %v, want 5s", DefaultCache().ttl)
	}
}
