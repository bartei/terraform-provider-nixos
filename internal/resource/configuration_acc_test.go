package resource_test

import (
	"fmt"
	"os"
	"regexp"
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

func mustCompile(t *testing.T, pat string) *regexp.Regexp {
	t.Helper()
	re, err := regexp.Compile(pat)
	if err != nil {
		t.Fatalf("compile %q: %v", pat, err)
	}
	return re
}

// minimalNixOSFlake returns a flake.nix the test target can `nixos-rebuild
// switch` to. It bakes the test pubkey into authorized_keys (otherwise switch
// would lock the next test step out) and embeds `marker` into a file inside
// /etc so consecutive applies produce different system_hash values.
//
// The output is later embedded inside a Terraform <<-EOT heredoc, where
// `${...}` is interpreted as Terraform interpolation. We emit `$${...}` so
// terraform passes the literal `${...}` through to the file the provider
// uploads.
func minimalNixOSFlake(pubKey, marker string) string {
	return fmt.Sprintf(`{
  description = "acctest";
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-24.11";
  outputs = { self, nixpkgs }: {
    nixosConfigurations.this = nixpkgs.lib.nixosSystem {
      system = "x86_64-linux";
      modules = [
        ({ modulesPath, ... }: {
          imports = [ "$${modulesPath}/profiles/qemu-guest.nix" ];
          boot.loader.grub = { enable = true; device = "/dev/vda"; };
          fileSystems."/" = { device = "/dev/vda1"; fsType = "ext4"; };
          networking.useDHCP = true;
          networking.firewall.enable = false;
          services.openssh = {
            enable = true;
            ports = [ 22 22222 ];
            settings = {
              PermitRootLogin = "yes";
              PasswordAuthentication = false;
              AllowAgentForwarding = "yes";
              PerSourcePenalties = "no";
            };
          };
          users.users.root.openssh.authorizedKeys.keys = [ %q ];
          environment.etc."acctest-marker".text = %q;
          system.stateVersion = "24.11";
        })
      ];
    };
  };
}
`, pubKey, marker)
}

// readPubKey reads the .pub sibling of NIXOS_TEST_KEY_PATH so we can bake it
// into deployed configs.
func readPubKey(t *testing.T, target acctest.Target) string {
	t.Helper()
	b, err := os.ReadFile(target.KeyPath + ".pub")
	if err != nil {
		t.Fatalf("reading pubkey: %v", err)
	}
	return strings.TrimSpace(string(b))
}

// configHCL renders the HCL for one acceptance step.
//
//   - useAgent=false: ssh_private_key = file(<KeyPath>)
//   - useAgent=true:  ssh_use_agent = true (caller must set SSH_AUTH_SOCK)
//   - withBuildHost=true: also sets build_host/build_port/build_user, plus
//     either build_private_key or build_use_agent depending on useAgent.
func configHCL(t acctest.Target, flake string, useAgent, withBuildHost bool) string {
	var auth string
	if useAgent {
		auth = "ssh_use_agent = true"
	} else {
		auth = fmt.Sprintf("ssh_private_key = file(%q)", t.KeyPath)
	}

	build := ""
	if withBuildHost {
		var buildAuth string
		if useAgent {
			buildAuth = "build_use_agent = true"
		} else {
			buildAuth = fmt.Sprintf("build_private_key = file(%q)", t.KeyPath)
		}
		build = fmt.Sprintf(`
  build_host = %q
  build_port = %s
  build_user = %q
  %s
`, t.Host, t.Port, t.User, buildAuth)
	}

	// Indent the heredoc body by 4 spaces — the leading whitespace is
	// stripped by Terraform's `<<-EOT` indented-heredoc syntax.
	indented := strings.ReplaceAll(flake, "\n", "\n    ")

	return fmt.Sprintf(`
resource "nixos_configuration" "this" {
  ssh_host = %q
  ssh_port = %s
  ssh_user = %q
  %s
%s
  configuration_files = {
    "flake.nix" = <<-EOT
    %s
    EOT
  }
}
`, t.Host, t.Port, t.User, auth, build, indented)
}

// systemHashRegex matches the format the provider stores: "sha256:<base32>".
const systemHashRegex = `^sha256:[a-z0-9]+$`

// TestAcc_Configuration_PrivateKey_Lifecycle covers the default auth path:
// apply → update → destroy with a literal SSH private key.
func TestAcc_Configuration_PrivateKey_Lifecycle(t *testing.T) {
	target, err := acctest.TargetFromEnv()
	if err != nil {
		t.Skip(err.Error())
	}
	pub := readPubKey(t, target)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: configHCL(target, minimalNixOSFlake(pub, "v1"), false, false),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"nixos_configuration.this",
						tfjsonpath.New("system_hash"),
						knownvalue.StringRegexp(mustCompile(t, systemHashRegex)),
					),
				},
			},
			{
				Config: configHCL(target, minimalNixOSFlake(pub, "v2"), false, false),
				// The closure changes (different /etc/acctest-marker), so the
				// terraform plan must show a non-empty diff for system_hash.
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(
							"nixos_configuration.this", plancheck.ResourceActionUpdate),
					},
				},
			},
		},
	})
}

// TestAcc_Configuration_SSHAgent covers the ssh-agent auth path. The harness
// starts a transient agent, ssh-adds the test key, and exports SSH_AUTH_SOCK
// for the duration of the test.
func TestAcc_Configuration_SSHAgent(t *testing.T) {
	target, err := acctest.TargetFromEnv()
	if err != nil {
		t.Skip(err.Error())
	}
	pub := readPubKey(t, target)

	socket := acctest.StartAgent(t, target)
	t.Setenv("SSH_AUTH_SOCK", socket)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: configHCL(target, minimalNixOSFlake(pub, "agent"), true, false),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"nixos_configuration.this",
						tfjsonpath.New("system_hash"),
						knownvalue.StringRegexp(mustCompile(t, systemHashRegex)),
					),
				},
			},
		},
	})
}

// TestAcc_Configuration_Keys verifies the keys-deployment branch: a key is
// uploaded, ownership and mode are set, and the file appears at the expected
// remote path.
func TestAcc_Configuration_Keys(t *testing.T) {
	target, err := acctest.TargetFromEnv()
	if err != nil {
		t.Skip(err.Error())
	}
	pub := readPubKey(t, target)
	const keyContent = "secret-acctest-payload"

	hcl := strings.Replace(
		configHCL(target, minimalNixOSFlake(pub, "keys"), false, false),
		"configuration_files = {",
		fmt.Sprintf(`keys = {
    "test-secret" = {
      content     = %q
      destination = "/var/keys"
      user        = "root"
      group       = "root"
      mode        = "0600"
    }
  }

  configuration_files = {`, keyContent),
		1,
	)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: hcl,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("nixos_configuration.this", "system_hash"),
					func(_ *tftest.State) error {
						cli := acctest.SSHClient(t, target)
						out := acctest.RunRemote(t, cli, "cat /var/keys/test-secret")
						if strings.TrimSpace(out) != keyContent {
							return fmt.Errorf("key content mismatch: got %q want %q",
								out, keyContent)
						}
						mode := strings.TrimSpace(
							acctest.RunRemote(t, cli, "stat -c %a /var/keys/test-secret"))
						if mode != "600" {
							return fmt.Errorf("key mode = %q, want 600", mode)
						}
						return nil
					},
				),
			},
		},
	})
}

// TestAcc_Configuration_BuildHost_PrivateKey exercises switchViaBuildHost
// with the same VM as both target and build host, authenticated by literal
// private keys on both ends.
func TestAcc_Configuration_BuildHost_PrivateKey(t *testing.T) {
	target, err := acctest.TargetFromEnv()
	if err != nil {
		t.Skip(err.Error())
	}
	pub := readPubKey(t, target)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: configHCL(target, minimalNixOSFlake(pub, "buildhost-key"), false, true),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"nixos_configuration.this",
						tfjsonpath.New("system_hash"),
						knownvalue.StringRegexp(mustCompile(t, systemHashRegex)),
					),
				},
			},
		},
	})
}

// TestAcc_Configuration_BuildHost_Agent exercises switchViaBuildHost with
// ssh-agent auth on both target and build host. Crucially, nix-copy-closure
// runs on the build host and must authenticate to the target via *forwarded*
// agent — there's no key file to materialize.
func TestAcc_Configuration_BuildHost_Agent(t *testing.T) {
	target, err := acctest.TargetFromEnv()
	if err != nil {
		t.Skip(err.Error())
	}
	pub := readPubKey(t, target)

	socket := acctest.StartAgent(t, target)
	t.Setenv("SSH_AUTH_SOCK", socket)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: configHCL(target, minimalNixOSFlake(pub, "buildhost-agent"), true, true),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(
						"nixos_configuration.this",
						tfjsonpath.New("system_hash"),
						knownvalue.StringRegexp(mustCompile(t, systemHashRegex)),
					),
				},
			},
		},
	})
}
