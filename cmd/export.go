package cmd

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/Facets-cloud/facets-sdk-go/facets/client"
	"github.com/Facets-cloud/facets-sdk-go/facets/client/ui_deployment_controller"
	"github.com/Facets-cloud/facets-sdk-go/facets/client/ui_stack_controller"
	"github.com/Facets-cloud/fctl/pkg/config"
	"github.com/Facets-cloud/fctl/pkg/utils"
	"github.com/go-openapi/runtime"
	"github.com/hashicorp/terraform-exec/tfexec"
	"github.com/spf13/cobra"
	"github.com/yarlson/pin"
)

// progressWriter tracks download progress
type progressWriter struct {
	total      int64
	downloaded int64
	startTime  time.Time
	avgTime    time.Duration
	lastUpdate time.Time
	spinner    interface {
		UpdateMessage(string)
	}
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n := len(p)
	pw.downloaded += int64(n)

	// Only update every 100ms to prevent too frequent updates
	if time.Since(pw.lastUpdate) < 100*time.Millisecond {
		return n, nil
	}
	pw.lastUpdate = time.Now()

	// Calculate current speed in MB/s
	elapsed := time.Since(pw.startTime)
	speed := float64(pw.downloaded) / elapsed.Seconds() / 1024 / 1024 // MB/s

	if pw.total > 0 {
		percentage := float64(pw.downloaded) / float64(pw.total) * 100

		// Calculate estimated time remaining
		var estimatedMsg string
		if percentage > 0 {
			if pw.avgTime > 0 {
				// Use historical average if available
				estimatedMsg = fmt.Sprintf(" (⏱️ Est. %s based on history)", utils.FormatDuration(pw.avgTime))
			} else {
				// Calculate based on current progress and speed
				remaining := float64(pw.total-pw.downloaded) / (speed * 1024 * 1024) // seconds
				remainingDuration := time.Duration(remaining) * time.Second
				estimatedMsg = fmt.Sprintf(" (⏱️ Est. %s remaining at %.1f MB/s)", utils.FormatDuration(remainingDuration), speed)
			}
		}

		pw.spinner.UpdateMessage(fmt.Sprintf("📥 Downloading: %.1f%% (%.2f MB / %.2f MB)%s",
			percentage,
			float64(pw.downloaded)/1024/1024,
			float64(pw.total)/1024/1024,
			estimatedMsg))
	} else {
		// If total size is unknown, show current speed
		pw.spinner.UpdateMessage(fmt.Sprintf("📥 Downloading: %.2f MB (%.1f MB/s)",
			float64(pw.downloaded)/1024/1024,
			speed))
	}
	return n, nil
}

// getHistoricalDeploymentTime fetches the last 10 successful terraform exports and calculates average time
func getHistoricalDeploymentTime(client *client.Facets, auth runtime.ClientAuthInfoWriter, environment string) time.Duration {
	params := ui_deployment_controller.NewGetDeploymentsParams()
	params.ClusterID = environment

	response, err := client.UIDeploymentController.GetDeployments(params, auth)
	if err != nil {
		return 0
	}

	var deploymentTimes []time.Duration
	for _, deployment := range response.Payload.Deployments {
		// Only consider successful terraform exports
		if deployment.Status == "SUCCEEDED" && deployment.ReleaseType == "TERRAFORM_EXPORT" {
			timeTaken := time.Duration(deployment.TimeTakenInSeconds) * time.Second
			deploymentTimes = append(deploymentTimes, timeTaken)
		}
	}

	// Sort deployments by time and get the last 10
	sort.Slice(deploymentTimes, func(i, j int) bool {
		return deploymentTimes[i] < deploymentTimes[j]
	})
	if len(deploymentTimes) > 10 {
		deploymentTimes = deploymentTimes[len(deploymentTimes)-10:]
	}

	// Calculate average time
	if len(deploymentTimes) == 0 {
		return 0
	}
	var total time.Duration
	for _, t := range deploymentTimes {
		total += t
	}
	return total / time.Duration(len(deploymentTimes))
}

// Recursively set user rwx permissions on all files and directories
func ensureWritable(path string) error {
	return filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chmod(p, 0700)
	})
}

var exportCopyPairs []string // --copy source:destination
var exportUploadReleaseMetadata bool
var allowDestroy bool

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export a Facets environment as a Terraform configuration.",
	Long:  `Export your Facets project environment as a Terraform configuration zip file. This enables you to manage infrastructure as code, perform offline planning, and apply changes in a controlled manner. Supports adding files to the zip via --copy source:destination pairs.`,
	Run: func(cmd *cobra.Command, args []string) {
		environment, _ := cmd.Flags().GetString("environment-id")
		project, _ := cmd.Flags().GetString("project")
		envName, _ := cmd.Flags().GetString("env-name")
		includeProviders, _ := cmd.Flags().GetBool("include-providers")

		s := pin.New("🚀 Initializing export...",
			pin.WithSpinnerColor(pin.ColorCyan),
			pin.WithTextColor(pin.ColorYellow),
			pin.WithDoneSymbol('✔'),
			pin.WithDoneSymbolColor(pin.ColorGreen),
			pin.WithPrefix("pin"),
			pin.WithPrefixColor(pin.ColorMagenta),
			pin.WithSeparatorColor(pin.ColorGray),
		)

		cancel := s.Start(context.Background())
		defer cancel()

		profile, _ := cmd.Flags().GetString("profile")
		client, auth, err := config.GetClient(profile, false)
		if err != nil {
			s.Fail("❌ Error fetching client")
			fmt.Printf("🔴 Could not get client: %v\n", err)
			return
		}

		// If environment is not provided, but project and env-name are, resolve environment ID
		if environment == "" && project != "" && envName != "" {
			s.UpdateMessage("🔍 Resolving environment ID from project and environment name...")
			// 1. Get all stacks (projects)
			stackParams := ui_stack_controller.NewGetStacksParams()
			stacksResp, err := client.UIStackController.GetStacks(stackParams, auth)
			if err != nil {
				s.Fail("❌ Error fetching projects (stacks)")
				if stacksResp.Code() == 503 {
					fmt.Printf("🔴 Control plane is unreachable or down (HTTP 503)\n")
				} else {
					fmt.Printf("🔴 Could not get stacks: %v\n", err)
				}
				return
			}
			var foundStackName string
			for _, stack := range stacksResp.Payload {
				if stack.Name == project {
					foundStackName = stack.Name
					break
				}
			}
			if foundStackName == "" {
				s.Fail("❌ Project (stack) not found: " + project)
				return
			}
			// 2. Get all clusters (environments) for the stack
			clusterParams := ui_stack_controller.NewGetClustersParams()
			clusterParams.StackName = foundStackName
			clustersResp, err := client.UIStackController.GetClusters(clusterParams, auth)
			if err != nil {
				s.Fail("❌ Error fetching environments (clusters) for project: " + foundStackName)
				if clustersResp.Code() == 503 {
					fmt.Printf("🔴 Control plane is unreachable or down (HTTP 503)\n")
				} else {
					fmt.Printf("🔴 Could not get clusters: %v\n", err)
				}
				return
			}
			var foundEnvID string
			for _, cluster := range clustersResp.Payload {
				if cluster.Name != nil && *cluster.Name == envName {
					foundEnvID = cluster.ID
					break
				}
			}
			if foundEnvID == "" {
				s.Fail("❌ Environment not found: " + envName)
				return
			}
			environment = foundEnvID
			s.UpdateMessage("✅ Resolved environment ID: " + environment)
		}

		if environment == "" {
			s.Fail("❌ Environment ID is required (either --environment-id or --project and --env-name)")
			return
		}

		// Get average deployment time from history
		avgTime := getHistoricalDeploymentTime(client, auth, environment)
		var timeEstimateMsg string
		if avgTime > 0 {
			timeEstimateMsg = fmt.Sprintf(" (⏱️ Est. %s based on last 10 exports)", utils.FormatDuration(avgTime))
		}

		// 1. Check for running TERRAFORM_EXPORT deployments
		getDeploymentsParams := ui_deployment_controller.NewGetDeploymentsParams()
		getDeploymentsParams.ClusterID = environment
		deploymentsResp, err := client.UIDeploymentController.GetDeployments(getDeploymentsParams, auth)
		if err != nil {
			// Check for control plane down (HTTP 503)
			if apiErr, ok := err.(*runtime.APIError); ok && apiErr.Code == 503 {
				s.Fail("❌ Control plane is down. Please try again later.")
				fmt.Println("🔴 The Facets control plane is currently unavailable (HTTP 503). Please try again later.")
				return
			}
			s.Fail("❌ Error fetching deployments")
			fmt.Printf("🔴 Could not get deployments: %v\n", err)
			return
		}

		var runningExportID string
		var runningExportStatus string
		for _, d := range deploymentsResp.Payload.Deployments {
			if d.ReleaseType == "TERRAFORM_EXPORT" && (d.Status == "IN_PROGRESS" || d.Status == "QUEUED") {
				runningExportID = d.ID
				runningExportStatus = d.Status
				break
			}
		}

		var deploymentID string
		var deploymentStartTime time.Time
		if runningExportID != "" {
			s.UpdateMessage(fmt.Sprintf("⏳ Found running Terraform export (status: %s, id: %s). Waiting for it to complete...", runningExportStatus, runningExportID))
			deploymentID = runningExportID
			// Find the running deployment object to get its start time
			for _, d := range deploymentsResp.Payload.Deployments {
				if d.ID == runningExportID {
					deploymentStartTime = time.Time(d.CreatedOn)
					break
				}
			}
		} else {
			// 2. No running export, trigger a new one
			params := ui_deployment_controller.NewTriggerTerraformExportParams()
			params.ClusterID = environment
			response, err := client.UIDeploymentController.TriggerTerraformExport(params, auth)
			if err != nil {
				s.Fail("❌ Error triggering Terraform Export")
				fmt.Printf("🔴 Could not trigger terraform export: %v\n", err)
				return
			}
			if response.IsCode(200) && response.Payload.Status == "IN_PROGRESS" {
				s.UpdateMessage("🦄 Terraform export triggered with id: " + response.Payload.ID + timeEstimateMsg)
				deploymentID = response.Payload.ID
				deploymentStartTime = time.Now()
			} else {
				s.Fail("❌ Could not trigger terraform export: response code " + strconv.Itoa(response.Code()) + " and payload: " + response.Payload.ID + " and status: " + response.Payload.Status)
				return
			}
		}

		// 3. Wait for the export to complete
		for {
			time.Sleep(5 * time.Second)
			getDeploymentParams := ui_deployment_controller.NewGetDeploymentParams()
			getDeploymentParams.ClusterID = environment
			getDeploymentParams.DeploymentID = deploymentID
			deploymentStatus, err := client.UIDeploymentController.GetDeployment(getDeploymentParams, auth)
			if err != nil {
				s.Fail("❌ Could not get deployment status")
				fmt.Printf("🔴 Could not get deployment status: %v\n", err)
				return
			}
			if deploymentStatus.Payload.Status == "SUCCEEDED" || deploymentStatus.Payload.Status == "FAILED" {
				if deploymentStatus.Payload.Status == "FAILED" {
					s.Fail("❌ Terraform export failed")
					for _, log := range deploymentStatus.Payload.ErrorLogs {
						fmt.Printf("🔴 Error logs : %v,", log.ErrorMessage)
					}
					return
				}
				break
			} else {
				elapsed := time.Since(deploymentStartTime)
				var remainingMsg string
				if avgTime > 0 {
					remaining := avgTime - elapsed
					if remaining > 0 {
						remainingMsg = fmt.Sprintf(" (⏱️ Est. %s remaining)", utils.FormatDuration(remaining))
					}
				}
				s.UpdateMessage("⚡ Terraform export in progress..." + remainingMsg)
			}
		}

		// 4. Download the export for the completed deployment
		clientConfig := config.GetClientConfig(profile)
		if clientConfig == nil {
			s.Fail("❌ Could not get client configuration")
			return
		}
		s.UpdateMessage("📥 Preparing to download Terraform export...")

		filename := fmt.Sprintf("%s.zip", deploymentID)
		currentDir, err := os.Getwd()
		if err != nil {
			s.Fail("❌ Could not get current directory: " + err.Error())
			return
		}

		zipFilePath := filepath.Join(currentDir, filename)
		downloadURL := fmt.Sprintf("%s/cc-ui/v1/clusters/%s/deployments/%s/download-terraform-export",
			clientConfig.ControlPlaneURL,
			environment,
			deploymentID)

		req, err := http.NewRequest("GET", downloadURL, nil)
		if err != nil {
			s.Fail("❌ Could not create download request: " + err.Error())
			return
		}

		req.Header.Add("Accept", "*/*")
		req.SetBasicAuth(clientConfig.Username, clientConfig.Token)

		httpClient := &http.Client{}
		resp, err := httpClient.Do(req)
		if err != nil {
			s.Fail("❌ Could not download export: " + err.Error())
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			s.Fail(fmt.Sprintf("❌ Download failed with status: %s", resp.Status))
			return
		}

		file, err := os.Create(zipFilePath)
		if err != nil {
			s.Fail("❌ Could not create export file: " + err.Error())
			return
		}
		defer file.Close()

		// Create progress writer with total size from response
		progress := &progressWriter{
			total:      resp.ContentLength,
			startTime:  time.Now(),
			avgTime:    avgTime,
			lastUpdate: time.Now(),
			spinner:    s,
		}

		// Copy the response body to the file while tracking progress
		_, err = io.Copy(file, io.TeeReader(resp.Body, progress))
		if err != nil {
			s.Fail("❌ Error downloading file: " + err.Error())
			return
		}

		// Always clean the exported files and optionally include providers
		// This requires extracting, processing, and re-zipping
		tempDir, err := os.MkdirTemp("", "fctl-export-process-*")
		if err != nil {
			s.Fail("❌ Could not create temp directory: " + err.Error())
			return
		}
		defer os.RemoveAll(tempDir)

		s.UpdateMessage("📦 Processing exported files...")
		if err := utils.ExtractZip(zipFilePath, tempDir); err != nil {
			s.Fail("❌ Could not extract zip: " + err.Error())
			return
		}

		// Ensure all files/dirs are writable
		if err := ensureWritable(tempDir); err != nil {
			s.Fail("❌ Could not set permissions: " + err.Error())
			return
		}

		// Clean the extracted files (remove facets.yaml, resource_gen.tf, and clean JSON files)
		s.UpdateMessage("🧹 Cleaning exported files...")
		if err := utils.CleanExportedFiles(tempDir); err != nil {
			s.Fail("❌ Error cleaning exported files: " + err.Error())
			return
		}

		// If include-providers is set, run terraform init
		if includeProviders {
			s.UpdateMessage("🔧 Including Terraform providers...")
			// Run 'terraform init' in tempDir using terraform-exec
			tf, err := tfexec.NewTerraform(fmt.Sprintf("%s/tfexport", tempDir), "terraform")
			if err != nil {
				s.Fail("❌ Failed to create terraform executor: " + err.Error())
				return
			}
			tf.SetStdout(io.Discard)
			tf.SetStderr(io.Discard)
			if err := tf.Init(context.Background()); err != nil {
				s.Fail("❌ 'terraform init' failed: " + err.Error())
				return
			}
		}

		// Re-zip the cleaned (and optionally provider-included) directory
		if err := utils.ZipDir(tempDir, zipFilePath); err != nil {
			s.Fail("❌ Could not re-zip directory: " + err.Error())
			return
		}

		// If --copy is set, extract zip, copy files, and re-zip
		if len(exportCopyPairs) > 0 {
			tempDir, err := os.MkdirTemp("", "fctl-export-copy-*")
			if err != nil {
				s.Fail("❌ Could not create temp directory for --copy: " + err.Error())
				return
			}
			defer os.RemoveAll(tempDir)
			if err := utils.ExtractZip(zipFilePath, tempDir); err != nil {
				s.Fail("❌ Could not extract zip for --copy: " + err.Error())
				return
			}
			s.UpdateMessage("📄 Copying files to zip structure...")
			for _, pair := range exportCopyPairs {
				sepIdx := -1
				for i, c := range pair {
					if c == ':' {
						sepIdx = i
						break
					}
				}
				if sepIdx == -1 {
					s.Fail(fmt.Sprintf("❌ Invalid --copy value: %s (expected format source:destination)", pair))
					return
				}
				source := pair[:sepIdx]
				dest := pair[sepIdx+1:]
				if source == "" || dest == "" {
					s.Fail(fmt.Sprintf("❌ Invalid --copy value: %s (source and destination required)", pair))
					return
				}
				destPath := filepath.Join(tempDir, dest)
				srcInfo, err := os.Stat(source)
				if err != nil {
					s.Fail(fmt.Sprintf("❌ Failed to stat source: %s", source))
					return
				}
				if srcInfo.IsDir() {
					if err := utils.CopyDir(source, destPath); err != nil {
						s.Fail(fmt.Sprintf("❌ Failed to copy directory: %s", source))
						return
					}
				} else {
					if err := utils.CopyFile(source, destPath); err != nil {
						s.Fail(fmt.Sprintf("❌ Failed to copy file: %s", source))
						return
					}
				}
			}
			if err := utils.ZipDir(tempDir, zipFilePath); err != nil {
				s.Fail("❌ Could not re-zip after --copy: " + err.Error())
				return
			}
		}

		s.Stop(fmt.Sprintf("✅ Export completed successfully! 📁 Saved to: %s", zipFilePath))

		// Handle post-export actions
		applyFlag, _ := cmd.Flags().GetBool("apply")
		planFlag, _ := cmd.Flags().GetBool("plan")
		destroyFlag, _ := cmd.Flags().GetBool("destroy")
		if exportUploadReleaseMetadata && !(applyFlag || destroyFlag) {
			fmt.Println("❌ --upload-release-metadata can only be used with --apply or --destroy.")
			return
		}
		flagCount := 0
		if applyFlag {
			flagCount++
		}
		if planFlag {
			flagCount++
		}
		if destroyFlag {
			flagCount++
		}
		if flagCount > 1 {
			fmt.Println("❌ Only one of --apply, --plan, or --destroy can be specified at a time.")
			return
		}
		if applyFlag {
			fmt.Println("\n➡️  Invoking 'fctl apply' on exported zip...")
			applyCmd.Flags().Set("zip", filename)
			if exportUploadReleaseMetadata {
				applyCmd.Flags().Set("upload-release-metadata", "true")
			}
			if allowDestroy {
				applyCmd.Flags().Set("allow-destroy", "true")
			}
			err := runApply(applyCmd, []string{})
			if err != nil {
				fmt.Printf("❌ Error during apply: %v\n", err)
			}
		}
		if planFlag {
			fmt.Println("\n➡️  Invoking 'fctl plan' on exported zip...")
			planCmd.Flags().Set("zip", filename)
			if exportUploadReleaseMetadata {
				planCmd.Flags().Set("upload-release-metadata", "true")
			}
			if allowDestroy {
				planCmd.Flags().Set("allow-destroy", "true")
			}
			err := runPlan(planCmd, []string{})
			if err != nil {
				fmt.Printf("❌ Error during plan: %v\n", err)
			}
		}
		if destroyFlag {
			fmt.Println("\n➡️  Invoking 'fctl destroy' on exported zip...")
			destroyCmd.Flags().Set("zip", filename)
			if exportUploadReleaseMetadata {
				destroyCmd.Flags().Set("upload-release-metadata", "true")
			}
			if allowDestroy {
				destroyCmd.Flags().Set("allow-destroy", "true")
			}
			err := runDestroy(destroyCmd, []string{})
			if err != nil {
				fmt.Printf("❌ Error during destroy: %v\n", err)
			}
		}
	},
}

func init() {
	rootCmd.AddCommand(exportCmd)
	exportCmd.Flags().StringP("environment-id", "e", "", "The environment to export")
	exportCmd.Flags().String("project", "", "The project (stack) name to use for environment lookup")
	exportCmd.Flags().String("env-name", "", "The environment (cluster) name to use for environment lookup")
	exportCmd.Flags().Bool("include-providers", false, "Include Terraform providers in the exported zip (runs 'terraform init' and bundles providers for airgapped use)")

	// Add mutually exclusive flags for post-export actions
	exportCmd.Flags().Bool("apply", false, "Automatically apply the exported Terraform configuration after export")
	exportCmd.Flags().Bool("plan", false, "Automatically run terraform plan on the exported configuration after export")
	exportCmd.Flags().Bool("destroy", false, "Automatically destroy resources using the exported configuration after export")

	exportCmd.Flags().StringArrayVar(&exportCopyPairs, "copy", nil, "Copy a file or directory from local into a specific path inside the zip. Format: source:destination. Can be specified multiple times.")
	exportCmd.Flags().BoolVar(&exportUploadReleaseMetadata, "upload-release-metadata", false, "Upload release metadata to control plane after apply/plan/destroy (must be used with --apply, --plan, or --destroy)")
}
