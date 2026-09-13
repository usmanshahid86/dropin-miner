package selfupdate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeReplacementFixture(t *testing.T) (current, candidate, previous string) {
	t.Helper()
	dir := t.TempDir()
	current = filepath.Join(dir, "dropin-miner")
	candidate = filepath.Join(dir, ".dropin-miner.candidate-test")
	previous = current + ".previous"
	for path, body := range map[string]string{current: "old", candidate: "new", previous: "older"} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return current, candidate, previous
}

func fileBody(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path) // #nosec G304 -- test-owned temporary path
	if err != nil {
		return "<missing>"
	}
	return string(raw)
}

func TestPOSIXReplacementCommitsCandidateAndOnePrevious(t *testing.T) {
	current, candidate, previous := writeReplacementFixture(t)
	if err := replaceWithOps(current, candidate, ReplacePOSIX, defaultReplacementOps()); err != nil {
		t.Fatal(err)
	}
	if got := fileBody(t, current); got != "new" {
		t.Fatalf("current = %q", got)
	}
	if got := fileBody(t, previous); got != "old" {
		t.Fatalf("previous = %q", got)
	}
}

func TestPOSIXReplacementFailuresPreserveOrTruthfullyReportState(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		failRename                int
		failSync                  int
		wantCurrent, wantPrevious string
		wantChanged               bool
	}{
		{name: "candidate rename", failRename: 1, wantCurrent: "old", wantPrevious: "older"},
		{name: "candidate directory sync", failSync: 1, wantCurrent: "old", wantPrevious: "older"},
		{name: "previous rename", failRename: 2, wantCurrent: "old", wantPrevious: "older"},
		{name: "final directory sync", failSync: 2, wantCurrent: "new", wantPrevious: "old", wantChanged: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current, candidate, previous := writeReplacementFixture(t)
			ops := defaultReplacementOps()
			realRename, realSync := ops.renameReplace, ops.syncDir
			renames, syncs := 0, 0
			ops.renameReplace = func(from, to string) error {
				renames++
				if renames == tc.failRename {
					return errors.New("injected rename failure")
				}
				return realRename(from, to)
			}
			ops.syncDir = func(dir string) error {
				syncs++
				if syncs == tc.failSync {
					return errors.New("injected sync failure")
				}
				return realSync(dir)
			}
			err := replaceWithOps(current, candidate, ReplacePOSIX, ops)
			if err == nil {
				t.Fatal("injected failure reported success")
			}
			var state *ReplacementError
			if !errors.As(err, &state) || state.CanonicalChanged != tc.wantChanged {
				t.Fatalf("state = %#v", state)
			}
			if got := fileBody(t, current); got != tc.wantCurrent {
				t.Fatalf("current = %q, want %q", got, tc.wantCurrent)
			}
			if got := fileBody(t, previous); got != tc.wantPrevious {
				t.Fatalf("previous = %q, want %q", got, tc.wantPrevious)
			}
		})
	}
}

type memoryReplacementFS struct {
	files                  map[string]string
	newCalls, replaceCalls int
	failNew, failReplace   int
}

func (m *memoryReplacementFS) ops() replacementOps {
	return replacementOps{
		reserve: func(string) (string, error) { return "/d/displaced", nil },
		renameNew: func(from, to string) error {
			m.newCalls++
			if m.newCalls == m.failNew {
				return errors.New("injected new-name failure")
			}
			body, ok := m.files[from]
			if !ok {
				return os.ErrNotExist
			}
			if _, exists := m.files[to]; exists {
				return os.ErrExist
			}
			delete(m.files, from)
			m.files[to] = body
			return nil
		},
		renameReplace: func(from, to string) error {
			m.replaceCalls++
			if m.replaceCalls == m.failReplace {
				return errors.New("injected replace failure")
			}
			body, ok := m.files[from]
			if !ok {
				return os.ErrNotExist
			}
			delete(m.files, from)
			m.files[to] = body
			return nil
		},
		remove: func(path string) error { delete(m.files, path); return nil },
	}
}

func TestWindowsReplacementSuccessAndInjectedFailures(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		failNew, failReplace      int
		wantCurrent, wantPrevious string
		wantError                 bool
	}{
		{name: "success", wantCurrent: "new", wantPrevious: "old"},
		{name: "running rename fails", failNew: 1, wantCurrent: "old", wantPrevious: "older", wantError: true},
		{name: "candidate rename fails and restores", failNew: 2, wantCurrent: "old", wantPrevious: "older", wantError: true},
		{name: "previous commit fails and restores", failReplace: 1, wantCurrent: "old", wantPrevious: "older", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &memoryReplacementFS{files: map[string]string{
				"/d/dropin-miner": "old", "/d/candidate": "new", "/d/dropin-miner.previous": "older",
			}, failNew: tc.failNew, failReplace: tc.failReplace}
			err := replaceWithOps("/d/dropin-miner", "/d/candidate", ReplaceWindows, m.ops())
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v", err)
			}
			if m.files["/d/dropin-miner"] != tc.wantCurrent || m.files["/d/dropin-miner.previous"] != tc.wantPrevious {
				t.Fatalf("files = %#v", m.files)
			}
		})
	}
}

func TestReplacementCanImplementOneLevelRollbackSwap(t *testing.T) {
	current, candidate, previous := writeReplacementFixture(t)
	// A rollback first copies and validates .previous as its candidate. This
	// fixture models that staged copy without consuming .previous early.
	if err := os.WriteFile(candidate, []byte(fileBody(t, previous)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := replaceWithOps(current, candidate, ReplacePOSIX, defaultReplacementOps()); err != nil {
		t.Fatal(err)
	}
	if fileBody(t, current) != "older" || fileBody(t, previous) != "old" {
		t.Fatalf("rollback did not swap one level: current=%q previous=%q", fileBody(t, current), fileBody(t, previous))
	}
}
