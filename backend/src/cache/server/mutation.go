// Copyright 2020 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/kubeflow/pipelines/backend/src/cache/model"
	"github.com/kubeflow/pipelines/backend/src/cache/storage"
	"k8s.io/api/admission/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	KFPAnnotation        string = "pipelines.kubeflow.org"
	ArgoWorkflowNodeName string = "workflows.argoproj.io/node-name"
	ArgoWorkflowTemplate string = "workflows.argoproj.io/template"
	ExecutionKey         string = "pipelines.kubeflow.org/execution_cache_key"
	CacheIDLabelKey      string = "pipelines.kubeflow.org/cache_id"
	AnnotationPath       string = "/metadata/annotations"
	LabelPath            string = "/metadata/labels"
	ArgoWorkflowOutputs  string = "workflows.argoproj.io/outputs"
	TFXPodSuffix         string = "tfx/orchestration/kubeflow/container_entrypoint.py"
	ArchiveLocationKey   string = "archiveLocation"
)

var (
	podResource = metav1.GroupVersionResource{Version: "v1", Resource: "pods"}
)

type ClientManagerInterface interface {
	CacheStore() storage.ExecutionCacheStoreInterface
}

// MutatePodIfCached will check whether the execution has already been run before from MLMD and apply the output into pod.metadata.output
func MutatePodIfCached(req *v1beta1.AdmissionRequest, clientMgr ClientManagerInterface) ([]patchOperation, error) {
	// This handler should only get called on Pod objects as per the MutatingWebhookConfiguration in the YAML file.
	// However, if (for whatever reason) this gets invoked on an object of a different kind, issue a log message but
	// let the object request pass through otherwise.
	if req.Resource != podResource {
		log.Printf("Expect resource to be %q, but found %q", podResource, req.Resource)
		return nil, nil
	}

	// Parse the Pod object.
	raw := req.Object.Raw
	pod := corev1.Pod{}
	if _, _, err := universalDeserializer.Decode(raw, nil, &pod); err != nil {
		return nil, fmt.Errorf("could not deserialize pod object: %v", err)
	}

	// Pod filtering to only cache KFP argo pods except TFX pods
	if !isValidPod(&pod) {
		log.Printf("This pod %s is not a valid pod.", pod.ObjectMeta.Name)
		return nil, nil
	}

	var patches []patchOperation
	annotations := pod.ObjectMeta.Annotations
	labels := pod.ObjectMeta.Labels
	template, exists := annotations[ArgoWorkflowTemplate]
	var executionHashKey string
	if !exists {
		return patches, nil
	}

	// Generate the executionHashKey based on pod.metadata.annotations.workflows.argoproj.io/template
	executionHashKey, err := generateCacheKeyFromTemplate(template)
	if err != nil {
		log.Printf("Unable to generate cache key for pod %s : %s", pod.ObjectMeta.Name, err.Error())
		return patches, nil
	}

	annotations[ExecutionKey] = executionHashKey
	labels[CacheIDLabelKey] = ""

	var cachedExecution *model.ExecutionCache
	cachedExecution, err = clientMgr.CacheStore().GetExecutionCache(executionHashKey)
	if err != nil {
		log.Println(err.Error())
	}
	// Found cached execution, add cached output and cache_id and replace container images.
	if cachedExecution != nil {
		log.Println("Cached output: " + cachedExecution.ExecutionOutput)
		annotations[ArgoWorkflowOutputs] = cachedExecution.ExecutionOutput
		labels[CacheIDLabelKey] = strconv.FormatInt(cachedExecution.ID, 10)
		dummyContainer := corev1.Container{
			Name:    "dummy",
			Image:   "alpine",
			Command: []string{"true"},
		}
		dummyContainers := []corev1.Container{
			dummyContainer,
		}
		patches = append(patches, patchOperation{
			Op:    "replace",
			Path:  "/spec/containers",
			Value: dummyContainers,
		})
		if pod.Spec.InitContainers != nil || len(pod.Spec.InitContainers) != 0 {
			patches = append(patches, patchOperation{
				Op:   "remove",
				Path: "/spec/initContainers",
			})
		}
	}
	log.Println(cachedExecution)

	// Add executionKey to pod.metadata.annotations
	patches = append(patches, patchOperation{
		Op:    OperationTypeAdd,
		Path:  AnnotationPath,
		Value: annotations,
	})

	// Add cache_id label key
	patches = append(patches, patchOperation{
		Op:    OperationTypeAdd,
		Path:  LabelPath,
		Value: labels,
	})

	return patches, nil
}

func generateCacheKeyFromTemplate(template string) (string, error) {
	var templateMap map[string]interface{}
	b := []byte(template)
	err := json.Unmarshal(b, &templateMap)
	if err != nil {
		return "", err
	}

	// template[archiveLocation] needs to be removed when calculating cache key.
	// Because archiveLocation.key is different in every single run.
	_, exists := templateMap[ArchiveLocationKey]
	if exists {
		log.Println("ArchiveLocation exists in template.")
		delete(templateMap, ArchiveLocationKey)
	}
	b, err = json.Marshal(templateMap)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	hash.Write(b)
	md := hash.Sum(nil)
	executionHashKey := hex.EncodeToString(md)

	return executionHashKey, nil
}

func isValidPod(pod *corev1.Pod) bool {
	annotations := pod.ObjectMeta.Annotations
	if annotations == nil || len(annotations) == 0 {
		log.Printf("The annotation of this pod %s is empty.", pod.ObjectMeta.Name)
		return false
	}
	if !isKFPArgoPod(&annotations, pod.ObjectMeta.Name) {
		log.Printf("This pod %s is not created by KFP.", pod.ObjectMeta.Name)
		return false
	}
	containers := pod.Spec.Containers
	if containers != nil && len(containers) != 0 && isTFXPod(&containers) {
		log.Printf("This pod %s is created by TFX pipelines.", pod.ObjectMeta.Name)
		return false
	}
	return true
}

func isKFPArgoPod(annotations *map[string]string, podName string) bool {
	// is argo pod or not
	if _, exists := (*annotations)[ArgoWorkflowNodeName]; !exists {
		log.Printf("This pod %s is not created by Argo.", podName)
		return false
	}
	// is KFP pod or not
	for k := range *annotations {
		if strings.Contains(k, KFPAnnotation) {
			return true
		}
	}
	return false
}

func isTFXPod(containers *[]corev1.Container) bool {
	var mainContainers []corev1.Container
	for _, c := range *containers {
		if c.Name != "" && c.Name == "main" {
			mainContainers = append(mainContainers, c)
		}
	}
	if len(mainContainers) != 1 {
		return false
	}
	mainContainer := mainContainers[0]
	return len(mainContainer.Command) != 0 && strings.HasSuffix(mainContainer.Command[len(mainContainer.Command)-1], TFXPodSuffix)
}
