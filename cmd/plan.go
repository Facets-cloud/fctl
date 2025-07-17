package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Facets-cloud/fctl/pkg/config"
	"github.com/Facets-cloud/fctl/pkg/utils"
	"github.com/hashicorp/terraform-exec/tfexec"
	"github.com/spf13/cobra"
)

var planCmd = &cobra.Command{
	Use:   "plan",
	Short: "Preview changes for a Terraform export in your Facets environment.",
	Long:  `Generate and review an execution plan for a Terraform export in your Facets environment. This command mimics 'terraform plan', allowing you to see what changes will be made before applying them. Supports state file management and selective module targeting.`,
	RunE:  runPlan,
}

func init() {
	rootCmd.AddCommand(planCmd)

	// Add flags - reusing the same flags as apply command
	planCmd.Flags().StringVarP(&zipPath, "zip", "z", "", "Path to the exported zip file (required)")
	planCmd.Flags().StringVarP(&targetAddr, "target", "t", "", "Module target address for selective releases")
	planCmd.Flags().StringVarP(&statePath, "state", "s", "", "Path to the state file")

	planCmd.MarkFlagRequired("zip")
}

func runPlan(cmd *cobra.Command, args []string) error {
	allowDestroy, _ := cmd.Flags().GetBool("allow-destroy")
	fmt.Println("🔍 Starting terraform plan process...")

	// Initialize backend configuration
	backendConfig, err := config.NewBackendConfig(backendType)
	if err != nil {
		return fmt.Errorf("❌ Failed to initialize backend configuration: %v", err)
	}

	// Validate backend configuration if a backend type is specified
	if backendConfig != nil {
		if err := backendConfig.Validate(); err != nil {
			return fmt.Errorf("❌ Invalid backend configuration: %v", err)
		}
		fmt.Printf("🔐 Using %s backend for state management\n", backendConfig.Type)
	}

	// Extract deployment ID from zip filename
	deploymentID, err := utils.ExtractDeploymentID(zipPath)
	if err != nil {
		return fmt.Errorf("❌ Failed to extract deployment ID: %v", err)
	}

	// Unzip to a temp dir to read deploymentcontext.json
	tempDir, err := os.MkdirTemp("", "fctl-unzip-*")
	if err != nil {
		return fmt.Errorf("❌ Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)
	if err := utils.ExtractZip(zipPath, tempDir); err != nil {
		return fmt.Errorf("❌ Failed to extract zip: %v", err)
	}
	envID, err := utils.ExtractEnvIDFromDeploymentContext(tempDir)
	if err != nil {
		return fmt.Errorf("❌ Failed to extract environment ID from deploymentcontext.json: %v", err)
	}
	fmt.Printf("🌍 Environment ID: %s\n", envID)
	fmt.Printf("🆔 Deployment ID: %s\n", deploymentID)

	// Create base directory structure
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("❌ Failed to get home directory: %v", err)
	}
	baseDir := filepath.Join(homeDir, ".facets")
	envDir := filepath.Join(baseDir, envID)

	// Cleanup old releases (directories and zips)
	cleanupOldReleases(envDir, baseDir, envID)

	deployDir := filepath.Join(envDir, deploymentID)
	tfWorkDir := filepath.Join(deployDir, "tfexport")

	// Create directories
	fmt.Printf("📁 Creating deployment directory for environment %s and deployment %s...\n", envID, deploymentID)
	if err := os.MkdirAll(deployDir, 0755); err != nil {
		return fmt.Errorf("❌ Failed to create directories: %v", err)
	}

	// Check for existing deployments only if:
	// 1. This deploymentID directory doesn't exist
	// 2. No backend is configured (we need local state management)
	if _, err := os.Stat(tfWorkDir); os.IsNotExist(err) {
		if backendConfig == nil {
			tfStatePath := filepath.Join(envDir, "tf.tfstate")
			existingDeployments, err := utils.ListExistingDeployments(envDir, deploymentID)
			if err != nil {
				return fmt.Errorf("❌ Failed to list existing deployments: %v", err)
			}
			if len(existingDeployments) > 0 {
				proceed, selectedDeployment, err := utils.PromptUser(existingDeployments, tfStatePath)
				if err != nil {
					return fmt.Errorf("❌ User input error: %v", err)
				}
				if proceed {
					if selectedDeployment == "__USE_TF_TFSTATE__" {
						fmt.Println("📝 Using tf.tfstate for this deployment...")
						stateDir := filepath.Join(tfWorkDir, "terraform.tfstate.d", envID)
						if err := os.MkdirAll(stateDir, 0755); err != nil {
							return fmt.Errorf("❌ Failed to create state directory: %v", err)
						}
						destPath := filepath.Join(stateDir, "terraform.tfstate")
						if err := utils.CopyFile(tfStatePath, destPath); err != nil {
							return fmt.Errorf("❌ Failed to copy tf.tfstate: %v", err)
						}
					} else {
						fmt.Println("🔄 User chose to proceed with state file from existing deployment")
						if err := utils.CopyStateFromPreviousDeployment(envDir, deploymentID, envID, selectedDeployment); err != nil {
							return fmt.Errorf("❌ Failed to copy state file: %v", err)
						}
					}
				}
			}
		} else {
			fmt.Printf("ℹ️  Using %s backend for state management\n", backendConfig.Type)
		}
		// Now extract zip contents to deployDir
		fmt.Println("📦 Extracting terraform configuration...")
		if err := utils.ExtractZip(zipPath, deployDir); err != nil {
			return fmt.Errorf("❌ Failed to extract zip: %v", err)
		}
		// Fix permissions after extraction
		if err := utils.FixPermissions(tfWorkDir); err != nil {
			return fmt.Errorf("❌ Failed to fix permissions: %v", err)
		}
		if allowDestroy {
			fmt.Println("🔒 Enforcing prevent_destroy = true in all Terraform resources...")
			if err := utils.UpdatePreventDestroyInTFs(tfWorkDir); err != nil {
				return fmt.Errorf("❌ Failed to update prevent_destroy in .tf files: %v", err)
			}
		}
	} else {
		fmt.Println("♻️ Using existing deployment directory")
		// Check if zip contents differ from deployDir
		different, err := utils.IsZipDifferentFromDir(zipPath, deployDir)
		if err != nil {
			return fmt.Errorf("❌ Failed to compare zip and directory: %v", err)
		}
		if different {
			fmt.Println("📦 Changes detected in zip, extracting to deployment directory...")
			if err := utils.ExtractZip(zipPath, deployDir); err != nil {
				return fmt.Errorf("❌ Failed to extract zip: %v", err)
			}
			// Fix permissions after extraction
			if err := utils.FixPermissions(tfWorkDir); err != nil {
				return fmt.Errorf("❌ Failed to fix permissions: %v", err)
			}
			if allowDestroy {
				fmt.Println("🔒 Enforcing prevent_destroy = true in all Terraform resources...")
				if err := utils.UpdatePreventDestroyInTFs(tfWorkDir); err != nil {
					return fmt.Errorf("❌ Failed to update prevent_destroy in .tf files: %v", err)
				}
			}
		} else {
			fmt.Println("✅ No changes detected in zip, skipping extraction.")
		}
	}

	// Initialize terraform
	fmt.Println("🔧 Initializing terraform...")
	tf, err := tfexec.NewTerraform(tfWorkDir, "terraform")
	if err != nil {
		return fmt.Errorf("❌ Failed to create terraform executor: %v", err)
	}

	// set logging for terraform
	tf.SetLog("INFO")
	tf.SetStderr(os.Stdout)
	tf.SetStdout(os.Stdout)

	// Handle state file
	if statePath != "" && backendConfig == nil {
		fmt.Println("📝 Copying provided state file...")
		stateDir := filepath.Join(tfWorkDir, "terraform.tfstate.d", envID)
		if err := os.MkdirAll(stateDir, 0755); err != nil {
			return fmt.Errorf("❌ Failed to create state directory: %v", err)
		}

		destPath := filepath.Join(stateDir, "terraform.tfstate")
		if err := utils.CopyFile(statePath, destPath); err != nil {
			return fmt.Errorf("❌ Failed to copy state file: %v", err)
		}
	} else if backendConfig == nil && statePath == "" {
		// No state file provided, check for latest.tfstate
		latestStatePath := filepath.Join(envDir, "latest.tfstate")
		if _, err := os.Stat(latestStatePath); err == nil {
			fmt.Println("📝 Using latest state for this environment...")
			stateDir := filepath.Join(tfWorkDir, "terraform.tfstate.d", envID)
			if err := os.MkdirAll(stateDir, 0755); err != nil {
				return fmt.Errorf("❌ Failed to create state directory: %v", err)
			}
			destPath := filepath.Join(stateDir, "terraform.tfstate")
			if err := utils.CopyFile(latestStatePath, destPath); err != nil {
				return fmt.Errorf("❌ Failed to copy latest state file: %v", err)
			}
		} else {
			fmt.Println("ℹ️ No previous state found. Proceeding as a fresh deployment.")
		}
	}

	// Initialize terraform with backend configuration if provided
	if backendConfig != nil {
		fmt.Printf("🔄 Writing backend.tf.json for %s backend...\n", backendConfig.Type)
		if err := backendConfig.WriteBackendTFJSON(tfWorkDir); err != nil {
			return fmt.Errorf("❌ Failed to write backend.tf.json: %v", err)
		}
	}
	if err := tf.Init(context.Background()); err != nil {
		return fmt.Errorf("❌ Terraform init failed: %v", err)
	}

	// Select workspace/environment
	if err := tf.WorkspaceSelect(context.Background(), envID); err != nil {
		// If workspace doesn't exist, create it
		if err := tf.WorkspaceNew(context.Background(), envID); err != nil {
			return fmt.Errorf("❌ Failed to create workspace: %v", err)
		}
	}

	// Run terraform plan
	planOptions := []tfexec.PlanOption{}
	if targetAddr != "" {
		fmt.Printf("🎯 Targeting module: %s\n", targetAddr)
		planOptions = append(planOptions, tfexec.Target(targetAddr))
	}

	fmt.Println("📋 Running terraform plan...")
	planResult, err := tf.Plan(context.Background(), planOptions...)
	if err != nil {
		return fmt.Errorf("❌ Terraform plan failed: %v", err)
	}

	if planResult {
		fmt.Println("🔄 Changes detected in plan")
	} else {
		fmt.Println("✅ No changes. Infrastructure is up-to-date.")
	}

	fmt.Printf("📍 Deployment directory: %s\n", deployDir)
	if backendConfig == nil {
		fmt.Printf("💾 State file location: %s/terraform.tfstate.d/%s/terraform.tfstate\n", tfWorkDir, envID)
	}

	return nil
}
