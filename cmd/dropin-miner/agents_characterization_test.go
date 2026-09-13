package main

// The pre-registry characterization: what agents.go's three host switches
// (install, uninstall, status), its selection rule and the top-level help
// text do today, captured before any of it moves into targets.go. This
// file references nothing commit 2 introduces — no installTarget, no
// targetKind, no registry of any kind — only agentSurfaces and the
// switch-based functions that already exist. The refactor that follows
// must reproduce every plan, every status line and the rendered help
// byte-for-byte; this is the baseline that is checked against.
//
// goldenHostIDs and goldenLabels are literals, not derived from
// agentSurfaces: dropping or reordering a host must fail this file by
// diverging from the literal, not by silently characterizing a different
// set or sequence.

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite the characterization golden fixtures from the current behavior")

var (
	goldenHostIDs    = []string{"claude", "codex", "cursor", "opencode", "pi", "hermes"}
	goldenHostLabels = []string{"Claude Code", "Codex", "Cursor", "opencode", "Pi", "Hermes"}
)

const goldenBin = "/home/u/.tokendrop/bin/dropin-miner"

func goldenEntry() binEntry { return binEntry{command: goldenBin, cfg: testCfg} }

// requireGoldenSequence is the guard every characterization test in this
// file starts with: the registry-to-be has exactly these six hosts, in
// exactly this order, with exactly these labels. Every other test in this
// file assumes that and would otherwise be characterizing hosts that no
// longer match the literal it reports against.
func requireGoldenSequence(t *testing.T) {
	t.Helper()
	if len(agentSurfaces) != len(goldenHostIDs) {
		t.Fatalf("agentSurfaces has %d hosts, want the literal %d (%v): a host was added or removed",
			len(agentSurfaces), len(goldenHostIDs), goldenHostIDs)
	}
	for i, s := range agentSurfaces {
		if s.id != goldenHostIDs[i] || s.label != goldenHostLabels[i] {
			t.Fatalf("agentSurfaces[%d] = {%q,%q}, want {%q,%q} in this order",
				i, s.id, s.label, goldenHostIDs[i], goldenHostLabels[i])
		}
	}
}

// ── install / uninstall plan goldens ────────────────────────────────────

type goldenWrite struct {
	Surface string `json:"surface"`
	Path    string `json:"path"`
	Mode    uint32 `json:"mode"`
	Why     string `json:"why"`
	Content string `json:"content"`
}

type goldenPlan struct {
	Writes  []goldenWrite `json:"writes"`
	Removes []string      `json:"removes,omitempty"`
	Skipped []string      `json:"skipped,omitempty"`
	Refused []string      `json:"refused,omitempty"`
	Notes   []string      `json:"notes,omitempty"`
}

func capturePlan(p agentPlan) goldenPlan {
	g := goldenPlan{Removes: p.removes, Skipped: p.skipped, Refused: p.refused, Notes: p.notes}
	for _, w := range p.writes {
		g.Writes = append(g.Writes, goldenWrite{
			Surface: w.surface,
			Path:    slash(w.path),
			Mode:    uint32(w.mode),
			Why:     w.why,
			Content: string(w.contents),
		})
	}
	return g
}

func comparePlanGolden(t *testing.T, plan agentPlan, path string) {
	t.Helper()
	got := capturePlan(plan)
	gotJSON, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}
	gotJSON = append(gotJSON, '\n')
	if *updateGolden {
		if err := os.WriteFile(path, gotJSON, 0o644); err != nil {
			t.Fatalf("writing golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden %s: %v (run with -update-golden to create it)", path, err)
	}
	if string(want) != string(gotJSON) {
		t.Errorf("plan differs from %s (run with -update-golden to inspect/update):\n--- got ---\n%s\n--- want ---\n%s",
			path, gotJSON, want)
	}
}

// TestInstallPlanGoldenPerHost characterizes buildInstallPlan one host at a
// time, against a fixed binEntry and an empty in-memory machine (nothing
// pre-existing to merge with).
func TestInstallPlanGoldenPerHost(t *testing.T) {
	requireGoldenSequence(t)
	entry := goldenEntry()
	for _, id := range goldenHostIDs {
		t.Run(id, func(t *testing.T) {
			surface, ok := surfaceByID(id)
			if !ok {
				t.Fatalf("no agentSurface registered for %q", id)
			}
			_, ops := newFakeMachine()
			paths := ops.paths(noEnv)
			plan := buildInstallPlan(ops, paths, []agentSurface{surface}, entry, noEnv)
			comparePlanGolden(t, plan, filepath.Join("testdata", "agents", id+".install.golden"))
		})
	}
}

// TestUninstallPlanGoldenPerHost characterizes buildUninstallPlan against
// the state a real install for that host actually leaves behind — Codex's
// marked sandbox-note branch, Cursor and Claude's hook merges, Pi's two
// files, Hermes' hook block — rather than a hand-built fixture that could
// drift from what install truly writes.
func TestUninstallPlanGoldenPerHost(t *testing.T) {
	requireGoldenSequence(t)
	entry := goldenEntry()
	for _, id := range goldenHostIDs {
		t.Run(id, func(t *testing.T) {
			surface, ok := surfaceByID(id)
			if !ok {
				t.Fatalf("no agentSurface registered for %q", id)
			}
			_, ops := newFakeMachine()
			paths := ops.paths(noEnv)
			installPlan := buildInstallPlan(ops, paths, []agentSurface{surface}, entry, noEnv)
			if failures := commitPlan(ops, &installPlan, io.Discard, io.Discard); failures != 0 {
				t.Fatalf("committing the install %s's uninstall is characterized against: %d failures", id, failures)
			}
			plan := buildUninstallPlan(ops, paths, []agentSurface{surface}, entry)
			comparePlanGolden(t, plan, filepath.Join("testdata", "agents", id+".uninstall.golden"))
		})
	}
}

// ── status: exact states, table-driven ──────────────────────────────────

// statusOutputFor installs exactly one host into a fresh in-memory
// machine (or nothing, for the "absent" cases), optionally deletes some of
// what install wrote to produce a partial state, and returns agents
// status's full rendered output.
func statusOutputFor(t *testing.T, id string, install bool, remove func(agentPaths) []string) string {
	t.Helper()
	_, ops := newFakeMachine()
	paths := ops.paths(noEnv)
	entry := goldenEntry()
	if install {
		surface, ok := surfaceByID(id)
		if !ok {
			t.Fatalf("no agentSurface for %q", id)
		}
		plan := buildInstallPlan(ops, paths, []agentSurface{surface}, entry, noEnv)
		if failures := commitPlan(ops, &plan, io.Discard, io.Discard); failures != 0 {
			t.Fatalf("install %s: %d failures", id, failures)
		}
		if remove != nil {
			for _, p := range remove(paths) {
				if _, err := ops.readFile(p); err != nil {
					t.Fatalf("install did not write %s, so removing it proves nothing about a partial state", p)
				}
				_ = ops.removeAll(p)
			}
		}
	}
	var out bytes.Buffer
	printAgentStatus(ops, paths, entry, nil, &out)
	return out.String()
}

// TestAgentStatusExactStates enumerates, per host, absent, fully
// installed, and every partial state printAgentStatus's own switch
// currently distinguishes (Claude and Cursor: skill without hooks; Pi and
// Hermes: either half alone). A state the switch does not name — such as
// Claude's hooks surviving with no skill file — is not characterized here
// because the current code does not distinguish it either; it prints
// "not installed" the same as true absence.
func TestAgentStatusExactStates(t *testing.T) {
	requireGoldenSequence(t)
	for _, tc := range []struct {
		id, label, name string
		install         bool
		remove          func(agentPaths) []string
		want            string
	}{
		{"claude", "Claude Code", "absent", false, nil, "not installed"},
		{"claude", "Claude Code", "skill only", true, func(p agentPaths) []string { return []string{p.claudeSettings} }, "installed (skill only)"},
		{"claude", "Claude Code", "skill+hooks", true, nil, "installed (skill+hooks)"},

		{"codex", "Codex", "absent", false, nil, "not installed"},
		{"codex", "Codex", "installed", true, nil, "installed (skill)"},

		{"cursor", "Cursor", "absent", false, nil, "not installed"},
		{"cursor", "Cursor", "skill only", true, func(p agentPaths) []string { return []string{p.cursorHooks} }, "installed (skill only)"},
		{"cursor", "Cursor", "skill+hooks", true, nil, "installed (skill+hooks)"},

		{"opencode", "opencode", "absent", false, nil, "not installed"},
		{"opencode", "opencode", "installed", true, nil, "installed (plugin)"},

		{"pi", "Pi", "absent", false, nil, "not installed"},
		{"pi", "Pi", "skill only", true, func(p agentPaths) []string { return []string{p.piExtension} }, "installed (skill only)"},
		{"pi", "Pi", "extension only", true, func(p agentPaths) []string { return []string{p.piSkill} }, "installed (extension only)"},
		{"pi", "Pi", "skill+extension", true, nil, "installed (skill+extension)"},

		{"hermes", "Hermes", "absent", false, nil, "not installed"},
		{"hermes", "Hermes", "skill only", true, func(p agentPaths) []string { return []string{p.hermesConfig} }, "installed (skill only)"},
		{"hermes", "Hermes", "hook only", true, func(p agentPaths) []string { return []string{p.hermesSkill} }, "installed (hook only)"},
		{"hermes", "Hermes", "skill+hook", true, nil, "installed (skill+hook)"},
	} {
		t.Run(tc.id+"/"+tc.name, func(t *testing.T) {
			out := statusOutputFor(t, tc.id, tc.install, tc.remove)
			want := fmt.Sprintf("  %-12s %-12s %s\n", tc.label, "not on PATH", tc.want)
			if !strings.Contains(out, want) {
				t.Errorf("status missing %q in:\n%s", want, out)
			}
		})
	}
}

// ── selection semantics ──────────────────────────────────────────────────

// TestSelectSurfacesSemantics pins selectSurfaces' current rule, which A.2
// freezes for the registry: no -client means the detected hosts and
// nothing more; an explicit -client selects that host regardless of
// detection, because naming a host by id is a stronger signal than a PATH
// probe.
func TestSelectSurfacesSemantics(t *testing.T) {
	_, ops := newFakeMachine("claude")

	selected, detected, err := selectSurfaces(ops, nil)
	if err != nil {
		t.Fatalf("selectSurfaces(nil): %v", err)
	}
	if len(detected) != 1 || detected[0].id != "claude" {
		t.Fatalf("detected = %v, want just claude", detected)
	}
	if len(selected) != 1 || selected[0].id != "claude" {
		t.Fatalf("no -client should select exactly what was detected: %v", selected)
	}

	selected, _, err = selectSurfaces(ops, []string{"codex"})
	if err != nil {
		t.Fatalf("selectSurfaces([codex]): %v", err)
	}
	if len(selected) != 1 || selected[0].id != "codex" {
		t.Fatalf("-client codex must select codex even though it is not on PATH: %v", selected)
	}
}

// ── ordered sequence, asserted everywhere it is rendered ────────────────

// TestHostSequenceAndLabelsAreOrderedLiterally is A.2's "order is part of
// the contract", pinned against the rendered output of three different
// commands so a reorder cannot pass by accident in one of them.
func TestHostSequenceAndLabelsAreOrderedLiterally(t *testing.T) {
	requireGoldenSequence(t)

	_, ops := newFakeMachine(goldenHostIDs...)
	code, out, _ := runAgents(t, ops, nil, "install", "-config", testCfg, "-dry-run")
	if code != exitOK {
		t.Fatalf("install -dry-run: exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "agents: "+strings.Join(goldenHostLabels, ", ")) {
		t.Fatalf("the detected-agents line is not the registry order %v:\n%s", goldenHostLabels, out)
	}

	_, out, _ = runAgents(t, ops, nil, "status", "-config", testCfg)
	last := -1
	for _, label := range goldenHostLabels {
		idx := strings.Index(out, "  "+label+" ")
		if idx < 0 {
			t.Fatalf("status output is missing %q:\n%s", label, out)
		}
		if idx < last {
			t.Fatalf("status lists %q out of registry order:\n%s", label, out)
		}
		last = idx
	}
}

// ── the rendered help, byte for byte ─────────────────────────────────────

// TestHelpTextGolden captures usageText — exactly what the `help` command
// prints — before it becomes a template rendered from the registry. A.4
// requires the post-refactor render to reproduce this byte for byte.
func TestHelpTextGolden(t *testing.T) {
	path := filepath.Join("testdata", "help", "usage.golden")
	if *updateGolden {
		if err := os.WriteFile(path, []byte(usageText), 0o644); err != nil {
			t.Fatalf("writing golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden %s: %v (run with -update-golden to create it)", path, err)
	}
	if usageText != string(want) {
		t.Errorf("usageText differs from %s (run with -update-golden to inspect/update):\n--- got ---\n%s\n--- want ---\n%s",
			path, usageText, want)
	}
}
