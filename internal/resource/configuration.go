package resource

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/bartei/terraform-provider-nixos/internal/sshclient"
)

var (
	_ resource.Resource = &ConfigurationResource{}
)

type ConfigurationResource struct{}

type ConfigurationModel struct {
	ID                 types.String `tfsdk:"id"`
	SSHHost            types.String `tfsdk:"ssh_host"`
	SSHUser            types.String `tfsdk:"ssh_user"`
	SSHPrivateKey      types.String `tfsdk:"ssh_private_key"`
	ConfigurationFiles types.Map    `tfsdk:"configuration_files"`
	ConfigurationName  types.String `tfsdk:"configuration_name"`
	RemoteDirectory    types.String `tfsdk:"remote_directory"`
	Keys               types.Map    `tfsdk:"keys"`
	BuildHost          types.String `tfsdk:"build_host"`
	BuildUser          types.String `tfsdk:"build_user"`
	BuildPrivateKey    types.String `tfsdk:"build_private_key"`
	AllowUnfree        types.Bool   `tfsdk:"allow_unfree"`
	AllowInsecure      types.Bool   `tfsdk:"allow_insecure"`
	GarbageCollect     types.Bool   `tfsdk:"garbage_collect"`
	SystemHash         types.String `tfsdk:"system_hash"`
}

type KeyModel struct {
	Content     types.String `tfsdk:"content"`
	Destination types.String `tfsdk:"destination"`
	User        types.String `tfsdk:"user"`
	Group       types.String `tfsdk:"group"`
	Mode        types.String `tfsdk:"mode"`
}

func NewConfigurationResource() resource.Resource {
	return &ConfigurationResource{}
}

func (r *ConfigurationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_configuration"
}

func (r *ConfigurationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a NixOS configuration on a remote host via SSH. " +
			"Uploads Nix flake files, deploys secret keys, builds the configuration, " +
			"and switches the system to the new generation.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Resource identifier (host:configuration_name).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"ssh_host": schema.StringAttribute{
				Required:    true,
				Description: "IP or hostname of the target NixOS machine.",
			},
			"ssh_user": schema.StringAttribute{
				Required:    true,
				Description: "SSH user for the target machine.",
			},
			"ssh_private_key": schema.StringAttribute{
				Required:    true,
				Sensitive:   true,
				Description: "SSH private key for authentication.",
			},
			"configuration_files": schema.MapAttribute{
				Required:    true,
				ElementType: types.StringType,
				Description: "Map of relative file paths to their contents for the NixOS flake " +
					"(e.g. {\"flake.nix\" = file(\"nix/flake.nix\")}). Changes to any file " +
					"trigger a rebuild and switch.",
			},
			"configuration_name": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("this"),
				Description: "Name of the NixOS configuration output in the flake.",
			},
			"remote_directory": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("/root/nix"),
				Description: "Remote directory where the NixOS configuration is uploaded.",
			},
			"keys": schema.MapNestedAttribute{
				Optional:    true,
				Description: "Secret files to deploy to the target host before building.",
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
			"build_host": schema.StringAttribute{
				Optional: true,
				Description: "SSH host of a dedicated build machine. When set, the NixOS " +
					"configuration is built here and the closure is copied to the target. " +
					"The build host must be able to reach the target via SSH.",
			},
			"build_user": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("root"),
				Description: "SSH user for the build host.",
			},
			"build_private_key": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "SSH private key for the build host.",
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
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "Run nix garbage collection after switching.",
			},
			"system_hash": schema.StringAttribute{
				Computed:      true,
				Description:   "Nix store hash of the running system after deployment.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *ConfigurationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ConfigurationModel
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

func (r *ConfigurationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ConfigurationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	client, err := sshclient.New(state.SSHHost.ValueString(), state.SSHUser.ValueString(), state.SSHPrivateKey.ValueString())
	if err != nil {
		// Host unreachable — keep state as-is, don't error on refresh
		tflog.Warn(ctx, "Cannot connect to host during refresh", map[string]interface{}{
			"host":  state.SSHHost.ValueString(),
			"error": err.Error(),
		})
		return
	}
	defer client.Close()

	hash, _, err := client.Run("nix-store --query --hash /run/current-system")
	if err != nil {
		tflog.Warn(ctx, "Failed to read system hash", map[string]interface{}{"error": err.Error()})
		return
	}

	state.SystemHash = types.StringValue(strings.TrimSpace(hash))
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *ConfigurationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan ConfigurationModel
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

func (r *ConfigurationResource) Delete(ctx context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
	tflog.Info(ctx, "NixOS configuration removed from Terraform state. The running system is unchanged.")
}

// deploy is the shared logic for Create and Update. It uploads configuration
// files and keys, builds the NixOS configuration, switches to it, and reads
// back the system hash.
func (r *ConfigurationResource) deploy(ctx context.Context, plan *ConfigurationModel, diags *diag.Diagnostics) {
	host := plan.SSHHost.ValueString()
	user := plan.SSHUser.ValueString()
	key := plan.SSHPrivateKey.ValueString()
	remoteDir := plan.RemoteDirectory.ValueString()
	configName := plan.ConfigurationName.ValueString()

	// --- Connect to target ---
	tflog.Info(ctx, "Connecting to target host", map[string]interface{}{"host": host})
	target, err := sshclient.New(host, user, key)
	if err != nil {
		diags.AddError("Target SSH Connection Failed",
			fmt.Sprintf("Could not connect to %s@%s: %s", user, host, err))
		return
	}
	defer target.Close()

	// --- Determine build client ---
	buildClient := target
	useBuildHost := !plan.BuildHost.IsNull() && !plan.BuildHost.IsUnknown() && plan.BuildHost.ValueString() != ""

	if useBuildHost {
		bh := plan.BuildHost.ValueString()
		bu := plan.BuildUser.ValueString()
		tflog.Info(ctx, "Connecting to build host", map[string]interface{}{"host": bh})
		buildClient, err = sshclient.New(bh, bu, plan.BuildPrivateKey.ValueString())
		if err != nil {
			diags.AddError("Build Host SSH Connection Failed",
				fmt.Sprintf("Could not connect to %s@%s: %s", bu, bh, err))
			return
		}
		defer buildClient.Close()
	}

	// --- Extract configuration files ---
	var configFiles map[string]string
	diags.Append(plan.ConfigurationFiles.ElementsAs(ctx, &configFiles, false)...)
	if diags.HasError() {
		return
	}

	// --- Step 1: Upload configuration files ---
	tflog.Info(ctx, "Uploading NixOS configuration files", map[string]interface{}{
		"target":     buildClient.Host(),
		"remote_dir": remoteDir,
		"files":      len(configFiles),
	})
	if err := buildClient.WriteFiles(remoteDir, configFiles); err != nil {
		diags.AddError("Failed to upload configuration files", err.Error())
		return
	}

	// --- Step 2: Deploy secret keys to target ---
	if !plan.Keys.IsNull() && !plan.Keys.IsUnknown() {
		var keys map[string]KeyModel
		diags.Append(plan.Keys.ElementsAs(ctx, &keys, false)...)
		if diags.HasError() {
			return
		}

		for name, k := range keys {
			dest := k.Destination.ValueString()
			remotePath := fmt.Sprintf("%s/%s", dest, name)
			tflog.Info(ctx, "Deploying secret key", map[string]interface{}{
				"name": name,
				"path": remotePath,
			})

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

		// Verify all keys are present
		for name, k := range keys {
			remotePath := fmt.Sprintf("%s/%s", k.Destination.ValueString(), name)
			if _, _, err := target.Run(fmt.Sprintf("test -f %s", remotePath)); err != nil {
				diags.AddError("Key verification failed",
					fmt.Sprintf("Key %s not found at %s after deployment", name, remotePath))
				return
			}
		}
	}

	// --- Step 3: Ensure git is available on the build host ---
	tflog.Info(ctx, "Ensuring git is installed on build host")
	buildClient.RunStreaming("nix profile install nixpkgs#git", func(line string) {
		tflog.Debug(ctx, "[git-install] "+line)
	})

	// --- Step 4: Build environment variables ---
	var envParts []string
	if plan.AllowUnfree.ValueBool() {
		envParts = append(envParts, "NIXPKGS_ALLOW_UNFREE=1")
	}
	if plan.AllowInsecure.ValueBool() {
		envParts = append(envParts, "NIXPKGS_ALLOW_INSECURE=1")
	}
	env := ""
	if len(envParts) > 0 {
		env = strings.Join(envParts, " ") + " "
	}

	// --- Step 5: Build ---
	buildCmd := fmt.Sprintf("%snixos-rebuild build --flake %s#%s --impure", env, remoteDir, configName)
	tflog.Info(ctx, "Building NixOS configuration", map[string]interface{}{"command": buildCmd})
	if err := buildClient.RunStreaming(buildCmd, func(line string) {
		tflog.Info(ctx, "[build] "+line)
	}); err != nil {
		diags.AddError("NixOS build failed", err.Error())
		return
	}

	// --- Step 6: Switch ---
	if useBuildHost {
		r.switchViaBuildHost(ctx, plan, target, buildClient, env, remoteDir, configName, diags)
	} else {
		switchCmd := fmt.Sprintf("%snixos-rebuild switch --flake %s#%s --impure", env, remoteDir, configName)
		tflog.Info(ctx, "Switching NixOS configuration", map[string]interface{}{"command": switchCmd})
		if err := target.RunStreaming(switchCmd, func(line string) {
			tflog.Info(ctx, "[switch] "+line)
		}); err != nil {
			diags.AddError("NixOS switch failed", err.Error())
			return
		}
	}
	if diags.HasError() {
		return
	}

	// --- Step 7: Cleanup old generations ---
	tflog.Info(ctx, "Cleaning up old system generations")
	if _, _, err := target.Run("nix-env -p /nix/var/nix/profiles/system --delete-generations +1"); err != nil {
		tflog.Warn(ctx, "Failed to delete old generations", map[string]interface{}{"error": err.Error()})
	}

	if plan.GarbageCollect.ValueBool() {
		tflog.Info(ctx, "Running nix garbage collection")
		target.RunStreaming("nix-store --gc", func(line string) {
			tflog.Debug(ctx, "[gc] "+line)
		})
	}

	// --- Step 8: Read system hash ---
	hashOutput, _, err := target.Run("nix-store --query --hash /run/current-system")
	if err != nil {
		diags.AddError("Failed to read system hash after deployment", err.Error())
		return
	}
	plan.SystemHash = types.StringValue(strings.TrimSpace(hashOutput))

	tflog.Info(ctx, "Deployment complete", map[string]interface{}{
		"host":        host,
		"system_hash": plan.SystemHash.ValueString(),
	})
}

// switchViaBuildHost handles the case where the build happened on a separate
// host. It copies the closure to the target and activates the configuration.
func (r *ConfigurationResource) switchViaBuildHost(
	ctx context.Context,
	plan *ConfigurationModel,
	target *sshclient.Client,
	buildClient *sshclient.Client,
	env, remoteDir, configName string,
	diags *diag.Diagnostics,
) {
	host := plan.SSHHost.ValueString()
	user := plan.SSHUser.ValueString()
	key := plan.SSHPrivateKey.ValueString()

	// Deploy target SSH key to build host temporarily
	tmpKeyPath := "/tmp/.terraform-nixos-target-key"
	tflog.Info(ctx, "Deploying temporary SSH key to build host for closure transfer")
	if err := buildClient.WriteFile(tmpKeyPath, []byte(key), 0600); err != nil {
		diags.AddError("Failed to deploy temporary key to build host", err.Error())
		return
	}
	defer func() {
		buildClient.Run(fmt.Sprintf("rm -f %s", tmpKeyPath))
	}()

	// Get the build result path
	resultPath, _, err := buildClient.Run(fmt.Sprintf("readlink -f %s/result", remoteDir))
	if err != nil {
		diags.AddError("Failed to read build result path", err.Error())
		return
	}
	resultPath = strings.TrimSpace(resultPath)
	tflog.Info(ctx, "Build result", map[string]interface{}{"path": resultPath})

	// Copy closure from build host to target
	copyCmd := fmt.Sprintf(
		"NIX_SSHOPTS='-i %s -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null' nix-copy-closure --to %s@%s %s",
		tmpKeyPath, user, host, resultPath,
	)
	tflog.Info(ctx, "Copying closure from build host to target")
	if err := buildClient.RunStreaming(copyCmd, func(line string) {
		tflog.Info(ctx, "[copy-closure] "+line)
	}); err != nil {
		diags.AddError("Failed to copy closure to target", err.Error())
		return
	}

	// Activate on target
	tflog.Info(ctx, "Activating configuration on target")
	if _, _, err := target.Run(fmt.Sprintf("nix-env -p /nix/var/nix/profiles/system --set %s", resultPath)); err != nil {
		diags.AddError("Failed to set system profile", err.Error())
		return
	}

	switchCmd := fmt.Sprintf("%s/bin/switch-to-configuration switch", resultPath)
	if err := target.RunStreaming(switchCmd, func(line string) {
		tflog.Info(ctx, "[switch] "+line)
	}); err != nil {
		diags.AddError("Failed to switch configuration on target", err.Error())
		return
	}
}