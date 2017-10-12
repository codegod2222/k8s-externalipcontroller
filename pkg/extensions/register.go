// Copyright 2016 Mirantis
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package extensions

import (
	"fmt"

	"time"

	"strings"

	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

var (
	resources = []string{"ip-node", "ip-claim", "ip-claim-pool"}
)

func fqName(name string) string {
	return fmt.Sprintf("%ss.%s", name, GroupName)
}

func lowercase(name string) string {
	parts := strings.Split(name, "-")
	return strings.Join(parts, "")
}

func kind(name string) string {
	parts := strings.Split(name, "-")
	kindBytes := []byte{}
	for _, part := range parts {
		kindBytes = append(kindBytes, []byte(strings.Title(part))...)
	}
	return string(kindBytes)
}

func EnsureCRDsExist(ki kubernetes.Interface) error {
	client := apiextensionsclient.New(ki.Core().RESTClient())
	for _, res := range resources {
		if err := createCRD(client, res); err != nil && !errors.IsAlreadyExists(err) {
			return err
		}
	}
	return nil
}

func RemoveCRDs(ki kubernetes.Interface) error {
	client := apiextensionsclient.New(ki.Core().RESTClient())
	for _, res := range resources {
		if err := client.Apiextensions().CustomResourceDefinitions().Delete(
			fqName(res), &metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func createCRD(client apiextensionsclient.Interface, name string) error {
	singular := lowercase(name)
	crd := &apiextensionsv1beta1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: fqName(name),
		},
		Spec: apiextensionsv1beta1.CustomResourceDefinitionSpec{
			Group:   GroupName,
			Version: Version,
			Scope:   apiextensionsv1beta1.ClusterScoped,
			Names: apiextensionsv1beta1.CustomResourceDefinitionNames{
				Plural:   singular + "s",
				Singular: singular,
				Kind:     kind(name),
			},
		},
	}
	_, err := client.Apiextensions().CustomResourceDefinitions().Create(crd)
	return err
}

func WaitCRDsEstablished(ki kubernetes.Interface, timeout time.Duration) error {
	client := apiextensionsclient.New(ki.Core().RESTClient())
	interval := time.Tick(200 * time.Millisecond)
	timer := time.After(timeout)
	for {
		select {
		case <-timer:
			return fmt.Errorf("timed out waiting for CRDs to get established")
		case <-interval:
			established := 0
			for _, res := range resources {
				crd, err := client.Apiextensions().CustomResourceDefinitions().Get(fqName(res), metav1.GetOptions{})
				if err != nil {
					break
				}
				for _, condition := range crd.Status.Conditions {
					if condition.Type == apiextensionsv1beta1.Established &&
						condition.Status == apiextensionsv1beta1.ConditionTrue {
						established++
					} else {
						break
					}
				}
			}
			if established == len(resources) {
				return nil
			}
		}
	}
}
