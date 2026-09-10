package resource

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/bartei/terraform-provider-nixos/internal/sshclient"
)

var (
	_ resource.Resource = &SystemManagerResource{}
)

// Paths used by system-manager (see crates/system-manager-engine/src/lib.rs
// in numtide/system-manager). Note that /run/system-manager is a directory
// (holding the `sw` link to the merged package path), not a link to the
// active generation, so the profile link is what identifies the deployed
// configuration.
const (
	systemManagerProfile = "/nix/var/nix/profiles/system-manager-profiles/system-manager"
	nixDefaultProfileBin = "/nix/var/nix/profiles/default/bin"
)

// SystemManagerResource manages a numtide/system-manager profile on a
// non-NixOS Linux host (Debian, Ubuntu, ...). It is the counterpart of
// nixos_configuration for machines where the OS itself is not NixOS but we
// still want NixOS-style modules (systemd units, /etc files, packages) applied
// declaratively from a flake.
type SystemManagerResource struct{}

type SystemManagerModel struct {
	ID                 types.String `tfsdk:"id"`
	SSHHost            types.String `tfsdk:"ssh_host"`
	SSHPort            types.Int64  `tfsdk:"ssh_port"`
	SSHUser            types.String `tfsdk:"ssh_user"`
	SSHPrivateKey      types.String `tfsdk:"ssh_private_key"`
	SSHAgent           types.Bool   `tfsdk:"ssh_use_agent"`
	ConfigurationFiles types.Map    `tfsdk:"configuration_files"`
	ConfigurationName  types.String `tfsdk:"configuration_name"`
	RemoteDirectory    types.String `tfsdk:"remote_directory"`
	Keys               types.Map    `tfsdk:"keys"`
	InstallNix         types.Bool   `tfsdk:"install_nix"`
	NixInstallerURL    types.String `tfsdk:"nix_installer_url"`
	SystemManagerFlake types.String `tfsdk:"system_manager_flake"`
	AllowUnfree        types.Bool   `tfsdk:"allow_unfree"`
	AllowInsecure      types.Bool   `tfsdk:"allow_insecure"`
	GarbageCollect     types.Bool   `tfsdk:"garbage_collect"`
	SystemHash         types.String `tfsdk:"system_hash"`
}

func NewSystemManagerResource() resource.Resource {
	return &SystemManagerResource{}
}

func (r *SystemManagerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_system_manager"
}

func (r *SystemManagerResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a numtide/system-manager profile on a non-NixOS Linux host via SSH. " +
			"Optionally installs Nix, uploads the flake files, deploys secret keys, then runs " +
			"`system-manager switch` so the flake's systemd units, /etc files and packages are " +
			"activated. Use this for Debian/Ubuntu machines (e.g. Proxmox hosts) that cannot run NixOS.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Resource identifier (host:configuration_name).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"ssh_host": schema.StringAttribute{
				Required:    true,
				Description: "IP or hostname of the target machine.",
			},
			"ssh_port": schema.Int64Attribute{
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(22),
				Description: "SSH port for the target machine.",
			},
			"ssh_user": schema.StringAttribute{
				Required:    true,
				Description: "SSH user for the target machine. Must be root: system-manager needs root to activate.",
			},
			"ssh_private_key": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "SSH private key for authentication.",
			},
			"ssh_use_agent": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "Use ssh agent for connecting to target.",
			},
			"configuration_files": schema.MapAttribute{
				Required:    true,
				ElementType: types.StringType,
				Description: "Map of relative file paths to their contents for the system-manager flake " +
					"(e.g. {\"flake.nix\" = file(\"nix/flake.nix\")}). Changes to any file " +
					"trigger a rebuild and switch.",
			},
			"configuration_name": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("default"),
				Description: "Name of the `systemConfigs.<name>` output in the flake.",
			},
			"remote_directory": schema.StringAttribute{
				Optional:   true,
				Computed:   true,
				Default:    stringdefault.StaticString("/root/system-manager"),
				Validators: []validator.String{remoteDirValidator{}},
				Description: "Remote directory where the flake is uploaded. It is deleted and " +
					"recreated on every deploy, so it must be a clean absolute path with at least " +
					"two components (e.g. /root/system-manager) and not under a system directory " +
					"such as /nix or /usr.",
			},
			"keys": schema.MapNestedAttribute{
				Optional:    true,
				Description: "Secret files to deploy to the target host before switching.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"content": schema.StringAttribute{
							Required:    true,
							Sensitive:   true,
							Description: "File content.",
						},
						"destination": schema.StringAttribute{
							Optional:    true,
							Computed:    true,
							Default:     stringdefault.StaticString("/var/keys"),
							Description: "Directory on the target to place the key file.",
						},
						"user": schema.StringAttribute{
							Required:    true,
							Description: "Owner user for the key file.",
						},
						"group": schema.StringAttribute{
							Required:    true,
							Description: "Owner group for the key file.",
						},
						"mode": schema.StringAttribute{
							Required:    true,
							Description: "File permission mode passed to chmod (e.g. \"0640\").",
						},
					},
				},
			},
			"install_nix": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
				Description: "Install Nix (multi-user, daemon mode) with the official installer if " +
					"/nix/var/nix/profiles/default/bin/nix is missing on the target.",
			},
			"nix_installer_url": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("https://nixos.org/nix/install"),
				Description: "URL of the Nix install script used when install_nix is true.",
			},
			"system_manager_flake": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString("github:numtide/system-manager"),
				Description: "Flake reference of the system-manager CLI to run on the target " +
					"(e.g. github:numtide/system-manager/release-26.05). Pin it to the same " +
					"branch as the `system-manager` input of your flake.",
			},
			"allow_unfree": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "Set NIXPKGS_ALLOW_UNFREE=1 during build.",
			},
			"allow_insecure": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "Set NIXPKGS_ALLOW_INSECURE=1 during build.",
			},
			"garbage_collect": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(true),
				Description: "After switching, delete system-manager profile generations older than the " +
					"previous one (the current and one previous generation are kept) and run nix garbage collection.",
			},
			"system_hash": schema.StringAttribute{
				Computed:    true,
				Description: "Nix store hash of the active system-manager profile generation after deployment.",
			},
		},
	}
}

func (r *SystemManagerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan SystemManagerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	r.deploy(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	plan.ID = types.StringValue(fmt.Sprintf("%s:%s", plan.SSHHost.ValueString(), plan.ConfigurationName.ValueString()))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *SystemManagerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state SystemManagerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	client, err := sshclient.New(state.SSHHost.ValueString(), int(state.SSHPort.ValueInt64()), state.SSHUser.ValueString(), state.SSHAgent.ValueBool(), state.SSHPrivateKey.ValueString())
	if err != nil {
		// Host unreachable — keep state as-is, don't error on refresh
		tflog.Warn(ctx, "Cannot connect to host during refresh", map[string]interface{}{
			"host":  state.SSHHost.ValueString(),
			"error": err.Error(),
		})
		return
	}
	defer client.Close()

	hash, _, err := client.Run(systemManagerHashCmd())
	if err != nil {
		tflog.Warn(ctx, "Failed to read system-manager profile hash", map[string]interface{}{"error": err.Error()})
		return
	}

	state.SystemHash = types.StringValue(strings.TrimSpace(hash))
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *SystemManagerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan SystemManagerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	r.deploy(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *SystemManagerResource) Delete(ctx context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
	tflog.Info(ctx, "system-manager profile removed from Terraform state. The running system is unchanged; "+
		"run `system-manager deactivate` on the host to remove the managed units and files.")
}

// nixEnv builds the environment prefix for every nix invocation on the target.
// Non-interactive SSH sessions on Debian do not source /etc/profile.d/nix.sh,
// so the default profile is put on PATH explicitly. Experimental features are
// enabled through NIX_CONFIG instead of editing /etc/nix/nix.conf, and
// accept-flake-config lets the system-manager flake's substituter settings
// (cache.numtide.com) apply without a prompt.
func nixEnv(plan *SystemManagerModel) string {
	parts := []string{
		fmt.Sprintf("PATH=%s:$PATH", nixDefaultProfileBin),
		`NIX_CONFIG="$(printf 'experimental-features = nix-command flakes\naccept-flake-config = true')"`,
	}
	if plan.AllowUnfree.ValueBool() {
		parts = append(parts, "NIXPKGS_ALLOW_UNFREE=1")
	}
	if plan.AllowInsecure.ValueBool() {
		parts = append(parts, "NIXPKGS_ALLOW_INSECURE=1")
	}
	return strings.Join(parts, " ") + " "
}

// systemManagerHashCmd returns the store hash of the active system-manager
// profile generation (the target of the profile link).
func systemManagerHashCmd() string {
	return fmt.Sprintf("PATH=%s:$PATH nix-store --query --hash \"$(readlink -f %s)\"", nixDefaultProfileBin, systemManagerProfile)
}

// deploy is the shared logic for Create and Update.
func (r *SystemManagerResource) deploy(ctx context.Context, plan *SystemManagerModel, diags *diag.Diagnostics) {
	host := plan.SSHHost.ValueString()
	port := int(plan.SSHPort.ValueInt64())
	user := plan.SSHUser.ValueString()
	key := plan.SSHPrivateKey.ValueString()
	useAgent := plan.SSHAgent.ValueBool()
	remoteDir := plan.RemoteDirectory.ValueString()
	configName := plan.ConfigurationName.ValueString()

	// --- Connect to target ---
	progress(ctx, fmt.Sprintf("Connecting to %s@%s:%d", user, host, port))
	target, err := sshclient.New(host, port, user, useAgent, key)
	if err != nil {
		diags.AddError("Target SSH Connection Failed",
			fmt.Sprintf("Could not connect to %s@%s:%d: %s", user, host, port, err))
		return
	}
	defer target.Close()

	// --- Step 1: Ensure Nix is installed ---
	if _, _, err := target.Run(fmt.Sprintf("test -x %s/nix", nixDefaultProfileBin)); err != nil {
		if !plan.InstallNix.ValueBool() {
			diags.AddError("Nix not found on target",
				fmt.Sprintf("%s/nix is missing and install_nix is false.", nixDefaultProfileBin))
			return
		}
		progress(ctx, fmt.Sprintf("Nix not found, installing (multi-user daemon) from %s", plan.NixInstallerURL.ValueString()))
		installCmd := fmt.Sprintf(
			"set -e; curl -fsSL %q -o /tmp/nix-install.sh && sh /tmp/nix-install.sh --daemon --yes; rm -f /tmp/nix-install.sh; "+
				"grep -qs '^experimental-features' /etc/nix/nix.conf || echo 'experimental-features = nix-command flakes' >> /etc/nix/nix.conf",
			plan.NixInstallerURL.ValueString(),
		)
		if err := target.RunStreaming(installCmd, streamer(ctx, "nix-install")); err != nil {
			diags.AddError("Nix installation failed", err.Error())
			return
		}
		if _, _, err := target.Run(fmt.Sprintf("test -x %s/nix", nixDefaultProfileBin)); err != nil {
			diags.AddError("Nix installation failed", fmt.Sprintf("%s/nix still missing after running the installer", nixDefaultProfileBin))
			return
		}
	}
	version, _, err := target.Run(fmt.Sprintf("%s/nix --version", nixDefaultProfileBin))
	if err != nil {
		diags.AddError("Nix is not runnable on target", err.Error())
		return
	}
	progress(ctx, fmt.Sprintf("Using %s", strings.TrimSpace(version)))

	// --- Step 2: Upload configuration files ---
	var configFiles map[string]string
	diags.Append(plan.ConfigurationFiles.ElementsAs(ctx, &configFiles, false)...)
	if diags.HasError() {
		return
	}
	progress(ctx, fmt.Sprintf("Uploading %d configuration files to %s:%s", len(configFiles), host, remoteDir))
	if err := target.WriteFiles(remoteDir, configFiles); err != nil {
		diags.AddError("Failed to upload configuration files", err.Error())
		return
	}

	// --- Step 3: Deploy secret keys ---
	if !plan.Keys.IsNull() && !plan.Keys.IsUnknown() {
		var keys map[string]KeyModel
		diags.Append(plan.Keys.ElementsAs(ctx, &keys, false)...)
		if diags.HasError() {
			return
		}
		deployKeys(ctx, target, keys, diags)
		if diags.HasError() {
			return
		}
	}

	// --- Step 4: Build, register and activate via system-manager ---
	// `switch` = nix build → register profile → activate (units + /etc).
	// The CLI itself is fetched from system_manager_flake so it can be pinned
	// to the same release branch as the flake's system-manager input.
	switchCmd := fmt.Sprintf("%snix run %q -- switch --flake %q",
		nixEnv(plan), plan.SystemManagerFlake.ValueString(), fmt.Sprintf("%s#%s", remoteDir, configName))
	progress(ctx, fmt.Sprintf("Switching system-manager configuration (%s#%s)", remoteDir, configName))
	if err := target.RunStreaming(switchCmd, streamer(ctx, "switch")); err != nil {
		diags.AddError("system-manager switch failed", err.Error())
		return
	}

	// --- Step 5: Cleanup old generations ---
	if plan.GarbageCollect.ValueBool() {
		// Keep the current and the previous generation so a bad deploy can
		// still be rolled back by hand.
		progress(ctx, "Pruning system-manager generations (keeping current and previous)")
		if _, stderr, err := target.Run(fmt.Sprintf("%snix-env -p %s --delete-generations +2", nixEnv(plan), systemManagerProfile)); err != nil {
			tflog.Warn(ctx, "Failed to delete old system-manager generations", map[string]interface{}{
				"error":  err.Error(),
				"stderr": stderr,
			})
		}
		progress(ctx, "Running nix garbage collection")
		target.RunStreaming(fmt.Sprintf("%snix-store --gc", nixEnv(plan)), streamer(ctx, "gc"))
	}

	// --- Step 6: Read profile hash ---
	hashOutput, hashStderr, err := target.Run(systemManagerHashCmd())
	if err != nil {
		diags.AddError("Failed to read system-manager profile hash after deployment",
			fmt.Sprintf("%s: %s", err, strings.TrimSpace(hashStderr)))
		return
	}
	plan.SystemHash = types.StringValue(strings.TrimSpace(hashOutput))

	progress(ctx, fmt.Sprintf("Deployment complete (system_hash: %s)", plan.SystemHash.ValueString()))
}

// deployKeys writes secret files to the target with the requested ownership
// and mode, then verifies they exist.
func deployKeys(ctx context.Context, target *sshclient.Client, keys map[string]KeyModel, diags *diag.Diagnostics) {
	for name, k := range keys {
		dest := k.Destination.ValueString()
		remotePath := fmt.Sprintf("%s/%s", dest, name)
		progress(ctx, fmt.Sprintf("Deploying key %s to %s", name, remotePath))

		if _, _, err := target.Run(fmt.Sprintf("mkdir -p %s", dest)); err != nil {
			diags.AddError(fmt.Sprintf("Failed to create key directory %s", dest), err.Error())
			return
		}
		if err := target.WriteFile(remotePath, []byte(k.Content.ValueString()), 0600); err != nil {
			diags.AddError(fmt.Sprintf("Failed to write key %s", name), err.Error())
			return
		}
		if _, _, err := target.Run(fmt.Sprintf("chown %s:%s %s", k.User.ValueString(), k.Group.ValueString(), remotePath)); err != nil {
			diags.AddError(fmt.Sprintf("Failed to set ownership on %s", name), err.Error())
			return
		}
		if _, _, err := target.Run(fmt.Sprintf("chmod %s %s", k.Mode.ValueString(), remotePath)); err != nil {
			diags.AddError(fmt.Sprintf("Failed to set permissions on %s", name), err.Error())
			return
		}
	}

	for name, k := range keys {
		remotePath := fmt.Sprintf("%s/%s", k.Destination.ValueString(), name)
		if _, _, err := target.Run(fmt.Sprintf("test -f %s", remotePath)); err != nil {
			diags.AddError("Key verification failed",
				fmt.Sprintf("Key %s not found at %s after deployment", name, remotePath))
			return
		}
	}
}
