package loomdapp

import (
	"os"

	"loom.local/loom/internal/applicationbuild"
	"loom.local/loom/internal/projectapply"
	"loom.local/loom/internal/serviceregistry"
)

func applicationSourceAdapter() *projectapply.SourceApplicationAdapter {
	nixpkgs, platform := os.Getenv("LOOM_APPLICATION_BUILD_NIXPKGS"), os.Getenv("LOOM_APPLICATION_BUILD_PLATFORM")
	if nixpkgs == "" || platform == "" {
		return nil
	}
	builder, err := applicationbuild.NewService(applicationbuild.Options{WorkDir: "/var/lib/loom/application-builds/staging", NixpkgsPath: nixpkgs, Platform: platform, NixBinary: os.Getenv("LOOM_APPLICATION_BUILD_NIX"), LauncherPath: os.Getenv("LOOM_APPLICATION_BUILD_LAUNCHER"), RustVersion: os.Getenv("LOOM_APPLICATION_BUILD_RUST_VERSION"), PrefetchGit: os.Getenv("LOOM_APPLICATION_BUILD_PREFETCH_GIT")})
	if err != nil {
		return nil
	}
	return &projectapply.SourceApplicationAdapter{Builder: builder, Helper: serviceregistry.ApplicationHelperClient{SocketPath: serviceregistry.ApplicationSocketPath}, OutputRoot: "/var/lib/loom/application-builds/outputs", MemoryMaxBytes: 512 * 1024 * 1024}
}
