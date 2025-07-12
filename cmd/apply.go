package cmd

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Facets-cloud/facets-sdk-go/facets/client/ui_deployment_controller"
	"github.com/Facets-cloud/fctl/pkg/config"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/spf13/cobra"
)

var (
	zipPath               string
	targetAddr            string
	statePath             string
	backendType           string
	selectedDeployment    string
	uploadReleaseMetadata bool
)

var applyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Apply a Terraform export to your Facets environment.",
	Long:  `Apply a Terraform configuration exported from Facets to your target environment. This command mimics 'terraform apply', supports state file management, selective module targeting, and can upload release metadata to the control plane for audit and tracking.`,
	RunE:  runApply,
}

func init() {
	rootCmd.AddCommand(applyCmd)

	// Add flags
	applyCmd.Flags().StringVarP(&zipPath, "zip", "z", "", "Path to the exported zip file (required)")
	applyCmd.Flags().StringVarP(&targetAddr, "target", "t", "", "Module target address for selective releases")
	applyCmd.Flags().StringVarP(&statePath, "state", "s", "", "Path to the state file")
	applyCmd.Flags().StringVar(&backendType, "backend-type", "b", "Type of backend (e.g., s3, gcs)")
	applyCmd.Flags().BoolVar(&uploadReleaseMetadata, "upload-release-metadata", false, "Upload release metadata to control plane after apply")

	applyCmd.MarkFlagRequired("zip")
}

func runApply(cmd *cobra.Command, args []string) error {
	fmt.Println("🚀 Starting terraform apply process...")

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

	// Run terraform apply
	applyOptions := []tfexec.ApplyOption{}
	if targetAddr != "" {
		fmt.Printf("🎯 Targeting module: %s\n", targetAddr)
		applyOptions = append(applyOptions, tfexec.Target(targetAddr))
	}

	fmt.Println("🔨 Running terraform apply...")
	if err := tf.Apply(context.Background(), applyOptions...); err != nil {
		return fmt.Errorf("❌ Terraform apply failed: %v", err)
	}

	// Generate release metadata
	fmt.Println("📊 Generating release metadata...")
	if err := generateReleaseMetadata(tf, deployDir); err != nil {
		fmt.Printf("⚠️ Warning: Failed to generate release metadata: %v\n", err)
	}

	// Upload release metadata if flag is set
	if uploadReleaseMetadata {
		fmt.Println("☁️ Uploading release metadata to control plane...")
		profile, _ := cmd.Flags().GetString("profile")
		client, auth, err := config.GetClient(profile, false)
		if err != nil {
			fmt.Printf("❌ Failed to get API client: %v\n", err)
		} else {
			metadataFile := filepath.Join(deployDir, "release-metadata.json")
			metadataBytes, err := os.ReadFile(metadataFile)
			if err != nil {
				fmt.Printf("❌ Failed to read release metadata file: %v\n", err)
			} else {
				var metadata interface{}
				if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
					fmt.Printf("❌ Failed to unmarshal release metadata JSON: %v\n", err)
				} else {
					params := ui_deployment_controller.NewUploadReleaseMetadataParams()
					params.ClusterID = envID
					params.DeploymentID = deploymentID
					if body, ok := metadata.(ui_deployment_controller.UploadReleaseMetadataBody); ok {
						params.Body = body
						_, err := client.UIDeploymentController.UploadReleaseMetadata(params, auth)
						if err != nil {
							fmt.Printf("❌ Failed to upload release metadata: %v\n", err)
						} else {
							fmt.Println("✅ Release metadata uploaded to control plane.")
						}
					} else {
						fmt.Printf("❌ Release metadata is not of the expected type for upload.\n")
					}
				}
			}
		}
	}

	fmt.Printf("✅ Successfully applied terraform configuration!\n")
	fmt.Printf("📍 Deployment directory: %s\n", deployDir)
	if backendConfig == nil {
		fmt.Printf("💾 State file location: %s/terraform.tfstate.d/%s/terraform.tfstate\n", tfWorkDir, envID)
	}

	return nil
}

func extractEnvIDAndDeploymentID(zipPath string) (string, string, error) {
	pattern := regexp.MustCompile(`terraform-export-([^-]+)-([^-]+)-\d{8}-\d{6}\.zip`)
	matches := pattern.FindStringSubmatch(filepath.Base(zipPath))
	if len(matches) < 3 {
		return "", "", fmt.Errorf("invalid zip filename format")
	}
	return matches[1], matches[2], nil
}

func extractZip(zipPath, destPath string) error {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer reader.Close()

	for _, file := range reader.File {
		path := filepath.Join(destPath, file.Name)

		if file.FileInfo().IsDir() {
			os.MkdirAll(path, file.Mode())
			continue
		}

		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}

		dstFile, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, file.Mode())
		if err != nil {
			return err
		}

		srcFile, err := file.Open()
		if err != nil {
			dstFile.Close()
			return err
		}

		_, err = io.Copy(dstFile, srcFile)
		dstFile.Close()
		srcFile.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func listExistingDeployments(envDir, currentDeploymentID string) ([]string, error) {
	entries, err := os.ReadDir(envDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var deployments []string
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != currentDeploymentID {
			deployments = append(deployments, entry.Name())
		}
	}
	return deployments, nil
}

func promptUser(existingDeployments []string) (bool, error) {
	fmt.Println("\n⚠️  Found existing deployments in this environment:")
	for i, deploymentID := range existingDeployments {
		fmt.Printf("%d. %s\n", i+1, deploymentID)
	}
	fmt.Print("\n❓ Do you want to proceed with state file? (y/n): ")

	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false, err
	}

	response = strings.ToLower(strings.TrimSpace(response))
	if response != "y" && response != "yes" {
		return false, nil
	}

	// If user wants to proceed, ask which deployment to use
	fmt.Print("\n📂 Enter the number of the deployment to use (1-" + fmt.Sprint(len(existingDeployments)) + "): ")
	numStr, err := reader.ReadString('\n')
	if err != nil {
		return false, err
	}

	numStr = strings.TrimSpace(numStr)
	num := 0
	_, err = fmt.Sscanf(numStr, "%d", &num)
	if err != nil || num < 1 || num > len(existingDeployments) {
		return false, fmt.Errorf("invalid selection: please enter a number between 1 and %d", len(existingDeployments))
	}

	// Store the selected deployment in a global variable
	selectedDeployment = existingDeployments[num-1]
	return true, nil
}

func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}

func copyStateFromPreviousDeployment(envDir, currentDeploymentID, envID string) error {
	if selectedDeployment == "" {
		return fmt.Errorf("no deployment selected")
	}

	prevDeployDir := filepath.Join(envDir, selectedDeployment)
	prevStateDir := filepath.Join(prevDeployDir, "tfexport", "terraform.tfstate.d", envID)
	prevStatePath := filepath.Join(prevStateDir, "terraform.tfstate")

	// Check if state file exists in the selected deployment
	if _, err := os.Stat(prevStatePath); err != nil {
		return fmt.Errorf("no state file found in deployment %s", selectedDeployment)
	}

	fmt.Printf("📝 Found state file in deployment %s\n", selectedDeployment)

	// Create state directory in current deployment
	newStateDir := filepath.Join(envDir, currentDeploymentID, "tfexport", "terraform.tfstate.d", envID)
	if err := os.MkdirAll(newStateDir, 0755); err != nil {
		return fmt.Errorf("failed to create state directory: %v", err)
	}

	// Copy state file
	newStatePath := filepath.Join(newStateDir, "terraform.tfstate")
	if err := copyFile(prevStatePath, newStatePath); err != nil {
		return fmt.Errorf("failed to copy state file: %v", err)
	}

	fmt.Printf("✅ Successfully copied state file from deployment %s\n", selectedDeployment)
	return nil
}

func parseStateFile(state *tfjson.State) []map[string]interface{} {
	var releaseMetadataList []map[string]interface{}

	if state == nil || state.Values == nil {
		return releaseMetadataList
	}

	for _, resource := range state.Values.RootModule.Resources {
		if resource.Type == "scratch_string" && resource.Name == "release_metadata" {
			if attrs, ok := resource.AttributeValues["in"].(string); ok {
				var inData map[string]interface{}
				if err := json.Unmarshal([]byte(attrs), &inData); err != nil {
					fmt.Printf("⚠️ Warning: Failed to parse release metadata JSON: %v\n", err)
					continue
				}

				if releaseMetadata, ok := inData["release_metadata"].(map[string]interface{}); ok {
					if generateMetadata, ok := inData["generate_release_metadata"].(bool); ok && generateMetadata {
						releaseMetadataList = append(releaseMetadataList, releaseMetadata)
					}
				}
			}
		}
	}

	return releaseMetadataList
}

func generateReleaseMetadata(tf *tfexec.Terraform, deployDir string) error {
	// Run terraform show -json
	state, err := tf.Show(context.Background())
	if err != nil {
		return fmt.Errorf("terraform show failed: %w", err)
	}

	// Parse the state file and get release metadata
	releaseMetadataList := parseStateFile(state)
	if len(releaseMetadataList) == 0 {
		fmt.Println("ℹ️ No release metadata found in state")
		return nil
	}

	// Create metadata file
	metadataFile := filepath.Join(deployDir, "release-metadata.json")
	metadataJSON, err := json.MarshalIndent(releaseMetadataList, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal release metadata: %w", err)
	}

	if err := os.WriteFile(metadataFile, metadataJSON, 0644); err != nil {
		return fmt.Errorf("failed to write release metadata file: %w", err)
	}

	fmt.Printf("📝 Release metadata saved to: %s\n", metadataFile)
	return nil
}
