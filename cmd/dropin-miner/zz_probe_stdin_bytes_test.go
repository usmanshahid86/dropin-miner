package main

// PROBE ONLY — never merged. Dumps the exact bytes each real shell delivers
// to a native program's stdin, for four payload kinds, so H1's
// characterization can record what actually happens instead of a guess.
//
// It prints with fmt.Printf rather than t.Log because CI runs `go test`
// without -v: direct writes reach the job log even when the test passes,
// and a passing probe does not trip the matrix's fail-fast, so both Windows
// runners report.

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
)

const probeHelperEnv = "DROPIN_MINER_PROBE_HEXDUMP"

// TestProbeHexDumpStdinHelperProcess is not a test: it is the program the
// probe runs in each shell. It writes its stdin back as hex.
func TestProbeHexDumpStdinHelperProcess(t *testing.T) {
	if os.Getenv(probeHelperEnv) != "1" {
		t.Skip("helper process")
	}
	b, _ := io.ReadAll(os.Stdin)
	fmt.Printf("PROBEHEX:%x\n", b)
}

func probeHelperArgs(bin string, sh execShell) string {
	run := "-test.run=^TestProbeHexDumpStdinHelperProcess$"
	switch sh.kind {
	case shellPowerShell:
		// Every argument quoted: an unquoted -test.count=1 reached the helper
		// as "-test" on the first probe run.
		return "& '" + strings.ReplaceAll(bin, "'", "''") + "' '" + run + "' '-test.count=1'"
	case shellCmd:
		return `"` + bin + `" "` + run + `" "-test.count=1"`
	default:
		return "'" + strings.ReplaceAll(bin, "'", `'\''`) + "' '" + run + "' '-test.count=1'"
	}
}

func TestProbeStdinBytesPerShell(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var report strings.Builder
	defer func() { t.Errorf("PROBE REPORT (this test always fails, so the log shows it):\n%s", report.String()) }()
	fmt.Fprintf(&report, "os=%s\n", runtime.GOOS)
	payloads := []struct{ name, query string }{
		{"ascii", "exact query text"},
		{"latin1", "café naïve Ärger"},
		{"cjk", "東京の天気"},
		{"emoji", "weather 😀 today"},
	}
	shells := []execShell{shellBash, shellSh, shellHermesArgv}
	if runtime.GOOS == "windows" {
		shells = []execShell{shellWinPS, shellPwsh, shellCmdExe, shellGitBash, shellHermesArgv}
	}
	for _, p := range payloads {
		request := `{"version":1,"query":"` + p.query + `"}`
		fmt.Fprintf(&report, "%s want=%x\n", p.name, request)
		for _, sh := range shells {
			for _, delivery := range []string{"inherited", "literal"} {
				if delivery == "literal" && sh.kind != shellPowerShell {
					continue // the in-script pipe is the PowerShell question
				}
				script := probeHelperArgs(self, sh)
				var stdin []byte
				if delivery == "literal" {
					script = "'" + strings.ReplaceAll(request, "'", "''") + "' | " + script
				} else {
					stdin = []byte(request)
				}
				out := runInShell(t, sh, script, stdin, append(execEnv(), probeHelperEnv+"=1"))
				got := "NO PROBEHEX LINE"
				for _, line := range strings.Split(out.stdout, "\n") {
					if strings.HasPrefix(strings.TrimSpace(line), "PROBEHEX:") {
						got = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "PROBEHEX:"))
					}
				}
				fmt.Fprintf(&report, "%s %s %s exit=%d got=%s\n", p.name, sh.name, delivery, out.exit, got)
				if got == "NO PROBEHEX LINE" {
					fmt.Fprintf(&report, "%s %s %s stderr=%q stdout=%q\n", p.name, sh.name, delivery, out.stderr, out.stdout)
				}
			}
		}
	}
	// Also: what does PowerShell say about its own output encoding?
	if runtime.GOOS == "windows" {
		for _, sh := range []execShell{shellWinPS, shellPwsh} {
			out := runInShell(t, sh, `Write-Output ("OutputEncoding=" + $OutputEncoding.WebName + " preamble=" + ([System.BitConverter]::ToString($OutputEncoding.GetPreamble())) + " console=" + [Console]::OutputEncoding.WebName + " version=" + $PSVersionTable.PSVersion.ToString())`, nil, execEnv())
			fmt.Fprintf(&report, "encoding %s exit=%d out=%s\n", sh.name, out.exit, strings.TrimSpace(out.stdout))
		}
	}
}
