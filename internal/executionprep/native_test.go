package executionprep

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestRParserPreservesEffectsAcrossMissingArguments(t *testing.T) {
	interpreter := os.Getenv("SYNON_TEST_RSCRIPT")
	if interpreter == "" {
		var err error
		interpreter, err = exec.LookPath("Rscript")
		if err != nil {
			t.Skip("Rscript unavailable")
		}
	}
	for _, source := range []string{
		`x <- matrix(1:4, 2); y <- x[,1]; BiocManager::install("example")`,
		`x <- array(1:8, c(2,2,2)); y <- x[,,1]; utils::install.packages("example")`,
		`f <- function(x, y=1) x; z <- c(,1); install.packages("example")`,
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		command := exec.CommandContext(ctx, interpreter, "--vanilla", "-e", RParser)
		command.Stdin = strings.NewReader(source)
		output, err := command.Output()
		cancel()
		if err != nil {
			t.Fatalf("parse valid R: %s: %v", source, err)
		}
		facts, err := DecodeNative("r", output)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, fact := range facts {
			if packageMutationCalls[fact.Name] {
				found = true
			}
		}
		if !found {
			t.Fatalf("installation effect lost: %#v", facts)
		}
	}
}
