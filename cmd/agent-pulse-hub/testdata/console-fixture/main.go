//go:build windows

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var kernel = windows.NewLazySystemDLL("kernel32.dll")

type request struct {
	Op         string   `json:"op"`
	Executable string   `json:"executable"`
	Config     string   `json:"config"`
	Root       string   `json:"root"`
	Env        []string `json:"env"`
}

func ignoreCtrlC() {
	callback := syscall.NewCallback(func(kind uint32) uintptr {
		if kind == 0 {
			return 1
		}
		return 0
	})
	ok, _, err := kernel.NewProc("SetConsoleCtrlHandler").Call(callback, 1)
	if ok == 0 {
		panic(err)
	}
}

func main() {
	ignoreCtrlC()
	if len(os.Args) == 3 && os.Args[1] == "leaf" {
		writeJSON(filepath.Join(os.Args[2], "leaf.json"), map[string]any{"pid": os.Getpid()})
		for {
			time.Sleep(time.Hour)
		}
	}
	if len(os.Args) == 3 && os.Args[1] == "stubborn" {
		root := os.Args[2]
		child := exec.Command(os.Args[0], "leaf", root)
		child.Stdout, child.Stderr = io.Discard, io.Discard
		if err := child.Start(); err != nil {
			panic(err)
		}
		writeJSON(filepath.Join(root, "stubborn.json"), map[string]any{"pid": os.Getpid(), "descendant": child.Process.Pid})
		for {
			if _, err := os.Stat(filepath.Join(root, "leaf.json")); err == nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		fmt.Println(`{"version":1,"type":"ready"}`)
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			var frame map[string]any
			if json.Unmarshal(scanner.Bytes(), &frame) == nil && frame["type"] == "shutdown" {
				writeJSON(filepath.Join(root, "shutdown.json"), map[string]any{"pid": os.Getpid(), "at": time.Now().UTC()})
			}
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	var command *exec.Cmd
	var completed chan error
	var log *os.File
	defer func() {
		if command != nil {
			_ = command.Process.Kill()
			<-completed
		}
		if log != nil {
			_ = log.Close()
		}
	}()
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var input request
		if err := json.Unmarshal(scanner.Bytes(), &input); err != nil {
			panic(err)
		}
		switch input.Op {
		case "start":
			if command != nil {
				panic("daemon already owned")
			}
			var err error
			log, err = os.Create(filepath.Join(input.Root, "daemon.jsonl"))
			if err != nil {
				panic(err)
			}
			command = exec.Command(input.Executable, "daemon", "--config", input.Config)
			command.Env = input.Env
			command.Stdout, command.Stderr = log, log
			if err := command.Start(); err != nil {
				panic(err)
			}
			completed = make(chan error, 1)
			go func(cmd *exec.Cmd, done chan error) { done <- cmd.Wait() }(command, completed)
			respond(map[string]any{"pid": command.Process.Pid})
		case "stop":
			if command == nil {
				panic("no daemon")
			}
			handles, pids, err := descendants(uint32(command.Process.Pid))
			if err != nil {
				panic(err)
			}
			started := time.Now()
			ok, _, signalErr := kernel.NewProc("GenerateConsoleCtrlEvent").Call(0, 0)
			if ok == 0 {
				panic(signalErr)
			}
			select {
			case <-completed:
			case <-time.After(8 * time.Second):
				panic("daemon Ctrl+C deadline")
			}
			exit := command.ProcessState.ExitCode()
			allExited := true
			for _, handle := range handles {
				state, waitErr := windows.WaitForSingleObject(handle, 1000)
				if waitErr != nil || state != windows.WAIT_OBJECT_0 {
					allExited = false
					_ = windows.TerminateProcess(handle, 1)
					_, _ = windows.WaitForSingleObject(handle, 1000)
				}
				_ = windows.CloseHandle(handle)
			}
			_ = log.Close()
			log = nil
			command = nil
			respond(map[string]any{"exit": exit, "all_exited": allExited, "pids": pids, "elapsed_ms": time.Since(started).Milliseconds(), "ctrl_event": 0, "process_group": 0})
		case "exit":
			if command != nil {
				panic("owned daemon remains")
			}
			respond(map[string]any{"exit": 0})
			return
		default:
			panic("unknown console operation")
		}
	}
}

func respond(value any) {
	if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
		panic(err)
	}
}
func writeJSON(path string, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		panic(err)
	}
}

func descendants(parent uint32) ([]windows.Handle, []uint32, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, nil, err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	parents := map[uint32]uint32{}
	walkErr := windows.Process32First(snapshot, &entry)
	for ; walkErr == nil; walkErr = windows.Process32Next(snapshot, &entry) {
		parents[entry.ProcessID] = entry.ParentProcessID
	}
	if !errors.Is(walkErr, windows.ERROR_NO_MORE_FILES) {
		return nil, nil, walkErr
	}
	owned := map[uint32]bool{parent: true}
	for changed := true; changed; {
		changed = false
		for pid, pp := range parents {
			if owned[pp] && !owned[pid] {
				owned[pid] = true
				changed = true
			}
		}
	}
	var handles []windows.Handle
	var pids []uint32
	for pid := range owned {
		if pid == parent {
			continue
		}
		handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, pid)
		if err != nil {
			for _, h := range handles {
				windows.CloseHandle(h)
			}
			return nil, nil, err
		}
		handles = append(handles, handle)
		pids = append(pids, pid)
	}
	return handles, pids, nil
}
