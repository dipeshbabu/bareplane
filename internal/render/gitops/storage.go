package gitops

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/dipeshbabu/bareplane/internal/config"
	"gopkg.in/yaml.v3"
)

const StorageCheckImage = "python:3.12.14-slim-bookworm"

//go:embed storage/check.py
var storageCheck embed.FS

func renderStorage(cfg config.Config, files map[string][]byte) error {
	settings, err := cfg.RequireStorage()
	if err != nil {
		return err
	}
	script, err := storageCheck.ReadFile("storage/check.py")
	if err != nil {
		return err
	}
	script = bytes.ReplaceAll(script, []byte("\r\n"), []byte("\n"))
	metadata := func(name, wave string, namespaced bool) map[string]any {
		value := map[string]any{"name": name, "annotations": map[string]string{
			"bareplane.io/cluster": cfg.Metadata.Name, "bareplane.io/component": "storage", "argocd.argoproj.io/sync-wave": wave,
		}}
		if namespaced {
			value["namespace"] = "local-storage"
		}
		return value
	}
	namespace := metadata("local-storage", "-30", false)
	namespace["labels"] = map[string]string{"pod-security.kubernetes.io/enforce": "privileged"}
	objects := []map[string]any{
		{"apiVersion": "v1", "kind": "Namespace", "metadata": namespace},
		{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": metadata("bareplane-storage-check", "-20", true), "automountServiceAccountToken": false},
		{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": metadata("storage-check-isolation", "-20", true),
			"spec": map[string]any{"podSelector": map[string]any{}, "policyTypes": []string{"Ingress", "Egress"}}},
		{"apiVersion": "v1", "kind": "ConfigMap", "metadata": metadata("bareplane-storage-check", "-20", true), "data": map[string]string{"check.py": string(script)}},
		{"apiVersion": "storage.k8s.io/v1", "kind": "StorageClass", "metadata": metadata("bareplane-local", "0", false),
			"provisioner": "kubernetes.io/no-provisioner", "reclaimPolicy": "Retain", "volumeBindingMode": "WaitForFirstConsumer", "allowVolumeExpansion": false},
	}
	nodeCapacity := map[string]int{}
	for _, volume := range settings.Volumes {
		nodeCapacity[volume.Node] += volume.CapacityGiB
	}
	for _, volume := range settings.Volumes {
		identity, err := json.Marshal([]any{cfg.Metadata.Name, volume, nodeCapacity[volume.Node], StorageCheckImage, fmt.Sprintf("%x", sha256.Sum256(script))})
		if err != nil {
			return err
		}
		digest := sha256.Sum256(identity)
		jobName := fmt.Sprintf("local-check-%x", digest[:16])
		volumeDigest := sha256.Sum256([]byte(cfg.Metadata.Name + "/" + volume.Name))
		pvName := fmt.Sprintf("bareplane-local-%x", volumeDigest[:16])
		job := map[string]any{"apiVersion": "batch/v1", "kind": "Job", "metadata": metadata(jobName, "-10", true),
			"spec": map[string]any{"backoffLimit": 0, "activeDeadlineSeconds": 180, "template": map[string]any{
				"metadata": map[string]any{"labels": map[string]string{"app.kubernetes.io/name": "bareplane-storage-check"}},
				"spec": map[string]any{"nodeName": volume.Node, "restartPolicy": "Never", "serviceAccountName": "bareplane-storage-check",
					"automountServiceAccountToken": false,
					"tolerations":                  []map[string]string{{"key": "node-role.kubernetes.io/control-plane", "operator": "Exists", "effect": "NoSchedule"}},
					"securityContext":              map[string]any{"runAsUser": 0, "runAsGroup": 0, "seccompProfile": map[string]string{"type": "RuntimeDefault"}},
					"volumes": []map[string]any{{"name": "host", "hostPath": map[string]string{"path": "/", "type": "Directory"}},
						{"name": "check", "configMap": map[string]string{"name": "bareplane-storage-check"}}},
					"containers": []map[string]any{{"name": "check", "image": StorageCheckImage, "imagePullPolicy": "IfNotPresent",
						"command":         []string{"/usr/local/bin/python3", "-I", "-B", "/check/check.py"},
						"args":            []string{cfg.Metadata.Name, volume.Name, volume.Node, strconv.Itoa(nodeCapacity[volume.Node])},
						"env":             []map[string]any{{"name": "NODE_NAME", "valueFrom": map[string]any{"fieldRef": map[string]string{"fieldPath": "spec.nodeName"}}}},
						"volumeMounts":    []map[string]any{{"name": "host", "mountPath": "/host", "readOnly": true, "recursiveReadOnly": "Enabled"}, {"name": "check", "mountPath": "/check", "readOnly": true}},
						"securityContext": map[string]any{"readOnlyRootFilesystem": true, "allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []string{"ALL"}}},
						"resources":       map[string]any{"requests": map[string]string{"cpu": "10m", "memory": "32Mi"}, "limits": map[string]string{"cpu": "100m", "memory": "64Mi"}},
					}},
				},
			}},
		}
		pvMetadata := metadata(pvName, "0", false)
		pvMetadata["labels"] = map[string]string{"bareplane.io/local-volume": volume.Name}
		objects = append(objects, job, map[string]any{"apiVersion": "v1", "kind": "PersistentVolume", "metadata": pvMetadata,
			"spec": map[string]any{"capacity": map[string]string{"storage": fmt.Sprintf("%dGi", volume.CapacityGiB)},
				"volumeMode": "Filesystem", "accessModes": []string{"ReadWriteOnce"}, "persistentVolumeReclaimPolicy": "Retain",
				"storageClassName": "bareplane-local", "local": map[string]string{"path": volume.Directory(cfg.Metadata.Name)},
				"nodeAffinity": map[string]any{"required": map[string]any{"nodeSelectorTerms": []map[string]any{{"matchExpressions": []map[string]any{
					{"key": "kubernetes.io/hostname", "operator": "In", "values": []string{volume.Node}},
				}}}}},
			},
		})
	}
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	for _, obj := range objects {
		if err := encoder.Encode(obj); err != nil {
			return err
		}
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	if strings.Contains(output.String(), "BAREPLANE_") {
		return fmt.Errorf("storage payload contains unresolved public input")
	}
	files["components/storage/resources.yaml"] = output.Bytes()
	return nil
}
