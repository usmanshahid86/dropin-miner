package main

// PROBE ONLY — never merged. H2 has to render a PowerShell search that
// carries arbitrary query text byte for byte on Windows PowerShell 5.1 and
// pwsh, with the query on stdin (H-R2) and adversarial paths in the command.
// H1b's dump showed 5.1's literal pipe adding a byte-order mark and
// replacing every non-ASCII UTF-16 unit with "?". This measures candidate
// forms rather than trusting the documentation about why.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// probeRequests are the payload kinds H2 must carry, including the
// adversarial one from the prompt file: an apostrophe, a double quote, a
// backslash, `'@` mid-line, Latin-1, CJK and an astral emoji.
func probeRequests() map[string]string {
	return map[string]string{
		"ascii":       `{"version":1,"query":"exact query text"}`,
		"latin1":      `{"version":1,"query":"café naïve Ärger"}`,
		"cjk":         `{"version":1,"query":"東京の天気"}`,
		"emoji":       `{"version":1,"query":"weather 😀 today"}`,
		"adversarial": `{"version":1,"query":"it's \"quoted\" a\\b '@ inline café 東京 😀"}`,
	}
}

func psSingle(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// psHereString is the single-quoted here-string: no expansion inside, and
// only a line that begins with '@ ends it.
func psHereString(s string) string { return "@'\n" + s + "\n'@" }

// probePSVariants are the candidate command shapes, each piping the request
// into the hex-dump helper.
func probePSVariants(helper string) map[string]func(request string) string {
	return map[string]func(string) string{
		"v1-literal-pipe": func(r string) string { return psSingle(r) + " | " + helper },
		"v2-herestring":   func(r string) string { return psHereString(r) + " | " + helper },
		"v3-utf8-new": func(r string) string {
			return "$OutputEncoding = [System.Text.UTF8Encoding]::new($false); " + psHereString(r) + " | " + helper
		},
		"v4-utf8-newobject": func(r string) string {
			return "$OutputEncoding = New-Object System.Text.UTF8Encoding $false; " + psHereString(r) + " | " + helper
		},
		"v5-console-and-output": func(r string) string {
			return "[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false); $OutputEncoding = [Console]::OutputEncoding; " + psHereString(r) + " | " + helper
		},
		"v6-encoding-utf8-property": func(r string) string {
			return "$OutputEncoding = [System.Text.Encoding]::UTF8; " + psHereString(r) + " | " + helper
		},
	}
}

func probeHelperFor(t *testing.T, sh execShell, bin string) string {
	t.Helper()
	args := []string{"-test.run=^TestHexDumpStdinHelperProcess$", "-test.count=1"}
	switch sh.kind {
	case shellCmd:
		out := `"` + bin + `"`
		for _, a := range args {
			out += ` "` + a + `"`
		}
		return out
	case shellPowerShell:
		out := "& " + psSingle(bin)
		for _, a := range args {
			out += " " + psSingle(a)
		}
		return out
	default:
		out := "'" + strings.ReplaceAll(bin, "'", `'\''`) + "'"
		for _, a := range args {
			out += " '" + a + "'"
		}
		return out
	}
}

func probeRunHex(t *testing.T, sh execShell, script string) (string, execOutcome) {
	t.Helper()
	out := runInShell(t, sh, script, nil, append(execEnv(), hexDumpHelperEnv+"=1"))
	for _, line := range strings.Split(out.stdout, "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, hexDumpMarker); ok {
			return after, out
		}
	}
	return "NONE", out
}

func TestProbePowerShellFormCarriesBytes(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the PowerShell form is measured on the Windows runners")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var report strings.Builder
	defer func() { t.Errorf("PROBE REPORT:\n%s", report.String()) }()
	for _, sh := range []execShell{shellWinPS, shellPwsh} {
		helper := probeHelperFor(t, sh, self)
		for name, request := range probeRequests() {
			fmt.Fprintf(&report, "%s %s want=%x\n", sh.name, name, request)
			for variant, build := range probePSVariants(helper) {
				got, out := probeRunHex(t, sh, build(request))
				verdict := "MANGLED"
				switch got {
				case fmt.Sprintf("%x", request):
					verdict = "EXACT"
				case fmt.Sprintf("%x", request+"\r\n"):
					verdict = "EXACT+CRLF"
				case "NONE":
					verdict = "NOHELPER exit=" + fmt.Sprint(out.exit) + " stderr=" + strings.TrimSpace(out.stderr)
				}
				fmt.Fprintf(&report, "  %-26s %-10s got=%s\n", variant, verdict, got)
			}
		}
	}
}

// TestProbeAdversarialPathQuoting: the same helper, run from a directory
// whose name carries everything H2's execution tests must survive.
func TestProbeAdversarialPathQuoting(t *testing.T) {
	var report strings.Builder
	defer func() { t.Errorf("PROBE PATH REPORT:\n%s", report.String()) }()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"we ird & $tuff (x)'q"}
	if runtime.GOOS == "windows" {
		names = append(names, "pct %25 caret ^ bang !", "we ird & $tuff (x)'q")
	}
	shells := []execShell{shellBash, shellSh, shellHermesArgv}
	if runtime.GOOS == "windows" {
		shells = []execShell{shellWinPS, shellPwsh, shellCmdExe, shellGitBash, shellHermesArgv}
	}
	for _, name := range names {
		dir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			fmt.Fprintf(&report, "mkdir %q: %v\n", name, err)
			continue
		}
		bin := filepath.Join(dir, exeName("probe-helper"))
		if err := os.WriteFile(bin, src, 0o700); err != nil { // #nosec G306 G703 -- the probe's own copy under its temp dir
			fmt.Fprintf(&report, "copy into %q: %v\n", name, err)
			continue
		}
		for _, sh := range shells {
			script := probeHelperFor(t, sh, bin)
			request := `{"version":1,"query":"exact query text"}`
			var stdin []byte
			if sh.kind == shellPowerShell {
				script = psHereString(request) + " | " + script
			} else {
				stdin = []byte(request)
			}
			out := runInShell(t, sh, script, stdin, append(execEnv(), hexDumpHelperEnv+"=1"))
			found := strings.Contains(out.stdout, hexDumpMarker)
			fmt.Fprintf(&report, "%-22s %-12s ran=%v exit=%d %s\n", name, sh.name, found, out.exit, strings.TrimSpace(firstLine(out.stderr)))
		}
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
