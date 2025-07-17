package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"

	"github.com/Facets-cloud/fctl/pkg/config"
	"github.com/Facets-cloud/fctl/pkg/utils"
	"github.com/hashicorp/terraform-exec/tfexec"
	"github.com/spf13/cobra"
)

var destroyCmd = &cobra.Command{
	Use:   "destroy",
	Short: "Destroy resources for a Terraform export in your Facets environment.",
	Long:  `Destroy all resources managed by a Terraform export in your Facets environment. This command mimics 'terraform destroy', supporting state file management and selective module targeting.`,
	RunE:  runDestroy,
}

func init() {
	rootCmd.AddCommand(destroyCmd)

	// Add flags - reusing the same flags as plan/apply
	destroyCmd.Flags().StringVarP(&zipPath, "zip", "z", "", "Path to the exported zip file (required)")
	destroyCmd.Flags().StringVarP(&targetAddr, "target", "t", "", "Module target address for selective releases")
	destroyCmd.Flags().StringVarP(&statePath, "state", "s", "", "Path to the state file")
	destroyCmd.Flags().BoolVar(&uploadReleaseMetadata, "upload-release-metadata", false, "Upload release metadata to control plane after apply")

	destroyCmd.MarkFlagRequired("zip")
}

func runDestroy(cmd *cobra.Command, args []string) error {
	allowDestroy, _ := cmd.Flags().GetBool("allow-destroy")
	fmt.Println("🔥 Starting terraform destroy process...")

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

	// Run terraform destroy
	destroyOptions := []tfexec.DestroyOption{}
	if targetAddr != "" {
		fmt.Printf("🎯 Targeting module: %s\n", targetAddr)
		destroyOptions = append(destroyOptions, tfexec.Target(targetAddr))
	}

	fmt.Println("💥 Running terraform destroy...")
	if err := tf.Destroy(context.Background(), destroyOptions...); err != nil {
		return fmt.Errorf("❌ Terraform destroy failed: %v", err)
	}

	// Generate release metadata
	fmt.Println("📊 Generating release metadata...")
	if err := utils.GenerateReleaseMetadata(tf, deployDir); err != nil {
		fmt.Printf("⚠️ Warning: Failed to generate release metadata: %v\n", err)
	}

	// Upload release metadata if flag is set
	if uploadReleaseMetadata {
		fmt.Println("☁️ Uploading release metadata to control plane...")
		metadataFile := filepath.Join(deployDir, "release-metadata.json")
		f, err := os.Open(metadataFile)
		if err != nil {
			fmt.Printf("❌ Failed to open release metadata file: %v\n", err)
		} else {
			defer f.Close()
			var requestBody bytes.Buffer
			writer := multipart.NewWriter(&requestBody)
			part, err := writer.CreateFormFile("file", filepath.Base(f.Name()))
			if err != nil {
				fmt.Printf("❌ Failed to create multipart form file: %v\n", err)
				return nil
			}
			_, err = io.Copy(part, f)
			if err != nil {
				fmt.Printf("❌ Failed to copy file to multipart writer: %v\n", err)
				return nil
			}
			writer.Close()

			// Build the upload URL (replace with actual endpoint if needed)
			clientConfig := config.GetClientConfig("") // use the correct profile if needed
			if clientConfig == nil {
				fmt.Printf("❌ Could not get client configuration\n")
				return nil
			}
			uploadURL := clientConfig.ControlPlaneURL + "/cc-ui/v1/clusters/" + envID + "/deployments/" + deploymentID + "/upload-release-metadata"

			req, err := http.NewRequest("POST", uploadURL, &requestBody)
			if err != nil {
				fmt.Printf("❌ Failed to create upload request: %v\n", err)
				return nil
			}
			req.Header.Set("Content-Type", writer.FormDataContentType())
			req.SetBasicAuth(clientConfig.Username, clientConfig.Token)

			httpClient := &http.Client{}
			resp, err := httpClient.Do(req)
			if err != nil {
				fmt.Printf("❌ Failed to upload release metadata: %v\n", err)
				return nil
			}
			defer resp.Body.Close()

			if resp.StatusCode == 503 {
				fmt.Printf("❌ Control plane is down. Please try again later. (HTTP 503)\n")
				return nil
			}
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				fmt.Printf("❌ Upload failed with status: %s\n%s\n", resp.Status, string(body))
			} else {
				fmt.Println("✅ Release metadata uploaded to control plane.")
			}
		}
	}

	fmt.Printf("✅ Successfully destroyed terraform-managed resources!\n")
	fmt.Printf("📍 Deployment directory: %s\n", deployDir)
	if backendConfig == nil {
		fmt.Printf("💾 State file location: %s/terraform.tfstate.d/%s/terraform.tfstate\n", tfWorkDir, envID)
		// Save latest state for this environment
		latestStatePath := filepath.Join(envDir, "tf.tfstate")
		currentStatePath := filepath.Join(tfWorkDir, "terraform.tfstate.d", envID, "terraform.tfstate")
		if _, err := os.Stat(currentStatePath); err == nil {
			if err := utils.CopyFile(currentStatePath, latestStatePath); err != nil {
				fmt.Printf("⚠️ Warning: Failed to save latest state: %v\n", err)
			} else {
				fmt.Printf("📝 Latest state saved to: %s\n", latestStatePath)
			}
		}
	}

	return nil
}
