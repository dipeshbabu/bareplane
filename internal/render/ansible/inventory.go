package ansible

import (
	"bytes"
	"fmt"
	"strconv"

	"github.com/dipeshbabu/bareplane/internal/config"
	"github.com/dipeshbabu/bareplane/internal/topology"
	"gopkg.in/yaml.v3"
)

const InventoryFilename = "inventory.yaml"

func RenderInventory(cfg config.Config) ([]byte, error) {
	if err := cfg.ValidateBootstrap(); err != nil {
		return nil, fmt.Errorf("validate bootstrap configuration: %w", err)
	}
	topo, err := topology.Build(cfg)
	if err != nil {
		return nil, fmt.Errorf("build topology: %w", err)
	}

	ssh := cfg.Spec.Bootstrap.SSH
	root := mappingNode()
	all := mappingNode()
	children := mappingNode()
	controlPlane := mappingNode()
	workers := mappingNode()
	controlHosts := mappingNode()
	workerHosts := mappingNode()

	for _, machine := range topo.Machines {
		host := ssh.Hosts[machine.Name]
		vars := hostVariables(machine, host, ssh.User, ssh.EffectivePort())
		switch machine.Role {
		case "control-plane":
			appendMapping(controlHosts, scalar(machine.Name), vars)
		case "worker":
			appendMapping(workerHosts, scalar(machine.Name), vars)
		default:
			return nil, fmt.Errorf("machine %q has unsupported role %q", machine.Name, machine.Role)
		}
	}

	appendMapping(controlPlane, scalar("hosts"), controlHosts)
	appendMapping(workers, scalar("hosts"), workerHosts)
	appendMapping(children, scalar("control_plane"), controlPlane)
	appendMapping(children, scalar("workers"), workers)
	appendMapping(all, scalar("children"), children)
	appendMapping(root, scalar("all"), all)

	document := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return nil, fmt.Errorf("encode Ansible inventory: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("close Ansible inventory encoder: %w", err)
	}
	return buffer.Bytes(), nil
}

func hostVariables(machine topology.Machine, host, user string, port int) *yaml.Node {
	vars := mappingNode()
	appendMapping(vars, scalar("ansible_host"), scalar(host))
	appendMapping(vars, scalar("ansible_user"), scalar(user))
	appendMapping(vars, scalar("ansible_port"), intScalar(port))
	appendMapping(vars, scalar("bareplane_group"), scalar(machine.Group))
	appendMapping(vars, scalar("bareplane_role"), scalar(machine.Role))
	if machine.Target != "" {
		appendMapping(vars, scalar("bareplane_provider_target"), scalar(machine.Target))
	}
	appendMapping(vars, scalar("bareplane_gpu"), boolScalar(machine.GPU))
	appendMapping(vars, scalar("bareplane_cpu"), intScalar(machine.CPU))
	appendMapping(vars, scalar("bareplane_memory_gb"), intScalar(machine.MemoryGB))
	appendMapping(vars, scalar("bareplane_disk_gb"), intScalar(machine.DiskGB))
	return vars
}

func mappingNode() *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
}

func appendMapping(mapping, key, value *yaml.Node) {
	mapping.Content = append(mapping.Content, key, value)
}

func scalar(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func intScalar(value int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(value)}
}

func boolScalar(value bool) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(value)}
}
