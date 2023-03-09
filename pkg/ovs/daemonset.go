/*
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package ovs

import (
	"context"
	"fmt"

	"github.com/openstack-k8s-operators/lib-common/modules/common/env"
	"github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	nad "github.com/openstack-k8s-operators/lib-common/modules/common/networkattachment"
	"github.com/openstack-k8s-operators/ovs-operator/api/v1beta1"

	ovnclient "github.com/openstack-k8s-operators/ovn-operator/api/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DaemonSet func
func DaemonSet(
	ctx context.Context,
	h *helper.Helper,
	instance *v1beta1.OVS,
	configHash string,
	labels map[string]string,
	annotations map[string]string,
) (*appsv1.DaemonSet, error) {

	runAsUser := int64(0)
	privileged := true

	//
	// https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/
	//
	ovsDbLivenessProbe := &corev1.Probe{
		// TODO might need tuning
		TimeoutSeconds:      5,
		PeriodSeconds:       3,
		InitialDelaySeconds: 3,
	}
	ovsDbLivenessProbe.Exec = &corev1.ExecAction{
		Command: []string{
			"/usr/bin/ovs-vsctl",
			"show",
		},
	}
	ovsVswitchdLivenessProbe := &corev1.Probe{
		// TODO might need tuning
		TimeoutSeconds:      5,
		PeriodSeconds:       3,
		InitialDelaySeconds: 3,
	}
	ovsVswitchdLivenessProbe.Exec = &corev1.ExecAction{
		Command: []string{
			"/usr/bin/ovs-appctl",
			"bond/show",
		},
	}

	dbmap, err := ovnclient.GetDBEndpoints(ctx, h, instance.Namespace, map[string]string{})
	if err != nil {
		return nil, err
	}

	envVars := map[string]env.Setter{}
	envVars["KOLLA_CONFIG_FILE"] = env.SetValue(KollaConfigAPI)
	envVars["KOLLA_CONFIG_STRATEGY"] = env.SetValue("COPY_ALWAYS")
	envVars["CONFIG_HASH"] = env.SetValue(configHash)
	envVars["OvnBridge"] = env.SetValue(instance.Spec.ExternalIDS.OvnBridge)
	envVars["OvnRemote"] = env.SetValue(dbmap["SB"])
	envVars["OvnEncapType"] = env.SetValue(instance.Spec.ExternalIDS.OvnEncapType)
	if instance.Spec.NetworkAttachment == "" {
		envVars["OvnEncapNIC"] = env.SetValue("eth0")
	} else {
		envVars["OvnEncapNIC"] = env.SetValue(nad.GetNetworkIFName(instance.Spec.NetworkAttachment))
	}
	envVars["EnableChassisAsGateway"] = env.SetValue(fmt.Sprintf("%t", instance.Spec.ExternalIDS.EnableChassisAsGateway))
	envVars["PhysicalNetworks"] = env.SetValue(getPhysicalNetworks(instance))
	envVars["OvnHostName"] = EnvDownwardAPI("spec.nodeName")

	daemonset := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ServiceName,
			Namespace: instance.Namespace,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: annotations,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: ServiceAccountName,
					Containers: []corev1.Container{
						// ovsdb-server container
						{
							Name: ServiceName + "db-server",
							Command: []string{
								"/usr/local/bin/container-scripts/start-ovsdb-server.sh",
							},
							Lifecycle: &corev1.Lifecycle{
								PreStop: &corev1.LifecycleHandler{
									Exec: &corev1.ExecAction{
										Command: []string{
											"/usr/share/openvswitch/scripts/ovs-ctl", "stop", "--no-ovs-vswitchd",
										},
									},
								},
							},
							Image: instance.Spec.OvsContainerImage,
							SecurityContext: &corev1.SecurityContext{
								Capabilities: &corev1.Capabilities{
									Add:  []corev1.Capability{"NET_ADMIN", "SYS_ADMIN", "SYS_NICE"},
									Drop: []corev1.Capability{},
								},
								RunAsUser:  &runAsUser,
								Privileged: &privileged,
							},
							Env:                      env.MergeEnvs([]corev1.EnvVar{}, envVars),
							VolumeMounts:             GetOvsDbVolumeMounts(),
							Resources:                instance.Spec.Resources,
							LivenessProbe:            ovsDbLivenessProbe,
							TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
						}, {
							// ovs-vswitchd container
							Name: ServiceName + "-vswitchd",
							Command: []string{
								"/usr/sbin/ovs-vswitchd",
							},
							Args: []string{
								"--pidfile", "--mlockall",
							},
							Lifecycle: &corev1.Lifecycle{
								PreStop: &corev1.LifecycleHandler{
									Exec: &corev1.ExecAction{
										Command: []string{
											"/usr/share/openvswitch/scripts/ovs-ctl", "stop", "--no-ovsdb-server",
										},
									},
								},
							},
							Image: instance.Spec.OvsContainerImage,
							SecurityContext: &corev1.SecurityContext{
								Capabilities: &corev1.Capabilities{
									Add:  []corev1.Capability{"NET_ADMIN", "SYS_ADMIN", "SYS_NICE"},
									Drop: []corev1.Capability{},
								},
								RunAsUser:  &runAsUser,
								Privileged: &privileged,
							},
							Env:                      env.MergeEnvs([]corev1.EnvVar{}, envVars),
							VolumeMounts:             GetVswitchdVolumeMounts(),
							Resources:                instance.Spec.Resources,
							LivenessProbe:            ovsVswitchdLivenessProbe,
							TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
						}, {
							// ovn-controller container
							Name: OvnControllerServiceName,
							Command: []string{
								"/bin/bash", "-c",
							},
							Args: []string{
								// First configure external ids and then start ovn controller
								"/usr/local/bin/container-scripts/init.sh && ovn-controller --pidfile unix:/run/openvswitch/db.sock",
							},
							Lifecycle: &corev1.Lifecycle{
								PreStop: &corev1.LifecycleHandler{
									Exec: &corev1.ExecAction{
										Command: []string{
											"/usr/share/ovn/scripts/ovn-ctl", "stop_controller",
										},
									},
								},
							},
							Image: instance.Spec.OvnContainerImage,
							// TODO(slaweq): to check if ovn-controller really needs such security contexts
							SecurityContext: &corev1.SecurityContext{
								Capabilities: &corev1.Capabilities{
									Add:  []corev1.Capability{"NET_ADMIN", "SYS_ADMIN", "SYS_NICE"},
									Drop: []corev1.Capability{},
								},
								RunAsUser:  &runAsUser,
								Privileged: &privileged,
							},
							Env:                      env.MergeEnvs([]corev1.EnvVar{}, envVars),
							VolumeMounts:             GetOvnVolumeMounts(),
							Resources:                instance.Spec.Resources,
							TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
						},
					},
				},
			},
		},
	}
	daemonset.Spec.Template.Spec.Volumes = GetVolumes(instance.Name)

	if instance.Spec.NodeSelector != nil && len(instance.Spec.NodeSelector) > 0 {
		daemonset.Spec.Template.Spec.NodeSelector = instance.Spec.NodeSelector
	}

	return daemonset, nil

}
