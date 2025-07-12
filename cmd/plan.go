package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Facets-cloud/fctl/pkg/config"
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
	planCmd.Flags().StringVar(&backendType, "backend-type", "", "Type of backend (e.g., s3, gcs)")

	planCmd.MarkFlagRequired("zip")
}

func runPlan(cmd *cobra.Command, args []string) error {
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

	// Extract environment ID and deployment ID from zip filename
	envID, deploymentID, err := extractEnvIDAndDeploymentID(zipPath)
	if err != nil {
		return fmt.Errorf("❌ Failed to extract environment or deployment ID: %v", err)
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
			existingDeployments, err := listExistingDeployments(envDir, deploymentID)
			if err != nil {
				return fmt.Errorf("❌ Failed to list existing deployments: %v", err)
			}

			if len(existingDeployments) > 0 {
				proceed, err := promptUser(existingDeployments)
				if err != nil {
					return fmt.Errorf("❌ User input error: %v", err)
				}
				if proceed {
					fmt.Println("🔄 User chose to proceed with state file from existing deployment")
					if err := copyStateFromPreviousDeployment(envDir, deploymentID, envID); err != nil {
						return fmt.Errorf("❌ Failed to copy state file: %v", err)
					}
				}
			}
		} else {
			fmt.Printf("ℹ️  Using %s backend for state management\n", backendConfig.Type)
		}

		// Extract zip contents
		fmt.Println("📦 Extracting terraform configuration...")
		if err := extractZip(zipPath, deployDir); err != nil {
			return fmt.Errorf("❌ Failed to extract zip: %v", err)
		}
	} else {
		fmt.Println("♻️ Using existing deployment directory")
	}
	
	// Initialize terraform
	fmt.Println("�� Initializing terraform...")
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
		if err := copyFile(statePath, destPath); err != nil {
			return fmt.Errorf("❌ Failed to copy state file: %v", err)
		}
	}

	// Initialize terraform with backend configuration if provided
	initOptions := []tfexec.InitOption{}

	if backendConfig != nil {
		fmt.Printf("🔄 Configuring %s backend...\n", backendConfig.Type)
		initOptions = append(initOptions, tfexec.Backend(true))
		for _, pair := range backendConfig.GetTerraformConfigPairs() {
			initOptions = append(initOptions, tfexec.BackendConfig(pair))
		}
	}

	if err := tf.Init(context.Background(), initOptions...); err != nil {
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
