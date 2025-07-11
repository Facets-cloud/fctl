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
	"github.com/Facets-cloud/fctl/pkg/config"
	"github.com/go-openapi/runtime"
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
				estimatedMsg = fmt.Sprintf(" (⏱️ Est. %.1f min based on history)", pw.avgTime.Minutes())
			} else {
				// Calculate based on current progress and speed
				remaining := float64(pw.total-pw.downloaded) / (speed * 1024 * 1024) // seconds
				estimatedMsg = fmt.Sprintf(" (⏱️ Est. %.1f min remaining at %.1f MB/s)", remaining/60, speed)
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

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export a project environment",
	Run: func(cmd *cobra.Command, args []string) {
		environment, _ := cmd.Flags().GetString("environment")

		s := pin.New("🚀 Initializing export for environment: "+environment+"...",
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

		if environment == "" {
			s.Fail("❌ Environment ID is required")
			return
		}

		profile, _ := cmd.Flags().GetString("profile")
		client, auth, err := config.GetClient(profile, false)
		if err != nil {
			s.Fail("❌ Error fetching client")
			fmt.Printf("🔴 Could not get client: %v\n", err)
			return
		}

		// Get average deployment time from history
		avgTime := getHistoricalDeploymentTime(client, auth, environment)
		var timeEstimateMsg string
		if avgTime > 0 {
			timeEstimateMsg = fmt.Sprintf(" (⏱️ Est. %.1f minutes based on last 10 exports)", avgTime.Minutes())
		}

		params := ui_deployment_controller.NewTriggerTerraformExportParams()
		params.ClusterID = environment

		response, err := client.UIDeploymentController.TriggerTerraformExport(params, auth)
		if err != nil {
			s.Fail("❌ Error triggering Terraform Export")
			fmt.Printf("🔴 Could not trigger terraform export: %v\n", err)
			return
		}

		if response.IsCode(200) {
			s.UpdateMessage("🦄 Terraform export triggered with id: " + response.Payload.ID + timeEstimateMsg)
		} else {
			s.Fail("❌ Could not trigger terraform export: response code " + strconv.Itoa(response.Code()))
		}

		startTime := time.Now()
		// wait for the export to complete, check if deployment is completed
		for {
			time.Sleep(5 * time.Second)
			getDeploymentParams := ui_deployment_controller.NewGetDeploymentParams()
			getDeploymentParams.ClusterID = environment
			getDeploymentParams.DeploymentID = response.Payload.ID
			deploymentStatus, err := client.UIDeploymentController.GetDeployment(getDeploymentParams, auth)
			if err != nil {
				s.Fail("❌ Could not get deployment status")
				fmt.Printf("🔴 Could not get deployment status: %v\n", err)
				return
			}
			if deploymentStatus.Payload.Status == "SUCCEEDED" || deploymentStatus.Payload.Status == "FAILED" {
				if deploymentStatus.Payload.Status == "FAILED" {
					s.Fail("❌ Terraform export failed")
					fmt.Printf("🔴 Terraform export failed: %v\n", deploymentStatus.Payload)
					return
				}
				break
			} else {
				elapsed := time.Since(startTime)
				var remainingMsg string
				if avgTime > 0 {
					remaining := avgTime - elapsed
					if remaining > 0 {
						remainingMsg = fmt.Sprintf(" (⏱️ Est. %.1f min remaining)", remaining.Minutes())
					}
				}
				s.UpdateMessage("⚡ Terraform export in progress..." + remainingMsg)
			}
		}

		// Now we download the export using direct HTTP request
		clientConfig := config.GetClientConfig(profile)
		if clientConfig == nil {
			s.Fail("❌ Could not get client configuration")
			return
		}
		s.UpdateMessage("📥 Preparing to download Terraform export...")

		downloadURL := fmt.Sprintf("%s/cc-ui/v1/clusters/%s/deployments/%s/download-terraform-export",
			clientConfig.ControlPlaneURL,
			environment,
			response.Payload.ID)

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

		filename := fmt.Sprintf("terraform-export-%s-%s.zip", environment, time.Now().Format("20060102-150405"))
		currentDir, err := os.Getwd()
		if err != nil {
			s.Fail("❌ Could not get current directory: " + err.Error())
			return
		}

		filepath := filepath.Join(currentDir, filename)
		file, err := os.Create(filepath)
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

		s.Stop(fmt.Sprintf("✅ Export completed successfully! 📁 Saved to: %s", filepath))
	},
}

func init() {
	rootCmd.AddCommand(exportCmd)
	exportCmd.Flags().StringP("environment", "e", "", "The environment to export")
	exportCmd.MarkFlagRequired("environment")
}
