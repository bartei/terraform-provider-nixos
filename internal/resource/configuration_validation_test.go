package resource_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/bartei/terraform-provider-nixos/internal/acctest"
)

// phraseRegexp matches phrase even if Terraform wraps the diagnostic across
// lines (whitespace between words becomes \s+).
func phraseRegexp(phrase string) *regexp.Regexp {
	words := strings.Fields(regexp.QuoteMeta(phrase))
	return regexp.MustCompile(strings.Join(words, `\s+`))
}

// validationHCL renders a resource block with the given extra attributes. The
// SSH settings are placeholders: these tests must fail at plan-time validation
// before the provider ever attempts a connection.
func validationHCL(extra string) string {
	return fmt.Sprintf(`
resource "nixos_configuration" "this" {
  ssh_host        = "192.0.2.1"
  ssh_user        = "root"
  ssh_private_key = "not-a-real-key"
  %s
  configuration_files = {
    "flake.nix" = "{}"
  }
}
`, extra)
}

// TestConfiguration_RemoteDirectoryValidation verifies that dangerous
// remote_directory values are rejected at plan time (the directory is
// `rm -rf`ed on every deploy). No TF_ACC or remote host required.
func TestConfiguration_RemoteDirectoryValidation(t *testing.T) {
	cases := []struct {
		dir  string
		want string
	}{
		{"/", "too shallow"},
		{"/root", "too shallow"},
		{"/home", "too shallow"},
		{"/nix/store", "must not be under /nix"},
		{"/boot/efi", "must not be under /boot"},
		{"/root/nix/", "must be a clean path"},
		{"/root/../etc", "must be a clean path"},
		{"root/nix", "must be an absolute path"},
		{"/root/my nix", "unsupported character"},
		{"/root/nix;rm -rf /", "unsupported character"},
	}
	for _, tc := range cases {
		t.Run(tc.dir, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: acctest.ProviderFactories(),
				Steps: []resource.TestStep{{
					Config:      validationHCL(fmt.Sprintf("remote_directory = %q", tc.dir)),
					ExpectError: phraseRegexp(tc.want),
				}},
			})
		})
	}
}

// TestConfiguration_KeepGenerationsValidation verifies keep_generations
// rejects negative values at plan time.
func TestConfiguration_KeepGenerationsValidation(t *testing.T) {
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: acctest.ProviderFactories(),
		Steps: []resource.TestStep{{
			Config:      validationHCL("keep_generations = -1"),
			ExpectError: phraseRegexp("Value must be at least 0"),
		}},
	})
}

// TestSystemManager_RemoteDirectoryValidation verifies nixos_system_manager
// applies the same plan-time remote_directory guard as nixos_configuration.
func TestSystemManager_RemoteDirectoryValidation(t *testing.T) {
	for _, tc := range []struct{ dir, want string }{
		{"/root", "too shallow"},
		{"/nix/var/nix", "must not be under /nix"},
		{"/usr/local/sm", "must not be under /usr"},
	} {
		t.Run(tc.dir, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: acctest.ProviderFactories(),
				Steps: []resource.TestStep{{
					Config: fmt.Sprintf(`
resource "nixos_system_manager" "this" {
  ssh_host            = "192.0.2.1"
  ssh_user            = "root"
  ssh_private_key     = "not-a-real-key"
  remote_directory    = %q
  configuration_files = { "flake.nix" = "{}" }
}
`, tc.dir),
					ExpectError: phraseRegexp(tc.want),
				}},
			})
		})
	}
}
