package generator

import (
	"fmt"
	"encoding/json"
	
	"github.com/ghodss/yaml"

	"github.com/sysdiglabs/kube-psp-advisor/advisor/types"	
	"github.com/sysdiglabs/kube-psp-advisor/utils"	

	v1 "k8s.io/api/core/v1"
	appsv1 "k8s.io/api/apps/v1"
	v1beta1 "k8s.io/api/policy/v1beta1"
	batch "k8s.io/api/batch/v1"
	batchv1beta1 "k8s.io/api/batch/v1beta1"
	
	"reflect"
	"strings"
	"time"
)

const (
	volumeTypeSecret = "secret"
)	

type Generator struct {
}

func NewGenerator() (*Generator, error) {

	return &Generator{}, nil
}

func getVolumeTypes(spec v1.PodSpec, sa *v1.ServiceAccount) (volumeTypes []string) {
	volumeTypeMap := map[string]bool{}
	for _, v := range spec.Volumes {
		if volumeType := getVolumeType(v); volumeType != "" {
			volumeTypeMap[getVolumeType(v)] = true
		}
	}

	// If don't opt out of automounting API credentils for a service account
	// or a particular pod, "secret" needs to be into PSP allowed volume types.
	if sa == nil || mountServiceAccountToken(spec, *sa) {
		volumeTypeMap[volumeTypeSecret] = true
	}

	volumeTypes = utils.MapToArray(volumeTypeMap)
	return
}

func getVolumeHostPaths(spec v1.PodSpec) map[string]bool {
	hostPathMap := map[string]bool{}

	containerMountMap := map[string]bool{}

	for _, c := range spec.Containers {
		for _, vm := range c.VolumeMounts {
			if _, exists := containerMountMap[vm.Name]; !exists {
				containerMountMap[vm.Name] = vm.ReadOnly
			} else {
				containerMountMap[vm.Name] = containerMountMap[vm.Name] && vm.ReadOnly
			}
		}
	}

	for _, c := range spec.InitContainers {
		for _, vm := range c.VolumeMounts {
			if _, exists := containerMountMap[vm.Name]; !exists {
				containerMountMap[vm.Name] = vm.ReadOnly
			} else {
				containerMountMap[vm.Name] = containerMountMap[vm.Name] && vm.ReadOnly
			}
		}
	}

	for _, v := range spec.Volumes {
		if v.HostPath != nil {
			if _, exists := containerMountMap[v.Name]; exists {
				hostPathMap[v.HostPath.Path] = containerMountMap[v.Name]
			}
		}
	}

	return hostPathMap
}

func getVolumeType(v v1.Volume) string {
	val := reflect.ValueOf(v.VolumeSource)
	for i := 0; i < val.Type().NumField(); i++ {
		if !val.Field(i).IsNil() {
			protos := strings.Split(val.Type().Field(i).Tag.Get("protobuf"), ",")
			for _, p := range protos {
				if strings.HasPrefix(p, "name=") {
					return p[5:]
				}
			}
		}
	}
	return ""
}

func getRunAsUser(sc *v1.SecurityContext, psc *v1.PodSecurityContext) *int64 {
	if sc == nil {
		if psc != nil {
			return psc.RunAsUser
		}
		return nil
	}

	return sc.RunAsUser
}

func getRunAsGroup(sc *v1.SecurityContext, psc *v1.PodSecurityContext) *int64 {
	if sc == nil {
		if psc != nil {
			return psc.RunAsGroup
		}
		return nil
	}

	return sc.RunAsGroup
}

func getHostPorts(containerPorts []v1.ContainerPort) (hostPorts []int32) {
	for _, p := range containerPorts {
		hostPorts = append(hostPorts, p.HostPort)
	}
	return
}

func getEffectiveCapablities(add, drop []string) (effectiveCaps []string) {
	dropCapMap := utils.ArrayToMap(drop)
	defaultCaps := types.DefaultCaps

	for _, cap := range defaultCaps {
		if _, exists := dropCapMap[cap]; !exists {
			effectiveCaps = append(effectiveCaps, cap)
		}
	}

	effectiveCaps = append(effectiveCaps, add...)

	return
}

func getPrivileged(sc *v1.SecurityContext) bool {
	if sc == nil {
		return false
	}

	if sc.Privileged == nil {
		return false
	}

	return *sc.Privileged
}

func getRunAsNonRootUser(sc *v1.SecurityContext, psc *v1.PodSecurityContext) *bool {
	if sc == nil {
		if psc != nil {
			return psc.RunAsNonRoot
		}
		return nil
	}

	return sc.RunAsNonRoot
}

func getAllowedPrivilegeEscalation(sc *v1.SecurityContext) *bool {
	if sc == nil {
		return nil
	}

	return sc.AllowPrivilegeEscalation
}

func getIDs(podStatus v1.PodStatus, containerName string) (containerID, imageID string) {
	containers := podStatus.ContainerStatuses
	for _, c := range containers {
		if c.Name == containerName {
			if len(c.ContainerID) > 0 {
				idx := strings.Index(c.ContainerID, "docker://") + 9
				if idx > len(c.ContainerID) {
					idx = 0
				}
				containerID = c.ContainerID[idx:]
			}

			if len(c.ImageID) > 0 {
				imageID = c.ImageID[strings.Index(c.ImageID, "sha256"):]
			}

			return
		}
	}
	return
}

func getReadOnlyRootFileSystem(sc *v1.SecurityContext) bool {
	if sc == nil {
		return false
	}

	if sc.ReadOnlyRootFilesystem == nil {
		return false
	}

	return *sc.ReadOnlyRootFilesystem
}

func getCapabilities(sc *v1.SecurityContext) (addList []string, dropList []string) {
	if sc == nil {
		return
	}

	if sc.Capabilities == nil {
		return
	}

	addCaps := sc.Capabilities.Add
	dropCaps := sc.Capabilities.Drop

	for _, cap := range addCaps {
		addList = append(addList, string(cap))
	}

	for _, cap := range dropCaps {
		dropList = append(dropList, string(cap))
	}
	return
}

func mountServiceAccountToken(spec v1.PodSpec, sa v1.ServiceAccount) bool {
	// First Pod's preference is checked
	if spec.AutomountServiceAccountToken != nil {
		return *spec.AutomountServiceAccountToken
	}

	// Then service account's
	if sa.AutomountServiceAccountToken != nil {
		return *sa.AutomountServiceAccountToken
	}

	return true
}

func (pg *Generator) GetSecuritySpecFromPodSpec(metadata types.Metadata, namespace string, spec v1.PodSpec, sa *v1.ServiceAccount) ([]types.ContainerSecuritySpec, types.PodSecuritySpec) {
	cssList := []types.ContainerSecuritySpec{}
	podSecuritySpec := types.PodSecuritySpec{
		Metadata:       metadata,
		Namespace:      namespace,
		HostPID:        spec.HostPID,
		HostNetwork:    spec.HostNetwork,
		HostIPC:        spec.HostIPC,
		VolumeTypes:    getVolumeTypes(spec, sa),
		MountHostPaths: getVolumeHostPaths(spec),
	}

	for _, container := range spec.InitContainers {
		addCapList, dropCapList := getCapabilities(container.SecurityContext)
		csc := types.ContainerSecuritySpec{
			Metadata:                 metadata,
			ContainerName:            container.Name,
			ImageName:                container.Image,
			PodName:                  metadata.Name,
			Namespace:                namespace,
			HostName:                 spec.NodeName,
			Capabilities:             getEffectiveCapablities(addCapList, dropCapList),
			AddedCap:                 addCapList,
			DroppedCap:               dropCapList,
			ReadOnlyRootFS:           getReadOnlyRootFileSystem(container.SecurityContext),
			RunAsNonRoot:             getRunAsNonRootUser(container.SecurityContext, spec.SecurityContext),
			AllowPrivilegeEscalation: getAllowedPrivilegeEscalation(container.SecurityContext),
			Privileged:               getPrivileged(container.SecurityContext),
			RunAsGroup:               getRunAsGroup(container.SecurityContext, spec.SecurityContext),
			RunAsUser:                getRunAsUser(container.SecurityContext, spec.SecurityContext),
			HostPorts:                getHostPorts(container.Ports),
		}
		cssList = append(cssList, csc)
	}

	for _, container := range spec.Containers {
		addCapList, dropCapList := getCapabilities(container.SecurityContext)
		csc := types.ContainerSecuritySpec{
			Metadata:                 metadata,
			ContainerName:            container.Name,
			ImageName:                container.Image,
			PodName:                  metadata.Name,
			Namespace:                namespace,
			HostName:                 spec.NodeName,
			Capabilities:             getEffectiveCapablities(addCapList, dropCapList),
			AddedCap:                 addCapList,
			DroppedCap:               dropCapList,
			ReadOnlyRootFS:           getReadOnlyRootFileSystem(container.SecurityContext),
			RunAsNonRoot:             getRunAsNonRootUser(container.SecurityContext, spec.SecurityContext),
			AllowPrivilegeEscalation: getAllowedPrivilegeEscalation(container.SecurityContext),
			Privileged:               getPrivileged(container.SecurityContext),
			RunAsGroup:               getRunAsGroup(container.SecurityContext, spec.SecurityContext),
			RunAsUser:                getRunAsUser(container.SecurityContext, spec.SecurityContext),
			HostPorts:                getHostPorts(container.Ports),
		}
		cssList = append(cssList, csc)
	}
	return cssList, podSecuritySpec
}

// GeneratePSP generate Pod Security Policy
func (pg *Generator) GeneratePSP(
	cssList []types.ContainerSecuritySpec,
	pssList []types.PodSecuritySpec,
	namespace string,
	serverGitVersion string) *v1beta1.PodSecurityPolicy {
	
	var ns string
	// no PSP will be generated if no security spec is provided
	if len(cssList) == 0 && len(pssList) == 0 {
		return nil
	}

	psp := &v1beta1.PodSecurityPolicy{}

	psp.APIVersion = "policy/v1beta1"
	psp.Kind = "PodSecurityPolicy"

	addedCap := map[string]int{}
	droppedCap := map[string]int{}

	effectiveCap := map[string]bool{}

	runAsUser := map[int64]bool{}

	volumeTypes := map[string]bool{}

	hostPaths := map[string]bool{}

	runAsUserCount := 0

	runAsNonRootCount := 0

	notAllowPrivilegeEscationCount := 0

	ns = namespace

	if ns == "" {
		ns = "all"
	}

	psp.Name = fmt.Sprintf("%s-%s-%s", "pod-security-policy", ns, time.Now().Format("20060102150405"))

	for _, sc := range pssList {
		psp.Spec.HostPID = psp.Spec.HostPID || sc.HostPID
		psp.Spec.HostIPC = psp.Spec.HostIPC || sc.HostIPC
		psp.Spec.HostNetwork = psp.Spec.HostNetwork || sc.HostNetwork

		for _, t := range sc.VolumeTypes {
			volumeTypes[t] = true
		}

		for path, readOnly := range sc.MountHostPaths {
			if _, exists := hostPaths[path]; !exists {
				hostPaths[path] = readOnly
			} else {
				hostPaths[path] = readOnly && hostPaths[path]
			}
		}
	}

	for _, sc := range cssList {
		for _, cap := range sc.Capabilities {
			effectiveCap[cap] = true
		}

		for _, cap := range sc.AddedCap {
			addedCap[cap]++
		}

		for _, cap := range sc.DroppedCap {
			droppedCap[cap]++
		}

		psp.Spec.Privileged = psp.Spec.Privileged || sc.Privileged

		psp.Spec.ReadOnlyRootFilesystem = psp.Spec.ReadOnlyRootFilesystem || sc.ReadOnlyRootFS

		if sc.RunAsNonRoot != nil && *sc.RunAsNonRoot {
			runAsNonRootCount++
		}

		if sc.RunAsUser != nil {
			runAsUser[*sc.RunAsUser] = true
			runAsUserCount++
		}

		if sc.AllowPrivilegeEscalation != nil && !*sc.AllowPrivilegeEscalation {
			notAllowPrivilegeEscationCount++
		}

		// set host ports
		// TODO: need to integrate with listening port during the runtime, might cause false positive.
		//for _, port := range sc.HostPorts {
		//	psp.Spec.HostPorts = append(psp.Spec.HostPorts, v1beta1.HostPortRange{Min: port, Max: port})
		//}
	}

	// set allowedPrivilegeEscalation
	if notAllowPrivilegeEscationCount == len(cssList) {
		notAllowed := false
		psp.Spec.AllowPrivilegeEscalation = &notAllowed
	}

	// set runAsUser strategy
	if runAsNonRootCount == len(cssList) {
		psp.Spec.RunAsUser.Rule = v1beta1.RunAsUserStrategyMustRunAsNonRoot
	}

	if runAsUserCount == len(cssList) {
		psp.Spec.RunAsUser.Rule = v1beta1.RunAsUserStrategyMustRunAs
		for uid := range runAsUser {
			if psp.Spec.RunAsUser.Rule == v1beta1.RunAsUserStrategyMustRunAsNonRoot && uid != 0 {
				psp.Spec.RunAsUser.Ranges = append(psp.Spec.RunAsUser.Ranges, v1beta1.IDRange{
					Min: uid,
					Max: uid,
				})
			}
		}
	}

	// set allowed host path
	enforceReadOnly, _ := utils.CompareVersion(serverGitVersion, types.Version1_11)

	for path, readOnly := range hostPaths {
		psp.Spec.AllowedHostPaths = append(psp.Spec.AllowedHostPaths, v1beta1.AllowedHostPath{
			PathPrefix: path,
			ReadOnly:   readOnly || enforceReadOnly,
		})
	}

	// set limit volumes
	volumeTypeList := utils.MapToArray(volumeTypes)

	for _, v := range volumeTypeList {
		psp.Spec.Volumes = append(psp.Spec.Volumes, v1beta1.FSType(v))
	}

	// set allowedCapabilities
	defaultCap := utils.ArrayToMap(types.DefaultCaps)
	for cap := range defaultCap {
		if _, exists := effectiveCap[cap]; exists {
			delete(effectiveCap, cap)
		}
	}

	// set allowedAddCapabilities
	for cap := range effectiveCap {
		psp.Spec.AllowedCapabilities = append(psp.Spec.AllowedCapabilities, v1.Capability(cap))
	}

	// set defaultAddCapabilities
	for k, v := range addedCap {
		if v == len(cssList) {
			psp.Spec.DefaultAddCapabilities = append(psp.Spec.DefaultAddCapabilities, v1.Capability(k))
		}
	}

	// set requiredDroppedCapabilities
	for k, v := range droppedCap {
		if v == len(cssList) {
			psp.Spec.RequiredDropCapabilities = append(psp.Spec.RequiredDropCapabilities, v1.Capability(k))
		}
	}

	// set to default values
	if string(psp.Spec.RunAsUser.Rule) == "" {
		psp.Spec.RunAsUser.Rule = v1beta1.RunAsUserStrategyRunAsAny
	}

	if psp.Spec.RunAsGroup != nil && string(psp.Spec.RunAsGroup.Rule) == "" {
		psp.Spec.RunAsGroup.Rule = v1beta1.RunAsGroupStrategyRunAsAny
	}

	if string(psp.Spec.FSGroup.Rule) == "" {
		psp.Spec.FSGroup.Rule = v1beta1.FSGroupStrategyRunAsAny
	}

	if string(psp.Spec.SELinux.Rule) == "" {
		psp.Spec.SELinux.Rule = v1beta1.SELinuxStrategyRunAsAny
	}

	if string(psp.Spec.SupplementalGroups.Rule) == "" {
		psp.Spec.SupplementalGroups.Rule = v1beta1.SupplementalGroupsStrategyRunAsAny
	}

	return psp
}

func (pg *Generator) fromPodObj(metadata types.Metadata, spec v1.PodSpec) (string, error) {

	cssList, pss := pg.GetSecuritySpecFromPodSpec(metadata, "default", spec, nil)

	pssList := []types.PodSecuritySpec{pss}

	// We assume a namespace "default", which is only used for the
	// name of the resulting PSP, and assume a k8s version of
	// 1.11, which allows enforcing ReadOnly.
	psp := pg.GeneratePSP(cssList, pssList, "default", types.Version1_11)

	pspJson, err := json.Marshal(psp); if err != nil {
		return "", fmt.Errorf("Could not marshal resulting PSP: %v", err)
	}

	pspYaml, err := yaml.JSONToYAML(pspJson); if err != nil {
		return "", fmt.Errorf("Could not convert resulting PSP to Json: %v", err)
	}

	return string(pspYaml), nil
}

func (pg *Generator) fromDaemonSet(ds *appsv1.DaemonSet) (string, error) {
	return pg.fromPodObj(types.Metadata{
		Name: ds.Name,
		Kind: ds.Kind,
	}, ds.Spec.Template.Spec)
}

func (pg *Generator) fromDeployment(dep *appsv1.Deployment) (string, error) {
	return pg.fromPodObj(types.Metadata{
		Name: dep.Name,
		Kind: dep.Kind,
	}, dep.Spec.Template.Spec)
}

func (pg *Generator) fromReplicaSet(rs *appsv1.ReplicaSet) (string, error) {
	return pg.fromPodObj(types.Metadata{
		Name: rs.Name,
		Kind: rs.Kind,
	}, rs.Spec.Template.Spec)
}

func (pg *Generator) fromStatefulSet(ss *appsv1.StatefulSet) (string, error) {
	return pg.fromPodObj(types.Metadata{
		Name: ss.Name,
		Kind: ss.Kind,
	}, ss.Spec.Template.Spec)
}

func (pg *Generator) fromReplicationController(rc *v1.ReplicationController) (string, error) {
	return pg.fromPodObj(types.Metadata{
		Name: rc.Name,
		Kind: rc.Kind,
	}, rc.Spec.Template.Spec)
}

func (pg *Generator) fromCronJob(cj *batchv1beta1.CronJob) (string, error) {
	return pg.fromPodObj(types.Metadata{
		Name: cj.Name,
		Kind: cj.Kind,
	}, cj.Spec.JobTemplate.Spec.Template.Spec)
}

func (pg *Generator) fromJob(job *batch.Job) (string, error) {
	return pg.fromPodObj(types.Metadata{
		Name: job.Name,
		Kind: job.Kind,
	}, job.Spec.Template.Spec)
}

func (pg *Generator) fromPod(pod *v1.Pod) (string, error) {
	return pg.fromPodObj(types.Metadata{
		Name: pod.Name,
		Kind: pod.Kind,
	}, pod.Spec)
}

func (pg *Generator) FromPodObjString(podObjString string) (string, error) {

	podObjJson, err := yaml.YAMLToJSON([]byte(podObjString)); if err != nil {
		return "", fmt.Errorf("Could not parse pod Object: %v", err)
	}

	var anyJson map[string]interface{}

	err = json.Unmarshal(podObjJson, &anyJson)

	if err != nil {
		return "", fmt.Errorf("Could not unmarshal json document: %v", err)
	}

	switch kind := anyJson["kind"]; kind {
	case "DaemonSet":
		var ds appsv1.DaemonSet
		if err = json.Unmarshal(podObjJson, &ds); err != nil {
			return "", fmt.Errorf("Could not unmarshal json document as DaemonSet: %v", err)
		}
		return pg.fromDaemonSet(&ds)
	case "Deployment":
		var dep appsv1.Deployment
		if err = json.Unmarshal(podObjJson, &dep); err != nil {
			return "", fmt.Errorf("Could not unmarshal json document as Deployment: %v", err)
		}
		return pg.fromDeployment(&dep)
	case "ReplicaSet":
		var rs appsv1.ReplicaSet
		if err = json.Unmarshal(podObjJson, &rs); err != nil {
			return "", fmt.Errorf("Could not unmarshal json document as ReplicaSet: %v", err)
		}
		return pg.fromReplicaSet(&rs)
	case "StatefulSet":
		var ss appsv1.StatefulSet
		if err = json.Unmarshal(podObjJson, &ss); err != nil {
			return "", fmt.Errorf("Could not unmarshal json document as StatefulSet: %v", err)
		}
		return pg.fromStatefulSet(&ss)
	case "ReplicationController":
		var rc v1.ReplicationController
		if err = json.Unmarshal(podObjJson, &rc); err != nil {
			return "", fmt.Errorf("Could not unmarshal json document as ReplicationController: %v", err)
		}
		return pg.fromReplicationController(&rc)
	case "CronJob":
		var cj batchv1beta1.CronJob
		if err = json.Unmarshal(podObjJson, &cj); err != nil {
			return "", fmt.Errorf("Could not unmarshal json document as CronJob: %v", err)
		}
		return pg.fromCronJob(&cj)
	case "Job":
		var job batch.Job
		if err = json.Unmarshal(podObjJson, &job); err != nil {
			return "", fmt.Errorf("Could not unmarshal json document as Job: %v", err)
		}
		return pg.fromJob(&job)
	case "Pod":
		var pod v1.Pod
		if err = json.Unmarshal(podObjJson, &pod); err != nil {
			return "", fmt.Errorf("Could not unmarshal json document as Pod: %v", err)
		}
		return pg.fromPod(&pod)
	}

	return "", fmt.Errorf("K8s Object not one of supported types")
}

