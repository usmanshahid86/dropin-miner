//go:build windows

// Command winprobe answers, on a real Windows host, what PR C's Windows
// replacement strategy assumes: whether a process can rename the pathname of
// its own running image, move a freshly executed candidate into that name,
// replace an existing .previous while still mapped from the displaced name,
// delete a running image, and restore after an injected failure. It also
// tries the two primitives that might avoid an absent canonical name
// (ReplaceFileW, FileRenameInfoEx with POSIX semantics) and records the path
// forms os.Executable reports.
//
// Nothing is retried. Every operation is recorded with its Win32 result and
// elapsed time, and every scenario directory is listed afterwards with the
// identity of the bytes at each name. A failure is a result, not a bug: the
// probe exits 0 unless the orchestrator itself cannot run.
//
// Throwaway evidence for PR C (branch spike/windows-image-rename). Not built,
// tested or linted by the main module: the leading dot keeps it out of ./...
// and out of the module's source-walking guards.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	idLen   = 40
	idMagic = "WINPROBE-ID:"

	moveReplaceExisting = 0x1
	moveWriteThrough    = 0x8

	accessDelete   = 0x00010000
	fileShareAll   = 0x1 | 0x2 | 0x4
	openExisting   = 3
	attrNormal     = 0x80
	classDispEx    = 21 // FileDispositionInfoEx
	classRenameEx  = 22 // FileRenameInfoEx
	dispDelete     = 0x1
	dispPosix      = 0x2
	renameReplace  = 0x1
	renamePosix    = 0x2
	childTimeout   = 10 * time.Second
	helperTimeout  = 90 * time.Second
	validationKill = 3 * time.Second
)

var (
	kernel32                   = syscall.NewLazyDLL("kernel32.dll")
	procMoveFileExW            = kernel32.NewProc("MoveFileExW")
	procDeleteFileW            = kernel32.NewProc("DeleteFileW")
	procReplaceFileW           = kernel32.NewProc("ReplaceFileW")
	procSetFileInformation     = kernel32.NewProc("SetFileInformationByHandle")
	procGetShortPathNameW      = kernel32.NewProc("GetShortPathNameW")
	procGetVolumePathNameW     = kernel32.NewProc("GetVolumePathNameW")
	procGetVolumeInformationW  = kernel32.NewProc("GetVolumeInformationW")
	procGetModuleFileNameW     = kernel32.NewProc("GetModuleFileNameW")
	procGetFinalPathNameByHndl = kernel32.NewProc("GetFinalPathNameByHandleW")
)

// record is one observed operation.
type record struct {
	Root     string `json:"root,omitempty"`
	Scenario string `json:"scenario"`
	Step     string `json:"step"`
	Op       string `json:"op"`
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
	Flags    string `json:"flags,omitempty"`
	OK       bool   `json:"ok"`
	Errno    uint32 `json:"errno,omitempty"`
	Error    string `json:"error,omitempty"`
	Ms       int64  `json:"ms"`
	Detail   string `json:"detail,omitempty"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: winprobe run [-out file] root...")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		os.Exit(orchestrate(os.Args[2:]))
	case "version":
		exe, _ := os.Executable()
		fmt.Printf("winprobe %s\n", fileID(exe))
	case "whoami":
		exe, err := os.Executable()
		fmt.Printf("%s|%v|%s\n", exe, err, moduleFileName())
	case "sleep":
		n, _ := strconv.Atoi(os.Args[2])
		time.Sleep(time.Duration(n) * time.Second)
	case "scenario":
		h := &helper{name: os.Args[2], dir: os.Args[3]}
		h.run()
	default:
		fmt.Fprintln(os.Stderr, "winprobe: unknown mode", os.Args[1])
		os.Exit(2)
	}
}

// ── Win32 calls, each returning the raw Errno ─────────────────────────────

func u16(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		panic(err)
	}
	return p
}

func callResult(r1 uintptr, err error) error {
	if r1 != 0 {
		return nil
	}
	var errno syscall.Errno
	if errors.As(err, &errno) && errno != 0 {
		return errno
	}
	return syscall.EINVAL
}

func errnoOf(err error) uint32 {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return uint32(errno)
	}
	return 0
}

func moveFileEx(from, to string, flags uintptr) error {
	r1, _, err := procMoveFileExW.Call(uintptr(unsafe.Pointer(u16(from))), uintptr(unsafe.Pointer(u16(to))), flags)
	return callResult(r1, err)
}

func deleteFile(path string) error {
	r1, _, err := procDeleteFileW.Call(uintptr(unsafe.Pointer(u16(path))))
	return callResult(r1, err)
}

func replaceFile(replaced, replacement, backup string) error {
	var b uintptr
	if backup != "" {
		b = uintptr(unsafe.Pointer(u16(backup)))
	}
	r1, _, err := procReplaceFileW.Call(uintptr(unsafe.Pointer(u16(replaced))), uintptr(unsafe.Pointer(u16(replacement))), b, 0, 0, 0)
	return callResult(r1, err)
}

func openForDelete(path string) (syscall.Handle, error) {
	return syscall.CreateFile(u16(path), accessDelete, fileShareAll, nil, openExisting, attrNormal, 0)
}

func setDispositionPosix(h syscall.Handle) error {
	flags := uint32(dispDelete | dispPosix)
	r1, _, err := procSetFileInformation.Call(uintptr(h), classDispEx, uintptr(unsafe.Pointer(&flags)), unsafe.Sizeof(flags))
	return callResult(r1, err)
}

// setRenameInfoEx renames the file behind h to target with FILE_RENAME_INFO
// (64-bit layout: Flags u32, pad, RootDirectory 8, FileNameLength u32 at 16,
// FileName at 20). The buffer is backed by uint64s so it is 8-byte aligned.
func setRenameInfoEx(h syscall.Handle, target string, flags uint32) error {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		return errors.New("64-bit layout only")
	}
	name, err := syscall.UTF16FromString(target)
	if err != nil {
		return err
	}
	size := 20 + len(name)*2
	if size < 24 {
		size = 24
	}
	words := make([]uint64, (size+7)/8)
	b := unsafe.Slice((*byte)(unsafe.Pointer(&words[0])), len(words)*8)
	binary.LittleEndian.PutUint32(b[0:], flags)
	binary.LittleEndian.PutUint32(b[16:], uint32((len(name)-1)*2))
	for i, c := range name {
		binary.LittleEndian.PutUint16(b[20+2*i:], c)
	}
	r1, _, callErr := procSetFileInformation.Call(uintptr(h), classRenameEx, uintptr(unsafe.Pointer(&words[0])), uintptr(size))
	return callResult(r1, callErr)
}

func shortPath(path string) (string, error) {
	buf := make([]uint16, 1024)
	r1, _, err := procGetShortPathNameW.Call(uintptr(unsafe.Pointer(u16(path))), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if r1 == 0 {
		return "", callResult(0, err)
	}
	return syscall.UTF16ToString(buf[:r1]), nil
}

func moduleFileName() string {
	buf := make([]uint16, 1024)
	r1, _, _ := procGetModuleFileNameW.Call(0, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf[:r1])
}

func finalPath(path string) string {
	h, err := syscall.CreateFile(u16(path), 0, fileShareAll, nil, openExisting, 0x02000000, 0) // FILE_FLAG_BACKUP_SEMANTICS
	if err != nil {
		return "open: " + err.Error()
	}
	defer syscall.CloseHandle(h)
	buf := make([]uint16, 1024)
	r1, _, callErr := procGetFinalPathNameByHndl.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0)
	if r1 == 0 {
		return "final: " + callErr.Error()
	}
	return syscall.UTF16ToString(buf[:r1])
}

func volumeFS(path string) string {
	vol := make([]uint16, 512)
	r1, _, err := procGetVolumePathNameW.Call(uintptr(unsafe.Pointer(u16(path))), uintptr(unsafe.Pointer(&vol[0])), uintptr(len(vol)))
	if r1 == 0 {
		return "volume: " + err.Error()
	}
	fs := make([]uint16, 64)
	r1, _, err = procGetVolumeInformationW.Call(uintptr(unsafe.Pointer(&vol[0])), 0, 0, 0, 0, 0, uintptr(unsafe.Pointer(&fs[0])), uintptr(len(fs)))
	if r1 == 0 {
		return syscall.UTF16ToString(vol) + " fs: " + err.Error()
	}
	return syscall.UTF16ToString(vol) + " " + syscall.UTF16ToString(fs)
}

// ── identities ─────────────────────────────────────────────────────────────

// copyWithID writes src plus a fixed-size trailer naming id. A PE image
// ignores trailing overlay bytes, so every copy runs the same program while
// the bytes at each name stay distinguishable.
func copyWithID(src, dst, id string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if i := bytes.LastIndex(data, []byte(idMagic)); i >= 0 && len(data)-i == idLen {
		data = data[:i]
	}
	tag := idMagic + id
	tag += strings.Repeat(" ", idLen-1-len(tag)) + "\n"
	return os.WriteFile(dst, append(data, tag...), 0o755)
}

// fileID reads the trailer with a handle closed before it returns, so it
// never holds a name another step is about to move.
func fileID(path string) string {
	f, err := os.Open(path)
	if err != nil {
		var errno syscall.Errno
		if errors.As(err, &errno) {
			return fmt.Sprintf("<unopenable errno=%d>", uint32(errno))
		}
		return "<unopenable>"
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() < idLen {
		return "<no-id>"
	}
	buf := make([]byte, idLen)
	if _, err := f.ReadAt(buf, st.Size()-idLen); err != nil {
		return "<unreadable>"
	}
	s := strings.TrimRight(string(buf), " \n")
	if !strings.HasPrefix(s, idMagic) {
		return "<no-id>"
	}
	return strings.TrimPrefix(s, idMagic)
}

func runChild(path string, timeout time.Duration, args ...string) (string, error, int64) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	t0 := time.Now()
	cmd := exec.CommandContext(ctx, path, args...)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	ms := time.Since(t0).Milliseconds()
	text := strings.TrimSpace(out.String())
	if stderr.Len() > 0 {
		text += " stderr=" + strings.TrimSpace(stderr.String())
	}
	if ctx.Err() != nil {
		err = fmt.Errorf("timeout after %s (killed): %v", timeout, err)
	}
	return text, err, ms
}

// ── helper: runs from <dir>\dropin-miner.exe ──────────────────────────────

type helper struct {
	name string
	dir  string
}

func (h *helper) p(name string) string { return filepath.Join(h.dir, name) }

func emit(r record) {
	b, _ := json.Marshal(r)
	fmt.Printf("REC %s\n", b)
}

func (h *helper) rel(path string) string {
	if path == "" {
		return ""
	}
	if r, err := filepath.Rel(h.dir, path); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return path
}

func (h *helper) op(step, op, from, to, flags string, fn func() error) error {
	t0 := time.Now()
	err := fn()
	r := record{Scenario: h.name, Step: step, Op: op, From: h.rel(from), To: h.rel(to), Flags: flags, OK: err == nil, Ms: time.Since(t0).Milliseconds()}
	if err != nil {
		r.Errno, r.Error = errnoOf(err), err.Error()
	}
	emit(r)
	return err
}

func (h *helper) move(step, from, to string, replace bool) error {
	flags, label := uintptr(moveWriteThrough), "WRITE_THROUGH"
	if replace {
		flags |= moveReplaceExisting
		label = "REPLACE_EXISTING|WRITE_THROUGH"
	}
	return h.op(step, "MoveFileExW", from, to, label, func() error { return moveFileEx(from, to, flags) })
}

// exec runs path with args and records its output; want, when set, is the
// identity the child must report for the step to count as ok.
func (h *helper) exec(step, path, want string, timeout time.Duration, args ...string) error {
	out, err, ms := runChild(path, timeout, args...)
	r := record{Scenario: h.name, Step: step, Op: "exec " + strings.Join(args, " "), From: h.rel(path), Ms: ms, Detail: out, OK: err == nil}
	if err != nil {
		r.Errno, r.Error = errnoOf(err), err.Error()
	} else if want != "" && out != "winprobe "+want {
		r.OK, r.Error = false, "want winprobe "+want
		err = errors.New(r.Error)
	}
	emit(r)
	return err
}

func (h *helper) observe(step string, names ...string) {
	var parts []string
	for _, n := range names {
		parts = append(parts, n+"="+fileID(h.p(n)))
	}
	exe, _ := os.Executable()
	parts = append(parts, "self.os.Executable="+exe, "self.GetModuleFileName="+moduleFileName())
	emit(record{Scenario: h.name, Step: step, Op: "observe", OK: true, Detail: strings.Join(parts, " ")})
}

const (
	canonical = "dropin-miner.exe"
	previous  = "dropin-miner.exe.previous"
)

func (h *helper) run() {
	C, P := h.p(canonical), h.p(previous)
	h.observe("start", canonical, previous)
	switch h.name {
	case "replace-first", "replace-second", "replace-second-mapped-previous":
		// The approved direct strategy: C→D, validate N, N→C, validate C,
		// D→P replacing the existing one-level P.
		N := h.p(".dropin-miner.candidate-1")
		D := h.p(".dropin-miner.displaced-1")
		h.observe("candidate", ".dropin-miner.candidate-1")
		if h.exec("validate candidate", N, "", childTimeout, "version") != nil {
			return
		}
		if h.move("C->D (running image)", C, D, false) != nil {
			return
		}
		if h.move("N->C (candidate just executed)", N, C, false) != nil {
			_ = h.move("restore D->C", D, C, false)
			h.observe("after restore", canonical, previous, ".dropin-miner.displaced-1")
			return
		}
		if h.exec("validate canonical", C, "", childTimeout, "version") != nil {
			return
		}
		moved := h.p(".dropin-miner.movedagain-1")
		if h.move("C->X (canonical just executed moves again)", C, moved, false) == nil {
			_ = h.move("X->C (back)", moved, C, false)
		}
		if h.move("D->P (replace existing P while mapped from D)", D, P, true) != nil {
			if h.move("recover C->N", C, N, false) == nil {
				_ = h.move("recover D->C", D, C, false)
			}
			h.observe("after commit-previous failure", canonical, previous, ".dropin-miner.displaced-1", ".dropin-miner.candidate-1")
			return
		}
		h.observe("end", canonical, previous)
	case "rollback":
		// Copy P (never executed in place) to R, validate R, then the same
		// transaction.
		R := h.p(".dropin-miner.candidate-R")
		D := h.p(".dropin-miner.displaced-R")
		if h.op("copy P->R", "copy", P, R, "", func() error { return copyFile(P, R) }) != nil {
			return
		}
		if h.exec("validate staged previous", R, "", childTimeout, "version") != nil {
			return
		}
		if h.move("C->D (running image)", C, D, false) != nil {
			return
		}
		if h.move("R->C", R, C, false) != nil {
			_ = h.move("restore D->C", D, C, false)
			return
		}
		if h.exec("validate canonical", C, "", childTimeout, "version") != nil {
			return
		}
		if h.move("D->P (replace P)", D, P, true) != nil {
			return
		}
		h.observe("end", canonical, previous)
	case "restore-missing-candidate":
		D := h.p(".dropin-miner.displaced-1")
		if h.move("C->D (running image)", C, D, false) != nil {
			return
		}
		_ = h.move("N->C injected failure (N absent)", h.p(".dropin-miner.candidate-absent"), C, false)
		_ = h.move("restore D->C", D, C, false)
		h.exec("validate restored canonical", C, "C", childTimeout, "version")
		h.observe("end", canonical, previous, ".dropin-miner.displaced-1")
	case "restore-after-failed-validation":
		N := h.p(".dropin-miner.candidate-BAD")
		D := h.p(".dropin-miner.displaced-1")
		if h.move("C->D (running image)", C, D, false) != nil {
			return
		}
		if h.move("BAD->C", N, C, false) != nil {
			_ = h.move("restore D->C", D, C, false)
			return
		}
		// The canonical validation hangs and is killed at its deadline; the
		// bad candidate is moved out immediately after.
		_ = h.exec("validate canonical (hangs, killed)", C, "", validationKill, "sleep", "30")
		if h.move("C->BAD (just-killed image moves out)", C, N, false) != nil {
			h.observe("after move-out failure", canonical, previous, ".dropin-miner.displaced-1")
			return
		}
		_ = h.move("restore D->C", D, C, false)
		h.exec("validate restored canonical", C, "C", childTimeout, "version")
		h.observe("end", canonical, previous, ".dropin-miner.displaced-1", ".dropin-miner.candidate-BAD")
	case "delete-running":
		_ = h.op("DeleteFileW running canonical", "DeleteFileW", C, "", "", func() error { return deleteFile(C) })
		h.observe("after DeleteFileW", canonical)
		h.posixDelete("running canonical", C)
		h.observe("after POSIX disposition", canonical)
	case "delete-running-displaced":
		D := h.p(".dropin-miner.removed-1")
		if h.move("C->D (running image)", C, D, false) != nil {
			return
		}
		_ = h.op("DeleteFileW running displaced", "DeleteFileW", D, "", "", func() error { return deleteFile(D) })
		h.observe("after DeleteFileW", canonical, ".dropin-miner.removed-1")
		h.posixDelete("running displaced", D)
		h.observe("after POSIX disposition", canonical, ".dropin-miner.removed-1")
	case "atomic-replacefile":
		N := h.p(".dropin-miner.candidate-1")
		_ = h.op("ReplaceFileW(C, N, backup)", "ReplaceFileW", N, C, "backup=dropin-miner.exe.bak", func() error {
			return replaceFile(C, N, h.p("dropin-miner.exe.bak"))
		})
		h.observe("end", canonical, ".dropin-miner.candidate-1", "dropin-miner.exe.bak")
	case "atomic-renameinfoex":
		N := h.p(".dropin-miner.candidate-1")
		var hdl syscall.Handle
		if h.op("open N for DELETE", "CreateFileW(DELETE)", N, "", "", func() error {
			var err error
			hdl, err = openForDelete(N)
			return err
		}) != nil {
			return
		}
		_ = h.op("FileRenameInfoEx N->C over running image", "SetFileInformationByHandle", N, C, "REPLACE_IF_EXISTS|POSIX_SEMANTICS", func() error {
			return setRenameInfoEx(hdl, `\\?\`+C, renameReplace|renamePosix)
		})
		_ = syscall.CloseHandle(hdl)
		h.observe("end", canonical, ".dropin-miner.candidate-1")
		h.exec("run canonical", C, "", childTimeout, "version")
	case "atomic-movefileex-replace":
		N := h.p(".dropin-miner.candidate-1")
		_ = h.move("N->C REPLACE_EXISTING over running image", N, C, true)
		h.observe("end", canonical, ".dropin-miner.candidate-1")
	default:
		emit(record{Scenario: h.name, Step: "unknown", Op: "none", Error: "unknown scenario"})
	}
}

func (h *helper) posixDelete(label, path string) {
	var hdl syscall.Handle
	if h.op("open "+label+" for DELETE", "CreateFileW(DELETE)", path, "", "", func() error {
		var err error
		hdl, err = openForDelete(path)
		return err
	}) != nil {
		return
	}
	_ = h.op("FileDispositionInfoEx "+label, "SetFileInformationByHandle", path, "", "DELETE|POSIX_SEMANTICS", func() error {
		return setDispositionPosix(hdl)
	})
	_ = h.op("close handle "+label, "CloseHandle", path, "", "", func() error { return syscall.CloseHandle(hdl) })
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// ── orchestrator ──────────────────────────────────────────────────────────

type orchestrator struct {
	self    string
	root    string
	records []record
}

func orchestrate(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	out := fs.String("out", "", "write every record as JSON to this file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "winprobe:", err)
		return 1
	}
	fmt.Printf("winprobe on %s/%s, %s\n", runtime.GOOS, runtime.GOARCH, runtime.Version())
	var all []record
	for _, root := range fs.Args() {
		o := &orchestrator{self: self}
		o.root = filepath.Join(root, fmt.Sprintf("winprobe-%d", time.Now().UnixNano()))
		if err := os.MkdirAll(o.root, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "winprobe:", err)
			return 1
		}
		o.note("environment", "volume", volumeFS(o.root)+" finalpath="+finalPath(o.root))
		o.scenarios()
		all = append(all, o.records...)
	}
	printReport(all)
	if *out != "" {
		b, _ := json.MarshalIndent(all, "", "  ")
		if err := os.WriteFile(*out, b, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "winprobe:", err)
			return 1
		}
	}
	return 0
}

func (o *orchestrator) note(scenario, step, detail string) {
	o.records = append(o.records, record{Root: o.root, Scenario: scenario, Step: step, Op: "observe", OK: true, Detail: detail})
}

func (o *orchestrator) failf(scenario, step, format string, args ...any) {
	o.records = append(o.records, record{Root: o.root, Scenario: scenario, Step: step, Op: "orchestrator", Error: fmt.Sprintf(format, args...)})
}

// prepare makes a scenario directory holding the named identities.
func (o *orchestrator) prepare(scenario, dirName string, files map[string]string) (string, bool) {
	dir := filepath.Join(o.root, dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		o.failf(scenario, "prepare", "%v", err)
		return "", false
	}
	for name, id := range files {
		if err := copyWithID(o.self, filepath.Join(dir, name), id); err != nil {
			o.failf(scenario, "prepare", "%s: %v", name, err)
			return "", false
		}
	}
	return dir, true
}

func (o *orchestrator) helper(scenario, dir string) {
	C := filepath.Join(dir, canonical)
	ctx, cancel := context.WithTimeout(context.Background(), helperTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, C, "scenario", scenario, dir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	t0 := time.Now()
	err := cmd.Run()
	sc := bufio.NewScanner(&stdout)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "REC ") {
			o.note(scenario, "helper stdout", line)
			continue
		}
		var r record
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "REC ")), &r) == nil {
			r.Root = o.root
			o.records = append(o.records, r)
		}
	}
	r := record{Root: o.root, Scenario: scenario, Step: "helper exit", Op: "exec helper", From: C, OK: err == nil, Ms: time.Since(t0).Milliseconds(), Detail: strings.TrimSpace(stderr.String())}
	if err != nil {
		r.Errno, r.Error = errnoOf(err), err.Error()
	}
	o.records = append(o.records, r)
	o.listing(scenario, dir)
}

func (o *orchestrator) listing(scenario, dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		o.failf(scenario, "listing", "%v", err)
		return
	}
	var parts []string
	for _, e := range entries {
		parts = append(parts, e.Name()+"="+fileID(filepath.Join(dir, e.Name())))
	}
	sort.Strings(parts)
	o.note(scenario, "directory after", strings.Join(parts, " "))
}

func (o *orchestrator) scenarios() {
	// replace-first, with a concurrent process mapped from the same image
	// (a detached flush started before the upgrade).
	dir, ok := o.prepare("replace-first", "replace", map[string]string{
		canonical:                   "C",
		previous:                    "P0",
		".dropin-miner.candidate-1": "N1",
	})
	if !ok {
		return
	}
	sleeper := exec.Command(filepath.Join(dir, canonical), "sleep", "600")
	if err := sleeper.Start(); err != nil {
		o.failf("replace-first", "start concurrent process", "%v", err)
	} else {
		time.Sleep(500 * time.Millisecond)
		o.note("replace-first", "concurrent process", fmt.Sprintf("pid %d running from %s", sleeper.Process.Pid, canonical))
	}
	o.helper("replace-first", dir)

	// A second upgrade while that first process still runs from the image
	// now named .previous: replacing a mapped P.
	if err := copyWithID(o.self, filepath.Join(dir, ".dropin-miner.candidate-1"), "N2"); err != nil {
		o.failf("replace-second-mapped-previous", "prepare", "%v", err)
	}
	o.helper("replace-second-mapped-previous", dir)
	if sleeper.Process != nil {
		_ = sleeper.Process.Kill()
		_ = sleeper.Wait()
		time.Sleep(500 * time.Millisecond)
		o.note("replace-second", "concurrent process", "killed and waited")
	}
	// The same second upgrade with nothing mapped from P. The candidate may
	// still be at its staging name if the mapped attempt restored it.
	if fileID(filepath.Join(dir, ".dropin-miner.candidate-1")) != "N2" && fileID(filepath.Join(dir, canonical)) != "N2" {
		_ = copyWithID(o.self, filepath.Join(dir, ".dropin-miner.candidate-1"), "N2")
	}
	if fileID(filepath.Join(dir, canonical)) != "N2" {
		o.listing("replace-second", dir)
		o.helper("replace-second", dir)
	}
	o.helper("rollback", dir)
	o.helper("rollback", dir) // a second rollback swaps back

	if d, ok := o.prepare("restore-missing-candidate", "restore-missing", map[string]string{canonical: "C", previous: "P0"}); ok {
		o.helper("restore-missing-candidate", d)
	}
	if d, ok := o.prepare("restore-after-failed-validation", "restore-validation", map[string]string{canonical: "C", previous: "P0", ".dropin-miner.candidate-BAD": "BAD"}); ok {
		o.helper("restore-after-failed-validation", d)
	}
	if d, ok := o.prepare("delete-running", "delete", map[string]string{canonical: "C"}); ok {
		o.helper("delete-running", d)
	}
	if d, ok := o.prepare("delete-running-displaced", "delete-displaced", map[string]string{canonical: "C"}); ok {
		o.helper("delete-running-displaced", d)
	}
	if d, ok := o.prepare("atomic-replacefile", "atomic-replacefile", map[string]string{canonical: "C", ".dropin-miner.candidate-1": "N1"}); ok {
		o.helper("atomic-replacefile", d)
	}
	if d, ok := o.prepare("atomic-renameinfoex", "atomic-renameinfoex", map[string]string{canonical: "C", ".dropin-miner.candidate-1": "N1"}); ok {
		o.helper("atomic-renameinfoex", d)
	}
	if d, ok := o.prepare("atomic-movefileex-replace", "atomic-movefileex", map[string]string{canonical: "C", ".dropin-miner.candidate-1": "N1"}); ok {
		o.helper("atomic-movefileex-replace", d)
	}
	o.pathForms()
}

// pathForms records what os.Executable reports when the same binary is
// launched through different spellings of its path.
func (o *orchestrator) pathForms() {
	const scenario = "path-forms"
	dir, ok := o.prepare(scenario, filepath.Join("Long Directory Name With Spaces", "bin"), map[string]string{canonical: "C"})
	if !ok {
		return
	}
	C := filepath.Join(dir, canonical)
	short, err := shortPath(C)
	if err != nil {
		o.note(scenario, "GetShortPathNameW", "error errno="+strconv.Itoa(int(errnoOf(err)))+" "+err.Error())
	} else {
		o.note(scenario, "GetShortPathNameW", short)
	}
	forms := map[string]string{
		"exact":         C,
		"lowercase":     strings.ToLower(C),
		"uppercase":     strings.ToUpper(C),
		"forward-slash": filepath.ToSlash(C),
		"short-8.3":     short,
	}
	keys := make([]string, 0, len(forms))
	for k := range forms {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		launch := forms[k]
		if launch == "" {
			continue
		}
		outText, err, ms := runChild(launch, childTimeout, "whoami")
		r := record{Root: o.root, Scenario: scenario, Step: "launch " + k, Op: "exec whoami", From: launch, OK: err == nil, Ms: ms, Detail: outText}
		if err != nil {
			r.Errno, r.Error = errnoOf(err), err.Error()
		} else {
			reported := strings.SplitN(outText, "|", 2)[0]
			r.Detail += fmt.Sprintf(" equal=%v equalFold=%v", reported == C, strings.EqualFold(reported, C))
		}
		o.records = append(o.records, r)
	}
	o.note(scenario, "GetFinalPathNameByHandle", finalPath(C))
}

func printReport(all []record) {
	var summary strings.Builder
	summary.WriteString(fmt.Sprintf("## winprobe %s/%s\n\n", runtime.GOOS, runtime.GOARCH))
	summary.WriteString("| root | scenario | step | op | from → to | flags | ok | errno | error / detail | ms |\n|---|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range all {
		b, _ := json.Marshal(r)
		fmt.Println(string(b))
		msg := r.Error
		if r.Detail != "" {
			if msg != "" {
				msg += " — "
			}
			msg += r.Detail
		}
		fromTo := r.From
		if r.To != "" {
			fromTo += " → " + r.To
		}
		summary.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s | %s | %v | %d | %s | %d |\n",
			filepath.Base(filepath.Dir(r.Root)), r.Scenario, r.Step, r.Op, cell(fromTo), r.Flags, r.OK, r.Errno, cell(msg), r.Ms))
	}
	if path := os.Getenv("GITHUB_STEP_SUMMARY"); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
		if err == nil {
			_, _ = f.WriteString(summary.String())
			_ = f.Close()
		}
	}
}

func cell(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ") }
