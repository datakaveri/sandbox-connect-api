package main

import (
	"strings"
	"testing"
)

func TestInitImageDemoCopyScriptCopiesFlavorAndTmpFiles(t *testing.T) {
	script := buildInitImageDemoCopyScript("cpu")

	for _, want := range []string{
		`SOURCE_DIR="/tmp/demo_notebooks/cpu"`,
		`FLAVOR_DIR="/tmp/cpu"`,
		`for f in "$src_dir"/*`,
		`copy_demo_files "$SOURCE_DIR" /home/jovyan || true`,
		`copy_demo_files "$FLAVOR_DIR" /home/jovyan || true`,
		`copy_demo_files /tmp /home/jovyan || true`,
		`cp "$f" "$dest_dir/$filename"`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("init image demo copy script missing %q:\n%s", want, script)
		}
	}

	if strings.Contains(script, "/tmp/demo.ipynb") || strings.Contains(script, "/tmp/requirements.txt") {
		t.Fatalf("init image demo copy script should not special-case only legacy demo files:\n%s", script)
	}
}

func TestNotebookImageDemoCopyScriptCopiesAllTopLevelTmpFiles(t *testing.T) {
	script := buildNotebookImageDemoCopyScript("gpu", "ps3_notebooks")

	for _, want := range []string{
		`PROJECT_NOTEBOOK_DIR="ps3_notebooks"`,
		`copy_demo_files "/home/jovyan/$PROJECT_NOTEBOOK_DIR" /mnt/data || true`,
		`copy_demo_files "/tmp/$PROJECT_NOTEBOOK_DIR" /mnt/data || true`,
		`SOURCE_DIR="/tmp/demo_notebooks/gpu"`,
		`FLAVOR_DIR="/tmp/gpu"`,
		`copy_demo_files "$SOURCE_DIR" /mnt/data || true`,
		`copy_demo_files "$FLAVOR_DIR" /mnt/data || true`,
		`copy_demo_files /tmp /mnt/data || true`,
		`for f in "$src_dir"/*`,
		`if [ -f "$f" ]; then`,
		`cp "$f" "$dest_dir/$filename"`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("notebook image demo copy script missing %q:\n%s", want, script)
		}
	}

	if strings.Contains(script, `/tmp/*.ipynb`) || strings.Contains(script, `/tmp/requirements.txt`) {
		t.Fatalf("notebook image demo copy script should copy all regular /tmp files, not only selected names:\n%s", script)
	}
}

func TestNotebookImageProjectNotebookDirDetectsNHAImages(t *testing.T) {
	tests := map[string]string{
		"098809772313.dkr.ecr.ap-south-1.amazonaws.com/tgdex/ai-sandbox-cpu-notebook:nha-ps1-v3": "ps1_notebooks",
		"098809772313.dkr.ecr.ap-south-1.amazonaws.com/tgdex/ai-sandbox-cpu-notebook:nha-ps2-v4": "ps2_notebooks",
		"098809772313.dkr.ecr.ap-south-1.amazonaws.com/tgdex/ai-sandbox-cpu-notebook:nha-ps3-v3": "ps3_notebooks",
		"098809772313.dkr.ecr.ap-south-1.amazonaws.com/tgdex/ai-sandbox-cpu-notebook:latest":     "",
	}

	for imageName, want := range tests {
		if got := notebookImageProjectNotebookDir(imageName); got != want {
			t.Fatalf("notebookImageProjectNotebookDir(%q) = %q, want %q", imageName, got, want)
		}
	}
}
