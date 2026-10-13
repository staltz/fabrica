package auth

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// OpenBrowser opens url in your web browser, and reports whether it could.
// It runs $BROWSER when that's set, `open` on macOS, and on Linux
// xdg-open (or wslview, under WSL) when there's a display to show it on.
// Over SSH there's none, so you open the link yourself.
func OpenBrowser(url string) bool {
	var name string
	switch {
	case os.Getenv("BROWSER") != "":
		// $BROWSER can list several, separated by colons; use the first.
		name, _, _ = strings.Cut(os.Getenv("BROWSER"), ":")
	case runtime.GOOS == "darwin":
		name = "open"
	case os.Getenv("WSL_DISTRO_NAME") != "" && found("wslview"):
		name = "wslview"
	case (os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "") && found("xdg-open"):
		name = "xdg-open"
	default:
		return false
	}
	cmd := exec.Command(name, url)
	if err := cmd.Start(); err != nil {
		return false
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err == nil
	case <-time.After(3 * time.Second):
		return true // still going, like a browser that started in the foreground
	}
}

func found(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
