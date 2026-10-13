package credentials

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// keychainTimeout bounds every call to a keychain tool. It's generous,
// since unlocking a keychain can mean waiting for you to type a password.
const keychainTimeout = 2 * time.Minute

// macSecurity is macOS's own keychain tool. Fabrica runs it by its full
// path, so nothing on your PATH can stand in for it.
const macSecurity = "/usr/bin/security"

// systemKeychain is the keychain this machine has, or nil.
func systemKeychain() backend {
	switch runtime.GOOS {
	case "darwin":
		if _, err := os.Stat(macSecurity); err == nil {
			return &macKeychain{path: macSecurity}
		}
	case "linux", "freebsd", "openbsd", "netbsd":
		if path, err := exec.LookPath("secret-tool"); err == nil {
			s := &secretService{path: path}
			if s.usable() {
				return s
			}
		}
	}
	return nil
}

// macKeychain keeps credentials in your login keychain, as generic
// passwords filed under service "fabrica", with the credential's key as the
// account. Keychain Access shows them as "Fabrica: <key>".
type macKeychain struct {
	path string
}

func (k *macKeychain) Name() string { return "your macOS Keychain" }

func (k *macKeychain) id() string { return "keychain" }

// macNotFound is the exit code of `security` when there's no such item.
const macNotFound = 44

func (k *macKeychain) Get(key string) (string, bool, error) {
	out, code, err := runTool(k.path, nil, "find-generic-password", "-s", service, "-a", key, "-w")
	if code == macNotFound {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return strings.TrimRight(out, "\r\n"), true, nil
}

// Set runs `security -i` and writes the command on its standard input, so
// the secret never shows up in the list of running processes, where
// anyone on the machine could see a command line.
func (k *macKeychain) Set(key, value string) error {
	cmd := fmt.Sprintf("add-generic-password -U -s %s -a %s -l %s -w %s\n",
		quote(service), quote(key), quote(label(key)), quote(value))
	_, _, err := runTool(k.path, []byte(cmd), "-i")
	return err
}

func (k *macKeychain) Delete(key string) (bool, error) {
	_, code, err := runTool(k.path, nil, "delete-generic-password", "-s", service, "-a", key)
	if code == macNotFound {
		return false, nil
	}
	return err == nil, err
}

// secretService keeps credentials in the Secret Service (GNOME Keyring,
// KWallet and friends) through libsecret's secret-tool, with attributes
// service=fabrica and account=<key>.
type secretService struct {
	path string
}

func (s *secretService) Name() string { return "your keyring (Secret Service)" }

func (s *secretService) id() string { return "keychain" }

// usable checks, once per process, that secret-tool can reach a Secret
// Service. Over SSH or in a container there's often no D-Bus session, and
// then the file is the place for credentials.
func (s *secretService) usable() bool {
	probeMu.Lock()
	defer probeMu.Unlock()
	if ok, done := probed[s.path]; done {
		return ok
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, code, err := runToolContext(ctx, s.path, nil, "lookup", "service", service, "account", "fabrica-probe")
	// Exit 1 with nothing on stderr is "not found", which means it works.
	var toolErr *toolError
	ok := err == nil || code == 1 && errors.As(err, &toolErr) && toolErr.stderr == ""
	probed[s.path] = ok
	return ok
}

var (
	probeMu sync.Mutex
	probed  = map[string]bool{}
)

func (s *secretService) Get(key string) (string, bool, error) {
	out, code, err := runTool(s.path, nil, "lookup", "service", service, "account", key)
	var toolErr *toolError
	if code == 1 && errors.As(err, &toolErr) && toolErr.stderr == "" {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return strings.TrimRight(out, "\r\n"), true, nil
}

// Set gives secret-tool the secret on its standard input, never on its
// command line.
func (s *secretService) Set(key, value string) error {
	_, _, err := runTool(s.path, []byte(value), "store", "--label="+label(key), "service", service, "account", key)
	return err
}

func (s *secretService) Delete(key string) (bool, error) {
	_, ok, err := s.Get(key)
	if err != nil || !ok {
		return false, err
	}
	_, _, err = runTool(s.path, nil, "clear", "service", service, "account", key)
	return err == nil, err
}

// label is how a credential is called in keychain apps.
func label(key string) string { return "Fabrica: " + key }

// quote quotes a word for `security -i`, which splits its input like a
// shell. Check keeps quotes and backslashes out of keys and values, so
// single quotes are enough.
func quote(s string) string { return "'" + s + "'" }

// toolError is a keychain tool that failed, with what it said.
type toolError struct {
	tool   string
	stderr string
	err    error
}

func (e *toolError) Error() string {
	if e.stderr != "" {
		return fmt.Sprintf("%s: %s", e.tool, e.stderr)
	}
	return fmt.Sprintf("%s: %v", e.tool, e.err)
}

func (e *toolError) Unwrap() error { return e.err }

func runTool(path string, stdin []byte, args ...string) (stdout string, code int, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), keychainTimeout)
	defer cancel()
	return runToolContext(ctx, path, stdin, args...)
}

// runToolContext runs a keychain tool and returns its output and exit
// code. Its stderr goes into the error, never into Fabrica's output.
func runToolContext(ctx context.Context, path string, stdin []byte, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.String(), 0, nil
	}
	code := -1
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = errors.New("timed out")
	}
	tool := path[strings.LastIndex(path, "/")+1:]
	return stdout.String(), code, &toolError{tool: tool, stderr: strings.TrimSpace(stderr.String()), err: err}
}
