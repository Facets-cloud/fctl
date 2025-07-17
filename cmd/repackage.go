package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Facets-cloud/fctl/pkg/utils"
	"github.com/spf13/cobra"
	"github.com/yarlson/pin"
)

var (
	repackageZipPath         string
	repackageSourcePath      string
	repackageDestinationPath string
	repackageOutputPath      string
	repackageInplace         bool
)

var repackageCmd = &cobra.Command{
	Use:   "repackage",
	Short: "Tweak the exported zip file by copying files from local into a specific path inside the zip.",
	Long:  `Copy files or directories from your local system into a specific directory structure inside an existing zip file.`,
	RunE:  runRepackage,
}

func init() {
	rootCmd.AddCommand(repackageCmd)

	repackageCmd.Flags().StringVarP(&repackageZipPath, "zip", "z", "", "Path to the zip file to modify (required)")
	repackageCmd.Flags().StringVarP(&repackageSourcePath, "source", "s", "", "Path to the local file or directory to copy from (required)")
	repackageCmd.Flags().StringVarP(&repackageDestinationPath, "destination", "d", "", "Destination path inside the zip (required)")
	repackageCmd.Flags().StringVarP(&repackageOutputPath, "output", "o", "", "Path for the output zip file (required if not using --inplace)")
	repackageCmd.Flags().BoolVar(&repackageInplace, "inplace", false, "Overwrite the original zip file (default: false)")

	repackageCmd.MarkFlagRequired("zip")
	repackageCmd.MarkFlagRequired("source")
	repackageCmd.MarkFlagRequired("destination")
}

func runRepackage(cmd *cobra.Command, args []string) error {
	s := pin.New("📦 Starting repackaging...",
		pin.WithSpinnerColor(pin.ColorCyan),
		pin.WithTextColor(pin.ColorYellow),
		pin.WithDoneSymbol('✔'),
		pin.WithDoneSymbolColor(pin.ColorGreen),
		pin.WithPrefix("pin"),
		pin.WithPrefixColor(pin.ColorMagenta),
		pin.WithSeparatorColor(pin.ColorGray),
	)
	cancel := s.Start(cmd.Context())
	defer cancel()

	if !repackageInplace && repackageOutputPath == "" {
		s.Fail("❌ --output is required unless --inplace is set")
		return fmt.Errorf("--output is required unless --inplace is set")
	}

	// 1. Unzip to temp dir
	s.UpdateMessage("🗂️  Creating temporary directory...")
	tempDir, err := os.MkdirTemp("", "fctl-repackage-*")
	if err != nil {
		s.Fail("❌ Failed to create temp dir")
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	s.UpdateMessage("📂 Extracting zip file...")
	if err := utils.ExtractZip(repackageZipPath, tempDir); err != nil {
		s.Fail("❌ Failed to extract zip")
		return fmt.Errorf("failed to extract zip: %w", err)
	}

	// 2. Copy file or directory to destination inside temp dir
	destPath := filepath.Join(tempDir, repackageDestinationPath)
	s.UpdateMessage("📄 Copying source to destination inside zip structure...")
	srcInfo, err := os.Stat(repackageSourcePath)
	if err != nil {
		s.Fail("❌ Failed to stat source")
		return fmt.Errorf("failed to stat source: %w", err)
	}
	if srcInfo.IsDir() {
		if err := utils.CopyDir(repackageSourcePath, destPath); err != nil {
			s.Fail("❌ Failed to copy directory")
			return fmt.Errorf("failed to copy directory: %w", err)
		}
	} else {
		if err := utils.CopyFile(repackageSourcePath, destPath); err != nil {
			s.Fail("❌ Failed to copy file")
			return fmt.Errorf("failed to copy file: %w", err)
		}
	}

	// 3. Zip temp dir to output
	outputZip := repackageZipPath
	if !repackageInplace {
		outputZip = repackageOutputPath
	}
	s.UpdateMessage("🗜️  Creating new zip file...")
	if err := utils.ZipDir(tempDir, outputZip); err != nil {
		s.Fail("❌ Failed to create zip")
		return fmt.Errorf("failed to create zip: %w", err)
	}

	s.Stop(fmt.Sprintf("✅ Repackaged zip created at: %s", outputZip))
	return nil
}
