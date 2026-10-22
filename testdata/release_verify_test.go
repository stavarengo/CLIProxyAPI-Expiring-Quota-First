package pluginstore

// Go skips a testdata directory, so this file is not a package of this module: scripts/verify-release.sh copies it into CLIProxyAPI's internal/pluginstore package, so a release is checked by the code that installs it.

import (
	"bytes"
	"context"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestExpiringQuotaFirstRelease(t *testing.T) {
	id := requireEnv(t, "PLUGIN_ID")
	platforms := strings.Fields(requireEnv(t, "PLUGIN_PLATFORMS"))
	plugin := registryPlugin(t, requireEnv(t, "PLUGIN_REGISTRY"), id)

	releaseDir := os.Getenv("PLUGIN_RELEASE_DIR")
	tag := os.Getenv("PLUGIN_RELEASE_TAG")
	switch {
	case releaseDir != "" && tag == "":
		checkBuiltAssets(t, plugin, requireEnv(t, "PLUGIN_VERSION"), releaseDir, platforms)
	case tag != "" && releaseDir == "":
		checkPublishedRelease(t, plugin, tag, platforms)
	default:
		t.Fatal("set exactly one of PLUGIN_RELEASE_DIR and PLUGIN_RELEASE_TAG")
	}
}

func registryPlugin(t *testing.T, registryPath string, id string) Plugin {
	t.Helper()
	data, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := ParseRegistry(data)
	if err != nil {
		t.Fatalf("registry.json: %v", err)
	}
	plugin, ok := registry.PluginByID(id)
	if !ok {
		t.Fatalf("registry.json has no plugin %q", id)
	}
	if installType := PluginInstallType(plugin); installType != InstallTypeGitHubRelease {
		t.Fatalf("registry.json installs %q as %q, want %q", id, installType, InstallTypeGitHubRelease)
	}
	return plugin
}

// checkBuiltAssets takes the steps of installRelease on local files: asset selection, checksum parsing and verification, and the archive install.
func checkBuiltAssets(t *testing.T, plugin Plugin, version string, releaseDir string, platforms []string) {
	t.Helper()
	entries, err := os.ReadDir(releaseDir)
	if err != nil {
		t.Fatal(err)
	}
	release := Release{TagName: "v" + version}
	for _, entry := range entries {
		release.Assets = append(release.Assets, ReleaseAsset{Name: entry.Name()})
	}
	releaseVersion, err := ReleaseVersion(release)
	if err != nil {
		t.Fatal(err)
	}
	if releaseVersion != version {
		t.Fatalf("release version %q, want %q", releaseVersion, version)
	}
	plugin.Version = version
	for _, platform := range platforms {
		goos, goarch := splitPlatform(t, platform)
		archiveAsset, checksumAsset, err := SelectReleaseAssets(release, plugin.ID, version, goos, goarch)
		if err != nil {
			t.Fatalf("%s: %v", platform, err)
		}
		archiveData, err := os.ReadFile(filepath.Join(releaseDir, archiveAsset.Name))
		if err != nil {
			t.Fatal(err)
		}
		checksumData, err := os.ReadFile(filepath.Join(releaseDir, checksumAsset.Name))
		if err != nil {
			t.Fatal(err)
		}
		checksums, err := ParseChecksums(checksumData)
		if err != nil {
			t.Fatal(err)
		}
		if err := VerifyChecksum(archiveAsset.Name, archiveData, checksums); err != nil {
			t.Fatal(err)
		}
		options := InstallOptions{PluginsDir: t.TempDir(), GOOS: goos, GOARCH: goarch}
		result, err := InstallArchive(archiveData, plugin, options)
		if err != nil {
			t.Fatalf("%s: %v", archiveAsset.Name, err)
		}
		checkInstalled(t, result, plugin.ID, version, options)
	}
}

// checkPublishedRelease installs the latest release from GitHub exactly as the store's install button does, and expects it to be the given tag.
func checkPublishedRelease(t *testing.T, plugin Plugin, tag string, platforms []string) {
	t.Helper()
	client := Client{}
	if os.Getenv("GH_TOKEN") != "" {
		client.Auth = []AuthConfig{{
			Match:    "https://api.github.com/",
			ApplyTo:  []string{RequestKindMetadata, RequestKindArtifact},
			Type:     AuthTypeGitHubToken,
			TokenEnv: "GH_TOKEN",
		}}
	}
	version := normalizeVersion(tag)
	for _, platform := range platforms {
		goos, goarch := splitPlatform(t, platform)
		options := InstallOptions{PluginsDir: t.TempDir(), GOOS: goos, GOARCH: goarch}
		result, err := client.Install(context.Background(), plugin, options)
		if err != nil {
			t.Fatalf("%s: %v", platform, err)
		}
		if result.ReleaseTag != tag {
			t.Fatalf("%s: latest release is %q, want %q", platform, result.ReleaseTag, tag)
		}
		if result.InstallType != InstallTypeGitHubRelease {
			t.Fatalf("%s: install type %q, want %q", platform, result.InstallType, InstallTypeGitHubRelease)
		}
		checkInstalled(t, result, plugin.ID, version, options)
	}
}

func checkInstalled(t *testing.T, result InstallResult, id string, version string, options InstallOptions) {
	t.Helper()
	wantPath := filepath.Join(options.PluginsDir, options.GOOS, options.GOARCH, id+"-v"+version+pluginExtension(options.GOOS))
	if result.Path != wantPath {
		t.Fatalf("installed to %q, want %q", result.Path, wantPath)
	}
	if result.ID != id || result.Version != version {
		t.Fatalf("installed %s %s, want %s %s", result.ID, result.Version, id, version)
	}
	checkLibrary(t, result.Path, options.GOOS, options.GOARCH)
}

// checkLibrary makes sure the zip held a library for the platform it is named after, with the entry point the plugin host looks up.
func checkLibrary(t *testing.T, path string, goos string, goarch string) {
	t.Helper()
	var symbols []string
	var err error
	switch goos {
	case "linux":
		symbols, err = elfSymbols(path, goarch)
	case "darwin":
		symbols, err = machoSymbols(path, goarch)
	case "windows":
		symbols, err = peExports(path, goarch)
	default:
		t.Fatalf("no library format known for %q", goos)
	}
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	entry := "cliproxy_plugin_init"
	if goos == "darwin" {
		entry = "_" + entry // Mach-O prefixes C symbols with an underscore.
	}
	if !slices.Contains(symbols, entry) {
		t.Fatalf("%s does not export %s", path, entry)
	}
}

func elfSymbols(path string, goarch string) ([]string, error) {
	machines := map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}
	wantMachine, ok := machines[goarch]
	if !ok {
		return nil, fmt.Errorf("no ELF machine known for %q", goarch)
	}
	library, err := elf.Open(path)
	if err != nil {
		return nil, err
	}
	defer library.Close()
	if library.Machine != wantMachine {
		return nil, fmt.Errorf("built for %s, want %s", library.Machine, wantMachine)
	}
	symbols, err := library.DynamicSymbols()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		names = append(names, symbol.Name)
	}
	return names, nil
}

func machoSymbols(path string, goarch string) ([]string, error) {
	cpus := map[string]macho.Cpu{"amd64": macho.CpuAmd64, "arm64": macho.CpuArm64}
	wantCPU, ok := cpus[goarch]
	if !ok {
		return nil, fmt.Errorf("no Mach-O CPU known for %q", goarch)
	}
	library, err := macho.Open(path)
	if err != nil {
		return nil, err
	}
	defer library.Close()
	if library.Cpu != wantCPU {
		return nil, fmt.Errorf("built for %s, want %s", library.Cpu, wantCPU)
	}
	if library.Symtab == nil {
		return nil, fmt.Errorf("no symbol table")
	}
	names := make([]string, 0, len(library.Symtab.Syms))
	for _, symbol := range library.Symtab.Syms {
		names = append(names, symbol.Name)
	}
	return names, nil
}

// peExports reads the names in the DLL's export directory, which debug/pe does not expose.
func peExports(path string, goarch string) ([]string, error) {
	machines := map[string]uint16{"amd64": pe.IMAGE_FILE_MACHINE_AMD64}
	wantMachine, ok := machines[goarch]
	if !ok {
		return nil, fmt.Errorf("no PE machine known for %q", goarch)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	library, err := pe.NewFile(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer library.Close()
	if library.Machine != wantMachine {
		return nil, fmt.Errorf("built for machine %#x, want %#x", library.Machine, wantMachine)
	}
	header, ok := library.OptionalHeader.(*pe.OptionalHeader64)
	if !ok {
		return nil, fmt.Errorf("no 64-bit optional header")
	}
	exportDirectory := header.DataDirectory[pe.IMAGE_DIRECTORY_ENTRY_EXPORT]
	if exportDirectory.Size == 0 {
		return nil, fmt.Errorf("no export directory")
	}
	// IMAGE_EXPORT_DIRECTORY: NumberOfNames at byte 24, AddressOfNames at byte 32.
	directory, err := peBytes(library, data, exportDirectory.VirtualAddress, 40)
	if err != nil {
		return nil, err
	}
	numberOfNames := binary.LittleEndian.Uint32(directory[24:])
	nameTable, err := peBytes(library, data, binary.LittleEndian.Uint32(directory[32:]), 4*numberOfNames)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, numberOfNames)
	for i := range numberOfNames {
		nameAddress := binary.LittleEndian.Uint32(nameTable[4*i:])
		name, err := peBytes(library, data, nameAddress, 0)
		if err != nil {
			return nil, err
		}
		end := bytes.IndexByte(name, 0)
		if end < 0 {
			return nil, fmt.Errorf("export name at %#x is not terminated", nameAddress)
		}
		names = append(names, string(name[:end]))
	}
	return names, nil
}

// peBytes maps a relative virtual address to the file bytes of the section holding it, from that address to the end of the section or at least size bytes.
func peBytes(library *pe.File, data []byte, address uint32, size uint32) ([]byte, error) {
	for _, section := range library.Sections {
		if address < section.VirtualAddress || address >= section.VirtualAddress+section.VirtualSize {
			continue
		}
		start := uint64(section.Offset) + uint64(address-section.VirtualAddress)
		end := uint64(section.Offset) + uint64(section.Size)
		if start+uint64(size) > end || end > uint64(len(data)) {
			return nil, fmt.Errorf("address %#x runs past section %s", address, section.Name)
		}
		return data[start:end], nil
	}
	return nil, fmt.Errorf("address %#x is in no section", address)
}

func splitPlatform(t *testing.T, platform string) (string, string) {
	t.Helper()
	goos, goarch, ok := strings.Cut(platform, "/")
	if !ok {
		t.Fatalf("platform %q is not goos/goarch", platform)
	}
	return goos, goarch
}

func requireEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s is required", name)
	}
	return value
}
