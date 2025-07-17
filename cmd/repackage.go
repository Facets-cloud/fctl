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
	repackageZipPath    string
	repackageOutputPath string
	repackageInplace    bool
	copyPairs           []string // --copy source:destination
)

var repackageCmd = &cobra.Command{
	Use:   "repackage",
	Short: "Tweak the exported zip file by copying files from local into specific paths inside the zip.",
	Long:  `Copy files or directories from your local system into specific directory structures inside an existing zip file. Supports multiple source:destination pairs via --copy flag.`,
	RunE:  runRepackage,
}

func init() {
	rootCmd.AddCommand(repackageCmd)

	repackageCmd.Flags().StringVarP(&repackageZipPath, "zip", "z", "", "Path to the zip file to modify (required)")
	repackageCmd.Flags().StringVarP(&repackageOutputPath, "output", "o", "", "Path for the output zip file (required if not using --inplace)")
	repackageCmd.Flags().BoolVar(&repackageInplace, "inplace", false, "Overwrite the original zip file (default: false)")
	repackageCmd.Flags().StringArrayVar(&copyPairs, "copy", nil, "Copy a file or directory from local into a specific path inside the zip. Format: source:destination. Can be specified multiple times.")

	repackageCmd.MarkFlagRequired("zip")
	repackageCmd.MarkFlagsRequiredTogether("copy")
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

	if len(copyPairs) == 0 {
		s.Fail("❌ At least one --copy <source>:<destination> pair is required")
		return fmt.Errorf("at least one --copy <source>:<destination> pair is required")
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

	// 2. For each copy pair, copy file/dir to destination inside temp dir
	for _, pair := range copyPairs {
		sepIdx := -1
		for i, c := range pair {
			if c == ':' {
				sepIdx = i
				break
			}
		}
		if sepIdx == -1 {
			s.Fail(fmt.Sprintf("❌ Invalid --copy value: %s (expected format source:destination)", pair))
			return fmt.Errorf("invalid --copy value: %s (expected format source:destination)", pair)
		}
		source := pair[:sepIdx]
		dest := pair[sepIdx+1:]
		if source == "" || dest == "" {
			s.Fail(fmt.Sprintf("❌ Invalid --copy value: %s (source and destination required)", pair))
			return fmt.Errorf("invalid --copy value: %s (source and destination required)", pair)
		}
		destPath := filepath.Join(tempDir, dest)
		s.UpdateMessage(fmt.Sprintf("\U0001F4C4 Copying %s to %s inside zip structure...", source, dest))
		srcInfo, err := os.Stat(source)
		if err != nil {
			s.Fail(fmt.Sprintf("❌ Failed to stat source: %s", source))
			return fmt.Errorf("failed to stat source %s: %w", source, err)
		}
		if srcInfo.IsDir() {
			if err := utils.CopyDir(source, destPath); err != nil {
				s.Fail(fmt.Sprintf("❌ Failed to copy directory: %s", source))
				return fmt.Errorf("failed to copy directory %s: %w", source, err)
			}
		} else {
			if err := utils.CopyFile(source, destPath); err != nil {
				s.Fail(fmt.Sprintf("❌ Failed to copy file: %s", source))
				return fmt.Errorf("failed to copy file %s: %w", source, err)
			}
		}
	}

	// 3. Zip temp dir to output
	outputZip := repackageZipPath
	if !repackageInplace {
		outputZip = repackageOutputPath
	}
	s.UpdateMessage("🗜️ Creating new zip file...")
	if err := utils.ZipDir(tempDir, outputZip); err != nil {
		s.Fail("❌ Failed to create zip")
		return fmt.Errorf("failed to create zip: %w", err)
	}

	s.Stop(fmt.Sprintf("✅ Repackaged zip created at: %s", outputZip))
	return nil
}
