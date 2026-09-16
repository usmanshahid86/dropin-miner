package main

// PROBE ONLY — never merged. H3 needs two things measured on Windows:
//
//  1. A Cursor hook command that runs under cmd, Windows PowerShell 5.1 AND
//     pwsh (its runner is ruled, not observed), including adversarial paths.
//     A quoted first token is an expression in PowerShell; a leading & is a
//     syntax error in cmd; a bare path breaks on a space. So the candidates
//     below are measured rather than argued about.
//  2. A PowerShell bridge prefix that reaches the binary and does not leak
//     the variable into a later command in the same shell.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func probeHookForms(exe, cfg, event string) map[string]string {
	q := func(s string) string { return `"` + s + `"` }
	tail := fmt.Sprintf(" hook -config %s cursor %s", q(cfg), event)
	return map[string]string{
		"A-quoted-v029":    q(exe) + tail,
		"B-call-operator":  "& " + q(exe) + tail,
		"C-cmd-c-call":     "cmd /c call " + q(exe) + tail,
		"D-cmd-c-wrapped":  `cmd /c "` + q(exe) + tail + `"`,
		"E-bare-unquoted":  exe + " hook -config " + cfg + " cursor " + event,
		"F-ampersand-tick": "& '" + strings.ReplaceAll(exe, "'", "''") + "' hook -config '" + strings.ReplaceAll(cfg, "'", "''") + "' cursor " + event,
	}
}

// TestProbeCursorHookFormsOnWindows runs each candidate through each runner
// Cursor might use, for an ordinary path and for adversarial ones.
func TestProbeCursorHookFormsOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the Cursor hook runner question is Windows-only")
	}
	var report strings.Builder
	defer func() { t.Errorf("PROBE HOOK FORMS:\n%s", report.String()) }()

	payload, _ := json.Marshal(map[string]any{"conversation_id": "probe", "workspace_roots": []string{"C:\\Temp"}, "cwd": "C:\\Temp"})
	for _, dirName := range []string{"", "with space", "we ird & $tuff (x)'q", "pct % caret ^ bang !"} {
		in := newExecInstallationIn(t, dirName)
		forms := probeHookForms(in.bin, in.cfg, "sessionStart")
		for _, name := range []string{"A-quoted-v029", "B-call-operator", "C-cmd-c-call", "D-cmd-c-wrapped", "E-bare-unquoted", "F-ampersand-tick"} {
			for _, sh := range []execShell{shellCmdExe, shellWinPS, shellPwsh} {
				out := runInShell(t, sh, forms[name], payload, in.env)
				verdict := "NO ANSWER"
				if strings.Contains(out.stdout, `"env"`) {
					verdict = "RAN"
				}
				fmt.Fprintf(&report, "%-22q %-16s %-11s %s exit=%d %s\n", dirName, name, sh.name, verdict, out.exit, strings.TrimSpace(firstProbeLine(out.stderr)))
			}
		}
	}
}

// TestProbePowerShellBridgePrefix measures the bridge forms for a PowerShell
// host: does the binary receive the envelope, and does the variable leak into
// a second command in the same shell?
func TestProbePowerShellBridgePrefix(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PowerShell bridge forms are measured on Windows")
	}
	var report strings.Builder
	defer func() { t.Errorf("PROBE BRIDGE:\n%s", report.String()) }()

	for _, sh := range []execShell{shellWinPS, shellPwsh} {
		const bridge = "PROBEBRIDGEVALUE"
		// One installation per form: a shared router would count the previous
		// form's request and every row after the first would read wrong.
		formFor := func(in *execInstallation) map[string]string {
			_, search, err := searchBlockForShell(shellPowerShell, in.entry, `{"version":1,"query":"exact query text"}`)
			if err != nil {
				t.Fatal(err)
			}
			return map[string]string{
				"posix-assignment": "TOKENDROP_TRACE_BRIDGE=" + bridge + " " + search,
				"env-assignment":   "$env:TOKENDROP_TRACE_BRIDGE='" + bridge + "'\n" + search,
				"scoped-block":     "& { $env:TOKENDROP_TRACE_BRIDGE='" + bridge + "'; " + search + " }",
				"scoped-then-clear": "$env:TOKENDROP_TRACE_BRIDGE='" + bridge + "'\n" + search +
					"\nRemove-Item Env:TOKENDROP_TRACE_BRIDGE -ErrorAction SilentlyContinue",
			}
		}
		for _, name := range []string{"posix-assignment", "env-assignment", "scoped-block", "scoped-then-clear"} {
			in := newExecInstallation(t)
			script := formFor(in)[name] + "\nWrite-Output (\"LEAK=\" + [string]$env:TOKENDROP_TRACE_BRIDGE)"
			out := runInShell(t, sh, script, nil, in.env)
			got := in.router.received()
			reached := fmt.Sprintf("%d requests", len(got))
			if len(got) == 1 {
				reached = "1 request, trace=<nil>"
				if got[0].Trace != nil {
					reached = "1 request, harness=" + got[0].Trace.Harness
				}
			}
			leak := "LEAK not printed"
			for _, line := range strings.Split(out.stdout, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "LEAK=") {
					leak = strings.TrimSpace(line)
				}
			}
			fmt.Fprintf(&report, "%-11s %-18s exit=%d %s | %s | %s\n", sh.name, name, out.exit, reached, leak, strings.TrimSpace(firstProbeLine(out.stderr)))
		}
	}
}

func firstProbeLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	if len(s) > 120 {
		return s[:120]
	}
	return s
}

var _ = os.Getenv
var _ = filepath.Join
