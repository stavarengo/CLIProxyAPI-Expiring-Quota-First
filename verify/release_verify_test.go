package pluginstore

// scripts/verify-release.sh copies this file into CLIProxyAPI's internal/pluginstore package, so a release is checked by the code that installs it.

import (
	"context"
	"debug/elf"
	"os"
	"path/filepath"
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
	wantPath := filepath.Join(options.PluginsDir, options.GOOS, options.GOARCH, id+"-v"+version+".so")
	if result.Path != wantPath {
		t.Fatalf("installed to %q, want %q", result.Path, wantPath)
	}
	if result.ID != id || result.Version != version {
		t.Fatalf("installed %s %s, want %s %s", result.ID, result.Version, id, version)
	}
	checkLibrary(t, result.Path, options.GOARCH)
}

// checkLibrary makes sure the zip held a library for the platform it is named after, with the entry point the plugin host looks up.
func checkLibrary(t *testing.T, path string, goarch string) {
	t.Helper()
	machines := map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}
	wantMachine, ok := machines[goarch]
	if !ok {
		t.Fatalf("no ELF machine known for %q", goarch)
	}
	library, err := elf.Open(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	defer library.Close()
	if library.Machine != wantMachine {
		t.Fatalf("%s: built for %s, want %s", path, library.Machine, wantMachine)
	}
	symbols, err := library.DynamicSymbols()
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	for _, symbol := range symbols {
		if symbol.Name == "cliproxy_plugin_init" {
			return
		}
	}
	t.Fatalf("%s does not export cliproxy_plugin_init", path)
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
