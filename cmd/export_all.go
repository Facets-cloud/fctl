package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Facets-cloud/facets-sdk-go/facets/client"
	"github.com/Facets-cloud/facets-sdk-go/facets/client/ui_stack_controller"
	"github.com/Facets-cloud/fctl/pkg/config"
	"github.com/go-openapi/runtime"
	"github.com/hashicorp/terraform-exec/tfexec"
	"github.com/spf13/cobra"
)

// EnvironmentExportStatus tracks the export status of a single environment
type EnvironmentExportStatus struct {
	EnvironmentName string
	EnvironmentID   string
	Status          string // pending, triggering, waiting, downloading, extracting, cleaning, complete, failed
	Progress        string // detailed progress info
	StartTime       time.Time
	Error           error
	OutputPath      string
}

// ExportProgress tracks overall export progress
type ExportProgress struct {
	mu           sync.Mutex
	environments []EnvironmentExportStatus
	completed    int
	failed       int
	inProgress   int
	lastLines    int  // Track how many lines were printed last time
}

// NewExportProgress creates a new progress tracker
func NewExportProgress(environments []EnvironmentExportStatus) *ExportProgress {
	return &ExportProgress{
		environments: environments,
	}
}

// UpdateStatus updates the status of a specific environment
func (ep *ExportProgress) UpdateStatus(envID, status, progress string) {
	ep.mu.Lock()
	defer ep.mu.Unlock()
	
	for i := range ep.environments {
		if ep.environments[i].EnvironmentID == envID {
			oldStatus := ep.environments[i].Status
			ep.environments[i].Status = status
			ep.environments[i].Progress = progress
			
			// Update counters
			if oldStatus != "complete" && oldStatus != "failed" && (status == "complete" || status == "failed") {
				if oldStatus != "pending" {
					ep.inProgress--
				}
				if status == "complete" {
					ep.completed++
				} else {
					ep.failed++
				}
			} else if oldStatus == "pending" && status != "pending" && status != "complete" && status != "failed" {
				ep.inProgress++
			}
			
			// Only print for significant status changes, not intermediate updates
			break
		}
	}
}

// SetError sets an error for a specific environment
func (ep *ExportProgress) SetError(envID string, err error) {
	ep.mu.Lock()
	defer ep.mu.Unlock()
	
	for i := range ep.environments {
		if ep.environments[i].EnvironmentID == envID {
			ep.environments[i].Error = err
			ep.environments[i].Status = "failed"
			ep.environments[i].Progress = fmt.Sprintf("Error: %v", err)
			break
		}
	}
}

// DisplayStatus shows the current status of all environments
func (ep *ExportProgress) DisplayStatus(clearPrevious bool) {
	ep.mu.Lock()
	defer ep.mu.Unlock()
	
	// Clear previous output if needed
	if clearPrevious && ep.lastLines > 0 {
		// Move cursor up and clear lines
		for i := 0; i < ep.lastLines; i++ {
			fmt.Print("\033[1A") // Move up one line
			fmt.Print("\033[2K") // Clear entire line
		}
	}
	
	lineCount := 0
	
	fmt.Println("📊 Export Status:")
	lineCount++
	fmt.Println("─────────────────────────────────────────────────────────────────")
	lineCount++
	
	for _, env := range ep.environments {
		icon := "⏸️ "
		statusText := "Pending"
		
		switch env.Status {
		case "triggering":
			icon = "🚀"
			statusText = "Starting export..."
		case "waiting":
			icon = "⏳"
			statusText = env.Progress
		case "downloading":
			icon = "📥"
			statusText = env.Progress
		case "extracting":
			icon = "📦"
			statusText = "Extracting archive..."
		case "cleaning":
			icon = "🧹"
			statusText = "Cleaning exported files..."
		case "complete":
			icon = "✅"
			statusText = fmt.Sprintf("Complete → %s", env.OutputPath)
		case "failed":
			icon = "❌"
			if env.Error != nil {
				statusText = fmt.Sprintf("Failed: %v", env.Error)
			} else {
				statusText = "Failed"
			}
		}
		
		fmt.Printf("%s %-20s %s\n", icon, env.EnvironmentName, statusText)
		lineCount++
	}
	
	fmt.Println("─────────────────────────────────────────────────────────────────")
	lineCount++
	
	ep.lastLines = lineCount
}

// PrintSummary prints a simple progress summary
func (ep *ExportProgress) PrintSummary() {
	ep.mu.Lock()
	defer ep.mu.Unlock()
	
	pending := len(ep.environments) - ep.completed - ep.failed - ep.inProgress
	fmt.Printf("\n📊 Final Progress: %d/%d completed, %d failed, %d pending\n",
		ep.completed, len(ep.environments), ep.failed, pending)
}


var exportAllCmd = &cobra.Command{
	Use:   "export-all",
	Short: "Export all environments from a project as Terraform configurations",
	Long:  `Export all environments from a specific project. This command exports all environments in the specified project for exit strategy purposes, downloading all environments and their resources as Terraform configurations`,
	Run: func(cmd *cobra.Command, args []string) {
		project, _ := cmd.Flags().GetString("project")
		outputDir, _ := cmd.Flags().GetString("output-dir")
		includeProviders, _ := cmd.Flags().GetBool("include-providers")
		skipFailed, _ := cmd.Flags().GetBool("skip-failed")
		
		if outputDir == "" {
			var err error
			outputDir, err = os.Getwd()
			if err != nil {
				fmt.Printf("❌ Could not get current directory: %v\n", err)
				return
			}
		}
		
		// Get client and auth
		profile, _ := cmd.Flags().GetString("profile")
		client, auth, err := config.GetClient(profile, false)
		if err != nil {
			fmt.Printf("❌ Error getting client: %v\n", err)
			return
		}
		
		// Check if project is specified
		if project == "" {
			fmt.Printf("❌ Project is required. Use --project to specify which project to export\n")
			return
		}
		
		// Run the export-all logic
		if err := runExportAll(client, auth, project, outputDir, includeProviders, skipFailed); err != nil {
			fmt.Printf("❌ Export-all failed: %v\n", err)
			return
		}
	},
}

func runExportAll(
	client *client.Facets,
	auth runtime.ClientAuthInfoWriter,
	project string,
	outputDir string,
	includeProviders bool,
	skipFailed bool,
) error {
	// 1. Get all stacks (projects)
	stackParams := ui_stack_controller.NewGetStacksParams()
	stacksResp, err := client.UIStackController.GetStacks(stackParams, auth)
	if err != nil {
		if stacksResp != nil && stacksResp.Code() == 503 {
			return fmt.Errorf("control plane is unreachable or down (HTTP 503)")
		}
		return fmt.Errorf("could not get stacks: %w", err)
	}
	
	// Verify the project exists
	fmt.Printf("🔍 Looking up project: %s\n", project)
	found := false
	for _, stack := range stacksResp.Payload {
		if stack.Name == project {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("project (stack) not found: %s", project)
	}
	
	// Single project export
	projectName := project
	fmt.Printf("\n═══════════════════════════════════════════════════════════════\n")
	fmt.Printf("🚀 Exporting project: %s\n", projectName)
	fmt.Printf("═══════════════════════════════════════════════════════════════\n")
	
	if err := exportSingleProject(client, auth, projectName, outputDir, includeProviders, skipFailed); err != nil {
		return fmt.Errorf("failed to export project %s: %w", projectName, err)
	}
	
	return nil
}

func exportSingleProject(
	client *client.Facets,
	auth runtime.ClientAuthInfoWriter,
	projectName string,
	outputDir string,
	includeProviders bool,
	skipFailed bool,
) error {
	// Get all clusters (environments) for the project
	fmt.Printf("📋 Fetching environments for project: %s\n", projectName)
	clusterParams := ui_stack_controller.NewGetClustersParams()
	clusterParams.StackName = projectName
	clustersResp, err := client.UIStackController.GetClusters(clusterParams, auth)
	if err != nil {
		if clustersResp != nil && clustersResp.Code() == 503 {
			return fmt.Errorf("control plane is unreachable or down (HTTP 503)")
		}
		return fmt.Errorf("could not get clusters: %w", err)
	}
	
	if len(clustersResp.Payload) == 0 {
		fmt.Printf("⚠️  No environments found for project: %s\n", projectName)
		return nil // Don't fail, just skip
	}
	
	fmt.Printf("📊 Found %d environments to export\n\n", len(clustersResp.Payload))
	
	// Prepare environment export statuses
	environments := make([]EnvironmentExportStatus, 0, len(clustersResp.Payload))
	for _, cluster := range clustersResp.Payload {
		envName := "unnamed"
		if cluster.Name != nil {
			envName = *cluster.Name
		}
		environments = append(environments, EnvironmentExportStatus{
			EnvironmentName: envName,
			EnvironmentID:   cluster.ID,
			Status:          "pending",
			Progress:        "Pending",
		})
	}
	
	// Create project output directory
	projectDir := filepath.Join(outputDir, projectName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		return fmt.Errorf("failed to create project directory: %w", err)
	}
	
	// Setup progress tracking
	progress := NewExportProgress(environments)
	
	// Start a goroutine to periodically display status
	done := make(chan bool)
	go func() {
		// Initial display
		progress.DisplayStatus(false)
		
		ticker := time.NewTicker(500 * time.Millisecond) // Update more frequently for smoother progress
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				progress.DisplayStatus(true) // Clear previous display
			case <-done:
				return
			}
		}
	}()
	
	// Export all environments in parallel
	err = exportEnvironmentsParallel(client, auth, projectName, projectDir, environments, progress, includeProviders)
	
	// Stop the status display
	close(done)
	time.Sleep(100 * time.Millisecond) // Give display time to finish
	
	// Display final status
	progress.DisplayStatus(true)
	
	if err != nil && !skipFailed {
		return err
	}
	
	// Print final summary before post-processing
	progress.PrintSummary()
	
	// Post-processing: Extract, clean, consolidate modules, relocate deployment context
	fmt.Println("\n\n📦 Post-processing exports...")
	
	if err := postProcessExports(projectDir, projectName, environments); err != nil {
		fmt.Printf("⚠️  Post-processing encountered errors: %v\n", err)
	}
	
	// Show final summary
	showFinalSummary(projectName, projectDir, environments, progress)
	
	return nil
}

func exportEnvironmentsParallel(
	client *client.Facets,
	auth runtime.ClientAuthInfoWriter,
	projectName string,
	projectDir string,
	environments []EnvironmentExportStatus,
	progress *ExportProgress,
	includeProviders bool,
) error {
	var wg sync.WaitGroup
	
	// Export all environments concurrently without any limit
	for i := range environments {
		wg.Add(1)
		go func(env *EnvironmentExportStatus) {
			defer wg.Done()
			
			err := exportSingleEnvironment(client, auth, projectName, projectDir, env, progress, includeProviders)
			if err != nil {
				progress.SetError(env.EnvironmentID, err)
			}
		}(&environments[i])
	}
	
	wg.Wait()
	return nil
}


func exportSingleEnvironment(
	client *client.Facets,
	auth runtime.ClientAuthInfoWriter,
	projectName string,
	projectDir string,
	env *EnvironmentExportStatus,
	progress *ExportProgress,
	includeProviders bool,
) error {
	env.StartTime = time.Now()
	
	opts := ExportEnvironmentOptions{
		EnvironmentID:    env.EnvironmentID,
		EnvironmentName:  env.EnvironmentName,
		ProjectName:      projectName,
		OutputDir:        projectDir,
		IncludeProviders: includeProviders,
		Profile:          "", // Will use default profile
	}
	
	err := ProcessExportedEnvironment(client, auth, opts, progress)
	
	// Update the environment's output path on success
	if err == nil {
		env.OutputPath = fmt.Sprintf("%s/%s/", projectName, env.EnvironmentName)
	}
	
	return err
}

// postProcessExports performs post-export processing for a project
func postProcessExports(projectDir string, projectName string, environments []EnvironmentExportStatus) error {
	fmt.Println()
	
	// Only process successful exports
	successfulEnvs := make([]EnvironmentExportStatus, 0)
	for _, env := range environments {
		if env.Status == "complete" {
			successfulEnvs = append(successfulEnvs, env)
		}
	}
	
	if len(successfulEnvs) == 0 {
		fmt.Printf("⚠️  No successful exports to process for %s\n", projectName)
		return nil
	}
	
	fmt.Println("🔧 Restructuring exported files...")
	if err := restructureTfExport(projectDir, successfulEnvs); err != nil {
		fmt.Printf("⚠️  Error restructuring exports: %v\n", err)
	}
	
	fmt.Println("🔧 Relocating deployment contexts...")
	if err := relocateDeploymentContexts(projectDir, successfulEnvs); err != nil {
		fmt.Printf("⚠️  Error relocating deployment contexts: %v\n", err)
	}
	
	fmt.Println("🔧 Consolidating modules...")
	if err := consolidateModules(projectDir, successfulEnvs); err != nil {
		fmt.Printf("⚠️  Error consolidating modules: %v\n", err)
	}
	
	fmt.Println("🔧 Updating module references...")
	if err := updateModuleReferences(projectDir, successfulEnvs); err != nil {
		fmt.Printf("⚠️  Error updating module references: %v\n", err)
	}
	
	fmt.Println("🔧 Initializing Terraform state for each environment...")
	if err := initializeTerraformState(projectDir, successfulEnvs); err != nil {
		fmt.Printf("⚠️  Error initializing Terraform state: %v\n", err)
	}
	
	return nil
}

func showFinalSummary(project string, projectDir string, environments []EnvironmentExportStatus, progress *ExportProgress) {
	fmt.Println("\n═══════════════════════════════════════════════════════════════")
	fmt.Printf("Export Summary for project: %s\n", project)
	fmt.Println("═══════════════════════════════════════════════════════════════")
	
	successCount := 0
	failedCount := 0
	for _, env := range environments {
		if env.Status == "complete" {
			successCount++
		} else if env.Status == "failed" {
			failedCount++
		}
	}
	
	fmt.Printf("✅ Successfully exported: %d/%d environments\n", successCount, len(environments))
	if failedCount > 0 {
		fmt.Printf("❌ Failed: %d environments\n", failedCount)
		fmt.Println("\nFailed exports:")
		for _, env := range environments {
			if env.Status == "failed" && env.Error != nil {
				fmt.Printf("  - %s [%s]: %v\n", env.EnvironmentName, env.EnvironmentID, env.Error)
			}
		}
	}
	
	fmt.Printf("\n📁 All exports saved to: %s\n", projectDir)
	fmt.Println("═══════════════════════════════════════════════════════════════")
}

// restructureTfExport moves all contents from tfexport directory to environment root
func restructureTfExport(projectDir string, environments []EnvironmentExportStatus) error {
	for _, env := range environments {
		envDir := filepath.Join(projectDir, env.EnvironmentName)
		tfExportDir := filepath.Join(envDir, "tfexport")
		
		// Check if tfexport directory exists
		if _, err := os.Stat(tfExportDir); os.IsNotExist(err) {
			continue
		}
		
		// Move all contents from tfexport to environment root
		err := filepath.Walk(tfExportDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			
			// Skip the tfexport directory itself
			if path == tfExportDir {
				return nil
			}
			
			// Get relative path from tfexport
			relPath, err := filepath.Rel(tfExportDir, path)
			if err != nil {
				return err
			}
			
			newPath := filepath.Join(envDir, relPath)
			
			if info.IsDir() {
				// Create directory at new location
				if err := os.MkdirAll(newPath, 0755); err != nil {
					return err
				}
			} else {
				// Move file to new location
				if err := os.Rename(path, newPath); err != nil {
					// If rename fails, copy and delete
					if err := copyFile(path, newPath); err != nil {
						return err
					}
					os.Remove(path)
				}
			}
			
			return nil
		})
		
		if err != nil {
			fmt.Printf("  ⚠️  Failed to restructure tfexport for %s: %v\n", env.EnvironmentName, err)
			continue
		}
		
		// Remove the now-empty tfexport directory
		if err := os.RemoveAll(tfExportDir); err != nil {
			fmt.Printf("  ⚠️  Failed to remove tfexport directory for %s: %v\n", env.EnvironmentName, err)
		}
	}
	
	return nil
}

// relocateDeploymentContexts updates deployment context references
func relocateDeploymentContexts(projectDir string, environments []EnvironmentExportStatus) error {
	for _, env := range environments {
		envDir := filepath.Join(projectDir, env.EnvironmentName)
		
		// Since tfexport is removed, files are now at environment root
		// Update references in main.tf (now at root)
		mainTfPath := filepath.Join(envDir, "main.tf")
		if err := updateDeploymentContextRef(mainTfPath, "../deploymentcontext.json", "./deploymentcontext.json"); err != nil {
			fmt.Printf("  ⚠️  Failed to update main.tf for %s: %v\n", env.EnvironmentName, err)
		}
		
		// Update references in level2/main.tf (now at env_dir/level2)
		level2MainTfPath := filepath.Join(envDir, "level2", "main.tf")
		if err := updateDeploymentContextRef(level2MainTfPath, "../../deploymentcontext.json", "./deploymentcontext.json"); err != nil {
			fmt.Printf("  ⚠️  Failed to update level2/main.tf for %s: %v\n", env.EnvironmentName, err)
		}
		
		// Copy deploymentcontext.json to level2 directory
		deploymentPath := filepath.Join(envDir, "deploymentcontext.json")
		level2DeploymentPath := filepath.Join(envDir, "level2", "deploymentcontext.json")
		if _, err := os.Stat(deploymentPath); err == nil {
			if err := copyFile(deploymentPath, level2DeploymentPath); err != nil {
				fmt.Printf("  ⚠️  Failed to copy deploymentcontext.json to level2 for %s: %v\n", env.EnvironmentName, err)
			}
		}
		
		// Update references in level2/locals.tf
		level2LocalsTfPath := filepath.Join(envDir, "level2", "locals.tf")
		// Update to ./deploymentcontext.json since we're copying it to level2
		if err := updateDeploymentContextRef(level2LocalsTfPath, "../deploymentcontext.json", "./deploymentcontext.json"); err != nil {
			// Also try updating from ../../deploymentcontext.json in case it was pointing to root
			if err := updateDeploymentContextRef(level2LocalsTfPath, "../../deploymentcontext.json", "./deploymentcontext.json"); err != nil {
				// Don't print warning if file doesn't exist
				if !os.IsNotExist(err) {
					fmt.Printf("  ⚠️  Failed to update level2/locals.tf for %s: %v\n", env.EnvironmentName, err)
				}
			}
		}
	}
	
	return nil
}

// updateDeploymentContextRef updates deployment context file references in terraform files
func updateDeploymentContextRef(tfFile, oldRef, newRef string) error {
	content, err := os.ReadFile(tfFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // File doesn't exist, skip
		}
		return err
	}
	
	// Simple string replacement for file() function calls
	newContent := strings.ReplaceAll(string(content), 
		fmt.Sprintf(`file("%s")`, oldRef),
		fmt.Sprintf(`file("%s")`, newRef))
	
	if string(content) != newContent {
		return os.WriteFile(tfFile, []byte(newContent), 0644)
	}
	
	return nil
}

// consolidateModules consolidates all modules from environments into a single modules directory
func consolidateModules(projectDir string, environments []EnvironmentExportStatus) error {
	// Create consolidated modules directory
	consolidatedModulesDir := filepath.Join(projectDir, "modules")
	if err := os.MkdirAll(consolidatedModulesDir, 0755); err != nil {
		return fmt.Errorf("failed to create consolidated modules directory: %w", err)
	}
	
	moduleRegistry := make(map[string]bool) // Track which modules we've already copied
	conflictCount := 0
	
	for _, env := range environments {
		// Skip failed environments
		if env.Status != "complete" {
			continue
		}
		
		// Look for modules in the extracted location
		modulesDir := filepath.Join(projectDir, env.EnvironmentName, "modules")
		
		// Check if modules directory exists
		if _, err := os.Stat(modulesDir); os.IsNotExist(err) {
			fmt.Printf("  ℹ️  No modules directory found for %s at %s\n", env.EnvironmentName, modulesDir)
			continue
		}
		
		// Walk through all modules
		err := filepath.Walk(modulesDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			
			// Get relative path from modules dir
			relPath, err := filepath.Rel(modulesDir, path)
			if err != nil {
				return err
			}
			
			// Skip the root directory itself
			if relPath == "." {
				return nil
			}
			
			destPath := filepath.Join(consolidatedModulesDir, relPath)
			
			if info.IsDir() {
				// Create directory if it doesn't exist
				if err := os.MkdirAll(destPath, 0755); err != nil {
					return fmt.Errorf("failed to create directory %s: %w", destPath, err)
				}
			} else {
				// Check if file already exists in consolidated directory
				if _, exists := moduleRegistry[relPath]; exists {
					// File already exists, check if they're different
					if !areFilesIdentical(path, destPath) {
						conflictCount++
						fmt.Printf("  ⚠️  Module conflict detected: %s (keeping first version)\n", relPath)
					}
				} else {
					// Copy file to consolidated directory
					if err := copyFile(path, destPath); err != nil {
						return fmt.Errorf("failed to copy module file %s: %w", relPath, err)
					}
					moduleRegistry[relPath] = true
				}
			}
			
			return nil
		})
		
		if err != nil {
			fmt.Printf("  ⚠️  Error processing modules for %s: %v\n", env.EnvironmentName, err)
		}
		
		// Remove the individual modules directory after consolidation
		if err := os.RemoveAll(modulesDir); err != nil {
			fmt.Printf("  ⚠️  Failed to remove modules directory for %s: %v\n", env.EnvironmentName, err)
		}
	}
	
	fmt.Printf("  ✅ Consolidated %d unique module files\n", len(moduleRegistry))
	if conflictCount > 0 {
		fmt.Printf("  ⚠️  Found %d module conflicts (kept first version of each)\n", conflictCount)
	}
	
	// Update all deployment context references in modules to ./deploymentcontext.json
	err := filepath.Walk(consolidatedModulesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		
		// Only process .tf files
		if !info.IsDir() && (strings.HasSuffix(path, ".tf") || strings.HasSuffix(path, ".tf.json")) {
			// Update various possible deployment context paths to ./deploymentcontext.json
			if err := updateDeploymentContextRef(path, "../deploymentcontext.json", "./deploymentcontext.json"); err == nil {
				// Successfully updated
			}
			if err := updateDeploymentContextRef(path, "../../deploymentcontext.json", "./deploymentcontext.json"); err == nil {
				// Successfully updated
			}
			if err := updateDeploymentContextRef(path, "../../../deploymentcontext.json", "./deploymentcontext.json"); err == nil {
				// Successfully updated
			}
		}
		return nil
	})
	
	if err != nil {
		fmt.Printf("  ⚠️  Error updating deployment context references in modules: %v\n", err)
	}
	
	return nil
}

// updateModuleReferences updates module source paths to point to consolidated modules directory
func updateModuleReferences(projectDir string, environments []EnvironmentExportStatus) error {
	for _, env := range environments {
		// Skip failed environments
		if env.Status != "complete" {
			continue
		}
		
		// Update level2/main.tf (now at env_dir/level2 since tfexport is removed)
		level2MainTf := filepath.Join(projectDir, env.EnvironmentName, "level2", "main.tf")
		
		if err := updateModuleSourcePaths(level2MainTf); err != nil {
			fmt.Printf("  ⚠️  Failed to update module references for %s: %v\n", env.EnvironmentName, err)
		}
	}
	
	return nil
}

// updateModuleSourcePaths updates module source paths in a terraform file
func updateModuleSourcePaths(tfFile string) error {
	content, err := os.ReadFile(tfFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // File doesn't exist, skip
		}
		return err
	}
	
	// Simple string replacement for module source paths
	// Since we removed tfexport, level2 is now one level higher
	// Update from "../../modules/" to "../../modules/" (no change needed)
	newContent := strings.ReplaceAll(string(content), 
		`"../../modules/`,
		`"../../modules/`)
	
	if string(content) != newContent {
		return os.WriteFile(tfFile, []byte(newContent), 0644)
	}
	
	return nil
}

// copyFile copies a file from src to dst
func copyFile(src, dst string) error {
	// Create destination directory if needed
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()
	
	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()
	
	_, err = io.Copy(destFile, sourceFile)
	return err
}

// areFilesIdentical checks if two files have the same content
func areFilesIdentical(file1, file2 string) bool {
	content1, err1 := os.ReadFile(file1)
	content2, err2 := os.ReadFile(file2)
	
	if err1 != nil || err2 != nil {
		return false
	}
	
	return string(content1) == string(content2)
}

// initializeTerraformState pushes the downloaded state file and cleans it up
func initializeTerraformState(projectDir string, environments []EnvironmentExportStatus) error {
	ctx := context.Background()
	
	for _, env := range environments {
		envDir := filepath.Join(projectDir, env.EnvironmentName)
		stateFile := filepath.Join(envDir, "downloaded-terraform.tfstate")
		
		// Check if state file exists
		if _, err := os.Stat(stateFile); os.IsNotExist(err) {
			fmt.Printf("  ℹ️  No state file found for %s, skipping state initialization\n", env.EnvironmentName)
			continue
		}
		
		fmt.Printf("  🔄 Initializing Terraform state for %s...\n", env.EnvironmentName)
		
		// Create terraform executor for environment directory (where main.tf is)
		tf, err := tfexec.NewTerraform(envDir, "terraform")
		if err != nil {
			fmt.Printf("  ⚠️  Failed to initialize Terraform for %s: %v\n", env.EnvironmentName, err)
			continue
		}
		
		// Run terraform init with backend=false
		if err := tf.Init(ctx, tfexec.Backend(false)); err != nil {
			fmt.Printf("  ⚠️  Failed to run terraform init for %s: %v\n", env.EnvironmentName, err)
			continue
		}
		
		// Get absolute path for state file to avoid path resolution issues
		absStateFile, err := filepath.Abs(stateFile)
		if err != nil {
			fmt.Printf("  ⚠️  Failed to get absolute path for state file %s: %v\n", env.EnvironmentName, err)
			continue
		}
		
		// Push the state file using StatePush with absolute path
		if err := tf.StatePush(ctx, absStateFile); err != nil {
			fmt.Printf("  ⚠️  Failed to push terraform state for %s: %v\n", env.EnvironmentName, err)
			continue
		}
		
		// Remove the downloaded state file after successful push
		if err := os.Remove(stateFile); err != nil {
			fmt.Printf("  ⚠️  Failed to remove state file for %s: %v\n", env.EnvironmentName, err)
		} else {
			fmt.Printf("  ✅ Successfully initialized Terraform state for %s\n", env.EnvironmentName)
		}
	}
	
	return nil
}

func init() {
	rootCmd.AddCommand(exportAllCmd)
	exportAllCmd.Flags().String("project", "", "The project (stack) name to export (required)")
	exportAllCmd.Flags().String("output-dir", "", "Output directory for exports (default: current directory)")
	exportAllCmd.Flags().Bool("include-providers", false, "Include Terraform providers in exports")
	exportAllCmd.Flags().Bool("skip-failed", false, "Continue exporting even if some environments fail")
}