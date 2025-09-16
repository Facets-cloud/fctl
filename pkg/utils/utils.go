package utils

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/go-ini/ini"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/hashicorp/terraform-config-inspect/tfconfig"
	"github.com/hashicorp/terraform-exec/tfexec"
	tfjson "github.com/hashicorp/terraform-json"
	"github.com/zclconf/go-cty/cty"
	"golang.org/x/term"
)

// ExtractEnvIDFromDeploymentContext reads deploymentcontext.json in dir and returns .cluster.id
func ExtractEnvIDFromDeploymentContext(dir string) (string, error) {
	ctxPath := filepath.Join(dir, "deploymentcontext.json")
	f, err := os.Open(ctxPath)
	if err != nil {
		return "", fmt.Errorf("could not open deploymentcontext.json: %w", err)
	}
	defer f.Close()
	var ctx struct {
		Cluster struct {
			ID string `json:"id"`
		} `json:"cluster"`
	}
	if err := json.NewDecoder(f).Decode(&ctx); err != nil {
		return "", fmt.Errorf("could not decode deploymentcontext.json: %w", err)
	}
	if ctx.Cluster.ID == "" {
		return "", fmt.Errorf("cluster.id missing in deploymentcontext.json")
	}
	return ctx.Cluster.ID, nil
}

// ExtractDeploymentID extracts the deployment ID from a zip filename of the form uuid.zip
func ExtractDeploymentID(zipPath string) (string, error) {
	base := filepath.Base(zipPath)
	// UUIDs are usually 24-36 chars, with or without dashes
	re := regexp.MustCompile(`^([a-fA-F0-9-]{24,36})\.zip$`)
	matches := re.FindStringSubmatch(base)
	if len(matches) != 2 {
		return "", fmt.Errorf("invalid zip filename format, expected uuid.zip, got: %s", base)
	}
	return matches[1], nil
}

// ExtractZip extracts a zip file to the destination directory
func ExtractZip(zipPath, destPath string) error {
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

// ZipDir zips the contents of srcDir into zipPath
func ZipDir(source, target string) error {
	zipfile, err := os.Create(target)
	if err != nil {
		return err
	}
	defer zipfile.Close()

	archive := zip.NewWriter(zipfile)
	defer archive.Close()

	err = filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relPath == "." {
			return nil
		}
		if info.IsDir() {
			// Only add directory entry if empty
			files, err := os.ReadDir(path)
			if err != nil {
				return err
			}
			if len(files) == 0 {
				hdr := &zip.FileHeader{
					Name:     relPath + "/",
					Method:   zip.Deflate,
					Modified: info.ModTime(),
				}
				_, err := archive.CreateHeader(hdr)
				return err
			}
			return nil // skip non-empty directories
		}
		// Only add regular files
		if !info.Mode().IsRegular() {
			// skip non-regular files (symlinks, devices, etc.)
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}

		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			file.Close()
			return err
		}
		hdr.Name = relPath
		hdr.Method = zip.Deflate

		writer, err := archive.CreateHeader(hdr)
		if err != nil {
			file.Close()
			return err
		}
		_, err = io.Copy(writer, file)
		file.Close()
		return err
	})
	return err
}

// ListExistingDeployments lists existing deployments in envDir except the current one
func ListExistingDeployments(envDir, currentDeploymentID string) ([]string, error) {
	entries, err := os.ReadDir(envDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var deployments []string
	var deploymentInfos []struct {
		name  string
		ctime int64 // Unix timestamp
	}
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != currentDeploymentID {
			info, err := os.Stat(filepath.Join(envDir, entry.Name()))
			if err != nil {
				continue
			}
			deploymentInfos = append(deploymentInfos, struct {
				name  string
				ctime int64
			}{entry.Name(), info.ModTime().Unix()})
		}
	}
	sort.Slice(deploymentInfos, func(i, j int) bool {
		return deploymentInfos[i].ctime < deploymentInfos[j].ctime
	})
	for _, di := range deploymentInfos {
		deployments = append(deployments, di.name)
	}
	return deployments, nil
}

// PromptUser prompts the user to select a deployment or use tf.tfstate if available
func PromptUser(existingDeployments []string, tfStatePath string) (bool, string, error) {
	fmt.Println("\n⚠️  Found existing deployments for this environment:")
	for i, deploymentID := range existingDeployments {
		fmt.Printf("%d. %s\n", i+1, deploymentID)
	}
	promptMsg := "\n❓ Do you want to proceed with an existing state file? If yes enter 'y', else enter 'n' if you want to start fresh with a new state file, or just press enter to use the tf.tfstate file in the current environment (saved after each release): "
	fmt.Print(promptMsg)
	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false, "", err
	}
	response = strings.ToLower(strings.TrimSpace(response))
	if response == "" && tfStatePath != "" {
		if _, err := os.Stat(tfStatePath); err == nil {
			return true, "__USE_TF_TFSTATE__", nil
		}
	}
	if response != "y" && response != "yes" {
		return false, "", nil
	}
	fmt.Print("\n📂 Enter the number of the deployment to use (1-" + fmt.Sprint(len(existingDeployments)) + "): ")
	numStr, err := reader.ReadString('\n')
	if err != nil {
		return false, "", err
	}
	numStr = strings.TrimSpace(numStr)
	num := 0
	_, err = fmt.Sscanf(numStr, "%d", &num)
	if err != nil || num < 1 || num > len(existingDeployments) {
		return false, "", fmt.Errorf("invalid selection: please enter a number between 1 and %d", len(existingDeployments))
	}
	return true, existingDeployments[num-1], nil
}

// CopyFile copies a file from src to dst
func CopyFile(src, dst string) error {
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

// CopyStateFromPreviousDeployment copies the state file from a previous deployment
func CopyStateFromPreviousDeployment(envDir, currentDeploymentID, envID, selectedDeployment string) error {
	if selectedDeployment == "" {
		return fmt.Errorf("no deployment selected")
	}
	prevDeployDir := filepath.Join(envDir, selectedDeployment)
	prevStateDir := filepath.Join(prevDeployDir, "tfexport", "terraform.tfstate.d", envID)
	prevStatePath := filepath.Join(prevStateDir, "terraform.tfstate")
	if _, err := os.Stat(prevStatePath); err != nil {
		return fmt.Errorf("no state file found in deployment %s", selectedDeployment)
	}
	fmt.Printf("📝 Found state file in deployment %s\n", selectedDeployment)
	newStateDir := filepath.Join(envDir, currentDeploymentID, "tfexport", "terraform.tfstate.d", envID)
	if err := os.MkdirAll(newStateDir, 0755); err != nil {
		return fmt.Errorf("failed to create state directory: %v", err)
	}
	newStatePath := filepath.Join(newStateDir, "terraform.tfstate")
	if err := CopyFile(prevStatePath, newStatePath); err != nil {
		return fmt.Errorf("failed to copy state file: %v", err)
	}
	fmt.Printf("✅ Successfully copied state file from deployment %s\n", selectedDeployment)
	return nil
}

// ParseStateFile parses the terraform state and returns release metadata
func ParseStateFile(state *tfjson.State) []map[string]interface{} {
	var releaseMetadataList []map[string]interface{}
	if state == nil || state.Values == nil {
		return releaseMetadataList
	}
	var walkModule func(module *tfjson.StateModule)
	walkModule = func(module *tfjson.StateModule) {
		if module == nil {
			return
		}
		for _, resource := range module.Resources {
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
		for _, child := range module.ChildModules {
			walkModule(child)
		}
	}
	walkModule(state.Values.RootModule)
	return releaseMetadataList
}

// GenerateReleaseMetadata generates and saves release metadata from terraform state
func GenerateReleaseMetadata(tf *tfexec.Terraform, deployDir string) error {
	tf.SetStdout(io.Discard)
	tf.SetStderr(io.Discard)
	state, err := tf.Show(context.Background())
	tf.SetStdout(os.Stdout)
	tf.SetStderr(os.Stdout)
	if err != nil {
		return fmt.Errorf("terraform show failed: %w", err)
	}
	releaseMetadataList := ParseStateFile(state)
	if len(releaseMetadataList) == 0 {
		fmt.Println("ℹ️ No release metadata found in state")
		return nil
	}
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

// GetProfileName determines the active profile, falling back to "default"
func GetProfileName(profileFlag string) string {
	if profileFlag != "" {
		return profileFlag
	}
	return "default"
}

// UpdateProfileCredentials updates the credentials for a profile
func UpdateProfileCredentials(profile, host, username, token string) {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("❌ Failed to get home directory: %v\n", err)
		return
	}
	credsPath := home + "/.facets/credentials"
	if err := os.MkdirAll(filepath.Dir(credsPath), 0700); err != nil {
		fmt.Printf("❌ Failed to create credentials directory: %v\n", err)
		return
	}
	creds, err := ini.Load(credsPath)
	if err != nil {
		creds = ini.Empty()
	}
	creds.Section(profile).Key("control_plane_url").SetValue(host)
	creds.Section(profile).Key("username").SetValue(username)
	creds.Section(profile).Key("token").SetValue(token)
	if err := creds.SaveTo(credsPath); err != nil {
		fmt.Printf("❌ Failed to save credentials: %v\n", err)
	}
	configPath := home + "/.facets/config"
	configIni := ini.Empty()
	if _, err := os.Stat(configPath); err == nil {
		loadedIni, err := ini.Load(configPath)
		if err == nil {
			configIni = loadedIni
		}
	}
	configIni.Section("default").Key("profile").SetValue(profile)
	if err := configIni.SaveTo(configPath); err != nil {
		fmt.Printf("❌ Failed to save config file: %v\n", err)
	}
}

// UpdateProfileExpiry updates the token expiry for a profile
func UpdateProfileExpiry(profile string) {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("⚠️ Warning: Failed to get home directory to update expiry: %v\n", err)
		return
	}
	credsPath := home + "/.facets/credentials"
	creds, err := ini.Load(credsPath)
	if err != nil {
		fmt.Printf("⚠️ Warning: Could not load credentials to update expiry: %v\n", err)
		return
	}
	expiry := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	creds.Section(profile).Key("token_expiry").SetValue(expiry)
	if err := creds.SaveTo(credsPath); err != nil {
		fmt.Printf("⚠️ Warning: Failed to save updated token expiry: %v\n", err)
	}
}

// updatePreventDestroyInTFs recursively updates all .tf files in dir to set prevent_destroy = false in all resource blocks
func UpdatePreventDestroyInTFs(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		fmt.Printf("[DEBUG] Visiting directory: %s\n", path)
		// Check if this directory contains any .tf files
		hasTF := false
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !entry.IsDir() && filepath.Ext(entry.Name()) == ".tf" {
				hasTF = true
				break
			}
		}
		if hasTF {
			fmt.Printf("[DEBUG] Updating module in: %s\n", path)
			err := UpdatePreventDestroyInSingleModule(path)
			if err != nil {
				fmt.Printf("[DEBUG] Error updating module in %s: %v\n", path, err)
			}
			return err
		}
		return nil
	})
}

// updatePreventDestroyInSingleModule only updates .tf files in a single directory (non-recursive)
func UpdatePreventDestroyInSingleModule(dir string) error {
	module, diags := tfconfig.LoadModule(dir)
	if diags.HasErrors() {
		fmt.Printf("[DEBUG] tfconfig.LoadModule errors in %s: %v\n", dir, diags)
		return diags
	}
	fileToResources := make(map[string][]*tfconfig.Resource)
	for _, res := range module.ManagedResources {
		fileToResources[res.Pos.Filename] = append(fileToResources[res.Pos.Filename], res)
	}
	for file, resources := range fileToResources {
		absFile := filepath.Join(dir, filepath.Base(file))
		if _, err := os.Stat(absFile); err != nil {
			fmt.Printf("[DEBUG] Skipping missing file: %s\n", absFile)
			continue
		}
		src, err := os.ReadFile(absFile)
		if err != nil {
			fmt.Printf("[DEBUG] Could not open file: %s\n", absFile)
			return err
		}
		f, _ := hclwrite.ParseConfig(src, absFile, hcl.Pos{Line: 1, Column: 1})
		if f == nil {
			fmt.Printf("[DEBUG] Could not parse file: %s\n", absFile)
			continue
		}
		changed := false
		for _, block := range f.Body().Blocks() {
			if block.Type() != "resource" || len(block.Labels()) != 2 {
				continue
			}
			found := false
			for _, res := range resources {
				if block.Labels()[0] == res.Type && block.Labels()[1] == res.Name {
					found = true
					break
				}
			}
			if !found {
				continue
			}
			lifecycle := FindOrCreateBlock(block.Body(), "lifecycle")
			if lifecycle == nil || lifecycle.Body() == nil {
				fmt.Printf("[DEBUG] Could not get or create lifecycle block in: %s\n", absFile)
				continue
			}
			lifecycle.Body().SetAttributeValue("prevent_destroy", cty.BoolVal(false))
			changed = true
		}
		if changed {
			if err := os.WriteFile(absFile, f.Bytes(), 0644); err != nil {
				return err
			}
		}
	}
	return nil
}

// findOrCreateBlock finds a block by type in the given body, or creates it if not found
func FindOrCreateBlock(body *hclwrite.Body, blockType string) *hclwrite.Block {
	for _, block := range body.Blocks() {
		if block.Type() == blockType {
			return block
		}
	}
	// Not found, create
	return body.AppendNewBlock(blockType, nil)
}

// CopyDir recursively copies a directory from src to dst
func CopyDir(src string, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(dst, relPath)
		if info.IsDir() {
			return os.MkdirAll(targetPath, info.Mode())
		} else {
			return CopyFile(path, targetPath)
		}
	})
}

// IsZipDifferentFromDir compares the contents of a zip file and a directory.
// Returns true if any file in the zip is missing or different (by size or hash) in the directory,
// or if any file in the directory is missing from the zip.
func IsZipDifferentFromDir(zipPath, dirPath string) (bool, error) {
	zipReader, err := zip.OpenReader(zipPath)
	if err != nil {
		return true, err
	}
	defer zipReader.Close()

	zipFiles := make(map[string]*zip.File)
	for _, f := range zipReader.File {
		if f.FileInfo().IsDir() {
			continue
		}
		zipFiles[f.Name] = f
	}

	dirFiles := make(map[string]string) // map[path] = hash
	err = filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dirPath, path)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		// Only compare files that are in the zip (ignore extra files in dir)
		if _, ok := zipFiles[rel]; ok {
			hash, err := hashFile(path)
			if err != nil {
				return err
			}
			dirFiles[rel] = hash
		}
		return nil
	})
	if err != nil {
		return true, err
	}

	// Compare zip files to dir files
	for name, zf := range zipFiles {
		zfh, err := hashZipFile(zf)
		if err != nil {
			return true, err
		}
		dh, ok := dirFiles[name]
		if !ok {
			// File missing in dir
			return true, nil
		}
		if zfh != dh {
			// File content differs
			return true, nil
		}
	}
	// Optionally: check for extra files in dir not in zip (not required for your use case)
	return false, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sha := sha256.New()
	if _, err := io.Copy(sha, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha.Sum(nil)), nil
}

func hashZipFile(zf *zip.File) (string, error) {
	f, err := zf.Open()
	if err != nil {
		return "", err
	}
	defer f.Close()
	sha := sha256.New()
	if _, err := io.Copy(sha, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha.Sum(nil)), nil
}

// FixPermissions recursively sets permissions: 755 for directories, 644 for files, 755 for provider binaries
func FixPermissions(root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return os.Chmod(path, 0755)
		}
		mode := os.FileMode(0644)
		// Make provider binaries executable (common pattern)
		if strings.Contains(path, "terraform-provider-") || strings.HasSuffix(path, ".provider") {
			mode = 0755
		}
		return os.Chmod(path, mode)
	})
}

// ReadMaskedInput reads input from the terminal without echoing characters (for passwords/tokens)
func ReadMaskedInput(prompt string) (string, error) {
	fmt.Print(prompt)

	// Check if we're on a terminal
	if !term.IsTerminal(int(syscall.Stdin)) {
		// Fallback to regular input if not on terminal
		reader := bufio.NewReader(os.Stdin)
		input, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(input), nil
	}

	// Use terminal for masked input
	bytePassword, err := term.ReadPassword(int(syscall.Stdin))
	if err != nil {
		return "", err
	}

	fmt.Println() // Add newline after masked input
	return strings.TrimSpace(string(bytePassword)), nil
}

// FormatDuration formats a time.Duration in a human-readable format
// Examples: "1m30s", "45s", "2h15m"
func FormatDuration(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}

	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60

	var parts []string

	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if minutes > 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	if seconds > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%ds", seconds))
	}

	return strings.Join(parts, "")
}

// cleanupTerraformFiles removes unused code and references from .tf files using HCL parsing
func cleanupTerraformFiles(dir string) error {
	// Walk through all subdirectories looking for .tf files
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		
		// Only process .tf files (not .tfstate or .tf.json)
		if !strings.HasSuffix(path, ".tf") || strings.HasSuffix(path, ".tf.json") {
			return nil
		}
		
		filename := filepath.Base(path)
		
		// Read the file
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		
		// Parse the HCL file
		file, diags := hclwrite.ParseConfig(content, path, hcl.Pos{Line: 1, Column: 1})
		if diags.HasErrors() {
			// If HCL parsing fails, skip this file
			fmt.Printf("  ⚠️  Could not parse %s as HCL, skipping: %v\n", path, diags)
			return nil
		}
		
		modified := false
		rootBody := file.Body()
		
		// Handle specific files
		switch filename {
		case "main.tf":
			isLevel2 := strings.Contains(path, "level2")
			
			if isLevel2 {
				// In level2/main.tf, clean up module blocks that reference removed variables
				for _, block := range rootBody.Blocks() {
					if block.Type() == "module" {
						blockBody := block.Body()
						moduleName := ""
						if len(block.Labels()) > 0 {
							moduleName = block.Labels()[0]
						}
						
						// Skip cleaning blueprint_self module - it's required
						if moduleName == "blueprint_self" {
							continue
						}
						
						// For all modules in level2/main.tf (except blueprint_self)
						// Keep cluster attribute but we may need to update how it's referenced
						// since var.cluster won't exist anymore
						
						// Remove unnecessary attributes (these match the removed variables)
						attributesToClean := []string{
							"instance_type", "iac_version", 
							"release_metadata", "generate_release_metadata",
							"baseinfra", "settings",
						}
						// Note: keeping "instance", "advanced", "inputs", "environment" as they may be needed
						
						for _, attrName := range attributesToClean {
							if blockBody.GetAttribute(attrName) != nil {
								blockBody.RemoveAttribute(attrName)
								modified = true
							}
						}
						
						// Clean up empty providers and inputs blocks
						if attr := blockBody.GetAttribute("providers"); attr != nil {
							tokens := attr.Expr().BuildTokens(nil)
							if len(tokens) <= 3 {
								blockBody.RemoveAttribute("providers")
								modified = true
							}
						}
						
						if attr := blockBody.GetAttribute("inputs"); attr != nil {
							// Check if inputs only contains deployment_id
							tokens := attr.Expr().BuildTokens(nil)
							removeInputs := false
							for _, token := range tokens {
								if string(token.Bytes) == "deployment_id" {
									removeInputs = true
									break
								}
							}
							if removeInputs || len(tokens) <= 3 {
								blockBody.RemoveAttribute("inputs")
								modified = true
							}
						}
						
						fmt.Printf("    - Cleaning module: %s\n", moduleName)
					}
				}
				
				// DO NOT touch module blueprint_self - it's required
				
			} else {
				// Root main.tf cleanup
				// Clean up module "level2" block
				for _, block := range rootBody.Blocks() {
					if block.Type() == "module" && len(block.Labels()) > 0 && block.Labels()[0] == "level2" {
						// Remove cc_metadata attribute
						if block.Body().GetAttribute("cc_metadata") != nil {
							block.Body().RemoveAttribute("cc_metadata")
							modified = true
						}
						// Remove deployment_id if it references var.deployment_id
						if attr := block.Body().GetAttribute("deployment_id"); attr != nil {
							block.Body().RemoveAttribute("deployment_id")
							modified = true
						}
						// Remove empty providers block
						if attr := block.Body().GetAttribute("providers"); attr != nil {
							tokens := attr.Expr().BuildTokens(nil)
							if len(tokens) <= 3 {
								block.Body().RemoveAttribute("providers")
								modified = true
							}
						}
						// Remove state attribute if it's empty
						if attr := block.Body().GetAttribute("state"); attr != nil {
							tokens := attr.Expr().BuildTokens(nil)
							if len(tokens) <= 3 {
								block.Body().RemoveAttribute("state")
								modified = true
							}
						}
					}
				}
				
				// Remove variable blocks for deployment_id, dev_mode, releaseType
				blocksToRemove := []string{}
				for _, block := range rootBody.Blocks() {
					if block.Type() == "variable" && len(block.Labels()) > 0 {
						varName := block.Labels()[0]
						if varName == "deployment_id" || varName == "dev_mode" || varName == "releaseType" {
							blocksToRemove = append(blocksToRemove, varName)
						}
					}
				}
				for _, varName := range blocksToRemove {
					for _, block := range rootBody.Blocks() {
						if block.Type() == "variable" && len(block.Labels()) > 0 && block.Labels()[0] == varName {
							rootBody.RemoveBlock(block)
							modified = true
							break
						}
					}
				}
			}
			
		case "variables.tf":
			// Remove Facets-specific variables
			// Check directory context
			isLevel2 := strings.Contains(path, "level2")
			isModule := strings.Contains(path, "/modules/")
			
			blocksToRemove := []string{}
			for _, block := range rootBody.Blocks() {
				if block.Type() == "variable" && len(block.Labels()) > 0 {
					varName := block.Labels()[0]
					shouldRemove := false
					
					// Variables to remove from module directories (modules/*/variables.tf)
					if isModule {
						// Remove Facets-specific variables from modules
						if varName == "release_metadata" ||
						   varName == "instance_type" ||
						   varName == "iac_version" ||
						   varName == "generate_release_metadata" ||
						   varName == "settings" ||
						   varName == "baseinfra" ||
						   varName == "cc_metadata" {
							shouldRemove = true
						}
						// Keep: cluster, instance, advanced, inputs, environment, instance_name
					} else if isLevel2 {
						// Additional variables to remove in level2/variables.tf
						if varName == "infra_output" || 
						   varName == "settings" || 
						   varName == "state" ||
						   varName == "cc_metadata" ||
						   varName == "deployment_id" {
							shouldRemove = true
						}
					} else {
						// Common variables to remove from tfexport/variables.tf
						if strings.HasPrefix(varName, "cc_") || 
						   varName == "deployment_id" || 
						   varName == "dev_mode" || 
						   varName == "releaseType" ||
						   varName == "CUSTOMER_ARTIFACT_BUCKET" ||
						   varName == "USE_MINIO" {
							shouldRemove = true
						}
					}
					
					if shouldRemove {
						blocksToRemove = append(blocksToRemove, varName)
					}
				}
			}
			for _, varName := range blocksToRemove {
				for _, block := range rootBody.Blocks() {
					if block.Type() == "variable" && len(block.Labels()) > 0 && block.Labels()[0] == varName {
						rootBody.RemoveBlock(block)
						modified = true
						break
					}
				}
			}
			
		case "cc_metadata.tf":
			// Remove all content
			rootBody.Clear()
			modified = true
			
		case "outputs.tf":
			// Remove outputs that reference cc_metadata or deployment_id
			outputsToRemove := []string{}
			for _, block := range rootBody.Blocks() {
				if block.Type() == "output" && len(block.Labels()) > 0 {
					outputName := block.Labels()[0]
					valueAttr := block.Body().GetAttribute("value")
					if valueAttr != nil {
						// Check if value contains references to removed variables
						tokens := valueAttr.Expr().BuildTokens(nil)
						for _, token := range tokens {
							tokenStr := string(token.Bytes)
							// List of removed variables to check for
							removedVars := []string{"cc_metadata", "deployment_id", "release_metadata", 
								"generate_release_metadata", "baseinfra", "settings", "infra_output", "state"}
							
							for _, removedVar := range removedVars {
								if strings.Contains(tokenStr, removedVar) {
									outputsToRemove = append(outputsToRemove, outputName)
									fmt.Printf("    - Output '%s' references %s, will remove\n", outputName, removedVar)
									break
								}
							}
						}
					}
				}
			}
			// Remove outputs that reference cc_metadata
			for _, outputName := range outputsToRemove {
				for _, block := range rootBody.Blocks() {
					if block.Type() == "output" && len(block.Labels()) > 0 && block.Labels()[0] == outputName {
						rootBody.RemoveBlock(block)
						modified = true
						fmt.Printf("    - Removed output: %s\n", outputName)
						break
					}
				}
			}
		}
		
		// General cleanup for all .tf files
		// Remove cc_metadata attributes from any block
		for _, block := range rootBody.Blocks() {
			blockBody := block.Body()
			
			// Remove cc_metadata attribute
			if blockBody.GetAttribute("cc_metadata") != nil {
				blockBody.RemoveAttribute("cc_metadata")
				modified = true
			}
			
			// Remove other Facets-specific attributes from module blocks
			if block.Type() == "module" {
				// Remove these attributes if they exist
				attributesToRemove := []string{
					"settings", "state", "infra_output", "deployment_id",
					"release_metadata", "instance_type",
					"iac_version", "generate_release_metadata", "baseinfra", "cc_metadata",
				}
				for _, attrName := range attributesToRemove {
					if blockBody.GetAttribute(attrName) != nil {
						blockBody.RemoveAttribute(attrName)
						modified = true
						if len(block.Labels()) > 0 {
							fmt.Printf("    - Removing attribute '%s' from module '%s'\n", attrName, block.Labels()[0])
						}
					}
				}
			}
		}
		
		// Write back if modified
		if modified {
			newContent := file.Bytes()
			
			// Check if the file is now effectively empty (only whitespace/comments)
			isEmpty := true
			// Check if there are any blocks or attributes left
			if len(rootBody.Blocks()) > 0 {
				isEmpty = false
			}
			for _, attr := range rootBody.Attributes() {
				if attr != nil {
					isEmpty = false
					break
				}
			}
			
			// If file is empty, delete it instead of writing empty content
			if isEmpty {
				fmt.Printf("  🗑️  Deleting empty file: %s\n", path)
				if err := os.Remove(path); err != nil {
					return fmt.Errorf("failed to delete empty file %s: %w", path, err)
				}
			} else {
				// Write the modified content
				if err := os.WriteFile(path, newContent, 0644); err != nil {
					return fmt.Errorf("failed to write cleaned file %s: %w", path, err)
				}
			}
		}
		
		return nil
	})
}

// CleanExportedFiles removes unwanted files and cleans JSON files in the exported directory
func CleanExportedFiles(rootDir string) error {
	// 1. Remove all facets.yaml and resources_gen.tf files from modules/ directory recursively
	modulesDir := filepath.Join(rootDir, "modules")
	if _, err := os.Stat(modulesDir); err == nil {
		err := filepath.Walk(modulesDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() {
				filename := filepath.Base(path)
				if filename == "facets.yaml" || filename == "resources_gen.tf" {
					fmt.Printf("🗑️  Removing: %s\n", path)
					if err := os.Remove(path); err != nil {
						return fmt.Errorf("failed to remove %s: %w", path, err)
					}
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("error cleaning modules directory: %w", err)
		}
	}

	// 2. Remove terraform.d directory from tfexport
	terraformDDir := filepath.Join(rootDir, "tfexport", "terraform.d")
	if _, err := os.Stat(terraformDDir); err == nil {
		fmt.Printf("🗑️  Removing directory: %s\n", terraformDDir)
		if err := os.RemoveAll(terraformDDir); err != nil {
			return fmt.Errorf("failed to remove terraform.d directory: %w", err)
		}
	}

	// 3. Clean up terraform files in tfexport and modules directories
	// Clean tfexport directory
	tfexportDir := filepath.Join(rootDir, "tfexport")
	if _, err := os.Stat(tfexportDir); err == nil {
		if err := cleanupTerraformFiles(tfexportDir); err != nil {
			fmt.Printf("  ⚠️  Error cleaning tfexport directory: %v\n", err)
		}
	}
	
	// Clean modules directory
	modulesPath := filepath.Join(rootDir, "modules")
	if _, err := os.Stat(modulesPath); err == nil {
		if err := cleanupTerraformFiles(modulesPath); err != nil {
			fmt.Printf("  ⚠️  Error cleaning modules directory: %v\n", err)
		}
	}

	// 4. Clean scratch_string resources from downloaded-terraform.tfstate
	tfstatePath := filepath.Join(rootDir, "tfexport", "downloaded-terraform.tfstate")
	if _, err := os.Stat(tfstatePath); err == nil {
		
		// Read the tfstate file
		data, err := os.ReadFile(tfstatePath)
		if err != nil {
			return fmt.Errorf("failed to read tfstate file: %w", err)
		}
		
		// Parse as raw JSON to handle any format
		var rawState map[string]interface{}
		if err := json.Unmarshal(data, &rawState); err != nil {
			return fmt.Errorf("failed to parse tfstate as JSON: %w", err)
		}
		
		modified := false
		removedCount := 0
		
		// Add version if missing
		if _, hasVersion := rawState["version"]; !hasVersion {
			fmt.Printf("  ⚠️  State file missing version, adding version 4\n")
			rawState["version"] = 4
			if _, hasTfVersion := rawState["terraform_version"]; !hasTfVersion {
				rawState["terraform_version"] = "1.5.7"
			}
			modified = true
		}
		
		// Process resources array directly (the format from your state list output)
		if resources, ok := rawState["resources"].([]interface{}); ok {
			var filteredResources []interface{}
			
			// First pass: identify and remove scratch_string resources
			for _, res := range resources {
				if resMap, ok := res.(map[string]interface{}); ok {
					resType, _ := resMap["type"].(string)
					resName, _ := resMap["name"].(string) 
					resModule, _ := resMap["module"].(string)
					
					if resType == "scratch_string" || resType == "scratch_number" {
						if resModule != "" {
							fmt.Printf("  - Removing %s resource from %s: %s\n", resType, resModule, resName)
						} else {
							fmt.Printf("  - Removing %s resource: %s\n", resType, resName)
						}
						removedCount++
						modified = true
						continue
					}
					
					// For remaining resources, clean up dependencies
					if instances, ok := resMap["instances"].([]interface{}); ok {
						for _, inst := range instances {
							if instMap, ok := inst.(map[string]interface{}); ok {
								if deps, ok := instMap["dependencies"].([]interface{}); ok {
									var cleanedDeps []interface{}
									for _, dep := range deps {
										depStr, _ := dep.(string)
										// Check if this dependency is a scratch_string resource
										isScratch := false
										if strings.Contains(depStr, "scratch_string") || strings.Contains(depStr, "scratch_number") {
											isScratch = true
										}
										if !isScratch {
											cleanedDeps = append(cleanedDeps, dep)
										}
									}
									if len(cleanedDeps) != len(deps) {
										instMap["dependencies"] = cleanedDeps
										modified = true
									}
								}
							}
						}
					}
					
					filteredResources = append(filteredResources, res)
				}
			}
			
			rawState["resources"] = filteredResources
			
			if removedCount > 0 {
				fmt.Printf("  ✓ Removed %d scratch_string/scratch_number resource(s)\n", removedCount)
			} else {
				fmt.Printf("  ✓ No scratch_string resources found\n")
			}
		} else {
			fmt.Printf("  ⚠️  State file doesn't have resources array in expected format\n")
		}
		
		// Write back if modified
		if modified {
			updatedData, err := json.MarshalIndent(rawState, "", "  ")
			if err != nil {
				return fmt.Errorf("failed to marshal cleaned state: %w", err)
			}
			if err := os.WriteFile(tfstatePath, updatedData, 0644); err != nil {
				return fmt.Errorf("failed to write cleaned state: %w", err)
			}
		}
	}

	// 5. Process input_*.tf.json files in tfexport/level2 to remove flavor, version, and kind
	level2Dir := filepath.Join(rootDir, "tfexport", "level2")
	if _, err := os.Stat(level2Dir); err == nil {
		entries, err := os.ReadDir(level2Dir)
		if err != nil {
			return fmt.Errorf("failed to read level2 directory: %w", err)
		}
		
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasPrefix(entry.Name(), "input_") && strings.HasSuffix(entry.Name(), ".tf.json") {
				jsonPath := filepath.Join(level2Dir, entry.Name())
				
				// Read the JSON file
				data, err := os.ReadFile(jsonPath)
				if err != nil {
					return fmt.Errorf("failed to read %s: %w", jsonPath, err)
				}
				
				// Parse JSON
				var jsonData map[string]interface{}
				if err := json.Unmarshal(data, &jsonData); err != nil {
					return fmt.Errorf("failed to parse %s: %w", jsonPath, err)
				}
				
				// Navigate through the structure: locals -> input_* -> remove fields
				modified := false
				if locals, ok := jsonData["locals"].(map[string]interface{}); ok {
					// Iterate through all keys in locals (there should be one matching input_*)
					for key, value := range locals {
						if strings.HasPrefix(key, "input_") {
							if inputData, ok := value.(map[string]interface{}); ok {
								// Remove flavor, version, and kind fields
								if _, exists := inputData["flavor"]; exists {
									delete(inputData, "flavor")
									modified = true
								}
								if _, exists := inputData["version"]; exists {
									delete(inputData, "version")
									modified = true
								}
								if _, exists := inputData["kind"]; exists {
									delete(inputData, "kind")
									modified = true
								}
							}
						}
					}
				}
				
				// Write back if modified
				if modified {
					updatedData, err := json.MarshalIndent(jsonData, "", "  ")
					if err != nil {
						return fmt.Errorf("failed to marshal %s: %w", jsonPath, err)
					}
					if err := os.WriteFile(jsonPath, updatedData, 0644); err != nil {
						return fmt.Errorf("failed to write %s: %w", jsonPath, err)
					}
				}
			}
		}
	}
	
	return nil
}
