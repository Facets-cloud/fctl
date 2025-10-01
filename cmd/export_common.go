package cmd

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Facets-cloud/facets-sdk-go/facets/client"
	"github.com/Facets-cloud/facets-sdk-go/facets/client/ui_deployment_controller"
	"github.com/Facets-cloud/fctl/pkg/config"
	"github.com/Facets-cloud/fctl/pkg/utils"
	"github.com/go-openapi/runtime"
)

// ExportEnvironmentOptions contains options for exporting a single environment
type ExportEnvironmentOptions struct {
	EnvironmentID    string
	EnvironmentName  string
	ProjectName      string
	OutputDir        string
	IncludeProviders bool
	Profile          string
}

// TriggerOrWaitForExport checks for existing export or triggers a new one
func TriggerOrWaitForExport(
	client *client.Facets,
	auth runtime.ClientAuthInfoWriter,
	environmentID string,
	progress *ExportProgress,
) (string, error) {
	// Check for running TERRAFORM_EXPORT deployments
	getDeploymentsParams := ui_deployment_controller.NewGetDeploymentsParams()
	getDeploymentsParams.ClusterID = environmentID
	deploymentsResp, err := client.UIDeploymentController.GetDeployments(getDeploymentsParams, auth)
	if err != nil {
		if apiErr, ok := err.(*runtime.APIError); ok && apiErr.Code == 503 {
			return "", fmt.Errorf("control plane is down")
		}
		return "", fmt.Errorf("could not get deployments: %w", err)
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
		if progress != nil {
			progress.UpdateStatus(environmentID, "waiting", 
				fmt.Sprintf("Found existing export (status: %s)", runningExportStatus))
		}
		deploymentID = runningExportID
		// Find the running deployment object to get its start time
		for _, d := range deploymentsResp.Payload.Deployments {
			if d.ID == runningExportID {
				deploymentStartTime = time.Time(d.CreatedOn)
				break
			}
		}
	} else {
		// No running export, trigger a new one
		if progress != nil {
			progress.UpdateStatus(environmentID, "triggering", "Triggering new export...")
		}
		
		params := ui_deployment_controller.NewTriggerTerraformExportParams()
		params.ClusterID = environmentID
		response, err := client.UIDeploymentController.TriggerTerraformExport(params, auth)
		if err != nil {
			// Extract clean error message from API error
			errStr := err.Error()
			// Check if it's a 400 error with the common message
			if strings.Contains(errStr, "Cannot trigger terraform export on an environment that is not in a running state") {
				return "", fmt.Errorf("Cannot trigger terraform export on an environment that is not in a running state")
			}
			// Check for other common error patterns
			if strings.Contains(errStr, "[400]") {
				// Try to extract message from error string
				if idx := strings.Index(errStr, `"message":"`); idx != -1 {
					msgStart := idx + len(`"message":"`)
					if msgEnd := strings.Index(errStr[msgStart:], `"`); msgEnd != -1 {
						return "", fmt.Errorf("%s", errStr[msgStart:msgStart+msgEnd])
					}
				}
				return "", fmt.Errorf("Cannot trigger terraform export on this environment")
			}
			return "", fmt.Errorf("Failed to trigger export")
		}
		
		if response.IsCode(200) && response.Payload.Status == "IN_PROGRESS" {
			deploymentID = response.Payload.ID
			deploymentStartTime = time.Now()
		} else {
			return "", fmt.Errorf("unexpected response: code %d, status: %s", 
				response.Code(), response.Payload.Status)
		}
	}

	// Wait for the export to complete
	return deploymentID, WaitForExportCompletion(client, auth, environmentID, deploymentID, deploymentStartTime, progress)
}

// WaitForExportCompletion waits for an export to complete
func WaitForExportCompletion(
	client *client.Facets,
	auth runtime.ClientAuthInfoWriter,
	environmentID string,
	deploymentID string,
	startTime time.Time,
	progress *ExportProgress,
) error {
	for {
		time.Sleep(5 * time.Second)
		
		getDeploymentParams := ui_deployment_controller.NewGetDeploymentParams()
		getDeploymentParams.ClusterID = environmentID
		getDeploymentParams.DeploymentID = deploymentID
		deploymentStatus, err := client.UIDeploymentController.GetDeployment(getDeploymentParams, auth)
		if err != nil {
			return fmt.Errorf("could not get deployment status: %w", err)
		}
		
		if deploymentStatus.Payload.Status == "SUCCEEDED" || deploymentStatus.Payload.Status == "FAILED" {
			if deploymentStatus.Payload.Status == "FAILED" {
				errorMsg := "export failed"
				if len(deploymentStatus.Payload.ErrorLogs) > 0 {
					errorMsg = deploymentStatus.Payload.ErrorLogs[0].ErrorMessage
				}
				return fmt.Errorf(errorMsg)
			}
			break
		} else {
			elapsed := time.Since(startTime)
			if progress != nil {
				progress.UpdateStatus(environmentID, "waiting", 
					fmt.Sprintf("Export in progress (%s)...", utils.FormatDuration(elapsed)))
			}
		}
	}
	
	return nil
}

// DownloadExport downloads the exported zip file
func DownloadExport(
	environmentID string,
	deploymentID string,
	outputPath string,
	profile string,
	progress *ExportProgress,
) error {
	clientConfig := config.GetClientConfig(profile)
	if clientConfig == nil {
		return fmt.Errorf("could not get client configuration")
	}
	
	if progress != nil {
		progress.UpdateStatus(environmentID, "downloading", "Preparing download...")
	}
	
	downloadURL := fmt.Sprintf("%s/cc-ui/v1/clusters/%s/deployments/%s/download-terraform-export",
		clientConfig.ControlPlaneURL,
		environmentID,
		deploymentID)
	
	req, err := http.NewRequest("GET", downloadURL, nil)
	if err != nil {
		return fmt.Errorf("could not create download request: %w", err)
	}
	
	req.Header.Add("Accept", "*/*")
	req.SetBasicAuth(clientConfig.Username, clientConfig.Token)
	
	httpClient := &http.Client{}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("could not download export: %w", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status: %s", resp.Status)
	}
	
	// Create output directory if it doesn't exist
	outputDir := filepath.Dir(outputPath)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("could not create output directory: %w", err)
	}
	
	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("could not create export file: %w", err)
	}
	defer file.Close()
	
	// Get content length for progress tracking
	contentLength := resp.ContentLength
	
	// Create a progress writer if we have progress tracking
	var writer io.Writer = file
	if progress != nil && contentLength > 0 {
		writer = &exportProgressWriter{
			writer:        file,
			total:         contentLength,
			environmentID: environmentID,
			progress:      progress,
			startTime:     time.Now(),
			lastUpdate:    time.Now(),
		}
	}
	
	_, err = io.Copy(writer, resp.Body)
	if err != nil {
		return fmt.Errorf("could not save export file: %w", err)
	}
	
	if progress != nil {
		progress.UpdateStatus(environmentID, "downloading", "Download complete")
	}
	
	return nil
}

// exportProgressWriter tracks download progress for a specific environment
type exportProgressWriter struct {
	writer        io.Writer
	total         int64
	downloaded    int64
	environmentID string
	progress      *ExportProgress
	lastUpdate    time.Time
	startTime     time.Time
}

func (epw *exportProgressWriter) Write(p []byte) (int, error) {
	n, err := epw.writer.Write(p)
	if err != nil {
		return n, err
	}
	
	epw.downloaded += int64(n)
	
	// Update progress every 100ms to avoid too frequent updates
	if time.Since(epw.lastUpdate) > 100*time.Millisecond {
		elapsed := time.Since(epw.startTime)
		speed := float64(epw.downloaded) / elapsed.Seconds() / 1024 / 1024 // MB/s
		
		if epw.total > 0 {
			percentage := float64(epw.downloaded) / float64(epw.total) * 100
			
			// Calculate estimated time remaining
			remaining := float64(epw.total-epw.downloaded) / (speed * 1024 * 1024) // seconds
			remainingDuration := time.Duration(remaining) * time.Second
			
			progressMsg := fmt.Sprintf("📥 %.1f%% (%.1fMB/%.1fMB) at %.1f MB/s - %s remaining",
				percentage,
				float64(epw.downloaded)/1024/1024,
				float64(epw.total)/1024/1024,
				speed,
				utils.FormatDuration(remainingDuration))
			epw.progress.UpdateStatus(epw.environmentID, "downloading", progressMsg)
		} else {
			// No total size available, just show downloaded amount and speed
			progressMsg := fmt.Sprintf("📥 %.1fMB downloaded at %.1f MB/s",
				float64(epw.downloaded)/1024/1024,
				speed)
			epw.progress.UpdateStatus(epw.environmentID, "downloading", progressMsg)
		}
		
		epw.lastUpdate = time.Now()
	}
	
	return n, nil
}

// ExtractZip extracts a zip file to a directory
func ExtractZip(zipPath string, destDir string) error {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("could not open zip: %w", err)
	}
	defer reader.Close()
	
	for _, file := range reader.File {
		path := filepath.Join(destDir, file.Name)
		
		if file.FileInfo().IsDir() {
			os.MkdirAll(path, file.Mode())
			continue
		}
		
		// Create directory for file if needed
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return fmt.Errorf("could not create directory: %w", err)
		}
		
		fileReader, err := file.Open()
		if err != nil {
			return fmt.Errorf("could not open file in zip: %w", err)
		}
		defer fileReader.Close()
		
		targetFile, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, file.Mode())
		if err != nil {
			return fmt.Errorf("could not create target file: %w", err)
		}
		defer targetFile.Close()
		
		if _, err := io.Copy(targetFile, fileReader); err != nil {
			return fmt.Errorf("could not extract file: %w", err)
		}
	}
	
	return nil
}

// ProcessExportedEnvironment handles the full export process for a single environment
func ProcessExportedEnvironment(
	client *client.Facets,
	auth runtime.ClientAuthInfoWriter,
	opts ExportEnvironmentOptions,
	progress *ExportProgress,
) error {
	// 1. Trigger or wait for export
	if progress != nil {
		progress.UpdateStatus(opts.EnvironmentID, "triggering", "Starting export process...")
	}
	
	deploymentID, err := TriggerOrWaitForExport(client, auth, opts.EnvironmentID, progress)
	if err != nil {
		// Return the error as-is since it's already cleaned up in TriggerOrWaitForExport
		return err
	}
	
	// 2. Create environment directory with environment name
	// OutputDir already contains the project name, so add environments folder and environment name
	envDir := filepath.Join(opts.OutputDir, "environments", opts.EnvironmentName)
	if err := os.MkdirAll(envDir, 0755); err != nil {
		return fmt.Errorf("could not create environment directory: %w", err)
	}
	
	// 3. Download the export
	zipPath := filepath.Join(envDir, fmt.Sprintf("%s.zip", deploymentID))
	if err := DownloadExport(opts.EnvironmentID, deploymentID, zipPath, opts.Profile, progress); err != nil {
		return fmt.Errorf("Download failed")
	}
	
	// 4. Extract the zip
	if progress != nil {
		progress.UpdateStatus(opts.EnvironmentID, "extracting", "Extracting archive...")
	}
	
	if err := ExtractZip(zipPath, envDir); err != nil {
		return fmt.Errorf("Failed to extract archive")
	}
	
	// 5. Clean exported files
	if progress != nil {
		progress.UpdateStatus(opts.EnvironmentID, "cleaning", "Cleaning exported files...")
		
		// For export-all, suppress the cleaning output to avoid interfering with status display
		// Capture stdout temporarily
		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w
		
		// Run the cleaning
		cleanErr := utils.CleanExportedFiles(envDir)
		
		// Restore stdout
		w.Close()
		os.Stdout = oldStdout
		io.Copy(io.Discard, r) // Discard the output
		r.Close()
		
		if cleanErr != nil {
			// Don't fail the whole export if cleaning has issues
			// Error will be tracked internally, no need to print
		}
	} else {
		// For regular export, show the cleaning output
		if err := utils.CleanExportedFiles(envDir); err != nil {
			// Don't fail the whole export if cleaning has issues
			fmt.Printf("⚠️  Warning: Clean exported files encountered issues for %s: %v\n", 
				opts.EnvironmentName, err)
		}
	}
	
	// 6. Remove the zip file after successful extraction and cleaning
	if err := os.Remove(zipPath); err != nil {
		// Just log warning, don't fail the export
		fmt.Printf("⚠️  Warning: Could not remove zip file %s: %v\n", filepath.Base(zipPath), err)
	}
	
	// 7. Mark as complete
	if progress != nil {
		// Extract just the last two parts of the path for display
		projectName := filepath.Base(opts.OutputDir)
		// Show extracted path since zip is removed
		outputPath := fmt.Sprintf("%s/%s/", projectName, opts.EnvironmentName)
		progress.UpdateStatus(opts.EnvironmentID, "complete", outputPath)
	}
	
	return nil
}