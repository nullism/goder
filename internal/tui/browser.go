package tui

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func openExternalURL(rawURL string) error {
	type candidate struct {
		name string
		args []string
	}

	var candidates []candidate
	if isWSL() {
		candidates = []candidate{
			{name: "wslview", args: []string{rawURL}},
			{name: "powershell.exe", args: []string{"-NoProfile", "-Command", "Start-Process -FilePath '" + escapePowerShellSingleQuoted(rawURL) + "'"}},
			{name: "cmd.exe", args: []string{"/C", "start", "", "\"" + rawURL + "\""}},
			{name: "xdg-open", args: []string{rawURL}},
			{name: "gio", args: []string{"open", rawURL}},
		}
	} else {
		switch runtime.GOOS {
		case "darwin":
			candidates = []candidate{{name: "open", args: []string{rawURL}}}
		case "windows":
			candidates = []candidate{
				{name: "rundll32", args: []string{"url.dll,FileProtocolHandler", rawURL}},
				{name: "powershell", args: []string{"-NoProfile", "-Command", "Start-Process", rawURL}},
			}
		default:
			candidates = []candidate{{name: "xdg-open", args: []string{rawURL}}, {name: "gio", args: []string{"open", rawURL}}}
		}
	}

	var lastErr error
	var lastCmd string
	for _, c := range candidates {
		if _, err := exec.LookPath(c.name); err != nil {
			lastErr = err
			lastCmd = c.name
			continue
		}
		cmd := exec.Command(c.name, c.args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			lastErr = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
			lastCmd = c.name
			continue
		}
		return nil
	}

	if lastErr != nil {
		if lastCmd != "" {
			return fmt.Errorf("%s failed: %w", lastCmd, lastErr)
		}
		return lastErr
	}
	return fmt.Errorf("no browser opener found")
}

func manualOpenCommand(rawURL string) string {
	if isWSL() {
		return "powershell.exe -NoProfile -Command \"Start-Process -FilePath '" + escapePowerShellSingleQuoted(rawURL) + "'\""
	}
	switch runtime.GOOS {
	case "darwin":
		return "open \"" + rawURL + "\""
	case "windows":
		return "powershell -NoProfile -Command \"Start-Process -FilePath '" + escapePowerShellSingleQuoted(rawURL) + "'\""
	default:
		return "xdg-open \"" + rawURL + "\""
	}
}

func escapePowerShellSingleQuoted(v string) string {
	return strings.ReplaceAll(v, "'", "''")
}

func formatAuthURL(rawURL string) string {
	if supportsTerminalHyperlinks() {
		return "\x1b]8;;" + rawURL + "\x1b\\" + rawURL + "\x1b]8;;\x1b\\"
	}
	return rawURL
}

func supportsTerminalHyperlinks() bool {
	term := strings.ToLower(os.Getenv("TERM"))
	if term == "" || term == "dumb" {
		return false
	}
	if os.Getenv("WT_SESSION") != "" || os.Getenv("TERM_PROGRAM") != "" || os.Getenv("VTE_VERSION") != "" {
		return true
	}
	if strings.Contains(term, "xterm") || strings.Contains(term, "tmux") || strings.Contains(term, "screen") {
		return true
	}
	return false
}

func isWSL() bool {
	if os.Getenv("WSL_DISTRO_NAME") != "" || os.Getenv("WSL_INTEROP") != "" {
		return true
	}
	b, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	v := strings.ToLower(string(b))
	return strings.Contains(v, "microsoft") || strings.Contains(v, "wsl")
}
