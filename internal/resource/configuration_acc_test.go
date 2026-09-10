package resource_test

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
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
	return configHCLExtra(t, flake, useAgent, withBuildHost, "")
}

// configHCLExtra is configHCL with additional raw attribute lines (`extra`)
// injected into the resource block.
func configHCLExtra(t acctest.Target, flake string, useAgent, withBuildHost bool, extra string) string {
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
  %s
  configuration_files = {
    "flake.nix" = <<-EOT
    %s
    EOT
  }
}
`, t.Host, t.Port, t.User, auth, build, extra, indented)
}

// systemGenerations returns the number of generations in the target's system
// profile.
func systemGenerations(t *testing.T, target acctest.Target) int {
	t.Helper()
	cli := acctest.SSHClient(t, target)
	out := strings.TrimSpace(acctest.RunRemote(t, cli,
		"nix-env -p /nix/var/nix/profiles/system --list-generations | wc -l"))
	n, err := strconv.Atoi(out)
	if err != nil {
		t.Fatalf("parsing generation count %q: %v", out, err)
	}
	return n
}

// checkGenerations returns a TestCheckFunc asserting on the generation count.
func checkGenerations(t *testing.T, target acctest.Target, ok func(int) bool, want string) resource.TestCheckFunc {
	return func(_ *tftest.State) error {
		n := systemGenerations(t, target)
		if !ok(n) {
			return fmt.Errorf("system profile has %d generations, want %s", n, want)
		}
		return nil
	}
}

// systemHashRegex matches the format the provider stores: "sha256:<base32>".
const systemHashRegex = `^sha256:[a-z0-9]+$`

// TestAcc_Configuration_PrivateKey_Lifecycle covers the default auth path:
// apply → update → destroy with a literal SSH private key. It also verifies
// generation pruning: the default keeps previous generations (so rollback is
// possible), keep_generations = 1 prunes down to the current one, and
// keep_generations = 0 disables pruning entirely.
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
				// Two applies in this test with the default keep_generations
				// must leave at least two generations (v1 is still bootable).
				Check: checkGenerations(t, target, func(n int) bool { return n >= 2 }, ">= 2"),
			},
			{
				Config: configHCLExtra(target, minimalNixOSFlake(pub, "v2"), false, false,
					"keep_generations = 1"),
				Check: checkGenerations(t, target, func(n int) bool { return n == 1 }, "== 1"),
			},
			{
				Config: configHCLExtra(target, minimalNixOSFlake(pub, "v3"), false, false,
					"keep_generations = 0"),
				// Previous step left exactly 1; this deploy adds one and must
				// not prune.
				Check: checkGenerations(t, target, func(n int) bool { return n == 2 }, "== 2"),
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
// private keys on both ends. It also verifies that the temporary target key
// materialized on the build host (a unique mktemp file) is removed afterwards.
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
				Check: func(_ *tftest.State) error {
					cli := acctest.SSHClient(t, target)
					out := strings.TrimSpace(acctest.RunRemote(t, cli,
						"ls -1 /tmp/.terraform-nixos-key.* /tmp/.terraform-nixos-target-key 2>/dev/null | wc -l"))
					if out != "0" {
						return fmt.Errorf("expected no leftover temporary key files on build host, found %s", out)
					}
					return nil
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
