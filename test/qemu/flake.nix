{
  description = "NixOS QCOW2 disk image for terraform-provider-nixos acceptance tests";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-24.11";

  outputs = { self, nixpkgs }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};

      # The test pubkey is supplied via env var so we can build with --impure
      # without committing a key into the repo. Emptied if not set so `nix
      # eval` still works for inspection, but the resulting image won't be
      # ssh-able (build.sh always sets it).
      pubKey = builtins.getEnv "NIXOS_TEST_PUBKEY";

      nixosConfig = nixpkgs.lib.nixosSystem {
        inherit system;
        modules = [
          ({ lib, modulesPath, ... }: {
            imports = [
              "${modulesPath}/profiles/qemu-guest.nix"
            ];

            boot.loader.grub = {
              enable = true;
              device = "/dev/vda";
            };

            fileSystems."/" = {
              device = "/dev/vda1";
              fsType = "ext4";
              autoResize = true;
            };

            networking.hostName = "nixos-test";
            networking.useDHCP = true;
            networking.firewall.enable = false;

            services.openssh = {
              enable = true;
              # Listen on 22 (default) plus 22222 — see test/qemu/run.sh.
              # When the build_host scenario runs nix-copy-closure from inside
              # the VM, both host-side and guest-side connections must hit
              # the same address. We forward host:22222 → guest:22222 so
              # `127.0.0.1:22222` works from either perspective.
              ports = [ 22 22222 ];
              settings = {
                PermitRootLogin = "yes";
                PasswordAuthentication = false;
                # Required so the provider's nix-copy-closure step can use a
                # forwarded agent (the ssh_use_agent + build_host scenario).
                AllowAgentForwarding = "yes";
                # Disable connection-rate penalties for the test container.
                # Otherwise the wait-for-sshd loop in run.sh and the
                # acceptance suite's many short-lived connections from the
                # same source (the QEMU NAT 10.0.2.2 address) get throttled
                # to the point of resetting handshakes.
                PerSourcePenalties = "no";
              };
            };

            users.users.root.openssh.authorizedKeys.keys =
              lib.optional (pubKey != "") pubKey;

            # Auto-login on the serial console for debugging.
            services.getty.autologinUser = "root";

            nix.settings = {
              experimental-features = [ "nix-command" "flakes" ];
              trusted-users = [ "root" ];
            };

            # Pre-installed so the provider's `nix profile install nixpkgs#git`
            # step is a no-op rather than a long round-trip on every test.
            environment.systemPackages = with pkgs; [ git ];

            system.stateVersion = "24.11";
          })
        ];
      };

      qcowImage = import "${nixpkgs}/nixos/lib/make-disk-image.nix" {
        inherit pkgs;
        inherit (nixosConfig) config;
        lib = nixpkgs.lib;
        format = "qcow2";
        # 8 GiB is enough for the base system + a few rebuilds; auto-resize on
        # boot lets the VM grow if we need more.
        diskSize = 8192;
        partitionTableType = "legacy";
      };
    in
    {
      packages.${system} = {
        default = qcowImage;
        qcow = qcowImage;
      };
    };
}
