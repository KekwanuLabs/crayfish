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
	"os/exec"
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

// IsAvailable returns true if ufw is installed and Crayfish can manage it.
func IsAvailable() bool {
	_, err := exec.LookPath("ufw")
	return err == nil
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

// sync detects current subnets and updates ufw if anything changed.
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

	m.logger.Info("network change detected — updating firewall rules",
		"added", added(previous, current),
		"removed", removed(previous, current),
		"active", current)

	if err := ApplyRules(previous, current, managedPorts); err != nil {
		m.logger.Warn("firewall rule update failed", "error", err)
	} else {
		m.logger.Info("firewall rules updated", "subnets", current, "ports", managedPorts)
	}
}

// ApplyRules removes rules for subnets no longer active and adds rules for
// new subnets. Only touches rules for managedPorts — user-added rules are
// left untouched.
func ApplyRules(previous, current []string, ports []int) error {
	toRemove := removed(previous, current)
	toAdd := added(previous, current)

	for _, port := range ports {
		for _, subnet := range toRemove {
			if err := deleteRule(subnet, port); err != nil {
				// Non-fatal: rule might not exist (e.g. first run)
				continue
			}
		}
		for _, subnet := range toAdd {
			if err := addRule(subnet, port); err != nil {
				return fmt.Errorf("ufw allow from %s port %d: %w", subnet, port, err)
			}
		}
	}
	return nil
}

// GetLocalSubnets returns sorted CIDR subnets for all active non-loopback
// IPv4 interfaces (ethernet, WiFi, etc.). Skips link-local (169.254.x.x).
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
			ip4 := ipNet.IP.To4()
			if ip4 == nil {
				continue // IPv6 — skip for now
			}
			if ip4[0] == 169 && ip4[1] == 254 {
				continue // link-local (APIPA) — skip
			}
			// Network address of this interface's subnet.
			network := &net.IPNet{
				IP:   ip4.Mask(ipNet.Mask),
				Mask: ipNet.Mask,
			}
			subnets = append(subnets, network.String())
		}
	}

	sort.Strings(subnets)
	return dedup(subnets), nil
}

func addRule(subnet string, port int) error {
	out, err := exec.Command("sudo", "ufw", "allow",
		"from", subnet, "to", "any",
		"port", fmt.Sprintf("%d", port),
		"proto", "tcp",
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func deleteRule(subnet string, port int) error {
	// ufw delete allow from <subnet> to any port <port> proto tcp
	// Returns non-zero if rule doesn't exist — that's fine.
	exec.Command("sudo", "ufw", "--force", "delete", "allow",
		"from", subnet, "to", "any",
		"port", fmt.Sprintf("%d", port),
		"proto", "tcp",
	).Run()
	return nil
}

// EnsureEnabled enables ufw with default deny-incoming/allow-outgoing
// if it isn't already active. Safe to call multiple times.
func EnsureEnabled() error {
	out, err := exec.Command("sudo", "ufw", "status").Output()
	if err == nil && strings.Contains(string(out), "Status: active") {
		return nil // already enabled
	}

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
	return nil
}

// IsEnabled returns true if ufw is active.
func IsEnabled() bool {
	out, err := exec.Command("sudo", "ufw", "status").Output()
	return err == nil && strings.Contains(string(out), "Status: active")
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
