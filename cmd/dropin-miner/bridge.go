package main

// The trace bridge, put on a command in the syntax of the shell that runs it.
//
// A lineage adapter hands the host back the same command with one thing
// added: the envelope, in an environment variable the binary reads. v0.2.9
// wrote one syntax for every host — `TOKENDROP_TRACE_BRIDGE=<b> <cmd>`, which
// is POSIX — so on Windows, where opencode runs PowerShell, the prefix was
// looked up as a program name and the search did not run at all (#68). The
// syntax now comes from the host's declared tool shell, and for Claude Code
// from the tool the payload names, because that host runs two.
//
// H-R4, provenance. Only a bridge THIS adapter generated for THIS call may
// carry this adapter's harness, so an adapter never stands down because a
// bridge is already on the command: a model can write one itself, and #68 is
// what that looks like at the router — a trace the model assembled, credited
// to us. Every assignment the adapter RECOGNIZES is removed and its own is
// prepended.
//
// "Recognized" is a syntactically standalone assignment statement in a
// declared shell's syntax and nothing else: a leading NAME=value word in
// POSIX, a standalone `$env:NAME = …` statement in PowerShell, `set NAME=…`
// as its own command in cmd. Never a substring inside quoted text, inside
// another argument, or inside the JSON request body — a query that happens to
// mention the variable is a query, not a bridge. A command still carrying one
// this cannot prove standalone is left exactly as it was found, with no
// lineage claimed for it.
//
// The binary cannot authenticate an environment variable; the client trace
// stays unauthenticated metadata. What this protects is the meaning of the
// harness field.

import (
	"regexp"
	"strings"
)

// bridgeAssignmentRe matches one standalone bridge assignment at the START of
// a command, in each declared shell's syntax. It is pinned to the JavaScript
// copy in agent_trace_common.js by TestBridgeGuardsAgree.
var bridgeAssignmentRe = regexp.MustCompile(
	`(?i)^(?:` +
		// POSIX: NAME=value as a leading word.
		bridgeEnv + `=[^\s]*\s+` +
		`|` +
		// PowerShell: $env:NAME = … as its own statement.
		`\$env:` + bridgeEnv + `\s*=\s*(?:'[^']*'|"[^"]*"|[^\s;]*)\s*[;\n]\s*` +
		`|` +
		// cmd: set NAME=value as its own command.
		`set\s+` + bridgeEnv + `=[^&\n]*(?:&+|\n)\s*` +
		`)`)

// stripBridgeAssignments removes every bridge assignment provably standalone
// at the front of the command.
func stripBridgeAssignments(cmd string) string {
	for {
		loc := bridgeAssignmentRe.FindStringIndex(cmd)
		if loc == nil {
			return cmd
		}
		cmd = cmd[loc[1]:]
	}
}

// carriesUnremovableBridge reports whether the command still mentions the
// bridge somewhere this cannot prove is a standalone assignment.
func carriesUnremovableBridge(cmd string) bool {
	return strings.Contains(cmd, bridgeEnv+"=") || strings.Contains(cmd, bridgeEnv+" =")
}

// withTraceBridge returns cmd carrying bridge in sh's syntax, and whether the
// command may be rewritten at all. false means: leave it exactly as it is.
func withTraceBridge(sh shellKind, bridge, cmd string) (string, bool) {
	stripped := stripBridgeAssignments(cmd)
	if carriesUnremovableBridge(stripped) {
		return cmd, false
	}
	switch sh {
	case shellPowerShell:
		// Scoped to this call: the assignment lives inside the block, so a
		// shell the host reuses for the next command does not keep it.
		return "& { $env:" + bridgeEnv + " = '" + bridge + "'; " + stripped + " }", true
	case shellPOSIX:
		return bridgeEnv + "=" + bridge + " " + stripped, true
	default:
		return cmd, false
	}
}

// bridgeShellForTool is Claude Code's per-call answer. Its PreToolUse payload
// names the tool, and on Windows that is the difference between Git Bash and
// PowerShell — the one host where the shell is not settled at install time
// (#77). An unnamed or unknown tool is not guessed at: the Bash tool is the
// one whose name this client has always matched, and a tool it does not know
// gets no rewrite.
func bridgeShellForTool(toolName string) (shellKind, bool) {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "bash":
		return shellPOSIX, true
	case "powershell":
		return shellPowerShell, true
	}
	return "", false
}
