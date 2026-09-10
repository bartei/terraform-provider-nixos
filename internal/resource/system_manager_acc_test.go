package resource_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	tftest "github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/bartei/terraform-provider-nixos/internal/acctest"
)

// systemManagerBranch and its matching nixpkgs release. system-manager's
// makeSystemConfig refuses mismatched pairs (see its README, "Nixpkgs
// compatibility"), so keep these two in sync.
const (
	systemManagerBranch  = "release-26.05"
	systemManagerNixpkgs = "nixos-26.05"
)

// minimalSystemManagerFlake returns a flake.nix for a non-NixOS target that
// writes an /etc marker file and defines a oneshot systemd unit, so a test can
// verify both activation paths. `$${...}` survives the Terraform heredoc as a
// literal `${...}` (see minimalNixOSFlake).
func minimalSystemManagerFlake(marker string) string {
	return fmt.Sprintf(`{
  description = "sysmgr acctest";
  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/%s";
    system-manager = {
      url = "github:numtide/system-manager/%s";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };
  outputs = { self, nixpkgs, system-manager }: {
    systemConfigs.default = system-manager.lib.makeSystemConfig {
      modules = [
        ({ pkgs, ... }: {
          config = {
            nixpkgs.hostPlatform = "x86_64-linux";
            environment.etc."sysmgr-acctest-marker".text = %q;
            systemd.services.sysmgr-acctest = {
              enable = true;
              description = "terraform-provider-nixos acceptance marker";
              wantedBy = [ "system-manager.target" ];
              serviceConfig = {
                Type = "oneshot";
                RemainAfterExit = true;
                ExecStart = "$${pkgs.coreutils}/bin/true";
              };
            };
          };
        })
      ];
    };
  };
}
`, systemManagerNixpkgs, systemManagerBranch, marker)
}

// systemManagerHCL renders one nixos_system_manager step. `extra` is injected
// verbatim into the resource block.
func systemManagerHCL(t acctest.Target, flake, extra string) string {
	indented := strings.ReplaceAll(flake, "\n", "\n    ")
	return fmt.Sprintf(`
resource "nixos_system_manager" "this" {
  ssh_host             = %q
  ssh_port             = %s
  ssh_user             = %q
  ssh_private_key      = file(%q)
  system_manager_flake = %q
  %s
  configuration_files = {
    "flake.nix" = <<-EOT
    %s
    EOT
  }
}
`, t.Host, t.Port, t.User, t.KeyPath, "github:numtide/system-manager/"+systemManagerBranch, extra, indented)
}

// TestAcc_SystemManager_Lifecycle runs against the Debian VM (SYSMGR_TEST_HOST,
// see test/debian/run.sh). It covers: installing Nix on a host that has none,
// the first switch, key deployment, and an in-place update when the flake
// changes. It is skipped when SYSMGR_TEST_HOST is unset.
func TestAcc_SystemManager_Lifecycle(t *testing.T) {
	target, err := acctest.SystemManagerTargetFromEnv()
	if err != nil {
		t.Skip(err.Error())
	}
	const keyContent = "sysmgr-secret-payload"
	keys := fmt.Sprintf(`keys = {
    "sysmgr-secret" = {
      content = %q
      user    = "root"
      group   = "root"
      mode    = "0640"
    }
  }`, keyContent)

	remote := func(cmd string) string {
		cli := acctest.SSHClient(t, target)
		return strings.TrimSpace(acctest.RunRemote(t, cli, cmd))
	}
	checkHost := func(marker string) resource.TestCheckFunc {
		return func(_ *tftest.State) error {
			if got := remote("cat /etc/sysmgr-acctest-marker"); got != marker {
				return fmt.Errorf("/etc/sysmgr-acctest-marker = %q, want %q", got, marker)
			}
			if got := remote("systemctl is-active sysmgr-acctest.service"); got != "active" {
				return fmt.Errorf("sysmgr-acctest.service is %q, want active", got)
			}
			if got := remote("cat /var/keys/sysmgr-secret"); got != keyContent {
				return fmt.Errorf("key content = %q, want %q", got, keyContent)
			}
			if got := remote("stat -c %a /var/keys/sysmgr-secret"); got != "640" {
				return fmt.Errorf("key mode = %q, want 640", got)
			}
			if got := remote("readlink -f /nix/var/nix/profiles/system-manager-profiles/system-manager"); !strings.HasPrefix(got, "/nix/store/") {
				return fmt.Errorf("system-manager profile resolves to %q, want a store path", got)
			}
			if got := remote("readlink -f /run/system-manager/sw"); !strings.HasPrefix(got, "/nix/store/") {
				return fmt.Errorf("/run/system-manager/sw resolves to %q, want a store path", got)
			}
			if got := remote("test -x /nix/var/nix/profiles/default/bin/nix && echo ok"); got != "ok" {
				return fmt.Errorf("nix not installed at the default profile")
			}
			return nil
		}
	}
	// After two applies with different closures, pruning must leave exactly
	// the current and the previous generation.
	checkGenerationCount := func(want string) resource.TestCheckFunc {
		return func(_ *tftest.State) error {
			got := remote("PATH=/nix/var/nix/profiles/default/bin:$PATH nix-env -p /nix/var/nix/profiles/system-manager-profiles/system-manager --list-generations | wc -l")
			if got != want {
				return fmt.Errorf("system-manager profile has %s generations, want %s", got, want)
			}
			return nil
		}
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheckTarget(t, target) },
		ProtoV6ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: systemManagerHCL(target, minimalSystemManagerFlake("v1"), keys),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"nixos_system_manager.this",
						tfjsonpath.New("system_hash"),
						knownvalue.StringRegexp(mustCompile(t, systemHashRegex)),
					),
				},
				Check: checkHost("v1"),
			},
			{
				Config: systemManagerHCL(target, minimalSystemManagerFlake("v2"), keys),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"nixos_system_manager.this", plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(checkHost("v2"), checkGenerationCount("2")),
			},
		},
	})
}
