{ lib, buildGoModule, fetchFromGitHub, pkg-config, libsecret, libfido2 }:

# Version and fixed hashes follow nixpkgs' upstream Bridge package, without
# changing Main's other pinned packages or taking the older Bridge default.
buildGoModule (finalAttrs: {
  pname = "protonmail-bridge";
  version = "3.27.1";
  src = fetchFromGitHub {
    owner = "ProtonMail";
    repo = "proton-bridge";
    rev = "v${finalAttrs.version}";
    hash = "sha256-hQuIaL4t/FaihXvKHrRJhZ90va4uOgAC0h9beQQVN4A=";
  };
  vendorHash = "sha256-mfDtRWyT5MAK7Z56RuA8BOyTdPv7h8ovkuATrtIl/Gs=";
  nativeBuildInputs = [ pkg-config ];
  buildInputs = [ libsecret libfido2 ];
  preBuild = ''
    patchShebangs ./utils/
    (cd ./utils/ && ./credits.sh bridge)
  '';
  ldflags = let constants = "github.com/ProtonMail/proton-bridge/v3/internal/constants"; in [
    "-X ${constants}.Version=${finalAttrs.version}"
    "-X ${constants}.Revision=${finalAttrs.src.rev}"
    "-X ${constants}.buildTime=unknown"
    "-X ${constants}.FullAppName=ProtonMailBridge"
  ];
  subPackages = [ "cmd/Desktop-Bridge" ];
  postInstall = ''mv $out/bin/Desktop-Bridge $out/bin/protonmail-bridge'';
  meta = {
    description = "Proton Mail Bridge headless IMAP/SMTP service";
    homepage = "https://github.com/ProtonMail/proton-bridge";
    license = lib.licenses.gpl3Plus;
    mainProgram = "protonmail-bridge";
    platforms = lib.platforms.linux;
  };
})
