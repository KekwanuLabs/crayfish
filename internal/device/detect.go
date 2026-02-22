// Package device provides hardware detection for adaptive configuration.
// Crayfish runs on everything from Raspberry Pi to cloud servers, and needs
// to automatically adjust its behavior based on available resources.
package device

import (
	"bufio"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// Info contains detected device capabilities.
type Info struct {
	// Architecture
	Arch     string // runtime.GOARCH: arm, arm64, amd64, etc.
	OS       string // runtime.GOOS: linux, darwin, windows
	ArmModel string // For ARM: "v6", "v7", "v8" (empty on non-ARM)

	// Resources
	TotalRAMMB   int // Total system RAM in MB
	AvailRAMMB   int // Available RAM in MB
	CPUCores     int // Number of CPU cores
	IsLowMemory  bool // < 1GB RAM
	IsVeryLowMem bool // < 512MB RAM

	// Device hints
	IsRaspberryPi bool
	PiModel       string // "Pi 2", "Pi 3", "Pi 4", "Pi 5", "Pi Zero", etc.
	IsMac         bool
	IsAppleSilicon bool
}

// Detect probes the system and returns device capabilities.
func Detect() Info {
	info := Info{
		Arch:     runtime.GOARCH,
		OS:       runtime.GOOS,
		CPUCores: runtime.NumCPU(),
	}

	// Detect RAM
	info.TotalRAMMB, info.AvailRAMMB = detectRAM()
	info.IsLowMemory = info.TotalRAMMB < 1024
	info.IsVeryLowMem = info.TotalRAMMB < 512

	// Detect ARM version
	if strings.HasPrefix(info.Arch, "arm") {
		info.ArmModel = detectARMVersion()
	}

	// Detect Raspberry Pi
	info.IsRaspberryPi, info.PiModel = detectRaspberryPi()

	// Detect Mac
	if info.OS == "darwin" {
		info.IsMac = true
		info.IsAppleSilicon = info.Arch == "arm64"
	}

	return info
}

// detectRAM returns total and available RAM in MB.
func detectRAM() (total, avail int) {
	switch runtime.GOOS {
	case "linux":
		return detectRAMLinux()
	case "darwin":
		return detectRAMMac()
	default:
		return 0, 0
	}
}

func detectRAMLinux() (total, avail int) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "MemTotal:") {
			total = parseMemInfoLine(line)
		} else if strings.HasPrefix(line, "MemAvailable:") {
			avail = parseMemInfoLine(line)
		}
	}
	return total, avail
}

func parseMemInfoLine(line string) int {
	// Format: "MemTotal:        1921988 kB"
	fields := strings.Fields(line)
	if len(fields) >= 2 {
		kb, _ := strconv.Atoi(fields[1])
		return kb / 1024 // Convert to MB
	}
	return 0
}

func detectRAMMac() (total, avail int) {
	// sysctl hw.memsize returns bytes
	out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 0, 0
	}
	bytes, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	total = int(bytes / 1024 / 1024)

	// Get available from vm_stat (rough approximation)
	out, err = exec.Command("vm_stat").Output()
	if err != nil {
		return total, total / 2 // Estimate 50% available
	}

	// Parse "Pages free" and "Pages inactive"
	var freePages, inactivePages int64
	pageSize := int64(4096) // Default macOS page size

	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "Pages free:") {
			freePages = parseVMStatLine(line)
		} else if strings.HasPrefix(line, "Pages inactive:") {
			inactivePages = parseVMStatLine(line)
		}
	}

	avail = int((freePages + inactivePages) * pageSize / 1024 / 1024)
	return total, avail
}

func parseVMStatLine(line string) int64 {
	// Format: "Pages free:                             1234."
	fields := strings.Fields(line)
	if len(fields) >= 3 {
		numStr := strings.TrimSuffix(fields[2], ".")
		num, _ := strconv.ParseInt(numStr, 10, 64)
		return num
	}
	return 0
}

// detectARMVersion determines the ARM architecture version.
func detectARMVersion() string {
	if runtime.GOARCH == "arm64" {
		return "v8"
	}

	// Check /proc/cpuinfo for ARM version
	file, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "model name") || strings.HasPrefix(line, "Processor") {
			lower := strings.ToLower(line)
			if strings.Contains(lower, "armv7") {
				return "v7"
			} else if strings.Contains(lower, "armv6") {
				return "v6"
			} else if strings.Contains(lower, "armv8") || strings.Contains(lower, "aarch64") {
				return "v8"
			}
		}
		// Also check CPU architecture field
		if strings.HasPrefix(line, "CPU architecture") {
			fields := strings.Split(line, ":")
			if len(fields) >= 2 {
				ver := strings.TrimSpace(fields[1])
				switch ver {
				case "6":
					return "v6"
				case "7":
					return "v7"
				case "8":
					return "v8"
				}
			}
		}
	}

	return ""
}

// detectRaspberryPi checks if we're running on a Raspberry Pi.
func detectRaspberryPi() (bool, string) {
	// Check /proc/device-tree/model (most reliable)
	data, err := os.ReadFile("/proc/device-tree/model")
	if err == nil {
		model := strings.TrimRight(string(data), "\x00")
		if strings.Contains(model, "Raspberry Pi") {
			return true, parsePiModel(model)
		}
	}

	// Fallback: check /proc/cpuinfo for Raspberry Pi
	file, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return false, ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "Model") && strings.Contains(line, "Raspberry Pi") {
			return true, parsePiModel(line)
		}
	}

	return false, ""
}

// parsePiModel extracts a friendly Pi model name.
func parsePiModel(raw string) string {
	lower := strings.ToLower(raw)

	switch {
	case strings.Contains(lower, "pi 5"):
		return "Pi 5"
	case strings.Contains(lower, "pi 4"):
		return "Pi 4"
	case strings.Contains(lower, "pi 3"):
		return "Pi 3"
	case strings.Contains(lower, "pi 2"):
		return "Pi 2"
	case strings.Contains(lower, "pi zero 2"):
		return "Pi Zero 2"
	case strings.Contains(lower, "pi zero"):
		return "Pi Zero"
	case strings.Contains(lower, "pi 1"), strings.Contains(lower, "model b"):
		return "Pi 1"
	default:
		return "Pi"
	}
}

// RecommendedWhisperModel returns the best whisper model for this device.
// Returns empty string if device can't run whisper.
func (i Info) RecommendedWhisperModel() string {
	// Not enough RAM for any model
	if i.TotalRAMMB < 400 {
		return ""
	}

	// Very constrained (Pi Zero, Pi 1, low-end Pi 2)
	if i.TotalRAMMB < 600 {
		return "" // Even tiny model will struggle
	}

	// Low memory devices (Pi 2, Pi 3 with 1GB)
	if i.TotalRAMMB < 1500 {
		return "tiny" // 39MB model, ~400MB RAM needed
	}

	// Medium devices (Pi 4 2GB, low-end laptops)
	if i.TotalRAMMB < 3000 {
		return "base" // 142MB model, ~500MB RAM needed
	}

	// Well-resourced devices (Pi 4 4GB+, most laptops/desktops)
	if i.TotalRAMMB < 6000 {
		return "small" // 466MB model, ~1GB RAM needed
	}

	// High-end devices (8GB+ RAM)
	return "medium" // 1.5GB model, best accuracy
}

// CanRunWhisper returns whether this device can run whisper at all.
func (i Info) CanRunWhisper() bool {
	return i.RecommendedWhisperModel() != ""
}

// WhisperBinaryName returns the expected whisper binary name for this platform.
func (i Info) WhisperBinaryName() string {
	if i.OS == "windows" {
		return "whisper.exe"
	}
	return "whisper"
}

// String returns a human-readable device summary.
func (i Info) String() string {
	var parts []string

	if i.IsRaspberryPi {
		parts = append(parts, "Raspberry "+i.PiModel)
	} else if i.IsMac {
		if i.IsAppleSilicon {
			parts = append(parts, "Mac (Apple Silicon)")
		} else {
			parts = append(parts, "Mac (Intel)")
		}
	} else {
		parts = append(parts, i.OS+"/"+i.Arch)
	}

	parts = append(parts, strconv.Itoa(i.TotalRAMMB)+"MB RAM")
	parts = append(parts, strconv.Itoa(i.CPUCores)+" cores")

	return strings.Join(parts, ", ")
}
