package spec

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkFileGeneratorPrefix(b *testing.B) {
	originalCWD, err := os.Getwd()
	if err != nil {
		b.Fatal(err)
	}
	workDir := b.TempDir()
	for index := range 500 {
		name := fmt.Sprintf("scalar-project-%03d", index)
		if err := os.Mkdir(filepath.Join(workDir, name), 0755); err != nil {
			b.Fatal(err)
		}
	}
	if err := os.Chdir(workDir); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := os.Chdir(originalCWD); err != nil {
			b.Errorf("restore working directory: %v", err)
		}
	})
	generator := FileGenerator("/")
	tokens := []string{"cd", "scalar-project-4"}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		results := generator(tokens, "cd ", "scalar-project-4")
		if len(results) != 100 {
			b.Fatalf("expected 100 matching directories, got %d", len(results))
		}
	}
}
