package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"syscall"
	"unsafe"
)

var getConsoleProcessList = syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleProcessList")

// ownConsole reports whether tagshelf has its console window to itself, which
// is how it is when started by a double click: the window was made for it and
// closes when it exits. Started from a terminal, the shell is there too. The
// tests put a stand-in in its place.
var ownConsole = func() bool {
	var pids [2]uint32
	n, _, _ := getConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	return n == 1
}

// waitForEnter keeps the window open until Enter is pressed, so that what was
// printed can be read before it closes.
var waitForEnter = func() {
	fmt.Fprint(log.Writer(), "\nPress Enter to close this window.")
	bufio.NewReader(os.Stdin).ReadString('\n')
}
