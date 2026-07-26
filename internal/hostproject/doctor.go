package hostproject

import (
	"os/exec"
	"runtime"
)

type DoctorReport struct {
	Conformance Report
	Platform    string
	Runtime     string
	Executable  string
	Available   bool
}

func Doctor(root string) (DoctorReport, error) {
	report, err := Inspect(root)
	if err != nil {
		return DoctorReport{}, err
	}
	runtimeID := report.Manifest.Project.Runtime
	if runtimeID == "" {
		switch report.Manifest.Host.ID {
		case "fabric":
			runtimeID = "java"
		case "bepinex-mono", "bepinex-il2cpp":
			runtimeID = "csharp"
		case "luanti":
			runtimeID = "lua"
		}
	}
	executables := map[string]string{
		"go": "go", "javascript": "node", "python": "python3",
		"csharp": "dotnet", "java": "java", "lua": "lua",
	}
	executable := executables[runtimeID]
	_, pathErr := exec.LookPath(executable)
	return DoctorReport{
		Conformance: report,
		Platform:    runtime.GOOS + "/" + runtime.GOARCH,
		Runtime:     runtimeID,
		Executable:  executable,
		Available:   executable != "" && pathErr == nil,
	}, nil
}
