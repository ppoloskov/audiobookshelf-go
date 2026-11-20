{
  description = "Monstrobook (Audiobookshelf Go replacement) dev and verification environment";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
  };

  outputs = { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "aarch64-darwin"
        "x86_64-darwin"
      ];
      forAllSystems = f:
        nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      # Keep devShells.default self-contained without a placeholder buildGoModule
      # vendorHash so `nix develop` and `nix flake check` work out of the box.
      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          packages = with pkgs; [
            # go.mod declares Go 1.27+; pkgs.go tracks nixpkgs-unstable's Go toolchain.
            go
            nodejs_22
            # nixpkgs ffmpeg includes libwebp, libmp3lame, aac, libopus, libvorbis,
            # and libx264 by default (much smaller/faster to fetch than ffmpeg-full).
            ffmpeg
            golangci-lint
            gnumake
            git
            python3
            sqlite
          ];

          shellHook = ''
            unset GOROOT
            export GOTOOLCHAIN=local
            export ORACLE_NODE_CMD=node
            export FFMPEG_PATH="${pkgs.lib.getBin pkgs.ffmpeg}/bin/ffmpeg"
            export FFPROBE_PATH="${pkgs.lib.getBin pkgs.ffmpeg}/bin/ffprobe"
            # Leave CGO_ENABLED unset so `go test -race` works inside `nix develop`.
          '';
        };
      });
    };
}
