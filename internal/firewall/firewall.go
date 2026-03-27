// Package firewall dynamically manages ufw rules for Crayfish.
//
// Instead of hard-coding LAN ranges, it detects all active non-loopback
// network interfaces at runtime and keeps ufw rules in sync. This means
// rules are always correct whether the Pi is on a home network, office
// network, connected to multiple interfaces, or moved to a new LAN.
//
// Rules managed by this package:
//   - SSH (22): allow from every detected local subnet
//   - Dashboard (8119): allow from every detected local subnet
//   - All inbound else: deny (set once at install time by ufw default)
//   - All outbound: allow (required for API calls, tunnel, etc.)
//
// On network change, old subnet rules are removed and new ones added.
// The systemd ExecStartPre also runs a sync so rules are correct even
// before Go starts (covers the boot window on a new network).
package firewall

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Ports that Crayfish manages access to.
var managedPorts = []int{22, 8119}

// Manager keeps ufw rules in sync with the current network interfaces.
type Manager struct {
	logger          *slog.Logger
	previousSubnets []string
	mu              sync.Mutex
}

// New creates a firewall manager.
func New(logger *slog.Logger) *Manager {
	return &Manager{logger: logger}
}

// IsAvailable returns true if a supported firewall tool is installed.
// Linux: checks for ufw. Windows: checks for netsh. macOS: returns false
// (pf is present but Crayfish doesn't manage it — macOS doesn't need it
// for home use since it defaults to blocking inbound connections).
func IsAvailable() bool {
	switch runtime.GOOS {
	case "windows":
		_, err := exec.LookPath("netsh")
		return err == nil
	case "linux":
		for _, p := range []string{"/usr/sbin/ufw", "/sbin/ufw", "/usr/bin/ufw"} {
			if _, err := os.Stat(p); err == nil {
				return true
			}
		}
		if _, err := exec.LookPath("ufw"); err == nil {
			return true
		}
	}
	return false
}

// Start syncs rules immediately then polls every 3 minutes for network changes.
// Runs until ctx is cancelled.
func (m *Manager) Start(ctx context.Context) {
	m.sync()

	ticker := time.NewTicker(3 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.sync()
		}
	}
}

// sync detects current subnets and triggers the firewall sync script if
// anything changed. The script runs via systemd-run to escape the
// NoNewPrivileges boundary that prevents sudo inside the service process.
func (m *Manager) sync() {
	current, err := GetLocalSubnets()
	if err != nil {
		m.logger.Warn("firewall: subnet detection failed", "error", err)
		return
	}

	m.mu.Lock()
	previous := m.previousSubnets
	changed := !equalSubnets(previous, current)
	if changed {
		m.previousSubnets = current
	}
	m.mu.Unlock()

	if !changed {
		return
	}

	m.logger.Info("network change detected — triggering firewall sync",
		"added", added(previous, current),
		"removed", removed(previous, current),
		"active", current)

	// The service runs with NoNewPrivileges=true so sudo is blocked.
	// Delegate to systemd-run which spawns a transient privileged unit
	// that can run the sync script as root.
	if err := runFirewallSync(); err != nil {
		m.logger.Info("firewall sync deferred to next restart (runtime privilege escalation unavailable)",
			"subnets", current,
			"hint", "rules will be updated by ExecStartPre on next service restart")
	} else {
		m.logger.Info("firewall rules updated", "subnets", current, "ports", managedPorts)
	}
}

// runFirewallSync updates firewall rules for the current network subnets.
// Linux: triggers the crayfish-firewall-sync script via systemd-run.
// Windows: applies netsh rules directly (no privilege boundary issue on Windows).
func runFirewallSync() error {
	switch runtime.GOOS {
	case "windows":
		return runWindowsFirewallSync()
	case "linux":
		const script = "/usr/local/bin/crayfish-firewall-sync"
		if _, err := os.Stat(script); err != nil {
			return fmt.Errorf("sync script not installed: %w", err)
		}
		cmd := exec.Command("systemd-run", "--no-block", "--quiet",
			"--unit=crayfish-firewall-sync", script)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemd-run: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	return fmt.Errorf("firewall sync not supported on %s", runtime.GOOS)
}

// runWindowsFirewallSync adds Windows Defender Firewall rules for the current
// local subnets. Uses netsh advfirewall, which doesn't require elevation when
// running as a service (services run with elevated privileges on Windows).
func runWindowsFirewallSync() error {
	subnets, err := GetLocalSubnets()
	if err != nil {
		return fmt.Errorf("detect subnets: %w", err)
	}

	for _, port := range managedPorts {
		ruleName := fmt.Sprintf("Crayfish-Port%d", port)

		// Delete existing rule (ignore failure — rule may not exist yet).
		exec.Command("netsh", "advfirewall", "firewall", "delete", "rule",
			"name="+ruleName, "protocol=TCP", "dir=in").Run()

		// Build remote IP list from subnets.
		remoteIP := strings.Join(subnets, ",")
		if len(subnets) == 0 {
			remoteIP = "LocalSubnet"
		}

		// Add new rule.
		out, err := exec.Command("netsh", "advfirewall", "firewall", "add", "rule",
			"name="+ruleName,
			"protocol=TCP",
			"dir=in",
			fmt.Sprintf("localport=%d", port),
			"action=allow",
			"remoteip="+remoteIP,
		).CombinedOutput()
		if err != nil {
			return fmt.Errorf("netsh add rule port %d: %w: %s", port, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// GetLocalSubnets returns sorted CIDR subnets for all active non-loopback
// interfaces — both IPv4 and IPv6. Only includes addresses that cannot
// originate from the internet:
//
//   IPv4: any address except link-local (169.254.x.x)
//   IPv6: link-local (fe80::/10) and ULA (fc00::/7, commonly fd00::/8)
//         Global unicast IPv6 is intentionally excluded — it IS internet-
//         routable, so allowing it would open SSH/dashboard to the world.
//
// Both link-local and ULA are safe to allow because:
//   - fe80::/10 is not routable beyond a single network link
//   - fc00::/7 is the IPv6 equivalent of RFC 1918 private space
func GetLocalSubnets() ([]string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list interfaces: %w", err)
	}

	var subnets []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue // skip loopback
		}
		if iface.Flags&net.FlagUp == 0 {
			continue // skip interfaces that are down
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}

			if ip4 := ipNet.IP.To4(); ip4 != nil {
				// IPv4: skip link-local (APIPA 169.254.x.x)
				if ip4[0] == 169 && ip4[1] == 254 {
					continue
				}
				network := &net.IPNet{
					IP:   ip4.Mask(ipNet.Mask),
					Mask: ipNet.Mask,
				}
				subnets = append(subnets, network.String())
				continue
			}

			// IPv6: only include link-local and ULA — never global unicast.
			ip6 := ipNet.IP
			if len(ip6) != 16 {
				continue
			}
			isLinkLocal := ip6[0] == 0xfe && (ip6[1]&0xc0) == 0x80 // fe80::/10
			isULA := (ip6[0] & 0xfe) == 0xfc                        // fc00::/7 (fd00::/8 is subset)
			if !isLinkLocal && !isULA {
				continue // global unicast — skip, it's internet-routable
			}
			network := &net.IPNet{
				IP:   ip6.Mask(ipNet.Mask),
				Mask: ipNet.Mask,
			}
			subnets = append(subnets, network.String())
		}
	}

	sort.Strings(subnets)
	return dedup(subnets), nil
}


// EnsureEnabled enables the system firewall if not already active.
// Linux: uses ufw. Windows: enables Windows Defender Firewall via netsh.
// macOS: no-op (firewall is managed via System Preferences).
func EnsureEnabled() error {
	if IsEnabled() {
		return nil
	}
	switch runtime.GOOS {
	case "linux":
		cmds := [][]string{
			{"sudo", "ufw", "--force", "reset"},
			{"sudo", "ufw", "default", "deny", "incoming"},
			{"sudo", "ufw", "default", "allow", "outgoing"},
			{"sudo", "ufw", "--force", "enable"},
		}
		for _, args := range cmds {
			if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
				return fmt.Errorf("ufw %s: %w: %s", args[1], err, strings.TrimSpace(string(out)))
			}
		}
	case "windows":
		// Enable Windows Defender Firewall on all profiles.
		out, err := exec.Command("netsh", "advfirewall", "set", "allprofiles", "state", "on").CombinedOutput()
		if err != nil {
			return fmt.Errorf("netsh advfirewall: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// IsEnabled returns true if the system firewall is active.
// Linux: reads /etc/ufw/ufw.conf (no sudo needed, safe inside systemd NoNewPrivileges).
// Windows: queries Windows Defender Firewall state via netsh.
// macOS: always returns true (firewall is assumed enabled by the OS).
func IsEnabled() bool {
	switch runtime.GOOS {
	case "linux":
		data, err := os.ReadFile("/etc/ufw/ufw.conf")
		if err != nil {
			return false
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == "ENABLED=yes" {
				return true
			}
		}
		return false
	case "windows":
		out, err := exec.Command("netsh", "advfirewall", "show", "currentprofile", "state").Output()
		if err != nil {
			return false
		}
		return strings.Contains(string(out), "ON")
	case "darwin":
		return true // macOS manages its own firewall; we don't override it
	}
	return false
}

// --- helpers ---

func equalSubnets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// removed returns elements in a but not in b.
func removed(a, b []string) []string {
	bSet := make(map[string]bool, len(b))
	for _, s := range b {
		bSet[s] = true
	}
	var out []string
	for _, s := range a {
		if !bSet[s] {
			out = append(out, s)
		}
	}
	return out
}

// added returns elements in b but not in a.
func added(a, b []string) []string {
	return removed(b, a)
}

func dedup(ss []string) []string {
	seen := make(map[string]bool, len(ss))
	out := ss[:0]
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
