package bootstrapapply

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/dipeshbabu/bareplane/internal/bootstrapdoctor"
)

const MaximumLogSize = 4 * 1024 * 1024

func phaseArguments(request Request) ([]string, error) {
	valid := false
	recovery := request.RecoveryID != ""
	argo := request.Argo != nil
	if argo {
		phase, kind := "argocd", "argocd-input"
		if request.Argo.Handoff {
			phase, kind = "handoff", "handoff-input"
		}
		valid = !recovery && request.Phase == phase && validDigest(request.Argo.Contract) &&
			request.Argo.PayloadDir == filepath.Join(request.StateDir, kind) &&
			(!request.Argo.Handoff || validDigest(request.Argo.HandoffContract))
	}
	if recovery {
		valid = !argo && validResetID(request.RecoveryID) && (request.Phase == "reset_validate" || request.Phase == "reset_execute")
	}
	for _, phase := range phases {
		if !recovery && !argo && phase == request.Phase {
			valid = true
		}
	}
	if !valid {
		return nil, errors.New("only owned bootstrap phases may execute")
	}
	// Ansible wraps this value in double quotes in its IdentityFile option.
	// Escape the inner OpenSSH configuration string, not a shell command.
	identity := strconv.Quote(strings.ReplaceAll(request.PrivateKeyFile, "%", "%%"))
	identity = identity[1 : len(identity)-1]
	args := []string{"--inventory", filepath.Join(request.BundleDir, "inventory.yaml"), "--private-key", identity}
	if recovery {
		variables, _ := json.Marshal(map[string]any{"bareplane_reset_id": request.RecoveryID, "bareplane_reset_scope": "cluster",
			"bareplane_reset_approved": true, "bareplane_reset_allow_unavailable_api": request.AllowUnavailableAPI})
		args = append(args, "--extra-vars", string(variables))
	}
	if argo {
		variables, _ := json.Marshal(map[string]any{"bareplane_argocd_approved": true,
			"bareplane_handoff_approved": request.Argo.Handoff, "bareplane_handoff_contract": request.Argo.HandoffContract,
			"bareplane_gitops_repo_url": request.Argo.Repository, "bareplane_gitops_revision": request.Argo.Revision,
			"bareplane_gitops_root_path": request.Argo.RootPath, "bareplane_argocd_input": request.Argo.PayloadDir,
			"bareplane_gitops_contract": request.Argo.Contract})
		args = append(args, "--extra-vars", string(variables))
	}
	return append(args, request.Phase+".yaml"), nil
}

func controlledEnvironment(base []string, request Request) []string {
	allowed := map[string]bool{"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "LANG": true, "LC_ALL": true, "LC_CTYPE": true, "TMPDIR": true, "TMP": true, "TEMP": true}
	env := make([]string, 0, 40)
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if allowed[key] {
			env = append(env, entry)
		}
	}
	env = append(env,
		"ANSIBLE_CONFIG="+filepath.Join(request.BundleDir, "ansible.cfg"),
		"ANSIBLE_INVENTORY="+filepath.Join(request.BundleDir, "inventory.yaml"),
		"ANSIBLE_LIBRARY="+filepath.Join(request.BundleDir, "library"),
		"ANSIBLE_MODULE_UTILS="+filepath.Join(request.BundleDir, "module_utils"),
		"ANSIBLE_ROLES_PATH="+filepath.Join(request.BundleDir, "roles"),
		"ANSIBLE_LOCAL_TEMP="+filepath.Join(request.StateDir, "ansible-local"),
		"ANSIBLE_HOST_KEY_CHECKING=True", "ANSIBLE_NOCOLOR=1", "ANSIBLE_FORCE_COLOR=False",
		"ANSIBLE_DEBUG=False", "ANSIBLE_VERBOSITY=0", "ANSIBLE_DISPLAY_ARGS_TO_STDOUT=False",
		"ANSIBLE_DIFF_ALWAYS=False", "ANSIBLE_STDOUT_CALLBACK=default",
		"ANSIBLE_CACHE_PLUGIN=memory", "ANSIBLE_KEEP_REMOTE_FILES=False", "ANSIBLE_RETRY_FILES_ENABLED=False",
		"ANSIBLE_PIPELINING=True",
	)
	for _, kind := range []string{"ACTION", "BECOME", "CACHE", "CALLBACK", "CONNECTION", "FILTER", "INVENTORY", "LOOKUP", "TEST", "VARS", "STRATEGY"} {
		env = append(env, "ANSIBLE_"+kind+"_PLUGINS="+filepath.Join(request.BundleDir, ".no-plugins"))
	}
	return env
}

func commandRunner(binary string) Runner {
	return func(ctx context.Context, request Request) error {
		if ctx == nil || request.Log == nil {
			return errors.New("owned phase context and private log are required")
		}
		args, err := phaseArguments(request)
		if err != nil {
			return err
		}
		if err := ensurePrivateDirectory(filepath.Join(request.StateDir, "ansible-local")); err != nil {
			return err
		}
		command := exec.CommandContext(ctx, binary, args...)
		command.Dir = request.BundleDir
		command.Env = controlledEnvironment(os.Environ(), request)
		command.Stdin = nil
		configureProcess(command)
		output := &limitedLog{writer: request.Log, remaining: MaximumLogSize}
		command.Stdout, command.Stderr = output, output
		if err := command.Run(); err != nil {
			return errors.New("owned Ansible phase failed")
		}
		return nil
	}
}

func validateControllerTools(ctx context.Context, request Request, kubernetesVersion string, lookPath bootstrapdoctor.LookPathFunc) error {
	if err := ensurePrivateDirectory(filepath.Join(request.StateDir, "ansible-local")); err != nil {
		return err
	}
	for _, tool := range []struct {
		name string
		args []string
	}{
		{"ansible-playbook", []string{"--version"}},
		{"kubectl", []string{"version", "--client=true", "-o", "json"}},
		{"openssl", []string{"version"}},
	} {
		binary, err := lookPath(tool.name)
		if err != nil {
			return errors.New("required controller tool is unavailable: " + tool.name)
		}
		toolCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		command := exec.CommandContext(toolCtx, binary, tool.args...)
		command.Env = controlledEnvironment(os.Environ(), request)
		command.Dir = request.BundleDir
		command.WaitDelay = time.Second
		var output bytes.Buffer
		bounded := &limitedLog{writer: &output, remaining: 65536}
		command.Stdout, command.Stderr = bounded, io.Discard
		err = command.Run()
		contextErr := toolCtx.Err()
		cancel()
		if err != nil || contextErr != nil || bounded.remaining == 0 || !supportedToolVersion(tool.name, output.Bytes(), kubernetesVersion) {
			return errors.New("unsupported controller toolchain; require ansible-core 2.19.9, OpenSSL 3, and kubectl matching the configured Kubernetes version")
		}
	}
	return nil
}

func supportedToolVersion(name string, output []byte, kubernetesVersion string) bool {
	switch name {
	case "ansible-playbook":
		line, _, _ := strings.Cut(string(output), "\n")
		return strings.Contains(line, "[core 2.19.9]")
	case "openssl":
		return bytes.HasPrefix(output, []byte("OpenSSL 3."))
	case "kubectl":
		var version struct {
			ClientVersion struct {
				GitVersion string `json:"gitVersion"`
			} `json:"clientVersion"`
		}
		return json.Unmarshal(output, &version) == nil && version.ClientVersion.GitVersion == "v"+kubernetesVersion
	default:
		return false
	}
}

func ensurePrivateDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return errors.New("cannot create private bootstrap working directory")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("bootstrap working paths must be real directories")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return errors.New("bootstrap working directories must be owner-only")
	}
	return nil
}

func createPhaseLog(stateDir, phase string) (*os.File, string, error) {
	directory := filepath.Join(stateDir, "logs")
	if err := ensurePrivateDirectory(directory); err != nil {
		return nil, "", err
	}
	file, err := os.CreateTemp(directory, phase+"-*.log")
	if err != nil {
		return nil, "", errors.New("cannot create private bootstrap phase log")
	}
	return file, file.Name(), nil
}

type limitedLog struct {
	writer    io.Writer
	remaining int
}

func (w *limitedLog) Write(data []byte) (int, error) {
	length := len(data)
	if len(data) > w.remaining {
		data = data[:w.remaining]
	}
	if len(data) > 0 {
		written, err := w.writer.Write(data)
		w.remaining -= written
		if err != nil {
			return written, err
		}
		if written != len(data) {
			return written, io.ErrShortWrite
		}
	}
	return length, nil
}
